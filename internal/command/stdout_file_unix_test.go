//go:build unix

package command

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/noor15102002/cloud-forge/pkg/model"
)

func TestExecRunnerOutputOverflowStopsDescendants(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "archive.tar")
	result := (ExecRunner{}).Run(context.Background(), Request{
		Name: executable, Args: []string{"-test.run=^TestCommandDescendantHelper$", "--", "--command-descendant-helper", "parent"},
		Dir: dir, Env: []string{"GORACE=atexit_sleep_ms=0"}, Timeout: 5 * time.Second,
		StdoutFile: &OutputFile{Path: path, MaxBytes: 32},
	})
	if result.FailureType != model.FailureExecution || !result.Truncated {
		t.Fatalf("overflow did not fail capture: %#v", result)
	}
	assertCaptureAbsent(t, path)
	if _, err := os.Stat(filepath.Join(dir, "child.ready")); err != nil {
		t.Fatal("descendant had not started before cancellation")
	}
	// The descendant would leave this marker 500ms after becoming ready if
	// only the parent were killed. The helper exits by itself in that case.
	time.Sleep(750 * time.Millisecond)
	assertCaptureAbsent(t, filepath.Join(dir, "child.survived"))
}

func TestCommandDescendantHelper(_ *testing.T) {
	index := slices.Index(os.Args, "--command-descendant-helper")
	if index == -1 || index+1 >= len(os.Args) {
		return
	}
	if os.Args[index+1] == "child" {
		if err := os.WriteFile("child.ready", []byte("ready"), 0o600); err != nil {
			os.Exit(8)
		}
		time.Sleep(500 * time.Millisecond)
		if err := os.WriteFile("child.survived", []byte("unexpected"), 0o600); err != nil {
			os.Exit(8)
		}
		os.Exit(0)
	}
	executable, err := os.Executable()
	if err != nil {
		os.Exit(8)
	}
	// #nosec G204 -- only this test executable is started, with fixed arguments.
	child := exec.Command(executable, "-test.run=^TestCommandDescendantHelper$", "--", "--command-descendant-helper", "child")
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	if err := child.Start(); err != nil {
		os.Exit(8)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat("child.ready"); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) || time.Now().After(deadline) {
			_ = child.Process.Kill()
			_ = child.Wait()
			os.Exit(8)
		}
		time.Sleep(time.Millisecond)
	}
	_, _ = os.Stdout.Write(bytes.Repeat([]byte{128}, 64*1024))
	_ = child.Wait()
	os.Exit(0)
}
