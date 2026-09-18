package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
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
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "package.json"), []byte(`{"name":"cli-test"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "Dockerfile"), []byte("FROM node:22-alpine\nUSER node\nEXPOSE 8080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := cliRunnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		result := model.CommandResult{Command: request.Name, Arguments: request.Args}
		if request.Name == "trivy" {
			result.Stdout = `{"Results":[]}`
		}
		for _, argument := range request.Args {
			if argument == "pods" {
				result.Stdout = `{"apiVersion":"v1","kind":"PodList","items":[{"metadata":{"name":"api"},"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}`
			}
		}
		return result
	})
	var stdout, stderr bytes.Buffer
	root := newRootCommand(&stdout, &stderr, runner)
	root.SetArgs([]string{"verify", directory, "--format", "json"})
	err := root.ExecuteContext(context.Background())
	if err != nil {
		t.Fatalf("verify failed: %v stderr=%q", err, stderr.String())
	}
	var result model.VerificationRun
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.SchemaVersion != model.SchemaVersion || result.Status != model.StatusPass || len(result.Evidence) != 8 {
		t.Fatalf("unexpected verification contract: %#v", result)
	}
}

func TestVerifyMarkdownReport(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "package.json"), []byte(`{"name":"cli-test"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "Dockerfile"), []byte("FROM node:22-alpine\nUSER node\nEXPOSE 8080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := cliRunnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		result := model.CommandResult{Command: request.Name, Arguments: request.Args}
		if request.Name == "trivy" {
			result.Stdout = `{"Results":[]}`
		}
		if containsCLIArgument(request.Args, "pods") {
			result.Stdout = `{"apiVersion":"v1","kind":"PodList","items":[{"metadata":{"name":"api"},"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}`
		}
		return result
	})
	var stdout, stderr bytes.Buffer
	root := newRootCommand(&stdout, &stderr, runner)
	root.SetArgs([]string{"verify", directory, "--format", "markdown"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("verify failed: %v stderr=%q", err, stderr.String())
	}
	if !bytes.Contains(stdout.Bytes(), []byte("<!-- cloudforge-verification-report:v1alpha1 -->")) || !bytes.Contains(stdout.Bytes(), []byte("## CloudForge verification")) {
		t.Fatalf("unexpected Markdown report:\n%s", stdout.String())
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

func TestMarkdownFormatIsLimitedToVerification(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"analyze", ".", "--format", "markdown"}, &stdout, &stderr)
	if code != 2 || stdout.Len() != 0 || !bytes.Contains(stderr.Bytes(), []byte("use text or json")) {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func containsCLIArgument(arguments []string, wanted string) bool {
	for _, argument := range arguments {
		if argument == wanted {
			return true
		}
	}
	return false
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
