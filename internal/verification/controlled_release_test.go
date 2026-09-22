package verification

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	k6executor "github.com/noor15102002/cloud-forge/internal/executor/k6"
	"github.com/noor15102002/cloud-forge/internal/executor/kubernetes"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

const controlledReadyTarget = `{"items":[{"metadata":{"name":"target"},"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}`

func assertControlledOutcome(t *testing.T, result recoveryOutcome, status model.Status, code int, diagnostic string) {
	t.Helper()
	if result.Evidence.Status != status || result.ExitCode != code || !result.MutationAttempted {
		t.Fatalf("controlled outcome status/exit/mutation incorrect: %+v", result)
	}
	if diagnostic != "" && (result.Diagnostic == nil || result.Diagnostic.Code != diagnostic || result.Diagnostic.Status != status) {
		t.Fatalf("controlled diagnostic incorrect: %+v", result.Diagnostic)
	}
	if status == model.StatusError && result.Finding != nil {
		t.Fatal("unavailable observation became an application finding")
	}
	out := Outcome{}
	applyExperimentOutcome(&out, result)
	finalEvidenceStatus(&out)
	wantOverall := status
	if status == model.StatusSkipped {
		wantOverall = model.StatusPass
	}
	if out.Run.Status != wantOverall || out.ExitCode != code || out.Run.Evidence[0].Status != status {
		t.Fatalf("controlled evidence changed during aggregation: %+v", out)
	}
}

func assertControlledResultRestoresAndContinues(t *testing.T, result recoveryOutcome, restoreFailure bool) {
	t.Helper()
	service := fixedService(runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		if restoreFailure && request.Name == "kubectl" && containsArgument(request.Args, "apply") {
			return model.CommandResult{ExitCode: 1, FailureType: model.FailureExit}
		}
		if len(request.Args) > 0 && strings.Contains(request.Args[len(request.Args)-1], "/proxy/_test/ready") {
			_, path, _ := strings.Cut(request.Args[len(request.Args)-1], "/pods/")
			pod, _, _ := strings.Cut(path, ":")
			return model.CommandResult{Stdout: fmt.Sprintf(`{"pod":%q,"ready":true}`, pod)}
		}
		return successfulCommand(request)
	}))
	current := experimentTestPlan(t)
	current.config.Experiments.ControlPath = "/_test"
	out := Outcome{Run: model.VerificationRun{Plan: &model.VerificationPlan{Capabilities: []model.Capability{{Name: "later", Disposition: "supported"}}}}}
	blocked := ""
	service.finishExperiment(context.Background(), kubernetes.New(service.runner), current, "restore.yaml", &out, result, false, &blocked)
	finalEvidenceStatus(&out)
	wantOverall, wantCode, wantRecovery := result.Evidence.Status, result.ExitCode, model.StatusPass
	if restoreFailure {
		wantOverall, wantCode, wantRecovery = model.StatusError, 2, model.StatusError
	}
	if wantOverall == model.StatusSkipped {
		wantOverall = model.StatusPass
	}
	completed := out.Run.Evidence[0]
	if out.Run.Status != wantOverall || out.ExitCode != wantCode || completed.Status != result.Evidence.Status || completed.Recovery == nil || completed.Recovery.Status != wantRecovery {
		t.Fatalf("restoration rewrote original controlled evidence or overall status: %+v", out)
	}
	prepared := service.prepareExperiment(context.Background(), kubernetes.New(service.runner), current, &out, "later", &blocked)
	if prepared == restoreFailure {
		t.Fatalf("restoration did not control dependent scheduling: restored=%t prepared=%t", !restoreFailure, prepared)
	}
	if prepared {
		applyExperimentOutcome(&out, lifecycleSuccess("later", "Later independent experiment", "runtime.later", "Observed success", 0, "observed", nil))
		finalEvidenceStatus(&out)
		if out.Run.Status != wantOverall || out.ExitCode != wantCode || out.Run.Evidence[0].Status != result.Evidence.Status {
			t.Fatal("later passing evidence erased the original controlled result")
		}
	} else if out.Run.Evidence[1].Status != model.StatusBlocked || out.Run.Evidence[1].Execution.Executed {
		t.Fatal("unrestored dependent experiment was not explicitly blocked")
	}
}

func TestControlledReadinessRequiresObservedTargetAndTimelyTransition(t *testing.T) {
	for _, scenario := range []string{"missing", "malformed", "empty-object", "null", "null-items", "api-error", "still-ready", "late-unready", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			var controlled atomic.Bool
			var finalReads atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprint(w, `{"pod":"target","ready":true}`)
			}))
			defer server.Close()
			runner := runnerFunc(func(ctx context.Context, request command.Request) model.CommandResult {
				args := strings.Join(request.Args, " ")
				switch {
				case strings.Contains(args, "/_test/unready"):
					controlled.Store(true)
					if scenario == "canceled" {
						cancel()
					}
					return model.CommandResult{Stdout: `{"pod":"target","ready":false}`}
				case strings.Contains(args, "/_test/identity"):
					return model.CommandResult{Stdout: `{"pod":"target","ready":true}`}
				case strings.Contains(args, "get pods"):
					if !controlled.Load() {
						return model.CommandResult{Stdout: controlledReadyTarget}
					}
					switch scenario {
					case "missing":
						return model.CommandResult{Stdout: strings.ReplaceAll(controlledReadyTarget, "target", "replacement")}
					case "malformed":
						return model.CommandResult{Stdout: "{"}
					case "empty-object":
						return model.CommandResult{Stdout: "{}"}
					case "null":
						return model.CommandResult{Stdout: "null"}
					case "null-items":
						return model.CommandResult{Stdout: `{"items":null}`}
					case "api-error", "canceled":
						return model.CommandResult{ExitCode: 1, FailureType: model.FailureExit}
					case "late-unready":
						if finalReads.Add(1) == 1 {
							<-ctx.Done()
							return model.CommandResult{ExitCode: -1, FailureType: model.FailureCanceled}
						}
						if deadline, ok := ctx.Deadline(); !ok || ctx.Err() != nil || time.Until(deadline) > lifecycleFinalObservationLimit {
							t.Error("late observation did not use a fresh bounded context")
						}
						return model.CommandResult{Stdout: strings.ReplaceAll(controlledReadyTarget, `"True"`, `"False"`)}
					default:
						return model.CommandResult{Stdout: controlledReadyTarget}
					}
				}
				return model.CommandResult{}
			})
			service := New(runner)
			service.recoveryTimeout, service.poll = 50*time.Millisecond, time.Millisecond
			current := plan{clusterName: "controlled", workloadName: "test", desiredReplicas: 2, readinessURL: server.URL, config: model.RuntimeConfiguration{Runtime: model.RuntimeSettings{Port: 8080}, Experiments: model.ExperimentSettings{ControlPath: "/_test"}}}
			result := service.runReadinessGating(parent, kubernetes.New(runner), current)
			status, code, diagnostic := model.StatusError, 2, "control_evidence_unavailable"
			if scenario == "still-ready" {
				status, code, diagnostic = model.StatusFail, 1, "readiness_gating_failed"
			}
			if scenario == "late-unready" {
				diagnostic = "readiness_transition_unobserved"
				if finalReads.Load() != 2 || measurementValue(result.Evidence.Measurements, "final_state_observed_after_deadline") != "true" {
					t.Fatalf("late readiness transition lost observation provenance: %+v", result)
				}
			}
			assertControlledOutcome(t, result, status, code, diagnostic)
			if scenario == "missing" && strings.Contains(result.Evidence.Summary, "remained") {
				t.Fatal("vanished target was described as remaining ready")
			}
			if scenario != "canceled" {
				assertControlledResultRestoresAndContinues(t, result, false)
			}
			if scenario == "still-ready" {
				assertControlledResultRestoresAndContinues(t, result, true)
			}
		})
	}
}

func TestTargetedRecoveryDistinguishesUnmetRequirementFromUnavailableObservation(t *testing.T) {
	for _, scenario := range []string{"unhealthy", "ready", "late-ready", "malformed", "empty-object", "null", "null-items", "api-error", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			requestStarted, terminationObserved := make(chan struct{}), make(chan struct{})
			var observations atomic.Int32
			runner := runnerFunc(func(ctx context.Context, request command.Request) model.CommandResult {
				args := strings.Join(request.Args, " ")
				switch {
				case strings.Contains(args, "/slow?"):
					close(requestStarted)
					select {
					case <-terminationObserved:
					case <-ctx.Done():
						return model.CommandResult{FailureType: model.FailureCanceled}
					}
					time.Sleep(5 * time.Millisecond)
					return model.CommandResult{Stdout: `{"pod":"target","completed":"test-shutdown","sigterm_received":true}`}
				case strings.Contains(args, "/state"):
					<-requestStarted
					return model.CommandResult{Stdout: `{"pod":"target","active":["test-shutdown"]}`}
				case strings.Contains(args, "get pods"):
					n := observations.Add(1)
					if n == 1 {
						return model.CommandResult{Stdout: controlledReadyTarget}
					}
					if n == 2 {
						close(terminationObserved)
						return model.CommandResult{Stdout: strings.Replace(controlledReadyTarget, `"name":"target"`, `"name":"target","deletionTimestamp":"2026-09-21T00:00:00Z"`, 1)}
					}
					switch scenario {
					case "unhealthy":
						return model.CommandResult{Stdout: `{"items":[]}`}
					case "malformed":
						return model.CommandResult{Stdout: "{"}
					case "empty-object":
						return model.CommandResult{Stdout: "{}"}
					case "null":
						return model.CommandResult{Stdout: "null"}
					case "null-items":
						return model.CommandResult{Stdout: `{"items":null}`}
					case "api-error":
						return model.CommandResult{ExitCode: 1, FailureType: model.FailureExit}
					case "canceled":
						cancel()
						return model.CommandResult{ExitCode: -1, FailureType: model.FailureCanceled}
					case "late-ready":
						if n == 3 {
							<-ctx.Done()
							return model.CommandResult{ExitCode: -1, FailureType: model.FailureCanceled}
						}
					}
					return model.CommandResult{Stdout: strings.ReplaceAll(controlledReadyTarget, "target", "replacement")}
				}
				return model.CommandResult{}
			})
			service := New(runner)
			service.recoveryTimeout, service.poll = 50*time.Millisecond, time.Millisecond
			current := plan{clusterName: "cloudforge-test", workloadName: "test", desiredReplicas: 1, config: model.RuntimeConfiguration{Runtime: model.RuntimeSettings{Port: 8080}, Experiments: model.ExperimentSettings{ControlPath: "/_test"}}}
			result := service.runInFlightShutdown(parent, kubernetes.New(runner), current)
			status, code, diagnostic := model.StatusError, 2, "control_evidence_unavailable"
			if scenario == "unhealthy" {
				status, code, diagnostic = model.StatusFail, 1, "inflight_shutdown_failed"
			}
			if scenario == "ready" {
				status, code, diagnostic = model.StatusPass, 0, ""
			}
			if scenario == "late-ready" {
				diagnostic = "targeted_recovery_completion_unobserved"
			}
			assertControlledOutcome(t, result, status, code, diagnostic)
			if scenario != "canceled" && measurementValue(result.Evidence.Measurements, "sigterm_received") != "true" {
				t.Fatal("recovery result discarded the completed request/SIGTERM observation")
			}
			if scenario != "canceled" {
				assertControlledResultRestoresAndContinues(t, result, false)
			}
			if scenario == "unhealthy" {
				assertControlledResultRestoresAndContinues(t, result, true)
			}
		})
	}
}

func TestHPAAtMaximumRetainsLoadWithoutRequiringGrowth(t *testing.T) {
	hpa := `{"status":{"currentReplicas":5,"desiredReplicas":5,"currentMetrics":[{"type":"Resource","resource":{"name":"cpu","current":{"averageUtilization":90}}}],"conditions":[{"type":"ScalingActive","status":"True"}]}}`
	runner := loadRunner(`{"metrics":{"http_reqs":{"values":{"count":20,"rate":10}},"http_req_failed":{"values":{"rate":0}},"http_req_duration":{"values":{"p(50)":1,"p(95)":2,"p(99)":3}}}}`, hpa)
	service := New(runner)
	service.poll, service.hpaScaleTimeout = time.Millisecond, 50*time.Millisecond
	current := plan{clusterName: "test", workloadName: "api", desiredReplicas: 2, loadURL: "http://127.0.0.1:8080/work", hpaName: "api", hpaMinReplicas: 5, hpaMaxReplicas: 5, hpaTargetCPU: 70}
	load, autoscaling := service.runLoadAndAutoscaling(context.Background(), k6executor.New(runner), kubernetes.New(runner), current, t.TempDir(), "hpa.yaml")
	assertControlledOutcome(t, autoscaling, model.StatusSkipped, 0, "hpa_no_scaling_headroom")
	if load.Evidence.Status != model.StatusPass || load.ExitCode != 0 || measurementValue(load.Evidence.Measurements, "request_count") != "20" || measurementValue(autoscaling.Evidence.Measurements, "scaling_headroom") != "false" || !strings.Contains(autoscaling.Evidence.Summary, "maximum") {
		t.Fatalf("maximum-replica HPA lost load/no-headroom evidence: load=%+v autoscaling=%+v", load, autoscaling)
	}
	out := Outcome{}
	applyExperimentOutcome(&out, load)
	applyExperimentOutcome(&out, autoscaling)
	finalEvidenceStatus(&out)
	if out.Run.Status != model.StatusPass || out.ExitCode != 0 {
		t.Fatal("no-headroom skip changed successful load overall status")
	}
	assertControlledResultRestoresAndContinues(t, autoscaling, false)
}

func TestControlledFailuresSurviveFullServiceRestorationContinuationAndCleanup(t *testing.T) {
	for _, scenario := range []struct {
		name, experiment, diagnostic string
		status                       model.Status
		code                         int
	}{
		{"missing-target", "readiness-gating", "control_evidence_unavailable", model.StatusError, 2},
		{"still-ready", "readiness-gating", "readiness_gating_failed", model.StatusFail, 1},
		{"unrecovered", "inflight-shutdown", "inflight_shutdown_failed", model.StatusFail, 1},
		{"recovery-unobservable", "inflight-shutdown", "control_evidence_unavailable", model.StatusError, 2},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var readinessControlled, targetDeleting, restored, cleaned atomic.Bool
			var terminationReads atomic.Int32
			requestStarted, terminationObserved := make(chan struct{}), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprint(w, `{"pod":"api-a","ready":true}`)
			}))
			defer server.Close()
			service := fixedService(successRunner())
			base := service.runner
			// Protocol requests use their own concurrent path; the existing fake
			// runtime serializes command state and must not hold that lock while
			// the targeted request waits for a later Kubernetes observation.
			service.runner = runnerFunc(func(ctx context.Context, request command.Request) model.CommandResult {
				args := strings.Join(request.Args, " ")
				switch {
				case strings.Contains(args, "/proxy/_test/identity"):
					return model.CommandResult{Stdout: `{"pod":"api-a","ready":true}`}
				case strings.Contains(args, "/proxy/_test/unready"):
					readinessControlled.Store(true)
					return model.CommandResult{Stdout: `{"pod":"api-a","ready":false}`}
				case strings.Contains(args, "/proxy/_test/ready"):
					_, path, _ := strings.Cut(request.Args[len(request.Args)-1], "/pods/")
					pod, _, _ := strings.Cut(path, ":")
					return model.CommandResult{Stdout: fmt.Sprintf(`{"pod":%q,"ready":true}`, pod)}
				case strings.Contains(args, "/proxy/_test/slow?"):
					close(requestStarted)
					select {
					case <-terminationObserved:
					case <-ctx.Done():
						return model.CommandResult{ExitCode: -1, FailureType: model.FailureCanceled}
					}
					time.Sleep(5 * time.Millisecond)
					return model.CommandResult{Stdout: `{"pod":"api-a","completed":"0123abcd-shutdown","sigterm_received":true}`}
				case strings.Contains(args, "/proxy/_test/state"):
					<-requestStarted
					return model.CommandResult{Stdout: `{"pod":"api-a","active":["0123abcd-shutdown"]}`}
				}
				result := base.Run(ctx, request)
				if request.Name == "docker" && containsArgument(request.Args, "port") {
					result.Stdout = server.Listener.Addr().String()
				}
				if request.Name == "kubectl" && containsArgument(request.Args, "apply") && (readinessControlled.Load() || targetDeleting.Load()) {
					readinessControlled.Store(false)
					targetDeleting.Store(false)
					restored.Store(true)
				}
				if request.Name == "kubectl" && containsArgument(request.Args, "delete") && containsArgument(request.Args, "pod") && scenario.experiment == "inflight-shutdown" && !restored.Load() {
					targetDeleting.Store(true)
				}
				if request.Name == "kubectl" && containsArgument(request.Args, "pods") {
					if readinessControlled.Load() && scenario.name == "missing-target" {
						result.Stdout = strings.ReplaceAll(result.Stdout, `"api-a"`, `"replacement"`)
					}
					if targetDeleting.Load() {
						if terminationReads.Add(1) == 1 {
							result.Stdout = strings.Replace(result.Stdout, `"name":"api-a"`, `"name":"api-a","deletionTimestamp":"2026-09-21T00:00:00Z"`, 1)
							close(terminationObserved)
						} else if scenario.name == "recovery-unobservable" {
							result.ExitCode, result.FailureType = 1, model.FailureExit
						} else {
							result.Stdout = `{"items":[]}`
						}
					}
				}
				if request.Name == "k3d" && containsArgument(request.Args, "delete") {
					cleaned.Store(true)
				}
				return result
			})
			service.controlledExperiments = true
			service.recoveryTimeout = 60 * time.Millisecond
			options := testOptions()
			options.OnPlan = func(plan model.VerificationPlan) {
				// Isolate the selected controlled experiment while all normal
				// independent lifecycle/load/restoration/cleanup paths still run.
				for index := range plan.Capabilities {
					capability := &plan.Capabilities[index]
					if (capability.Name == "readiness-gating" || capability.Name == "inflight-shutdown") && capability.Name != scenario.experiment {
						capability.Disposition = "skipped"
						capability.Reason = "This fixture isolates the other controlled experiment."
					}
				}
			}
			out := service.Run(context.Background(), fixturePath(t), options)
			evidence := evidenceByID(out.Run.Evidence, scenario.experiment)
			if out.Run.Status != scenario.status || out.ExitCode != scenario.code || evidence == nil || evidence.Status != scenario.status || evidence.Recovery == nil || evidence.Recovery.Status != model.StatusPass || !hasDiagnosticCode(out.Run.Diagnostics, scenario.diagnostic) {
				t.Fatalf("controlled verdict/restoration changed through full Service.Run: %+v", out)
			}
			for _, later := range []string{"pod-recovery", "rolling-deployment", "load-profile", "environment-cleanup"} {
				if evidence := evidenceByID(out.Run.Evidence, later); evidence == nil || evidence.Status != model.StatusPass {
					t.Fatalf("later independent evidence or cleanup unavailable: %s %+v", later, evidence)
				}
			}
			if !restored.Load() || !cleaned.Load() {
				t.Fatalf("full Service did not restore and clean: restored=%t cleaned=%t", restored.Load(), cleaned.Load())
			}
		})
	}
}
