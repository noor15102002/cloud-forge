package verification

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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
		if request.Name == "docker" && containsArgument(request.Args, "port") {
			result.Stdout = "127.0.0.1:18080\n"
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
	if len(outcome.Run.Evidence) != 6 || outcome.Run.Evidence[2].Measurements[0].Value != "2" {
		t.Fatalf("missing readiness evidence: %#v", outcome.Run.Evidence)
	}
	rollout := evidenceByID(outcome.Run.Evidence, "rolling-deployment")
	if rollout == nil || measurementValue(rollout.Measurements, "source_version") != "a" || measurementValue(rollout.Measurements, "target_version") != "b" || measurementValue(rollout.Measurements, "version_transitions") != "1" {
		t.Fatalf("missing rolling deployment evidence: %#v", rollout)
	}
	if len(calls) != 22 || !containsArgument(calls[len(calls)-3].Args, "delete") || !containsArgument(calls[len(calls)-2].Args, "rm") || !containsArgument(calls[len(calls)-1].Args, "rm") {
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
	if !hasCommand(calls, "k3d", "127.0.0.1:0:30080@server:0") {
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
		if request.Name == "docker" && containsArgument(request.Args, "port") {
			result.Stdout = "127.0.0.1:18080\n"
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
		if request.Name == "docker" && containsArgument(request.Args, "port") {
			result.Stdout = "127.0.0.1:18080\n"
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
	deleteCount := 0
	degradedOnce := false
	runner := runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		result := successfulCommand(request)
		if request.Name == "kubectl" && containsArgument(request.Args, "delete") {
			deleteCount++
		}
		if deleteCount == 2 && !degradedOnce && request.Name == "kubectl" && containsArgument(request.Args, "pods") {
			degradedOnce = true
			result.Stdout = degradedPodList
		}
		return result
	})
	outcome := fixedService(runner).Run(context.Background(), fixturePath(t), Options{})
	recovery := evidenceByID(outcome.Run.Evidence, "pod-recovery")
	if outcome.ExitCode != 0 || recovery == nil || recovery.Status != model.StatusPass || !degradedOnce {
		t.Fatalf("replacement was not observed: outcome=%#v degraded=%v", outcome, degradedOnce)
	}
}

func TestPodRecoveryRecordsTrafficFailure(t *testing.T) {
	var deleteCount atomic.Int32
	runner := runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		if request.Name == "kubectl" && containsArgument(request.Args, "delete") {
			deleteCount.Add(1)
		}
		return successfulCommand(request)
	})
	service := fixedService(runner)
	var failedOnce atomic.Bool
	service.probe = func(context.Context, string) (int, error) {
		if deleteCount.Load() == 2 && failedOnce.CompareAndSwap(false, true) {
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
	deleteCount := 0
	runner := runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		result := successfulCommand(request)
		if request.Name == "kubectl" && containsArgument(request.Args, "delete") {
			deleteCount++
		}
		if deleteCount == 2 && request.Name == "kubectl" && containsArgument(request.Args, "pods") {
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

func TestGracefulShutdownRecordsDroppedTraffic(t *testing.T) {
	var deleteCount atomic.Int32
	runner := runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		if request.Name == "kubectl" && containsArgument(request.Args, "delete") {
			deleteCount.Add(1)
		}
		result := successfulCommand(request)
		if request.Name == "kubectl" && containsArgument(request.Args, "pods") {
			result.Stdout = readySinglePodList
		}
		return result
	})
	service := fixedService(runner)
	var failedOnce atomic.Bool
	service.probe = func(context.Context, string) (int, error) {
		if deleteCount.Load() == 1 && failedOnce.CompareAndSwap(false, true) {
			return 503, errors.New("connection dropped during termination")
		}
		return 200, nil
	}
	outcome := service.Run(context.Background(), fixtureNamedPath(t, "broken-shutdown"), Options{})
	shutdown := evidenceByID(outcome.Run.Evidence, "graceful-shutdown")
	if outcome.ExitCode != 1 || shutdown == nil || shutdown.Status != model.StatusFail || measurementValue(shutdown.Measurements, "dropped_requests") != "1" {
		t.Fatalf("shutdown traffic failure was not recorded: %#v", outcome)
	}
}

func TestRollingDeploymentTimeoutIsApplicationFailure(t *testing.T) {
	rolloutStarted := false
	runner := runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		result := successfulCommand(request)
		if request.Name == "kubectl" && containsArgument(request.Args, "set") {
			rolloutStarted = true
		}
		if rolloutStarted && request.Name == "kubectl" && containsArgument(request.Args, "pods") {
			result.Stdout = oldVersionPodList
		}
		return result
	})
	service := fixedService(runner)
	service.rolloutTimeout = 5 * time.Millisecond
	outcome := service.Run(context.Background(), fixtureNamedPath(t, "broken-rollout"), Options{})
	rollout := evidenceByID(outcome.Run.Evidence, "rolling-deployment")
	if outcome.ExitCode != 1 || rollout == nil || rollout.Status != model.StatusFail || measurementValue(rollout.Measurements, "target_ready_pods") != "0" {
		t.Fatalf("rollout timeout should be an observed application failure: %#v", outcome)
	}
}

func TestRollingDeploymentCommandFailureIsExecutionError(t *testing.T) {
	runner := runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		result := successfulCommand(request)
		if request.Name == "kubectl" && containsArgument(request.Args, "set") {
			result.ExitCode = 1
			result.FailureType = model.FailureExit
		}
		return result
	})
	outcome := fixedService(runner).Run(context.Background(), fixturePath(t), Options{})
	rollout := evidenceByID(outcome.Run.Evidence, "rolling-deployment")
	if outcome.ExitCode != 2 || rollout == nil || rollout.Status != model.StatusError || !hasDiagnosticCode(outcome.Run.Diagnostics, "rollout_update_failed") {
		t.Fatalf("rollout command failure should remain an execution error: %#v", outcome)
	}
}

func TestPodDeleteErrorPreservesCollectedTraffic(t *testing.T) {
	deleteCount := 0
	runner := runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		result := successfulCommand(request)
		if request.Name == "kubectl" && containsArgument(request.Args, "delete") {
			deleteCount++
			if deleteCount == 2 {
				result.ExitCode = 1
				result.FailureType = model.FailureExit
			}
		}
		return result
	})
	outcome := fixedService(runner).Run(context.Background(), fixturePath(t), Options{})
	recovery := evidenceByID(outcome.Run.Evidence, "pod-recovery")
	if outcome.ExitCode != 2 || recovery == nil || recovery.Status != model.StatusError || measurementValue(recovery.Measurements, "request_count") == "0" {
		t.Fatalf("pod deletion error should preserve collected traffic: %#v", outcome)
	}
}

func TestTrafficCancellationIsNotAnApplicationFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	service := fixedService(successRunner())
	service.probe = func(context.Context, string) (int, error) {
		cancel()
		return 0, context.Canceled
	}
	observed := service.collectTraffic(ctx, "http://127.0.0.1:18080/ready", make(chan trafficSample, 1))
	if observed.Requests != 0 || observed.Failures != 0 {
		t.Fatalf("CloudForge cancellation was counted as application traffic: %#v", observed)
	}
}

func TestOverlappingRequestsCountsOnlyTerminationWindow(t *testing.T) {
	started := time.Unix(100, 0)
	completed := started.Add(100 * time.Millisecond)
	samples := []trafficSample{
		{StartedAt: started.Add(-20 * time.Millisecond), CompletedAt: started.Add(-time.Millisecond)},
		{StartedAt: started.Add(-time.Millisecond), CompletedAt: started.Add(time.Millisecond)},
		{StartedAt: started.Add(50 * time.Millisecond), CompletedAt: started.Add(60 * time.Millisecond)},
		{StartedAt: completed.Add(time.Millisecond), CompletedAt: completed.Add(2 * time.Millisecond)},
	}
	if got := overlappingRequests(samples, started, completed); got != 2 {
		t.Fatalf("expected two requests overlapping termination, got %d", got)
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
		if request.Name == "docker" && containsArgument(request.Args, "port") {
			result.Stdout = "127.0.0.1:18080\n"
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
		if request.Name == "docker" && containsArgument(request.Args, "port") {
			result.Stdout = "127.0.0.1:18080\n"
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

func TestBuildPlanSkipsHTTPSReadinessMeasurement(t *testing.T) {
	analysis := verificationAnalysis([]model.Endpoint{{Purpose: "readiness", Path: "/ready", Port: "http", Protocol: "HTTPS"}})
	planned, err := buildPlan(analysis, "0123abcd")
	if err != nil {
		t.Fatal(err)
	}
	if planned.readinessPath != "" || planned.httpSkipReason == "" {
		t.Fatalf("HTTPS readiness should be preserved for Kubernetes and skipped by the loopback HTTP experiment: %#v", planned)
	}
	if !strings.Contains(string(planned.manifest), "scheme: HTTPS") {
		t.Fatalf("generated manifest did not preserve the HTTPS readiness probe:\n%s", planned.manifest)
	}
}

func TestBuildPlanRejectsHealthProbeOnDifferentPort(t *testing.T) {
	analysis := verificationAnalysis([]model.Endpoint{
		{Purpose: "readiness", Path: "/ready", Port: "http", Protocol: "HTTP"},
		{Purpose: "liveness", Path: "/health", Port: "9090", Protocol: "HTTP"},
	})
	if _, err := buildPlan(analysis, "0123abcd"); err == nil || !strings.Contains(err.Error(), "liveness probe uses port 9090") {
		t.Fatalf("expected mismatched liveness port error, got %v", err)
	}
}

func TestDirectHTTPClientDoesNotUseProxyEnvironment(t *testing.T) {
	client := directHTTPClient()
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil {
		t.Fatalf("loopback probes must use a direct HTTP transport: %#v", client.Transport)
	}
}

func verificationAnalysis(endpoints []model.Endpoint) model.AnalysisResult {
	return model.AnalysisResult{Application: model.Application{
		Name: "api",
		Containers: []model.Container{{
			Ports:  []model.ContainerPort{{Name: "http", Port: 8080, Protocol: "TCP"}},
			Source: model.SourceReference{Path: "Dockerfile"},
		}},
		Kubernetes: model.KubernetesConfiguration{Deployments: []model.Deployment{{
			Name: "api",
			Containers: []model.Container{{
				Ports: []model.ContainerPort{{Name: "http", Port: 8080, Protocol: "TCP"}},
			}},
			Endpoints: endpoints,
		}}},
	}}
}

func fixedService(runner command.Runner) *Service {
	service := New(runner)
	service.newID = func() (string, error) { return "0123abcd", nil }
	service.now = clock(time.Unix(100, 0), time.Unix(101, 0))
	service.probe = func(context.Context, string) (int, error) { return 200, nil }
	service.poll = time.Millisecond
	service.trafficPoll = time.Millisecond
	service.readinessTimeout = 50 * time.Millisecond
	service.recoveryTimeout = 50 * time.Millisecond
	service.rolloutTimeout = 50 * time.Millisecond
	return service
}

func fixturePath(t *testing.T) string {
	return fixtureNamedPath(t, "healthy-node")
}

func fixtureNamedPath(t *testing.T, name string) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "testdata", name))
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
	if request.Name == "docker" && containsArgument(request.Args, "port") {
		result.Stdout = "127.0.0.1:18080\n"
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

const readyPodList = `{"apiVersion":"v1","kind":"PodList","items":[{"metadata":{"name":"api-a"},"spec":{"containers":[{"name":"application","image":"cloudforge/healthy-node-api:0123abcd-b"}]},"status":{"conditions":[{"type":"Ready","status":"True"}],"containerStatuses":[{"restartCount":0}]}},{"metadata":{"name":"api-b"},"spec":{"containers":[{"name":"application","image":"cloudforge/healthy-node-api:0123abcd-b"}]},"status":{"conditions":[{"type":"Ready","status":"True"}],"containerStatuses":[{"restartCount":0}]}}]}`

const degradedPodList = `{"apiVersion":"v1","kind":"PodList","items":[{"metadata":{"name":"api-a"},"status":{"conditions":[{"type":"Ready","status":"True"}]}},{"metadata":{"name":"api-new"},"status":{"conditions":[{"type":"Ready","status":"False"}]}}]}`

const readySinglePodList = `{"apiVersion":"v1","kind":"PodList","items":[{"metadata":{"name":"api-a"},"spec":{"containers":[{"name":"application","image":"cloudforge/broken-shutdown-api:0123abcd-a"}]},"status":{"conditions":[{"type":"Ready","status":"True"}],"containerStatuses":[{"restartCount":0}]}}]}`

const oldVersionPodList = `{"apiVersion":"v1","kind":"PodList","items":[{"metadata":{"name":"api-a"},"spec":{"containers":[{"name":"application","image":"cloudforge/healthy-node-api:0123abcd-a"}]},"status":{"conditions":[{"type":"Ready","status":"True"}]}},{"metadata":{"name":"api-b"},"spec":{"containers":[{"name":"application","image":"cloudforge/healthy-node-api:0123abcd-a"}]},"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}`
