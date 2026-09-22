package verification

import (
	"context"
	"strings"
	"testing"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

func TestClusterProvisioningFailureKeepsEvidenceAndCleansOwnedState(t *testing.T) {
	for _, tc := range []struct {
		name, kind, operation string
		cancel                bool
	}{
		{"network-create-failed", "network", "create", false},
		{"network-create-canceled", "network", "create", true},
		{"volume-create-failed", "volume", "create", false},
		{"volume-create-canceled", "volume", "create", true},
		{"network-observation-invalid", "network", "inspect", false},
		{"volume-observation-invalid", "volume", "inspect", false},
		{"cluster-create-failed", "cluster", "create", false},
		{"cluster-create-canceled", "cluster", "create", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			base := clusterProvisionFixture(successRunner())
			injected := false
			clusterStarted := false
			removals := 0
			runner := runnerFunc(func(callCtx context.Context, request command.Request) model.CommandResult {
				result := base.Run(callCtx, request)
				args := request.Args
				if len(args) < 2 {
					return result
				}
				if request.Name == "docker" && (args[0] == "network" || args[0] == "volume") && args[1] == "rm" {
					removals++
					if callCtx.Err() != nil {
						t.Fatal("private resource cleanup inherited cancellation")
					}
				}
				if request.Name == "k3d" && args[0] == "cluster" && args[1] == "create" {
					clusterStarted = true
				}
				if !injected && args[0] == tc.kind && args[1] == tc.operation && (request.Name == "docker" || request.Name == "k3d") {
					injected = true
					if tc.operation == "inspect" {
						result.Stdout = "{" // The owned object exists, but its first observation is unusable.
					} else {
						result.ExitCode, result.FailureType = 1, model.FailureExit
						if tc.cancel {
							cancel()
							result.ExitCode, result.FailureType = -1, model.FailureCanceled
						}
					}
				}
				return result
			})
			service := New(runner)
			service.newID = func() (string, error) { return "0123abcd", nil }
			out := service.Run(ctx, fixturePath(t), testOptions())
			diagnostic := "cluster_resource_create_failed"
			if tc.kind == "cluster" {
				diagnostic = "cluster_create_failed"
			}
			if !injected || out.Run.Status != model.StatusError || out.ExitCode != 2 || !hasDiagnosticCode(out.Run.Diagnostics, diagnostic) || hasDiagnosticCode(out.Run.Diagnostics, "verification_canceled") != tc.cancel {
				t.Fatalf("partial provision error lost: injected=%v status=%s exit=%d diagnostics=%+v", injected, out.Run.Status, out.ExitCode, out.Run.Diagnostics)
			}
			for _, id := range []string{"container-build", "container-scan", "environment-cleanup"} {
				if e := evidenceByID(out.Run.Evidence, id); e == nil || e.Status != model.StatusPass {
					t.Fatalf("earlier evidence or cleanup %s not retained: %+v; diagnostics=%+v", id, e, out.Run.Diagnostics)
				}
			}
			if clusterStarted != (tc.kind == "cluster") || removals < 1 {
				t.Fatalf("unsafe continuation or skipped cleanup: cluster=%v removals=%d", clusterStarted, removals)
			}
			for _, e := range out.Run.Evidence {
				if e.ExperimentID == "deployment-readiness" && e.Execution.Executed {
					t.Fatal("application deployed after failed infrastructure creation")
				}
			}
			for _, kind := range []string{"network", "volume"} {
				result := base.Run(context.Background(), command.Request{Name: "docker", Args: []string{kind, "ls", "--filter", "name=k3d-cloudforge-0123abcd", "--format", "unused"}})
				if failed(result) || strings.TrimSpace(result.Stdout) != "" {
					t.Fatalf("private %s remained after cleanup", kind)
				}
			}
		})
	}
}
