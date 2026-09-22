package verification

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

func TestBuilderFallbackAfterClusterTimeoutRetainsOriginalFailure(t *testing.T) {
	const containerName = "buildx_buildkit_cloudforge-0123abcd0"
	const volumeName = containerName + "_state"
	identifier := strings.Repeat("b", 64)
	containerExists, volumeExists := true, true
	clusterAttemptFinished, primaryRemovalFailed := false, false
	var fallbackContextError error
	runner := runnerFunc(func(ctx context.Context, request command.Request) model.CommandResult {
		result := successfulCommand(request)
		args := request.Args
		if request.Name == "k3d" && containsArgument(args, "delete") {
			<-ctx.Done()
			clusterAttemptFinished = true
			result.ExitCode, result.FailureType = -1, model.FailureTimeout
		}
		if request.Name != "docker" || len(args) < 2 {
			return result
		}
		if args[0] == "buildx" && args[1] == "rm" {
			primaryRemovalFailed = true
			result.ExitCode, result.FailureType = 70, model.FailureExit
			result.Stderr = "private cleanup details must not enter evidence"
		}
		if args[1] == "inspect" && (args[0] == "container" || args[0] == "volume") {
			if !strings.HasPrefix(args[len(args)-1], "buildx_buildkit_") && args[len(args)-1] != identifier {
				return result
			}
			result.ExitCode, result.FailureType, result.Stderr = 0, model.FailureNone, ""
			exists := containerExists
			if args[0] == "volume" {
				exists = volumeExists
			}
			if !exists {
				result.ExitCode, result.FailureType = 1, model.FailureExit
				result.Stderr = "Error: No such object: " + args[len(args)-1]
			} else if args[0] == "container" {
				result.Stdout = fmt.Sprintf("%q %q true %q %q", identifier, "/"+containerName, "volume", volumeName)
			} else {
				result.Stdout = fmt.Sprintf("%q %q %q %q", volumeName, "local", "local", "2026-09-21T12:00:00Z")
			}
		}
		if args[1] == "rm" && (args[0] == "container" || args[0] == "volume") {
			if reference := args[len(args)-1]; reference != identifier && reference != volumeName {
				return result
			}
			if !primaryRemovalFailed || !clusterAttemptFinished {
				t.Fatal("builder fallback ran before the failed primary removal and cluster attempt")
			}
			fallbackContextError = ctx.Err()
			if args[0] == "container" {
				if args[len(args)-1] != identifier {
					t.Fatal("fallback did not target the captured immutable container ID")
				}
				containerExists = false
			} else {
				if containerExists || args[len(args)-1] != volumeName || containsArgument(args, "--force") {
					t.Fatal("fallback volume removal was not bounded by the captured container relationship")
				}
				volumeExists = false
			}
		}
		return result
	})
	service := fixedService(runner)
	service.cleanupTimeout = 10 * time.Millisecond
	out := service.Run(context.Background(), fixturePath(t), testOptions())
	if containerExists || volumeExists || fallbackContextError != nil {
		t.Fatalf("builder fallback did not finish independently: container=%v volume=%v context=%v", containerExists, volumeExists, fallbackContextError)
	}
	if out.ExitCode != 2 || out.Run.Status != model.StatusError || !hasDiagnosticCode(out.Run.Diagnostics, "builder_cleanup_failed") ||
		!hasDiagnosticCode(out.Run.Diagnostics, "cluster_cleanup_failed") || hasDiagnosticCode(out.Run.Diagnostics, "builder_remnant_cleanup_failed") {
		t.Fatalf("cleanup recovery lost the original error or fabricated a new one: %#v", out.Run.Diagnostics)
	}
	if cleanup := evidenceByID(out.Run.Evidence, "environment-cleanup"); cleanup == nil || cleanup.Status != model.StatusError {
		t.Fatalf("builder fallback erased the original aggregate cleanup error: %+v", cleanup)
	}
	for _, diagnostic := range out.Run.Diagnostics {
		if strings.Contains(diagnostic.Guidance, "private cleanup details") {
			t.Fatal("raw cleanup output leaked into evidence")
		}
	}
}
