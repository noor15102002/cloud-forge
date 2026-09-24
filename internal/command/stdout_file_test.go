package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/noor15102002/cloud-forge/pkg/model"
)

func outputHelperRequest(t *testing.T, mode, path string, limit int64) Request {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return Request{
		Name: executable, Args: []string{"-test.run=^TestCommandOutputFileHelper$", "--", "--command-output-helper", mode},
		Env: []string{"GORACE=atexit_sleep_ms=0"}, Timeout: 5 * time.Second,
		StdoutFile: &OutputFile{Path: path, MaxBytes: limit},
	}
}

func outputHelperPayload() []byte {
	return append([]byte("PRIVATE_ARCHIVE_PAYLOAD\x00\xff\xfe\n"), bytes.Repeat([]byte{0, 1, 2, 255, 128}, 300)...)
}

func assertCaptureAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("incomplete capture was retained: %v", err)
	}
}

func assertCaptureResultPrivate(t *testing.T, result model.CommandResult, path string) {
	t.Helper()
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if result.Stdout != "" || strings.Contains(string(data), "PRIVATE_ARCHIVE_PAYLOAD") || strings.Contains(string(data), path) {
		t.Fatalf("binary stdout or private path escaped into command result: %s", data)
	}
}

func TestExecRunnerCapturesExactPrivateBinaryFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive.tar")
	payload := outputHelperPayload()
	result := (ExecRunner{}).Run(context.Background(), outputHelperRequest(t, "binary", path, int64(len(payload))))
	if result.FailureType != model.FailureNone || result.ExitCode != 0 || result.Truncated || result.Stderr != "bounded diagnostic\n" {
		t.Fatalf("unexpected result: %#v", result)
	}
	assertCaptureResultPrivate(t, result, path)
	// #nosec G304 -- this path is inside the test's private TempDir.
	actual, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(actual, payload) {
		t.Fatalf("binary capture differs from child output: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 || !info.Mode().IsRegular() {
		t.Fatalf("capture is not a private regular file: %v", err)
	}
}

func TestExecRunnerCaptureFailureCleanup(t *testing.T) {
	for _, test := range []struct {
		name, mode string
		maxBytes   int64
		failure    model.FailureType
		exitCode   int
		truncated  bool
	}{
		{"overflow", "binary", int64(len(outputHelperPayload()) - 1), model.FailureExecution, 0, true},
		{"continuous overflow", "continuous", 32 * 1024, model.FailureExecution, -1, true},
		{"nonzero exit", "failure", 4096, model.FailureExit, 7, false},
		{"stderr truncated", "stderr-overflow", 4096, model.FailureExecution, 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "archive.tar")
			req := outputHelperRequest(t, test.mode, path, test.maxBytes)
			req.OutputLimit = 64
			result := (ExecRunner{}).Run(context.Background(), req)
			if result.FailureType != test.failure || result.Truncated != test.truncated || len(result.Stderr) > req.OutputLimit {
				t.Fatalf("unexpected capture failure: %#v", result)
			}
			// Overflow can race with a short-lived child's normal exit. Its
			// actual exit is preserved rather than invented from the sink error.
			if test.mode != "binary" && test.mode != "continuous" && result.ExitCode != test.exitCode {
				t.Fatalf("exit code changed: %#v", result)
			}
			assertCaptureAbsent(t, path)
			assertCaptureResultPrivate(t, result, path)
		})
	}
}

func TestExecRunnerCaptureZeroOutputAndNotFound(t *testing.T) {
	t.Run("zero output", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "archive.tar")
		result := (ExecRunner{}).Run(context.Background(), outputHelperRequest(t, "empty", path, 1))
		info, err := os.Stat(path)
		if result.FailureType != model.FailureNone || err != nil || info.Size() != 0 {
			t.Fatalf("zero output should be left for caller validation: %#v, %v", result, err)
		}
	})
	t.Run("executable absent", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "archive.tar")
		req := outputHelperRequest(t, "empty", path, 1)
		req.Name = "cloudforge-command-that-does-not-exist"
		result := (ExecRunner{}).Run(context.Background(), req)
		if result.FailureType != model.FailureNotFound {
			t.Fatalf("unexpected failure: %#v", result)
		}
		assertCaptureAbsent(t, path)
		assertCaptureResultPrivate(t, result, path)
	})
}

func TestExecRunnerCaptureCancellationAndTimeoutRemovePartialFile(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		name := "cancellation"
		if timeout {
			name = "timeout"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "archive.tar")
			req := outputHelperRequest(t, "wait", path, 4096)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if timeout {
				req.Timeout = time.Second
			}
			done := make(chan model.CommandResult, 1)
			go func() { done <- (ExecRunner{}).Run(ctx, req) }()
			waitForCaptureBytes(t, path, done)
			if !timeout {
				cancel()
			}
			select {
			case result := <-done:
				expected := model.FailureCanceled
				if timeout {
					expected = model.FailureTimeout
				}
				if result.FailureType != expected {
					t.Fatalf("unexpected result: %#v", result)
				}
				assertCaptureAbsent(t, path)
				assertCaptureResultPrivate(t, result, path)
			case <-time.After(4 * time.Second):
				t.Fatal("canceled command did not finish promptly")
			}
		})
	}
}

func waitForCaptureBytes(t *testing.T, path string, done <-chan model.CommandResult) {
	t.Helper()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case result := <-done:
			t.Fatalf("command ended before emitting its partial capture: %#v", result)
		case <-deadline.C:
			t.Fatal("helper did not emit its partial capture")
		case <-tick.C:
			if info, err := os.Stat(path); err == nil && info.Size() > 0 {
				return
			}
		}
	}
}

func TestExecRunnerRefusesOutputCollisionsBeforeStartingCommand(t *testing.T) {
	for _, kind := range []string{"file", "symlink", "directory", "invalid limit", "negative limit", "relative path", "missing parent", "noncanonical path"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "archive.tar")
			target := filepath.Join(dir, "unrelated")
			if err := os.WriteFile(target, []byte("sentinel"), 0o600); err != nil {
				t.Fatal(err)
			}
			req := outputHelperRequest(t, "started", path, 4096)
			marker := filepath.Join(dir, "command-started")
			req.Env = append(req.Env, "CLOUDFORGE_OUTPUT_TEST_MARKER="+marker)
			switch kind {
			case "file":
				if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "invalid limit":
				req.StdoutFile.MaxBytes = 0
			case "negative limit":
				req.StdoutFile.MaxBytes = -1
			case "relative path":
				req.StdoutFile.Path = "private-output-relative.tar"
			case "missing parent":
				req.StdoutFile.Path = filepath.Join(dir, "missing", "archive.tar")
			case "noncanonical path":
				req.StdoutFile.Path = path + string(os.PathSeparator)
			}
			result := (ExecRunner{}).Run(context.Background(), req)
			if result.FailureType != model.FailureExecution || result.ExitCode != -1 {
				t.Fatalf("unexpected setup result: %#v", result)
			}
			assertCaptureAbsent(t, marker)
			assertCaptureResultPrivate(t, result, req.StdoutFile.Path)
			// #nosec G304 -- this sentinel is inside the test's private TempDir.
			actual, err := os.ReadFile(target)
			if err != nil || string(actual) != "sentinel" {
				t.Fatal("unrelated file changed")
			}
			switch kind {
			case "file":
				// #nosec G304 -- the existing file was created in this TempDir above.
				actual, err := os.ReadFile(path)
				if err != nil || string(actual) != "existing" {
					t.Fatal("existing output file changed")
				}
			case "symlink":
				actual, err := os.Readlink(path)
				if err != nil || actual != target {
					t.Fatal("existing symlink changed")
				}
			case "directory":
				info, err := os.Stat(path)
				if err != nil || !info.IsDir() {
					t.Fatal("existing directory changed")
				}
			}
		})
	}
}

func TestExecRunnerRedactsOutputPathFromDiagnostics(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive.tar")
	req := outputHelperRequest(t, "echo-path", path, 4096)
	req.Env = append(req.Env, "CLOUDFORGE_OUTPUT_TEST_PATH="+path)
	req.Args = append(req.Args, "--unused="+path)
	result := (ExecRunner{}).Run(context.Background(), req)
	if result.FailureType != model.FailureNone || !strings.Contains(result.Stderr, "[private output]") {
		t.Fatalf("unexpected redacted result: %#v", result)
	}
	assertCaptureResultPrivate(t, result, path)
}

func TestBoundedFileWriterEnforcesCapBeforeWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive.tar")
	canceled := 0
	capture, err := openOutputFile(OutputFile{Path: path, MaxBytes: 5}, func() { canceled++ })
	if err != nil {
		t.Fatal(err)
	}
	if n, err := capture.output.Write([]byte{0, 255, 1}); n != 3 || err != nil {
		t.Fatalf("first write failed: %d %v", n, err)
	}
	if n, err := capture.output.Write([]byte{128, 2, 3, 4}); n != 2 || !errors.Is(err, errOutputCapture) {
		t.Fatalf("overflow write not bounded: %d %v", n, err)
	}
	if n, err := capture.output.Write([]byte{5, 6}); n != 0 || !errors.Is(err, errOutputCapture) || canceled != 1 {
		t.Fatal("overflow did not stop subsequent writes and cancel exactly once")
	}
	// #nosec G304 -- this capped output was created in the test's private TempDir.
	actual, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(actual, []byte{0, 255, 1, 128, 2}) {
		t.Fatalf("capture exceeded cap or changed bytes: %v", err)
	}
	if err := capture.finish(false); err != nil {
		t.Fatal(err)
	}
	assertCaptureAbsent(t, path)
}

func TestFileCaptureWriteAndCloseFailures(t *testing.T) {
	t.Run("write failure", func(t *testing.T) {
		canceled := false
		writer := &boundedFileWriter{writer: failingOutputWriter{}, maxBytes: 8, cancel: func() { canceled = true }}
		if n, err := writer.Write([]byte("binary")); n != 2 || !errors.Is(err, errOutputCapture) || !canceled || writer.err == nil {
			t.Fatal("write failure was not sanitized and canceled")
		}
	})
	t.Run("short write", func(t *testing.T) {
		canceled := false
		writer := &boundedFileWriter{writer: shortOutputWriter{}, maxBytes: 8, cancel: func() { canceled = true }}
		if n, err := writer.Write([]byte("binary")); n != 1 || !errors.Is(err, errOutputCapture) || !canceled {
			t.Fatal("short write was not rejected and canceled")
		}
	})
	t.Run("close failure removes otherwise complete file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "archive.tar")
		capture, err := openOutputFile(OutputFile{Path: path, MaxBytes: 8}, func() {})
		if err != nil {
			t.Fatal(err)
		}
		capture.file = failingCloseWriter{WriteCloser: capture.file}
		if _, err := capture.output.Write([]byte("binary")); err != nil {
			t.Fatal(err)
		}
		if err := capture.finish(true); !errors.Is(err, errOutputCapture) {
			t.Fatal("close failure accepted as a complete capture")
		}
		assertCaptureAbsent(t, path)
	})
}

type failingOutputWriter struct{}

func (failingOutputWriter) Write([]byte) (int, error) {
	return 2, errors.New("write failed: /private/caller/file")
}

type shortOutputWriter struct{}

func (shortOutputWriter) Write([]byte) (int, error) { return 1, nil }

type failingCloseWriter struct{ io.WriteCloser }

func (w failingCloseWriter) Close() error {
	_ = w.WriteCloser.Close()
	return errors.New("close failed: /private/caller/file")
}

func TestFileCaptureDoesNotRemoveReplacedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "archive.tar")
	capture, err := openOutputFile(OutputFile{Path: path, MaxBytes: 8}, func() {})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, filepath.Join(dir, "moved.tar")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := capture.finish(false); !errors.Is(err, errOutputCapture) {
		t.Fatal("replacement file was not detected")
	}
	// #nosec G304 -- this replacement sentinel was created in this TempDir above.
	actual, err := os.ReadFile(path)
	if err != nil || string(actual) != "sentinel" {
		t.Fatal("replacement file was removed or modified")
	}
}

func TestFileCaptureRejectsRemovedSuccessFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive.tar")
	capture, err := openOutputFile(OutputFile{Path: path, MaxBytes: 8}, func() {})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := capture.finish(true); !errors.Is(err, errOutputCapture) {
		t.Fatal("removed output was accepted as a completed capture")
	}
}

func TestFileCapturePinsParentAndRejectsMovedSuccessPath(t *testing.T) {
	for _, keep := range []bool{false, true} {
		t.Run(fmt.Sprintf("keep=%t", keep), func(t *testing.T) {
			base := t.TempDir()
			dir := filepath.Join(base, "private")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "archive.tar")
			capture, err := openOutputFile(OutputFile{Path: path, MaxBytes: 8}, func() {})
			if err != nil {
				t.Fatal(err)
			}
			moved := filepath.Join(base, "moved")
			if err := os.Rename(dir, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("sentinel"), 0o600); err != nil {
				t.Fatal(err)
			}
			err = capture.finish(keep)
			if keep && !errors.Is(err, errOutputCapture) {
				t.Fatal("changed success path was not rejected")
			}
			if !keep && err != nil {
				t.Fatalf("owned original could not be cleaned: %v", err)
			}
			assertCaptureAbsent(t, filepath.Join(moved, "archive.tar"))
			// #nosec G304 -- this replacement sentinel was created in this TempDir above.
			actual, err := os.ReadFile(path)
			if err != nil || string(actual) != "sentinel" {
				t.Fatal("replacement parent redirected cleanup")
			}
		})
	}
}

func TestCommandOutputFileHelper(_ *testing.T) {
	index := slices.Index(os.Args, "--command-output-helper")
	if index == -1 || index+1 >= len(os.Args) {
		return
	}
	switch os.Args[index+1] {
	case "binary":
		_, _ = os.Stdout.Write(outputHelperPayload())
		_, _ = fmt.Fprintln(os.Stderr, "bounded diagnostic")
	case "continuous":
		for {
			if _, err := os.Stdout.Write(bytes.Repeat([]byte{128}, 4096)); err != nil {
				os.Exit(9)
			}
		}
	case "failure":
		_, _ = os.Stdout.Write(outputHelperPayload())
		_, _ = fmt.Fprintln(os.Stderr, "expected tool failure")
		os.Exit(7)
	case "stderr-overflow":
		_, _ = os.Stdout.Write(outputHelperPayload())
		_, _ = fmt.Fprint(os.Stderr, strings.Repeat("bounded diagnostic ", 100))
	case "wait":
		_, _ = os.Stdout.Write(outputHelperPayload())
		time.Sleep(30 * time.Second)
	case "started":
		// #nosec G703 -- this subprocess helper receives only a test-owned TempDir path.
		_ = os.WriteFile(os.Getenv("CLOUDFORGE_OUTPUT_TEST_MARKER"), []byte("started"), 0o600)
	case "echo-path":
		_, _ = fmt.Fprint(os.Stderr, "diagnostic "+os.Getenv("CLOUDFORGE_OUTPUT_TEST_PATH"))
	case "empty":
	default:
		os.Exit(8)
	}
	os.Exit(0)
}
