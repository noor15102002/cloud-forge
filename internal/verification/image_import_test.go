package verification

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/internal/executor/k3d"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

func TestImageImportFailureStopsDeploymentAndCleansPrivateStaging(t *testing.T) {
	for _, mode := range []string{"canceled", "wrong-image"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var archive string
			imports, deployed, stagingCleanup, clusterCleanup := 0, false, false, false
			runner := runnerFunc(func(callCtx context.Context, req command.Request) model.CommandResult {
				result := successfulCommand(req)
				if req.StdoutFile != nil {
					archive = req.StdoutFile.Path
				}
				if req.Name == "docker" && containsArgument(req.Args, "import") {
					imports++
					if mode == "canceled" {
						cancel()
						result.ExitCode, result.FailureType = -1, model.FailureCanceled
					}
				}
				if mode == "wrong-image" && req.Name == "docker" && containsArgument(req.Args, "inspecti") {
					result.Stdout = `{"id":"sha256:` + strings.Repeat("b", 64) + `"}`
				}
				if req.Name == "docker" && len(req.Args) > 2 && req.Args[0] == "exec" && containsArgument(req.Args, "rmdir") {
					stagingCleanup = true
					if callCtx.Err() != nil {
						t.Error("staging cleanup reused canceled context")
					}
				}
				if req.Name == "k3d" && containsArgument(req.Args, "delete") {
					clusterCleanup = true
					if callCtx.Err() != nil {
						t.Error("cluster cleanup reused canceled context")
					}
				}
				if req.Name == "kubectl" && containsArgument(req.Args, "apply") {
					deployed = true
				}
				return result
			})
			out := fixedService(runner).Run(ctx, fixturePath(t), testOptions())
			cleanup := evidenceByID(out.Run.Evidence, "environment-cleanup")
			if imports != 1 || deployed || !stagingCleanup || !clusterCleanup || out.ExitCode != 2 || out.Run.Status != model.StatusError || cleanup == nil || cleanup.Status != model.StatusPass {
				t.Fatalf("import failure changed lifecycle: imports=%d deployed=%t staging=%t cluster=%t status=%s cleanup=%+v", imports, deployed, stagingCleanup, clusterCleanup, out.Run.Status, cleanup)
			}
			if archive == "" {
				t.Fatal("archive was not staged")
			}
			if _, err := os.Lstat(filepath.Dir(archive)); !os.IsNotExist(err) {
				t.Fatal("private staging directory remains")
			}
			data, err := json.Marshal(out.Run)
			if err != nil || strings.Contains(string(data), archive) || strings.Contains(string(data), "test image archive") {
				t.Fatal("private archive path or contents entered report")
			}
		})
	}
}

func TestImportCleanupGuidanceRetainsSafeCauseWithoutRawOutput(t *testing.T) {
	result := model.CommandResult{Command: "docker", ExitCode: 1, FailureType: model.FailureExit, Stderr: "PRIVATE_TOOL_OUTPUT\n" + k3d.ImportCleanupWarning}
	guidance := commandGuidance(result, nil)
	if !strings.Contains(guidance, "exited with code 1") || !strings.Contains(guidance, k3d.ImportCleanupWarning) || strings.Contains(guidance, "PRIVATE_TOOL_OUTPUT") {
		t.Fatalf("unsafe or incomplete cleanup guidance: %s", guidance)
	}
}
