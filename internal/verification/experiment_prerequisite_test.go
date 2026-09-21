package verification

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/noor15102002/cloud-forge/internal/analyzer"
	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/internal/executor/k3d"
	"github.com/noor15102002/cloud-forge/internal/executor/kubernetes"
	"github.com/noor15102002/cloud-forge/internal/regression"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

func experimentTestPlan(t *testing.T) plan {
	t.Helper()
	analysis, err := analyzer.New().Analyze(fixturePath(t))
	if err != nil {
		t.Fatal(err)
	}
	current, err := buildPlan(analysis, "0123abcd")
	if err != nil {
		t.Fatal(err)
	}
	current.readinessURL, current.healthURL = "http://unused/ready", "http://unused/health"
	return current
}

func runTestLifecycle(t *testing.T, name string, service *Service, current plan) recoveryOutcome {
	t.Helper()
	client := kubernetes.New(service.runner)
	switch name {
	case "graceful-shutdown":
		return service.runGracefulShutdown(context.Background(), client, current)
	case "pod-recovery":
		return service.runPodRecovery(context.Background(), client, current)
	case "rolling-deployment":
		return service.runRollingDeployment(context.Background(), k3d.New(service.runner), client, current, model.CommandResult{FailureType: model.FailureNone})
	default:
		t.Fatal("unknown test experiment", name)
		return recoveryOutcome{}
	}
}

func TestFailedFreshSemanticProbePreventsLifecycleMutation(t *testing.T) {
	for _, name := range []string{"graceful-shutdown", "pod-recovery", "rolling-deployment"} {
		t.Run(name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !r.Close {
					t.Error("prerequisite probe reused a connection")
				}
				// Rolling deployment first revalidates A after image B import.
				if requests.Add(1) == 1 && name == "rolling-deployment" {
					_, _ = w.Write([]byte(`{"status":"ready"}`))
					return
				}
				_, _ = w.Write([]byte(`{"status":"PRIVATE_RESPONSE"}`))
			}))
			defer server.Close()
			var mutations atomic.Int32
			service := fixedService(runnerFunc(func(_ context.Context, r command.Request) model.CommandResult {
				if r.Name == "kubectl" && (containsArgument(r.Args, "delete") || containsArgument(r.Args, "set")) {
					mutations.Add(1)
				}
				return successfulCommand(r)
			}))
			current := experimentTestPlan(t)
			current.readinessURL = server.URL
			current.config.Readiness = &model.ReadinessAcceptance{Status: 200, JSON: map[string]string{"status": "ready"}}
			service.probe = newReadinessChecker(directHTTPClient(), *current.config.Readiness).probe
			result := runTestLifecycle(t, name, service, current)
			if result.Evidence.Status != model.StatusBlocked || result.MutationAttempted || mutations.Load() != 0 || result.Finding != nil {
				t.Fatalf("failed prerequisite became a mutation/application verdict: %+v", result)
			}
			if measurementValue(result.Evidence.Measurements, "probe_failure_semantic_mismatch") == "" || measurementValue(result.Evidence.Measurements, "probe_http_status_200") == "" {
				t.Fatalf("missing safe prerequisite diagnostics: %+v", result.Evidence.Measurements)
			}
			out := Outcome{}
			blocked := ""
			service.finishExperiment(context.Background(), kubernetes.New(service.runner), current, "unused", &out, result, false, &blocked)
			if out.Run.Evidence[0].Execution.Executed || out.Run.Evidence[0].Execution.MutationAttempted {
				t.Fatal("unstarted experiment was marked executed")
			}
			data, _ := json.Marshal(out.Run)
			if strings.Contains(string(data), "PRIVATE_RESPONSE") || strings.Contains(string(data), server.URL) {
				t.Fatal("prerequisite diagnostics retained response data or endpoint")
			}
		})
	}
}

func TestImagePreparationRevalidatesBaselineBeforeRollout(t *testing.T) {
	for _, observationError := range []bool{false, true} {
		t.Run(map[bool]string{false: "unhealthy-service", true: "unobservable-api"}[observationError], func(t *testing.T) {
			var importedB, changedImage, cleaned atomic.Bool
			service := fixedService(runnerFunc(func(_ context.Context, r command.Request) model.CommandResult {
				result := successfulCommand(r)
				if r.Name == "k3d" && containsArgument(r.Args, "import") {
					for _, arg := range r.Args {
						if strings.HasSuffix(arg, "-b") {
							importedB.Store(true)
						}
					}
				}
				if r.Name == "kubectl" && containsArgument(r.Args, "set") {
					changedImage.Store(true)
				}
				if r.Name == "k3d" && containsArgument(r.Args, "delete") {
					cleaned.Store(true)
				}
				if observationError && importedB.Load() && r.Name == "kubectl" && containsArgument(r.Args, "deployment") && containsArgument(r.Args, "get") {
					result.ExitCode, result.FailureType = -1, model.FailureTimeout
				}
				return result
			}))
			service.baselineTimeout = 30 * time.Millisecond
			service.probe = func(context.Context, string) (int, error) {
				if importedB.Load() && !observationError {
					return http.StatusServiceUnavailable, nil
				}
				return http.StatusOK, nil
			}
			out := service.Run(context.Background(), fixturePath(t), testOptions())
			evidence := evidenceByID(out.Run.Evidence, "rolling-deployment")
			expected := model.StatusBlocked
			if observationError {
				expected = model.StatusError
			}
			if !importedB.Load() || changedImage.Load() || !cleaned.Load() || evidence.Status != expected || evidence.Execution.Executed || evidence.Execution.MutationAttempted || evidence.Recovery == nil {
				t.Fatalf("post-import baseline was not enforced: %+v", evidence)
			}
			if findingByID(out.Run.Findings, "runtime.rolling-deployment") != nil {
				t.Fatal("preexisting baseline failure became a rollout finding")
			}
			// Added diagnostic measurements and prerequisite states use the same
			// saved-report contract accepted by the CLI and trusted reporter.
			data, _ := json.Marshal(out.Run)
			path := filepath.Join(t.TempDir(), "result.json")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := regression.Load(path); err != nil {
				t.Fatalf("report compatibility changed: %v", err)
			}
		})
	}
}

func TestLifecycleObservationDeadlineIsNotApplicationFailure(t *testing.T) {
	for _, name := range []string{"graceful-shutdown", "pod-recovery", "rolling-deployment"} {
		for _, apiStalls := range []bool{false, true} {
			t.Run(name+map[bool]string{false: "/valid-unready-observations", true: "/api-observation-stalls"}[apiStalls], func(t *testing.T) {
				var mutated atomic.Bool
				service := fixedService(runnerFunc(func(ctx context.Context, r command.Request) model.CommandResult {
					result := successfulCommand(r)
					if r.Name == "kubectl" && (containsArgument(r.Args, "set") || (containsArgument(r.Args, "delete") && containsArgument(r.Args, "pod"))) {
						mutated.Store(true)
					}
					if mutated.Load() && r.Name == "kubectl" && containsArgument(r.Args, "pods") {
						if apiStalls {
							<-ctx.Done()
							result.ExitCode, result.FailureType = -1, model.FailureCanceled
						} else {
							result.Stdout = degradedPodList
						}
					}
					return result
				}))
				service.recoveryTimeout, service.rolloutTimeout = 80*time.Millisecond, 80*time.Millisecond
				result := runTestLifecycle(t, name, service, experimentTestPlan(t))
				expected := model.StatusFail
				if apiStalls {
					expected = model.StatusError
				}
				if !mutated.Load() || !result.MutationAttempted || result.Evidence.Status != expected {
					t.Fatalf("observation/requirement deadline conflated: %+v", result)
				}
				if apiStalls && result.Finding != nil {
					t.Fatal("lost API observation became an application finding")
				}
				if measurementValue(result.Evidence.Measurements, "request_count") == "0" || measurementValue(result.Evidence.Measurements, "probe_http_status_200") == "" {
					t.Fatal("partial HTTP observations were discarded")
				}
			})
		}
	}
}

func TestUnhealthyPostImportBaselineUsesRemainingExperimentBudget(t *testing.T) {
	var mutated atomic.Bool
	service := fixedService(runnerFunc(func(_ context.Context, r command.Request) model.CommandResult {
		if r.Name == "kubectl" && containsArgument(r.Args, "set") {
			mutated.Store(true)
		}
		return successfulCommand(r)
	}))
	service.baselineTimeout = time.Second
	service.rolloutTimeout = 50 * time.Millisecond
	service.probe = func(context.Context, string) (int, error) { return 503, nil }
	result := runTestLifecycle(t, "rolling-deployment", service, experimentTestPlan(t))
	if mutated.Load() || result.Evidence.Status != model.StatusBlocked || result.Evidence.Recovery == nil || result.Evidence.Recovery.Status != model.StatusBlocked {
		t.Fatalf("known unhealthy prerequisite expiry became an observation error: %+v", result)
	}
}
