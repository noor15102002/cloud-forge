package verification

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

func TestEarlyBuilderCleanupRelease(t *testing.T) {
	for _, profile := range []string{"backend-rollout", "backend-no-rollout", "worker"} {
		for _, mode := range []string{"retained-container", "retained-volume", "fallback-complete", "truncated-removal"} {
			t.Run(profile+"/"+mode, func(t *testing.T) {
				var root string
				var current plan
				if profile == "worker" {
					root, current = workerFixture(t)
				} else {
					root, current = backendIntegrationFixture(t)
				}
				base := &backendIntegrationRunner{t: t, current: current}
				const containerName = "buildx_buildkit_cloudforge-0123abcd0"
				const volumeName = containerName + "_state"
				id := strings.Repeat("b", 64)
				containerExists, volumeExists := true, true
				builderRemovals, fallbackRemovals, clusterAttempts := 0, 0, 0
				runner := runnerFunc(func(ctx context.Context, request command.Request) model.CommandResult {
					result := base.Run(ctx, request)
					args := request.Args
					if request.Name == "k3d" && containsArgument(args, "create") {
						clusterAttempts++
						result.ExitCode, result.FailureType = 1, model.FailureExit
						result.Stderr = "intentional stop after builder cleanup"
					}
					if request.Name != "docker" || len(args) < 2 {
						return result
					}
					if args[0] == "buildx" && args[1] == "rm" {
						builderRemovals++
						if mode == "retained-volume" {
							containerExists = false
						}
						if mode == "truncated-removal" {
							containerExists, volumeExists = false, false
							if builderRemovals == 1 {
								result.Truncated = true
							}
						}
					}
					reference := args[len(args)-1]
					if args[1] == "inspect" && (reference == containerName || reference == volumeName || reference == id) {
						result.ExitCode, result.FailureType, result.Stderr = 0, model.FailureNone, ""
						exists := containerExists
						if args[0] == "volume" {
							exists = volumeExists
						}
						if !exists {
							result.ExitCode, result.FailureType = 1, model.FailureExit
							result.Stdout = ""
							result.Stderr = "Error: No such object: " + reference
						} else if args[0] == "container" {
							result.Stdout = fmt.Sprintf("%q %q true %q %q", id, "/"+containerName, "volume", volumeName)
						} else {
							result.Stdout = fmt.Sprintf("%q %q %q %q", volumeName, "local", "local", "2026-09-21T12:00:00Z")
						}
					}
					if args[1] == "rm" && (reference == containerName || reference == volumeName || reference == id) && (args[0] == "container" || args[0] == "volume") {
						fallbackRemovals++
						if ctx.Err() != nil {
							t.Fatal("cleanup inherited cancellation")
						}
						if mode == "fallback-complete" {
							if args[0] == "container" {
								containerExists = false
							} else {
								volumeExists = false
							}
						}
					}
					return result
				})
				service := New(clusterProvisionFixture(runner))
				service.newID = func() (string, error) { return "0123abcd", nil }
				service.backendCapacity = func(context.Context, command.Runner, *Outcome) bool { return true }
				options := testOptions()
				if profile == "backend-no-rollout" {
					options.OnPlan = func(plan model.VerificationPlan) {
						for i := range plan.Capabilities {
							if plan.Capabilities[i].Name == "rolling-deployment" {
								plan.Capabilities[i].Disposition = "skipped"
							}
						}
					}
				}
				out := service.Run(context.Background(), root, options)
				cleanup := evidenceByID(out.Run.Evidence, "environment-cleanup")
				build := evidenceByID(out.Run.Evidence, "container-build")
				rollout := evidenceByID(out.Run.Evidence, "rollout-image-build")
				wantCleanup := model.StatusError
				if mode == "fallback-complete" {
					wantCleanup = model.StatusPass
				}
				if out.Run.Status != model.StatusError || out.ExitCode != 2 || cleanup == nil || cleanup.Status != wantCleanup || cleanup.Execution == nil || !cleanup.Execution.Executed || build == nil || build.Status != model.StatusPass {
					t.Fatalf("mode=%s status=%s exit=%d cleanup=%+v diagnostics=%+v remaining=%v/%v", mode, out.Run.Status, out.ExitCode, cleanup, out.Run.Diagnostics, containerExists, volumeExists)
				}
				if profile != "backend-no-rollout" && (rollout == nil || rollout.Status != model.StatusPass) {
					t.Fatal("completed rollout build evidence was lost")
				}
				if mode == "fallback-complete" {
					if clusterAttempts != 1 || containerExists || volumeExists || fallbackRemovals != 2 || cleanupDiagnostic(out) || !hasDiagnosticCode(out.Run.Diagnostics, "cluster_create_failed") {
						t.Fatalf("absence was not established: attempts=%d remaining=%v/%v removals=%d diagnostics=%+v", clusterAttempts, containerExists, volumeExists, fallbackRemovals, out.Run.Diagnostics)
					}
				} else if clusterAttempts != 0 || !cleanupDiagnostic(out) {
					t.Fatalf("unverified early cleanup started cluster: attempts=%d diagnostics=%+v", clusterAttempts, out.Run.Diagnostics)
				}
				if mode == "truncated-removal" && !hasDiagnosticCode(out.Run.Diagnostics, "builder_cleanup_failed") {
					t.Fatal("original truncated removal error was lost")
				}
			})
		}
	}
}
