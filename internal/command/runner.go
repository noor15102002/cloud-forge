// Package command provides the single subprocess boundary used by CloudForge.
package command

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"time"

	"github.com/noor15102002/cloud-forge/pkg/model"
)

const defaultOutputLimit = 64 * 1024

// Request describes one direct executable invocation.
type Request struct {
	Name        string
	Args        []string
	Dir         string
	Timeout     time.Duration
	OutputLimit int
	Env         []string
}

// Runner executes commands and returns normalized results.
type Runner interface {
	Run(context.Context, Request) model.CommandResult
}

// ExecRunner invokes local executables without a shell.
type ExecRunner struct{}

// Run executes a command with cancellation, timeout, and bounded output.
func (ExecRunner) Run(ctx context.Context, req Request) model.CommandResult {
	limit := req.OutputLimit
	if limit <= 0 {
		limit = defaultOutputLimit
	}
	if req.Timeout <= 0 {
		req.Timeout = 10 * time.Second
	}
	commandCtx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()

	stdout := &limitedBuffer{limit: limit}
	stderr := &limitedBuffer{limit: limit}
	// #nosec G204 -- callers provide executable and argument arrays; no shell is involved.
	cmd := exec.CommandContext(commandCtx, req.Name, req.Args...)
	cmd.Dir = req.Dir
	cmd.Env = append(cmd.Environ(), req.Env...)
	configureCancellation(cmd)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	started := time.Now()
	err := cmd.Run()
	result := model.CommandResult{
		Command: req.Name, Arguments: append([]string(nil), req.Args...),
		ExitCode: 0, Stdout: stdout.String(), Stderr: stderr.String(),
		DurationMS: time.Since(started).Milliseconds(),
		Truncated:  stdout.truncated || stderr.truncated,
	}
	if err == nil {
		return result
	}
	result.ExitCode = -1
	switch {
	case errors.Is(commandCtx.Err(), context.DeadlineExceeded):
		result.FailureType = model.FailureTimeout
	case errors.Is(commandCtx.Err(), context.Canceled):
		result.FailureType = model.FailureCanceled
	default:
		var exitErr *exec.ExitError
		var pathErr *exec.Error
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
			result.FailureType = model.FailureExit
		} else if errors.As(err, &pathErr) {
			result.FailureType = model.FailureNotFound
		} else {
			result.FailureType = model.FailureExecution
		}
	}
	return result
}

type limitedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	original := len(p)
	remaining := b.limit - b.buf.Len()
	if remaining <= 0 {
		b.truncated = true
		return original, nil
	}
	if len(p) > remaining {
		p = p[:remaining]
		b.truncated = true
	}
	_, _ = b.buf.Write(p)
	return original, nil
}

func (b *limitedBuffer) String() string { return b.buf.String() }
