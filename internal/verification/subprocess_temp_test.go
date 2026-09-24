package verification

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

func requestEnvironment(req command.Request) map[string]string {
	values := map[string]string{}
	for _, entry := range req.Env {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = value
	}
	return values
}

func TestScopedRunnerPinsOwnedTempAndPreservesControlledToolTemp(t *testing.T) {
	for _, controlled := range []bool{false, true} {
		scoped := scopedRunner{tempDir: "/owned/runtime/temp", runner: runnerFunc(func(_ context.Context, req command.Request) model.CommandResult {
			want := "/owned/runtime/temp"
			if controlled {
				want = "/scanner/private/temp"
			}
			if requestEnvironment(req)["TMPDIR"] != want {
				t.Fatalf("wrong tool temporary directory: %+v", req)
			}
			return model.CommandResult{}
		})}
		scoped.Run(context.Background(), command.Request{Name: "trivy", ClearEnv: controlled, Env: []string{"TMPDIR=/scanner/private/temp"}})
	}
}

func TestRuntimeToolTemporaryFilesAreOwnedAndRemovedForEveryOutcome(t *testing.T) {
	for _, mode := range []string{"success", "build-failure", "canceled", "kept"} {
		t.Run(mode, func(t *testing.T) {
			external := t.TempDir()
			sentinel := filepath.Join(external, "external-sentinel")
			if err := os.WriteFile(sentinel, []byte("keep-external-data"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("TMPDIR", external)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			runtimeDirectory := ""
			commands := 0
			scans := 0
			cleanupCommands := 0
			runner := runnerFunc(func(callCtx context.Context, req command.Request) model.CommandResult {
				result := successfulCommand(req)
				env := requestEnvironment(req)
				workspace := filepath.Dir(env["DOCKER_CONFIG"])
				if !strings.HasPrefix(filepath.Base(workspace), "cloudforge-verify-") {
					return result
				}
				commands++
				expected := filepath.Join(workspace, "temp")
				if runtimeDirectory != "" && runtimeDirectory != expected {
					t.Fatal("runtime temp boundary changed during verification")
				}
				runtimeDirectory = expected
				temp := env["TMPDIR"]
				if req.ClearEnv {
					scans++
					if req.Name != "trivy" || temp == expected || !strings.HasPrefix(filepath.Base(filepath.Dir(temp)), "cloudforge-scan-") {
						t.Fatalf("controlled scanner temp was overridden: %+v", req)
					}
				} else if temp != expected {
					t.Fatalf("runtime command escaped owned temp: %+v", req)
				}
				info, err := os.Stat(temp)
				if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
					t.Fatalf("tool temp is absent or not private: %s %v", temp, err)
				}
				// Deliberately emulate a successful or failed external tool that forgets
				// to remove nested temporary files. Cleanup must remove these by ownership,
				// without inspecting or selectively deleting external host temporary data.
				nested := filepath.Join(temp, "tool-leftover", "nested")
				if err := os.MkdirAll(nested, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(nested, "artifact"), []byte("synthetic-tool-residue"), 0o600); err != nil {
					t.Fatal(err)
				}
				if req.Name == "docker" && containsArgument(req.Args, "build") {
					switch mode {
					case "build-failure":
						result.ExitCode, result.FailureType, result.Stderr = 1, model.FailureExit, "dockerfile parse error: unknown instruction"
					case "canceled":
						cancel()
						result.ExitCode, result.FailureType = -1, model.FailureCanceled
					}
				}
				if containsArgument(req.Args, "rm") || containsArgument(req.Args, "delete") {
					cleanupCommands++
					if callCtx.Err() != nil {
						t.Fatal("tool cleanup inherited cancellation")
					}
				}
				return result
			})
			options := testOptions()
			options.KeepEnvironment = mode == "kept"
			out := fixedService(runner).Run(ctx, fixturePath(t), options)
			want, exit := model.StatusWarn, 0
			switch mode {
			case "build-failure":
				want, exit = model.StatusFail, 1
			case "canceled":
				want, exit = model.StatusError, 2
			}
			if out.Run.Status != want || out.ExitCode != exit || runtimeDirectory == "" || commands == 0 || cleanupCommands == 0 {
				t.Fatalf("runtime outcome changed: %s/%d commands=%d cleanup=%d", out.Run.Status, out.ExitCode, commands, cleanupCommands)
			}
			if mode == "success" && scans == 0 {
				t.Fatal("scanner private temp boundary was not exercised")
			}
			if _, err := os.Stat(runtimeDirectory); !os.IsNotExist(err) {
				t.Fatal("nested tool temporary files survived workspace cleanup")
			}
			// #nosec G304 -- reads only this test's owned external-temp sentinel.
			data, err := os.ReadFile(sentinel)
			if err != nil || string(data) != "keep-external-data" {
				t.Fatal("external temporary sentinel changed")
			}
			entries, err := os.ReadDir(external)
			if err != nil || len(entries) != 1 || entries[0].Name() != "external-sentinel" {
				t.Fatalf("owned preflight/scanner/runtime state survived: %+v %v", entries, err)
			}
			cleanup := evidenceByID(out.Run.Evidence, "environment-cleanup")
			cleanupStatus := model.StatusPass
			if mode == "kept" {
				cleanupStatus = model.StatusSkipped
			}
			if cleanup == nil || cleanup.Status != cleanupStatus {
				t.Fatalf("cleanup evidence changed: %+v", cleanup)
			}
		})
	}
}
