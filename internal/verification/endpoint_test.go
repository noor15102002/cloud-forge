package verification

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/internal/runtimepolicy"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

func TestRuntimePinsResolvedEndpointThroughCleanupDespiteEnvironmentChange(t *testing.T) {
	t.Setenv("DOCKER_CONTEXT", "PRIVATE_SELECTED_CONTEXT")
	t.Setenv("DOCKER_HOST", "ssh://PRIVATE_IGNORED_HOST")
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("K3D_IMAGE_TOOLS", "PRIVATE_HELPER_IMAGE")
	t.Setenv("K3D_IMAGE_LOADBALANCER", "PRIVATE_LOADBALANCER_IMAGE")
	t.Setenv("K3D_HELPER_IMAGE_TAG", "PRIVATE_HELPER_TAG")
	resolved := 0
	phases := map[string]bool{}
	runner := runnerFunc(func(_ context.Context, req command.Request) model.CommandResult {
		if req.Name == "docker" && slices.Contains(req.Args, "context") {
			resolved++
			if len(req.Env) != 1 || req.Env[0] != "DOCKER_HOST=" {
				t.Fatal("endpoint was pinned before resolving user selection")
			}
			t.Setenv("DOCKER_CONTEXT", "PRIVATE_REPLACEMENT_CONTEXT")
			t.Setenv("DOCKER_HOST", "tcp://PRIVATE_NEW_HOST:2376")
			t.Setenv("DOCKER_TLS_VERIFY", "1")
			return model.CommandResult{Stdout: `"unix:///var/run/docker.sock"`}
		}
		if req.Name == "docker" || req.Name == "k3d" || req.Name == "trivy" {
			env := map[string]string{}
			for _, entry := range req.Env {
				key, value, _ := strings.Cut(entry, "=")
				env[key] = value
			}
			if env["DOCKER_HOST"] != runtimepolicy.DockerEndpoint || env["DOCKER_CONTEXT"] != "" || env["DOCKER_TLS_VERIFY"] != "" {
				t.Fatalf("operation selected a different Docker endpoint: %+v", req)
			}
			if req.Name == "k3d" {
				for _, key := range []string{"K3D_IMAGE_TOOLS", "K3D_IMAGE_LOADBALANCER", "K3D_HELPER_IMAGE_TAG"} {
					if value, present := env[key]; !present || value != "" {
						t.Fatalf("ambient helper image override reached k3d: %s", key)
					}
				}
			}
			if req.Name == "docker" && slices.Contains(req.Args, "info") {
				phases["preflight"] = true
			}
			if req.Name == "docker" && slices.Contains(req.Args, "build") {
				phases["build"] = true
			}
			if req.Name == "trivy" && slices.Contains(req.Args, "image") {
				phases["scan"] = true
			}
			if req.Name == "k3d" && slices.Contains(req.Args, "create") {
				phases["cluster"] = true
			}
			if req.Name == "k3d" && slices.Contains(req.Args, "import") {
				phases["import"] = true
			}
			if req.Name == "k3d" && slices.Contains(req.Args, "delete") {
				phases["cleanup"] = true
			}
		}
		return successfulCommand(req)
	})
	out := fixedService(runner).Run(context.Background(), fixturePath(t), testOptions())
	if resolved != 1 || out.ExitCode != 0 {
		t.Fatalf("endpoint resolve/run failed: resolved=%d out=%+v", resolved, out)
	}
	for _, phase := range []string{"preflight", "build", "scan", "cluster", "import", "cleanup"} {
		if !phases[phase] {
			t.Errorf("missing phase %s", phase)
		}
	}
	encoded, _ := json.Marshal(out.Run)
	if strings.Contains(string(encoded), "PRIVATE_") {
		t.Fatal("report leaked private endpoint")
	}
	if !strings.Contains(string(encoded), "environment_context") {
		t.Fatal("safe resolution provenance missing")
	}
}
func TestUnsupportedOrUnobservableEndpointStopsBeforeApplicationExecution(t *testing.T) {
	for _, value := range []string{`"ssh://PRIVATE_HOST"`, `"unix:///private/rootless.sock"`, `PRIVATE_BAD_JSON`} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("DOCKER_CONTEXT", "PRIVATE_CONTEXT")
			calls := 0
			runner := runnerFunc(func(_ context.Context, req command.Request) model.CommandResult {
				calls++
				if req.Name != "docker" || !slices.Contains(req.Args, "context") {
					t.Fatalf("ran beyond endpoint gate: %+v", req)
				}
				return model.CommandResult{Stdout: value}
			})
			out := fixedService(runner).Run(context.Background(), fixturePath(t), testOptions())
			want := model.StatusBlocked
			if value == "PRIVATE_BAD_JSON" {
				want = model.StatusError
			}
			if out.Run.Status != want || calls != 1 {
				t.Fatalf("endpoint gate failed: calls=%d out=%+v", calls, out)
			}
			for _, evidence := range out.Run.Evidence {
				if evidence.Execution != nil && evidence.Execution.Executed {
					t.Fatalf("application execution after endpoint rejection: %+v", evidence)
				}
			}
			encoded, _ := json.Marshal(out.Run)
			if strings.Contains(string(encoded), "PRIVATE_") || strings.Contains(string(encoded), "rootless.sock") {
				t.Fatal("endpoint details leaked")
			}
		})
	}
}
func TestBuildxFailureBlocksBeforeRuntimeMutation(t *testing.T) {
	calls := []command.Request{}
	runner := runnerFunc(func(_ context.Context, req command.Request) model.CommandResult {
		calls = append(calls, req)
		if req.Name == "docker" && slices.Contains(req.Args, "buildx") && slices.Contains(req.Args, "version") {
			return model.CommandResult{ExitCode: 1, FailureType: model.FailureExit}
		}
		return successfulCommand(req)
	})
	out := fixedService(runner).Run(context.Background(), fixturePath(t), testOptions())
	if out.ExitCode != 2 || !hasDiagnosticCode(out.Run.Diagnostics, "runtime_version_unavailable") {
		t.Fatalf("missing plugin was not an observation error: %+v", out)
	}
	for _, req := range calls {
		if slices.Contains(req.Args, "create") || slices.Contains(req.Args, "build") {
			t.Fatalf("missing plugin reached mutation: %+v", req)
		}
	}
}
func TestPlanDoesNotResolveOrRequireRuntimeEndpoint(t *testing.T) {
	t.Setenv("DOCKER_CONTEXT", "PRIVATE_REMOTE_CONTEXT")
	calls := 0
	runner := runnerFunc(func(_ context.Context, req command.Request) model.CommandResult {
		calls++
		return successfulCommand(req)
	})
	options := testOptions()
	options.PlanOnly = true
	out := fixedService(runner).Run(context.Background(), fixturePath(t), options)
	if out.ExitCode != 0 || calls != 0 || hasDiagnosticCode(out.Run.Diagnostics, "docker_endpoint_pinned") {
		t.Fatalf("plan acquired runtime prerequisites: calls=%d out=%+v", calls, out)
	}
}
