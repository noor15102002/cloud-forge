package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

func TestAnalyzeJSONContract(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"analyze", filepath.Join("..", "..", "testdata", "healthy-node"), "--format", "json"}, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	var result model.AnalysisResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.SchemaVersion != model.SchemaVersion || !result.Supported {
		t.Fatalf("unexpected contract: %#v", result)
	}
}

func TestVerifyJSONContractAndExitCode(t *testing.T) {
	runner := cliRunnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		result := model.CommandResult{Command: request.Name, Arguments: request.Args}
		for _, argument := range request.Args {
			if argument == "pods" {
				result.Stdout = `{"apiVersion":"v1","kind":"PodList","items":[{"status":{"conditions":[{"type":"Ready","status":"True"}]}},{"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}`
			}
		}
		return result
	})
	var stdout, stderr bytes.Buffer
	root := newRootCommand(&stdout, &stderr, runner)
	root.SetArgs([]string{"verify", filepath.Join("..", "..", "testdata", "healthy-node"), "--format", "json"})
	err := root.ExecuteContext(context.Background())
	if err != nil {
		t.Fatalf("verify failed: %v stderr=%q", err, stderr.String())
	}
	var result model.VerificationRun
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.SchemaVersion != model.SchemaVersion || result.Status != model.StatusPass || len(result.Evidence) != 2 {
		t.Fatalf("unexpected verification contract: %#v", result)
	}
}

type cliRunnerFunc func(context.Context, command.Request) model.CommandResult

func (f cliRunnerFunc) Run(ctx context.Context, request command.Request) model.CommandResult {
	return f(ctx, request)
}

func TestUnsupportedApplicationExitCode(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"analyze", t.TempDir(), "--format", "json"}, &stdout, &stderr)
	if code != 1 || stderr.Len() != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestInvalidFormatExitCode(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"version", "--format", "yaml"}, &stdout, &stderr)
	if code != 2 || stdout.Len() != 0 || stderr.Len() == 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestVerboseDiagnosticsStayOnStderr(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"--verbose", "analyze", filepath.Join("..", "..", "testdata", "healthy-python"), "--format", "json"}, &stdout, &stderr)
	if code != 0 || !bytes.Contains(stderr.Bytes(), []byte("analyzing repository")) {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	var result model.AnalysisResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("verbose logging corrupted JSON output: %v", err)
	}
}
