// Package safefile reads bounded regular files without following symlinks.
package safefile

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
)

// Read reads a regular file beneath root. Opening nonblocking prevents a FIFO
// swapped in before opening from stalling analysis; Root prevents path escapes.
func Read(root, name string, limit int64) ([]byte, error) {
	directory, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = directory.Close() }()
	file, err := directory.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("metadata must be a regular file")
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("file exceeds %d byte limit", limit)
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("file exceeds %d byte limit", limit)
	}
	return data, nil
}
