package verification

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

func cleanupDiagnostic(out Outcome) bool {
	for _, diagnostic := range out.Run.Diagnostics {
		if strings.Contains(diagnostic.Code, "cleanup") && diagnostic.Status == model.StatusError {
			return true
		}
	}
	return false
}

func TestCleanupObservationServiceContract(t *testing.T) {
	for _, kind := range []string{"container", "network", "volume"} {
		for _, mode := range []string{"complete", "truncated", "malformed", "retained", "nonzero"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				cleaning, clusterDeleted := false, false
				removals := 0
				prefix := "k3d-cloudforge-0123abcd"
				runner := runnerFunc(func(ctx context.Context, request command.Request) model.CommandResult {
					result := successfulCommand(request)
					args := request.Args
					if request.Name == "docker" && len(args) > 1 && args[0] == "buildx" && args[1] == "rm" {
						cleaning = true
					}
					if request.Name == "k3d" && containsArgument(args, "delete") {
						clusterDeleted = true
					}
					if !cleaning || request.Name != "docker" || len(args) < 2 {
						return result
					}
					if ctx.Err() != nil {
						t.Fatal("cleanup inherited cancellation")
					}
					if args[1] == "ls" && args[0] == kind {
						switch mode {
						case "truncated":
							result.Truncated = true
						case "malformed":
							result.Stdout = "incomplete inventory record\n"
						case "nonzero":
							result.ExitCode, result.FailureType = 1, model.FailureExit
						case "retained":
							name := prefix
							if kind == "container" {
								name += "-server-0"
							}
							if kind == "volume" {
								name += "-images"
								result.Stdout = name + "\n"
							} else {
								id := strings.Repeat("a", 64)
								if kind == "network" {
									id = strings.Repeat("d", 64)
								}
								result.Stdout = id + " " + name + "\n"
							}
						}
					}
					// A current run-marked node establishes the network/volume's
					// relationship before normal k3d deletion removes that node.
					if mode == "retained" && kind != "container" && !clusterDeleted && args[0] == "container" && args[1] == "ls" {
						result.Stdout = strings.Repeat("b", 64) + " " + prefix + "-server-0\n"
					}
					if mode == "retained" && args[1] == "inspect" && (args[0] == "container" || args[0] == "network" || args[0] == "volume") && !strings.Contains(args[len(args)-1], "buildx_buildkit") {
						result.ExitCode, result.FailureType, result.Stderr = 0, model.FailureNone, ""
						switch args[0] {
						case "container":
							result.Stdout = fmt.Sprintf("%q %q true true true %q %q true false", args[len(args)-1], prefix+"-server-0", strings.Repeat("d", 64), prefix+"-images")
						case "network":
							result.Stdout = fmt.Sprintf("%q %q true true true %q", args[len(args)-1], prefix, "")
						case "volume":
							result.Stdout = fmt.Sprintf("%q %q true true true %q", prefix+"-images", prefix+"-images", "2026-09-22T00:00:00Z")
						}
					}
					if args[0] == kind && args[1] == "rm" {
						removals++
					}
					return result
				})
				out := fixedService(runner).Run(context.Background(), fixturePath(t), testOptions())
				cleanup := evidenceByID(out.Run.Evidence, "environment-cleanup")
				build := evidenceByID(out.Run.Evidence, "container-build")
				wantStatus, wantCleanup, wantExit := model.StatusError, model.StatusError, 2
				if mode == "complete" {
					wantStatus, wantCleanup, wantExit = model.StatusWarn, model.StatusPass, 0
				}
				if out.Run.Status != wantStatus || out.ExitCode != wantExit || cleanup == nil || cleanup.Status != wantCleanup || !cleanup.Execution.Executed || cleanupDiagnostic(out) != (mode != "complete") || build == nil || build.Status != model.StatusPass {
					t.Fatalf("mode=%s status=%s exit=%d cleanup=%+v diagnostics=%+v", mode, out.Run.Status, out.ExitCode, cleanup, out.Run.Diagnostics)
				}
				if mode == "retained" && removals != 1 {
					t.Fatalf("retained remnant did not reach verified removal: %d", removals)
				}
			})
		}
	}
}

func TestApplicationFailureAndRestorationSurviveCleanupError(t *testing.T) {
	var deletes atomic.Int32
	cleaning := false
	runner := runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		result := successfulCommand(request)
		if request.Name == "kubectl" && containsArgument(request.Args, "delete") && containsArgument(request.Args, "pod") {
			deletes.Add(1)
		}
		if request.Name == "docker" && len(request.Args) > 1 && request.Args[0] == "buildx" && request.Args[1] == "rm" {
			cleaning = true
		}
		if cleaning && request.Name == "docker" && len(request.Args) > 1 && request.Args[0] == "network" && request.Args[1] == "ls" {
			result.Truncated = true
		}
		return result
	})
	service := fixedService(runner)
	var failedOnce atomic.Bool
	service.probe = func(context.Context, string) (int, error) {
		if deletes.Load() == 1 && failedOnce.CompareAndSwap(false, true) {
			return 503, nil
		}
		return 200, nil
	}
	out := service.Run(context.Background(), fixturePath(t), testOptions())
	shutdown := evidenceByID(out.Run.Evidence, "graceful-shutdown")
	cleanup := evidenceByID(out.Run.Evidence, "environment-cleanup")
	if out.Run.Status != model.StatusError || out.ExitCode != 2 || shutdown == nil || shutdown.Status != model.StatusFail || shutdown.Recovery == nil || shutdown.Recovery.Status != model.StatusPass || cleanup == nil || cleanup.Status != model.StatusError || !cleanupDiagnostic(out) {
		t.Fatalf("original failure/restoration lost: overall=%s exit=%d shutdown=%+v cleanup=%+v diagnostics=%+v", out.Run.Status, out.ExitCode, shutdown, cleanup, out.Run.Diagnostics)
	}
	if later := evidenceByID(out.Run.Evidence, "rolling-deployment"); later == nil || later.Status != model.StatusPass {
		t.Fatal("cleanup changed later independent application evidence")
	}
}

func TestCanceledPartialClusterCleansOnlyProvenCurrentObjects(t *testing.T) {
	for _, own := range []bool{true, false} {
		t.Run(fmt.Sprint(own), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			created, exists := false, false
			removals := 0
			id := strings.Repeat("a", 64)
			name := "k3d-cloudforge-0123abcd-server-0"
			runner := runnerFunc(func(callCtx context.Context, request command.Request) model.CommandResult {
				result := successfulCommand(request)
				args := request.Args
				if request.Name == "k3d" && containsArgument(args, "create") {
					created, exists = true, true
					cancel()
					result.ExitCode, result.FailureType = -1, model.FailureCanceled
				}
				if request.Name == "k3d" && containsArgument(args, "delete") {
					t.Fatal("failed creation authorized cluster-name deletion")
				}
				if created && request.Name == "docker" && len(args) > 1 && args[0] == "container" {
					if callCtx.Err() != nil {
						t.Fatal("cleanup used canceled context")
					}
					if args[1] == "ls" && exists {
						result.Stdout = id + " " + name + "\n"
					}
					if args[1] == "inspect" && args[len(args)-1] == id {
						result.Stdout = fmt.Sprintf("%q %q true true %t %q %q true false", id, name, own, strings.Repeat("d", 64), "k3d-cloudforge-0123abcd-images")
					}
					if args[1] == "rm" {
						if !own {
							t.Fatal("foreign same-name resource removed")
						}
						removals++
						exists = false
					}
				}
				return result
			})
			out := fixedService(runner).Run(ctx, fixturePath(t), testOptions())
			cleanup := evidenceByID(out.Run.Evidence, "environment-cleanup")
			want := model.StatusError
			if own {
				want = model.StatusPass
			}
			if out.Run.Status != model.StatusError || out.ExitCode != 2 || cleanup == nil || cleanup.Status != want || !hasDiagnosticCode(out.Run.Diagnostics, "verification_canceled") || cleanupDiagnostic(out) == own || own && (exists || removals != 1) || !own && (!exists || removals != 0) {
				t.Fatalf("cancellation ownership failed: status=%s exit=%d cleanup=%+v diagnostics=%+v", out.Run.Status, out.ExitCode, cleanup, out.Run.Diagnostics)
			}
		})
	}
}

func TestPrivateWorkspaceCleanupAndKubeconfigContract(t *testing.T) {
	for _, mode := range []string{"removed", "remove_error", "retained_without_error", "kept"} {
		t.Run(mode, func(t *testing.T) {
			userConfig := filepath.Join(t.TempDir(), "user-kubeconfig")
			sentinel := []byte("untouched user configuration")
			if err := os.WriteFile(userConfig, sentinel, 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("KUBECONFIG", userConfig)
			service := fixedService(successRunner())
			workspace := ""
			service.removeWorkspace = func(path string) error {
				workspace = path
				if mode == "remove_error" {
					return errors.New("PRIVATE filesystem failure text")
				}
				if mode == "retained_without_error" {
					return nil
				}
				return os.RemoveAll(path)
			}
			options := testOptions()
			options.KeepEnvironment = mode == "kept"
			out := service.Run(context.Background(), fixturePath(t), options)
			cleanup := evidenceByID(out.Run.Evidence, "environment-cleanup")
			defer func() {
				if err := os.RemoveAll(workspace); err != nil {
					t.Error(err)
				}
			}()
			failed := mode == "remove_error" || mode == "retained_without_error"
			want, wantOverall, wantExit := model.StatusPass, model.StatusWarn, 0
			if failed {
				want, wantOverall, wantExit = model.StatusError, model.StatusError, 2
			}
			if mode == "kept" {
				want = model.StatusSkipped
			}
			if out.Run.Status != wantOverall || out.ExitCode != wantExit || cleanup == nil || cleanup.Status != want || hasDiagnosticCode(out.Run.Diagnostics, "workspace_cleanup_failed") != failed {
				t.Fatalf("workspace contract failed: mode=%s status=%s exit=%d cleanup=%+v diagnostics=%+v", mode, out.Run.Status, out.ExitCode, cleanup, out.Run.Diagnostics)
			}
			// #nosec G304 -- this test reads only its own private kubeconfig sentinel.
			if bytes, err := os.ReadFile(userConfig); err != nil || string(bytes) != string(sentinel) {
				t.Fatal("user kubeconfig changed")
			}
			if !failed {
				if _, err := os.Stat(workspace); !os.IsNotExist(err) {
					t.Fatal("private workspace survived")
				}
			}
			for _, d := range out.Run.Diagnostics {
				if strings.Contains(d.Guidance, "PRIVATE") {
					t.Fatal("arbitrary filesystem failure text leaked")
				}
			}
		})
	}
}

func TestUntaggedImageInventoryCannotHideCleanupUncertainty(t *testing.T) {
	cleaning := false
	runner := runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		result := successfulCommand(request)
		if request.Name == "docker" && len(request.Args) > 1 && request.Args[0] == "buildx" && request.Args[1] == "rm" {
			cleaning = true
		}
		if cleaning && request.Name == "docker" && len(request.Args) > 1 && request.Args[0] == "image" && request.Args[1] == "ls" && strings.Contains(strings.Join(request.Args, " "), "label=cloudforge.dev/ownership=") {
			result.Truncated = true
		}
		return result
	})
	out := fixedService(runner).Run(context.Background(), fixturePath(t), testOptions())
	cleanup := evidenceByID(out.Run.Evidence, "environment-cleanup")
	build := evidenceByID(out.Run.Evidence, "container-build")
	if out.Run.Status != model.StatusError || out.ExitCode != 2 || cleanup == nil || cleanup.Status != model.StatusError || build == nil || build.Status != model.StatusPass || !hasDiagnosticCode(out.Run.Diagnostics, "image_cleanup_failed") {
		t.Fatalf("untagged inventory passed: status=%s exit=%d cleanup=%+v diagnostics=%+v", out.Run.Status, out.ExitCode, cleanup, out.Run.Diagnostics)
	}
}

func TestResourceNameCollisionPreservesExistingSentinel(t *testing.T) {
	for _, kind := range []string{"builder", "image", "cluster"} {
		t.Run(kind, func(t *testing.T) {
			id := strings.Repeat("a", 64)
			sentinelObserved := false
			runner := runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
				result := successfulCommand(request)
				args := request.Args
				joined := strings.Join(args, " ")
				if request.Name == "k3d" && containsArgument(args, "create") {
					t.Fatal("collision reached cluster creation")
				}
				if request.Name == "docker" && len(args) > 1 {
					if args[1] == "rm" && (args[len(args)-1] == id || args[len(args)-1] == "sha256:"+id || kind == "builder" && args[0] == "buildx") {
						t.Fatal("existing sentinel was removed")
					}
					if args[1] == "ls" {
						switch {
						case kind == "builder" && args[0] == "container" && strings.Contains(joined, "name=buildx_buildkit_cloudforge-0123abcd0"):
							result.Stdout = id + " buildx_buildkit_cloudforge-0123abcd0\n"
							sentinelObserved = true
						case kind == "image" && args[0] == "image" && strings.Contains(joined, "reference=cloudforge/healthy-node-api:0123abcd-a"):
							result.Stdout = "sha256:" + id + " cloudforge/healthy-node-api:0123abcd-a\n"
							sentinelObserved = true
						case kind == "cluster" && args[0] == "network" && strings.Contains(joined, "name=k3d-cloudforge-0123abcd"):
							result.Stdout = id + " k3d-cloudforge-0123abcd\n"
							sentinelObserved = true
						}
					}
				}
				return result
			})
			out := fixedService(runner).Run(context.Background(), fixturePath(t), testOptions())
			cleanup := evidenceByID(out.Run.Evidence, "environment-cleanup")
			build := evidenceByID(out.Run.Evidence, "container-build")
			buildPreserved := build == nil
			if kind == "cluster" {
				buildPreserved = build != nil && build.Status == model.StatusPass
			}
			if !sentinelObserved || out.Run.Status != model.StatusError || out.ExitCode != 2 || cleanup == nil || cleanup.Status != model.StatusPass || !hasDiagnosticCode(out.Run.Diagnostics, "resource_ownership_unavailable") || !buildPreserved {
				t.Fatalf("collision changed ownership/status: kind=%s status=%s exit=%d cleanup=%+v build=%+v diagnostics=%+v", kind, out.Run.Status, out.ExitCode, cleanup, build, out.Run.Diagnostics)
			}
		})
	}
}
