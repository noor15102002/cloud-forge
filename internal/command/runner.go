// Package command provides the single subprocess boundary used by CloudForge.
package command

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
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
	// ClearEnv opts into an environment consisting only of Env. The default
	// inherits the caller environment for backwards-compatible tool execution.
	ClearEnv bool
	// StdoutFile streams binary stdout to an exclusive private file instead of
	// including it in the result. The caller owns the parent directory and the
	// successfully completed file; failed or incomplete captures are removed.
	StdoutFile *OutputFile
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
	var capture *fileCapture
	if req.StdoutFile != nil {
		var err error
		capture, err = openOutputFile(*req.StdoutFile, cancel)
		if err != nil {
			return redactOutputPath(model.CommandResult{
				Command: req.Name, Arguments: append([]string(nil), req.Args...),
				ExitCode: -1, FailureType: model.FailureExecution,
			}, req.StdoutFile.Path, limit)
		}
	}
	// #nosec G204 -- callers provide executable and argument arrays; no shell is involved.
	cmd := exec.CommandContext(commandCtx, req.Name, req.Args...)
	cmd.Dir = req.Dir
	if req.ClearEnv {
		// A non-nil empty environment is intentional: nil means inherit to exec.
		cmd.Env = append([]string{}, req.Env...)
	} else {
		cmd.Env = append(cmd.Environ(), req.Env...)
	}
	configureCancellation(cmd)
	cmd.Stdout = stdout
	if capture != nil {
		cmd.Stdout = capture.output
	}
	cmd.Stderr = stderr
	started := time.Now()
	err := cmd.Run()
	result := model.CommandResult{
		Command: req.Name, Arguments: append([]string(nil), req.Args...),
		ExitCode: 0, Stdout: stdout.String(), Stderr: stderr.String(),
		DurationMS: time.Since(started).Milliseconds(),
		Truncated:  stdout.truncated || stderr.truncated,
	}
	if err != nil {
		classifyFailure(commandCtx, &result, err)
		if cmd.ProcessState != nil {
			// An output-copy error may occur after a successful child exit.
			// Keep the observed exit even when the capture itself is invalid.
			result.ExitCode = cmd.ProcessState.ExitCode()
		}
	}
	if capture != nil {
		result.Stdout = ""
		result.Truncated = result.Truncated || capture.output.truncated
		result = redactOutputPath(result, req.StdoutFile.Path, limit)
		// A sink failure cancels the process group itself. Preserve that cause
		// rather than misreporting the internal cancellation as a user request.
		if capture.output.err != nil || (err == nil && result.Truncated) {
			result.FailureType = model.FailureExecution
		}
		if finishErr := capture.finish(result.FailureType == model.FailureNone && !result.Truncated); finishErr != nil && result.FailureType == model.FailureNone {
			result.FailureType = model.FailureExecution
		}
	}
	return result
}

func classifyFailure(ctx context.Context, result *model.CommandResult, err error) {
	result.ExitCode = -1
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		result.FailureType = model.FailureTimeout
	case errors.Is(ctx.Err(), context.Canceled):
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
}

func redactOutputPath(result model.CommandResult, path string, limit int) model.CommandResult {
	if path == "" {
		return result
	}
	result.Command = strings.ReplaceAll(result.Command, path, "[private output]")
	for i := range result.Arguments {
		result.Arguments[i] = strings.ReplaceAll(result.Arguments[i], path, "[private output]")
	}
	result.Stderr = strings.ReplaceAll(result.Stderr, path, "[private output]")
	if len(result.Stderr) > limit {
		result.Stderr = result.Stderr[:limit]
		result.Truncated = true
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
