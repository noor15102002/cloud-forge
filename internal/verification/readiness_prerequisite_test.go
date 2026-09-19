package verification

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

func TestUnhealthyNodeBlocksUnobservedSemanticReadiness(t *testing.T) {
	for _, scenario := range []string{"disk", "image", "api", "pods"} {
		t.Run(scenario, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					_ = conn.Close()
				}
			}))
			defer server.Close()
			path := filepath.Join(t.TempDir(), "runtime.yaml")
			if err := os.WriteFile(path, []byte("schema_version: v1alpha2\nreadiness: {status: 200, json: {status: ready}}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			var calls []command.Request
			applied := false
			runner := runnerFunc(func(_ context.Context, r command.Request) model.CommandResult {
				calls = append(calls, r)
				result := successfulCommand(r)
				if r.Name == "docker" && containsArgument(r.Args, "port") {
					result.Stdout = strings.TrimPrefix(server.URL, "http://") + "\n"
				}
				if r.Name == "kubectl" && containsArgument(r.Args, "apply") {
					applied = true
				}
				if r.Name == "kubectl" && containsArgument(r.Args, "rollout") {
					result.ExitCode = 1
					result.FailureType = model.FailureTimeout
					if scenario == "api" {
						result.FailureType = model.FailureExecution
					}
				}
				if r.Name == "kubectl" && containsArgument(r.Args, "nodes") && applied && scenario == "disk" {
					result.Stdout = `{"items":[{"status":{"conditions":[{"type":"Ready","status":"True"},{"type":"DiskPressure","status":"True"}]}}]}`
				}
				if r.Name == "kubectl" && containsArgument(r.Args, "pods") {
					switch scenario {
					case "image":
						result.Stdout = `{"items":[{"status":{"containerStatuses":[{"state":{"waiting":{"reason":"ErrImageNeverPull"}}}]}}]}`
					case "pods":
						result.Stdout = `invalid`
					default:
						result.Stdout = `{"items":[]}`
					}
				}
				return result
			})
			options := testOptions()
			options.ConfigPath = path
			out := fixedService(runner).Run(context.Background(), fixturePath(t), options)
			semantic := evidenceByID(out.Run.Evidence, "semantic-readiness")
			if out.ExitCode != 2 || out.Run.Status != model.StatusError || semantic == nil || semantic.Status != model.StatusBlocked {
				t.Fatalf("invalid application verdict: %#v", out)
			}
			if !semantic.Execution.Executed || measurementValue(semantic.Measurements, "http_transport_available") != "false" {
				t.Fatal("attempted observations erased")
			}
			if findingByID(out.Run.Findings, "container.startup") != nil || !hasCommand(calls, "k3d", "delete") || !hasCommand(calls, "docker", "rm") {
				t.Fatal("lost error boundary or cleanup")
			}
		})
	}
}

func TestInfrastructureDoesNotRewriteRealSemanticResponses(t *testing.T) {
	for _, status := range []model.Status{model.StatusPass, model.StatusFail} {
		out := Outcome{Run: model.VerificationRun{Evidence: []model.Evidence{{ExperimentID: "semantic-readiness", Status: status, Summary: "observed response", Measurements: []model.Measurement{{Name: "http_transport_available", Value: "true"}, {Name: "http_status", Value: "200"}}}}}}
		blockUnobservedReadiness(&out, "Unhealthy node.")
		if out.Run.Evidence[0].Status != status || out.Run.Evidence[0].Summary != "observed response" {
			t.Fatal("valid historical observation rewritten")
		}
	}
}
