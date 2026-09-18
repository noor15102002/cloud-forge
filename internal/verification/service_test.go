package verification

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

type runnerFunc func(context.Context, command.Request) model.CommandResult

func (f runnerFunc) Run(ctx context.Context, request command.Request) model.CommandResult {
	return f(ctx, request)
}

func TestRunProducesReadinessEvidenceAndCleansUp(t *testing.T) {
	var calls []command.Request
	var manifest string
	runner := runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		calls = append(calls, request)
		result := model.CommandResult{Command: request.Name, Arguments: request.Args, DurationMS: 12}
		if request.Name == "trivy" {
			result.Stdout = `{"Results":[]}`
		}
		if request.Name == "kubectl" && containsArgument(request.Args, "apply") {
			data, err := os.ReadFile(request.Args[len(request.Args)-1])
			if err != nil {
				t.Fatal(err)
			}
			manifest = string(data)
		}
		if request.Name == "kubectl" && containsArgument(request.Args, "pods") {
			result.Stdout = readyPodList
		}
		return result
	})
	service := fixedService(runner)

	outcome := service.Run(context.Background(), fixturePath(t), Options{})
	if outcome.ExitCode != 0 || outcome.Run.Status != model.StatusPass {
		t.Fatalf("unexpected outcome: %#v", outcome)
	}
	if len(outcome.Run.Evidence) != 4 || outcome.Run.Evidence[2].Measurements[0].Value != "2" {
		t.Fatalf("missing readiness evidence: %#v", outcome.Run.Evidence)
	}
	if len(calls) != 12 || !containsArgument(calls[len(calls)-2].Args, "delete") || !containsArgument(calls[len(calls)-1].Args, "rm") {
		t.Fatalf("unexpected command lifecycle: %#v", calls)
	}
	for _, expected := range []string{"kind: Namespace", "kind: Deployment", "kind: Service", "imagePullPolicy: Never", "path: /ready", "cpu: 100m", "replicas: 2"} {
		if !strings.Contains(manifest, expected) {
			t.Fatalf("generated manifest is missing %q:\n%s", expected, manifest)
		}
	}
	for _, expected := range []string{"type: NodePort", "nodePort: 30080"} {
		if !strings.Contains(manifest, expected) {
			t.Fatalf("generated Service is missing %q:\n%s", expected, manifest)
		}
	}
	if outcome.Run.Environment.Endpoint != "http://127.0.0.1:18080/ready" {
		t.Fatalf("unexpected loopback endpoint: %q", outcome.Run.Environment.Endpoint)
	}
	if !hasCommand(calls, "k3d", "127.0.0.1:18080:30080@server:0") {
		t.Fatalf("k3d did not receive the loopback port mapping: %#v", calls)
	}
	if strings.Contains(manifest, "never-include-this-value") {
		t.Fatal("generated manifest leaked a source Secret value")
	}
}

func TestBuildFailureDoesNotCreateCluster(t *testing.T) {
	var calls []command.Request
	runner := runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		calls = append(calls, request)
		return model.CommandResult{Command: request.Name, Arguments: request.Args, ExitCode: 1, FailureType: model.FailureExit}
	})
	service := fixedService(runner)
	outcome := service.Run(context.Background(), fixturePath(t), Options{})
	if outcome.ExitCode != 1 || outcome.Run.Status != model.StatusFail || len(calls) != 1 || calls[0].Name != "docker" {
		t.Fatalf("unexpected build failure behavior: outcome=%#v calls=%#v", outcome, calls)
	}
}

func TestTrivyFailureIsExecutionErrorAndRemovesImage(t *testing.T) {
	var calls []command.Request
	runner := runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		calls = append(calls, request)
		result := model.CommandResult{Command: request.Name, Arguments: request.Args}
		if request.Name == "trivy" {
			result.ExitCode = 1
			result.FailureType = model.FailureExit
		}
		return result
	})
	outcome := fixedService(runner).Run(context.Background(), fixturePath(t), Options{})
	if outcome.ExitCode != 2 || outcome.Run.Status != model.StatusError || !hasDiagnosticCode(outcome.Run.Diagnostics, "trivy_scan_failed") {
		t.Fatalf("unexpected Trivy failure outcome: %#v", outcome)
	}
	if hasCommand(calls, "k3d", "create") || !hasCommand(calls, "docker", "rm") {
		t.Fatalf("Trivy failure lifecycle is incorrect: %#v", calls)
	}
}

func TestMalformedTrivyOutputIsExecutionError(t *testing.T) {
	runner := runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		result := model.CommandResult{Command: request.Name, Arguments: request.Args}
		if request.Name == "trivy" {
			result.Stdout = "{"
		}
		return result
	})
	outcome := fixedService(runner).Run(context.Background(), fixturePath(t), Options{})
	if outcome.ExitCode != 2 || !hasDiagnosticCode(outcome.Run.Diagnostics, "trivy_output_invalid") {
		t.Fatalf("unexpected malformed scan outcome: %#v", outcome)
	}
}

func TestVulnerabilityFindingProducesWarningWithoutExecutionFailure(t *testing.T) {
	runner := runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		result := model.CommandResult{Command: request.Name, Arguments: request.Args}
		if request.Name == "trivy" {
			result.Stdout = `{"Results":[{"Target":"image (alpine 3.23)","Vulnerabilities":[{"VulnerabilityID":"CVE-2026-0001","PkgName":"libc","InstalledVersion":"1","Severity":"HIGH"}]}]}`
		}
		if request.Name == "kubectl" && containsArgument(request.Args, "pods") {
			result.Stdout = readyPodList
		}
		return result
	})
	outcome := fixedService(runner).Run(context.Background(), fixturePath(t), Options{})
	if outcome.ExitCode != 0 || outcome.Run.Status != model.StatusWarn {
		t.Fatalf("vulnerability should be a warning, not an execution failure: %#v", outcome)
	}
	if finding := findingByID(outcome.Run.Findings, "security.trivy.cve-2026-0001.libc"); finding == nil || finding.Severity != model.SeverityHigh {
		t.Fatalf("missing normalized vulnerability: %#v", outcome.Run.Findings)
	}
}

func TestReadinessFailureStillCleansUp(t *testing.T) {
	var calls []command.Request
	runner := runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		calls = append(calls, request)
		result := model.CommandResult{Command: request.Name, Arguments: request.Args}
		if request.Name == "trivy" {
			result.Stdout = `{"Results":[]}`
		}
		if request.Name == "kubectl" && containsArgument(request.Args, "rollout") {
			result.ExitCode = 1
			result.FailureType = model.FailureTimeout
		}
		if request.Name == "kubectl" && containsArgument(request.Args, "pods") {
			result.Stdout = readyPodList
		}
		return result
	})
	outcome := fixedService(runner).Run(context.Background(), fixturePath(t), Options{})
	if outcome.ExitCode != 1 || outcome.Run.Status != model.StatusFail {
		t.Fatalf("unexpected readiness outcome: %#v", outcome)
	}
	if !hasCommand(calls, "k3d", "delete") || !hasCommand(calls, "docker", "rm") {
		t.Fatalf("cleanup was not attempted: %#v", calls)
	}
}

func TestReadinessMeasuresHTTPGating(t *testing.T) {
	service := fixedService(successRunner())
	probeCalls := 0
	var urls []string
	service.probe = func(_ context.Context, url string) (int, error) {
		probeCalls++
		urls = append(urls, url)
		if probeCalls == 1 {
			return 503, nil
		}
		return 200, nil
	}
	outcome := service.Run(context.Background(), fixturePath(t), Options{})
	if outcome.ExitCode != 0 {
		t.Fatalf("unexpected outcome: %#v", outcome)
	}
	readiness := evidenceByID(outcome.Run.Evidence, "deployment-readiness")
	if readiness == nil || measurementValue(readiness.Measurements, "readiness_http_status") != "200" || measurementValue(readiness.Measurements, "gated_attempts") != "1" {
		t.Fatalf("readiness gating was not measured: %#v", readiness)
	}
	if len(urls) == 0 || !strings.HasSuffix(urls[len(urls)-1], "/health") {
		t.Fatalf("final health check did not use the liveness endpoint: %#v", urls)
	}
}

func TestPodRecoveryWaitsForReplacement(t *testing.T) {
	podObservations := 0
	runner := runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		result := successfulCommand(request)
		if request.Name == "kubectl" && containsArgument(request.Args, "pods") {
			podObservations++
			if podObservations == 3 {
				result.Stdout = degradedPodList
			}
		}
		return result
	})
	outcome := fixedService(runner).Run(context.Background(), fixturePath(t), Options{})
	recovery := evidenceByID(outcome.Run.Evidence, "pod-recovery")
	if outcome.ExitCode != 0 || recovery == nil || recovery.Status != model.StatusPass || podObservations < 4 {
		t.Fatalf("replacement was not observed: outcome=%#v observations=%d", outcome, podObservations)
	}
}

func TestPodRecoveryRecordsTrafficFailure(t *testing.T) {
	service := fixedService(successRunner())
	probeCalls := 0
	service.probe = func(context.Context, string) (int, error) {
		probeCalls++
		if probeCalls == 3 {
			return 503, errors.New("temporary failure")
		}
		return 200, nil
	}
	outcome := service.Run(context.Background(), fixturePath(t), Options{})
	recovery := evidenceByID(outcome.Run.Evidence, "pod-recovery")
	if outcome.ExitCode != 1 || outcome.Run.Status != model.StatusFail || recovery == nil || measurementValue(recovery.Measurements, "failed_requests") != "1" {
		t.Fatalf("traffic failure was not recorded: %#v", outcome)
	}
}

func TestPodRecoveryTimeoutIsApplicationFailure(t *testing.T) {
	deleted := false
	runner := runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		result := successfulCommand(request)
		if request.Name == "kubectl" && containsArgument(request.Args, "delete") {
			deleted = true
		}
		if deleted && request.Name == "kubectl" && containsArgument(request.Args, "pods") {
			result.Stdout = degradedPodList
		}
		return result
	})
	service := fixedService(runner)
	service.recoveryTimeout = 5 * time.Millisecond
	outcome := service.Run(context.Background(), fixturePath(t), Options{})
	recovery := evidenceByID(outcome.Run.Evidence, "pod-recovery")
	if outcome.ExitCode != 1 || recovery == nil || recovery.Status != model.StatusFail {
		t.Fatalf("recovery timeout should be an observed failure: %#v", outcome)
	}
}

func TestClusterCreateFailureUsesFreshCleanupContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var cleanupContextError error
	runner := runnerFunc(func(callCtx context.Context, request command.Request) model.CommandResult {
		result := model.CommandResult{Command: request.Name, Arguments: request.Args}
		if request.Name == "trivy" {
			result.Stdout = `{"Results":[]}`
		}
		if request.Name == "k3d" && containsArgument(request.Args, "create") {
			cancel()
			result.ExitCode = -1
			result.FailureType = model.FailureCanceled
		}
		if (request.Name == "k3d" && containsArgument(request.Args, "delete")) || (request.Name == "docker" && containsArgument(request.Args, "rm")) {
			cleanupContextError = callCtx.Err()
		}
		return result
	})
	outcome := fixedService(runner).Run(ctx, fixturePath(t), Options{})
	if outcome.ExitCode != 2 || outcome.Run.Status != model.StatusError {
		t.Fatalf("unexpected cluster failure outcome: %#v", outcome)
	}
	if cleanupContextError != nil {
		t.Fatalf("cleanup inherited canceled context: %v", cleanupContextError)
	}
}

func TestKeepEnvironmentSkipsDelete(t *testing.T) {
	var deleted bool
	runner := runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		if request.Name == "k3d" && containsArgument(request.Args, "delete") {
			deleted = true
		}
		result := model.CommandResult{Command: request.Name, Arguments: request.Args}
		if request.Name == "trivy" {
			result.Stdout = `{"Results":[]}`
		}
		if request.Name == "kubectl" && containsArgument(request.Args, "pods") {
			result.Stdout = readyPodList
		}
		return result
	})
	outcome := fixedService(runner).Run(context.Background(), fixturePath(t), Options{KeepEnvironment: true})
	if outcome.ExitCode != 0 || !outcome.Run.Environment.Kept || deleted {
		t.Fatalf("unexpected retained environment behavior: %#v deleted=%v", outcome, deleted)
	}
}

func TestAmbiguousPortStopsBeforeExecution(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "package.json"), []byte(`{"name":"ambiguous"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "Dockerfile"), []byte("FROM node:22-alpine\nEXPOSE 8080 9090\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	service := fixedService(runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		called = true
		return model.CommandResult{Command: request.Name}
	}))
	outcome := service.Run(context.Background(), directory, Options{})
	if outcome.ExitCode != 1 || outcome.Run.Status != model.StatusFail || called {
		t.Fatalf("ambiguous plan should fail before execution: %#v called=%v", outcome, called)
	}
}

func fixedService(runner command.Runner) *Service {
	service := New(runner)
	service.newID = func() (string, error) { return "0123abcd", nil }
	service.now = clock(time.Unix(100, 0), time.Unix(101, 0))
	service.port = func() (int, error) { return 18080, nil }
	service.probe = func(context.Context, string) (int, error) { return 200, nil }
	service.poll = time.Millisecond
	service.readinessTimeout = 50 * time.Millisecond
	service.recoveryTimeout = 50 * time.Millisecond
	return service
}

func fixturePath(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "testdata", "healthy-node"))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func containsArgument(arguments []string, wanted string) bool {
	for _, argument := range arguments {
		if argument == wanted {
			return true
		}
	}
	return false
}

func hasCommand(calls []command.Request, name, argument string) bool {
	for _, call := range calls {
		if call.Name == name && containsArgument(call.Args, argument) {
			return true
		}
	}
	return false
}

func hasDiagnosticCode(values []model.Diagnostic, code string) bool {
	for _, item := range values {
		if item.Code == code {
			return true
		}
	}
	return false
}

func successRunner() command.Runner {
	return runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		return successfulCommand(request)
	})
}

func successfulCommand(request command.Request) model.CommandResult {
	result := model.CommandResult{Command: request.Name, Arguments: request.Args}
	if request.Name == "trivy" {
		result.Stdout = `{"Results":[]}`
	}
	if request.Name == "kubectl" && containsArgument(request.Args, "pods") {
		result.Stdout = readyPodList
	}
	return result
}

func evidenceByID(values []model.Evidence, id string) *model.Evidence {
	for index := range values {
		if values[index].ExperimentID == id {
			return &values[index]
		}
	}
	return nil
}

func measurementValue(values []model.Measurement, name string) string {
	for _, item := range values {
		if item.Name == name {
			return item.Value
		}
	}
	return ""
}

func findingByID(values []model.Finding, id string) *model.Finding {
	for index := range values {
		if values[index].ID == id {
			return &values[index]
		}
	}
	return nil
}

func clock(values ...time.Time) func() time.Time {
	index := 0
	return func() time.Time {
		if index >= len(values) {
			return values[len(values)-1]
		}
		value := values[index]
		index++
		return value
	}
}

const readyPodList = `{"apiVersion":"v1","kind":"PodList","items":[{"metadata":{"name":"api-a"},"status":{"conditions":[{"type":"Ready","status":"True"}],"containerStatuses":[{"restartCount":0}]}},{"metadata":{"name":"api-b"},"status":{"conditions":[{"type":"Ready","status":"True"}],"containerStatuses":[{"restartCount":0}]}}]}`

const degradedPodList = `{"apiVersion":"v1","kind":"PodList","items":[{"metadata":{"name":"api-a"},"status":{"conditions":[{"type":"Ready","status":"True"}]}},{"metadata":{"name":"api-new"},"status":{"conditions":[{"type":"Ready","status":"False"}]}}]}`
