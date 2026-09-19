package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
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
		result := successfulCLIRunner()(context.Background(), request)
		if request.Name == "trivy" && len(request.Args) > 1 {
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
	if result.SchemaVersion != model.VerificationSchemaVersion || result.Status != model.StatusPass || len(result.Evidence) != 12 {
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
		result := successfulCLIRunner()(context.Background(), request)
		if request.Name == "trivy" && len(request.Args) > 1 {
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
	if !bytes.Contains(stdout.Bytes(), []byte("<!-- cloudforge-verification-report:v1alpha2 -->")) || !bytes.Contains(stdout.Bytes(), []byte("## CloudForge verification")) {
		t.Fatalf("unexpected Markdown report:\n%s", stdout.String())
	}
}

func TestVerifyBaselineRegressionProducesReportAndExitOne(t *testing.T) {
	previousCommit := Commit
	Commit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	t.Cleanup(func() { Commit = previousCommit })
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "package.json"), []byte(`{"name":"cli-test"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "Dockerfile"), []byte("FROM node:22-alpine\nUSER node\nEXPOSE 8080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := successfulCLIRunner()
	var initialOutput, initialError bytes.Buffer
	initial := newRootCommand(&initialOutput, &initialError, runner)
	initial.SetArgs([]string{"verify", directory, "--format", "json"})
	if err := initial.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("initial verify failed: %v stderr=%q", err, initialError.String())
	}
	var baseline model.VerificationRun
	if err := json.Unmarshal(initialOutput.Bytes(), &baseline); err != nil {
		t.Fatal(err)
	}
	baselinePath := filepath.Join(t.TempDir(), "baseline.json")
	baselineData, err := json.Marshal(baseline)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(baselinePath, baselineData, 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	root := newRootCommand(&stdout, &stderr, cliTestRunner(true))
	root.SetArgs([]string{"verify", directory, "--format", "json", "--baseline", baselinePath})
	err = root.ExecuteContext(context.Background())
	var coded *exitError
	if !errors.As(err, &coded) || coded.code != 1 {
		t.Fatalf("expected regression exit code 1, got %v; baseline=%s current=%s", err, baselineData, stdout.String())
	}
	var current model.VerificationRun
	if err := json.Unmarshal(stdout.Bytes(), &current); err != nil {
		t.Fatalf("invalid comparison JSON: %v\n%s", err, stdout.String())
	}
	if current.Status != model.StatusWarn || current.Comparison == nil || current.Comparison.Status != model.StatusFail || len(current.Comparison.Regressions) == 0 {
		t.Fatalf("absolute and regression outcomes were not kept separate: %#v", current)
	}
}

func TestVerifyRejectsInvalidBaselineBeforeExecution(t *testing.T) {
	baselinePath := filepath.Join(t.TempDir(), "baseline.json")
	if err := os.WriteFile(baselinePath, []byte(`{"schema_version":"future"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	runner := cliRunnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		calls++
		return model.CommandResult{Command: request.Name}
	})
	var stdout, stderr bytes.Buffer
	root := newRootCommand(&stdout, &stderr, runner)
	root.SetArgs([]string{"verify", ".", "--baseline", baselinePath})
	var coded *exitError
	if err := root.ExecuteContext(context.Background()); !errors.As(err, &coded) || coded.code != 2 {
		t.Fatalf("expected baseline usage error, got %v", err)
	}
	if calls != 0 || stdout.Len() != 0 {
		t.Fatalf("invalid baseline executed commands or wrote a report: calls=%d stdout=%q", calls, stdout.String())
	}
}

func successfulCLIRunner() cliRunnerFunc {
	return cliTestRunner(false)
}

func cliTestRunner(vulnerable bool) cliRunnerFunc {
	return func(_ context.Context, request command.Request) model.CommandResult {
		result := model.CommandResult{Command: request.Name, Arguments: request.Args}
		if request.Name == "kubectl" && slices.Contains(request.Args, "/readyz") {
			result.Stdout = "ok"
		}
		if request.Name == "kubectl" && slices.Contains(request.Args, "nodes") {
			result.Stdout = `{"items":[{"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}`
		}
		if request.Name == "k3d" || request.Name == "k6" || (request.Name == "trivy" && len(request.Args) == 1) || (request.Name == "docker" && len(request.Args) > 0 && request.Args[0] == "info") {
			result.Stdout = "version 1.2.3"
		}
		if request.Name == "kubectl" && slices.Contains(request.Args, "--output=json") {
			result.Stdout = `{"serverVersion":{"gitVersion":"v1.34.0"}}`
		}
		if request.Name == "docker" && slices.Contains(request.Args, "inspect") {
			result.Stdout = `"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" []`
		}

		if request.Name == "trivy" && len(request.Args) > 1 {
			if vulnerable {
				result.Stdout = `{"Results":[{"Target":"image","Vulnerabilities":[{"VulnerabilityID":"CVE-2026-0001","PkgName":"libc","InstalledVersion":"1","Severity":"HIGH"}]}]}`
			} else {
				result.Stdout = `{"Results":[]}`
			}
		}
		if request.Name == "k6" && slices.Contains(request.Args, "run") {
			result.Stdout = `{"metrics":{"http_reqs":{"values":{"count":200,"rate":10}},"http_req_failed":{"values":{"rate":0}},"http_req_duration":{"values":{"med":10,"p(95)":20,"p(99)":30}}}}`
		}
		if containsCLIArgument(request.Args, "pods") {
			result.Stdout = `{"apiVersion":"v1","kind":"PodList","items":[{"metadata":{"name":"api"},"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}`
		}
		return result
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

func TestReportRendersValidatedJSONWithoutExecution(t *testing.T) {
	path := filepath.Join("..", "render", "testdata", "verification.json")
	var stdout, stderr bytes.Buffer
	root := newRootCommand(&stdout, &stderr, cliRunnerFunc(func(_ context.Context, _ command.Request) model.CommandResult {
		t.Fatal("report must not execute subprocesses")
		return model.CommandResult{}
	}))
	root.SetArgs([]string{"report", path, "--format", "markdown"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("report failed: %v stderr=%q", err, stderr.String())
	}
	if !bytes.HasPrefix(stdout.Bytes(), []byte("<!-- cloudforge-verification-report:v1alpha1 -->")) || !bytes.Contains(stdout.Bytes(), []byte("### Baseline comparison")) {
		t.Fatalf("unexpected rendered report:\n%s", stdout.String())
	}
}

func TestReportRejectsMalformedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":"v1alpha1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"report", path, "--format", "markdown"}, &stdout, &stderr)
	if code != 2 || stdout.Len() != 0 || !bytes.Contains(stderr.Bytes(), []byte("does not satisfy")) || bytes.Contains(stderr.Bytes(), []byte("baseline")) {
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

func TestDependencyPlanIsReadOnlyDeterministicAndVersioned(t *testing.T) {
	var previous []byte
	for range 2 {
		var stdout, stderr bytes.Buffer
		root := newRootCommand(&stdout, &stderr, cliRunnerFunc(func(context.Context, command.Request) model.CommandResult {
			t.Fatal("plan executed a subprocess")
			return model.CommandResult{}
		}))
		root.SetArgs([]string{"verify", filepath.Join("..", "..", "testdata", "healthy-node-redis"), "--plan", "--format", "json"})
		if err := root.ExecuteContext(context.Background()); err != nil {
			t.Fatal(err)
		}
		var plan model.VerificationPlan
		if err := json.Unmarshal(stdout.Bytes(), &plan); err != nil {
			t.Fatal(err)
		}
		if plan.SchemaVersion != model.VerificationSchemaVersion || plan.Status != model.StatusPass || stderr.Len() != 0 {
			t.Fatal("invalid plan contract")
		}
		if previous != nil && !bytes.Equal(previous, stdout.Bytes()) {
			t.Fatal("plan not deterministic")
		}
		previous = append([]byte(nil), stdout.Bytes()...)
	}
}
