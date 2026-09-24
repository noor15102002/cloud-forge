package command

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// OutputFile describes bounded binary stdout capture. Path must be absolute,
// absent, and inside a private directory owned exclusively by the caller.
// The caller must not concurrently change that directory or its contents.
type OutputFile struct {
	Path     string
	MaxBytes int64
}

var errOutputCapture = errors.New("private command output capture failed")

type fileCapture struct {
	root   *os.Root
	name   string
	path   string
	file   io.WriteCloser
	info   fs.FileInfo
	output *boundedFileWriter
}

func openOutputFile(spec OutputFile, cancel context.CancelFunc) (*fileCapture, error) {
	if !filepath.IsAbs(spec.Path) || filepath.Clean(spec.Path) != spec.Path || spec.MaxBytes <= 0 {
		return nil, errOutputCapture
	}
	// Pin the parent directory so its replacement cannot redirect cleanup.
	// Parent ownership/privacy is the caller's responsibility; O_EXCL refuses
	// an existing final component, including a symlink, atomically.
	root, err := os.OpenRoot(filepath.Dir(spec.Path))
	if err != nil {
		return nil, errOutputCapture
	}
	name := filepath.Base(spec.Path)
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = root.Close()
		return nil, errOutputCapture
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		_ = root.Remove(name)
		_ = root.Close()
		return nil, errOutputCapture
	}
	return &fileCapture{
		root: root, name: name, path: spec.Path, file: file, info: info,
		output: &boundedFileWriter{writer: file, maxBytes: spec.MaxBytes, cancel: cancel},
	}, nil
}

func (c *fileCapture) finish(keep bool) error {
	defer func() { _ = c.root.Close() }()
	closeErr := c.file.Close()
	if closeErr != nil {
		keep = false
	}
	pathChanged := false
	if keep {
		// A successful capture must still be reachable through the path the
		// caller will open. If the parent moved, remove through the pinned root.
		info, err := os.Lstat(c.path)
		if err != nil || !info.Mode().IsRegular() || !os.SameFile(c.info, info) {
			pathChanged = true
			keep = false
		}
	}
	info, err := c.root.Lstat(c.name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && !keep && !pathChanged && closeErr == nil {
			return nil
		}
		return errOutputCapture
	}
	if !info.Mode().IsRegular() || !os.SameFile(c.info, info) {
		// The caller violated exclusive directory ownership. Never remove an
		// unrelated replacement, even while reporting the failed capture.
		return errOutputCapture
	}
	if !keep {
		if err := c.root.Remove(c.name); err != nil {
			return errOutputCapture
		}
	}
	if closeErr != nil || pathChanged {
		return errOutputCapture
	}
	return nil
}

// boundedFileWriter retains no stdout bytes. Returning an error stops the
// copying goroutine, while cancel also stops descendants holding stdout open.
type boundedFileWriter struct {
	writer    io.Writer
	maxBytes  int64
	written   int64
	cancel    context.CancelFunc
	err       error
	truncated bool
}

func (w *boundedFileWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	remaining := w.maxBytes - w.written
	overflow := int64(len(p)) > remaining
	if overflow {
		p = p[:remaining]
	}
	n, err := w.writer.Write(p)
	w.written += int64(n)
	if err != nil || n != len(p) || overflow {
		w.truncated = overflow
		w.err = errOutputCapture
		w.cancel()
		return n, w.err
	}
	return n, nil
}
