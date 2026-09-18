package verification

import (
	"context"
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
	service := New(runner)
	service.newID = func() (string, error) { return "0123abcd", nil }
	service.now = clock(time.Unix(100, 0), time.Unix(101, 0))

	outcome := service.Run(context.Background(), fixturePath(t), Options{})
	if outcome.ExitCode != 0 || outcome.Run.Status != model.StatusPass {
		t.Fatalf("unexpected outcome: %#v", outcome)
	}
	if len(outcome.Run.Evidence) != 2 || outcome.Run.Evidence[1].Measurements[0].Value != "2" {
		t.Fatalf("missing readiness evidence: %#v", outcome.Run.Evidence)
	}
	if len(calls) != 8 || !containsArgument(calls[len(calls)-2].Args, "delete") || !containsArgument(calls[len(calls)-1].Args, "rm") {
		t.Fatalf("unexpected command lifecycle: %#v", calls)
	}
	for _, expected := range []string{"kind: Namespace", "kind: Deployment", "kind: Service", "imagePullPolicy: Never", "path: /ready", "cpu: 100m", "replicas: 2"} {
		if !strings.Contains(manifest, expected) {
			t.Fatalf("generated manifest is missing %q:\n%s", expected, manifest)
		}
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

func TestReadinessFailureStillCleansUp(t *testing.T) {
	var calls []command.Request
	runner := runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		calls = append(calls, request)
		result := model.CommandResult{Command: request.Name, Arguments: request.Args}
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

func TestClusterCreateFailureUsesFreshCleanupContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var cleanupContextError error
	runner := runnerFunc(func(callCtx context.Context, request command.Request) model.CommandResult {
		result := model.CommandResult{Command: request.Name, Arguments: request.Args}
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

const readyPodList = `{"apiVersion":"v1","kind":"PodList","items":[{"status":{"conditions":[{"type":"Ready","status":"True"}],"containerStatuses":[{"restartCount":0}]}},{"status":{"conditions":[{"type":"Ready","status":"True"}],"containerStatuses":[{"restartCount":0}]}}]}`
