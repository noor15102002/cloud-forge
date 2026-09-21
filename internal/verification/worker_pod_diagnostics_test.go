package verification

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/internal/executor/kubernetes"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

// This mutates only the injected runner's synthetic application status. It
// cannot introduce another observation or application command.
func withWorkerTermination(t *testing.T, result model.CommandResult, current bool, restarts int) model.CommandResult {
	t.Helper()
	var list map[string]any
	if err := json.Unmarshal([]byte(result.Stdout), &list); err != nil {
		t.Fatal(err)
	}
	for _, raw := range list["items"].([]any) {
		pod := raw.(map[string]any)
		status := pod["status"].(map[string]any)["containerStatuses"].([]any)[0].(map[string]any)
		status["restartCount"] = restarts
		termination := map[string]any{"terminated": map[string]any{"reason": "OOMKilled", "exitCode": 137, "signal": 9, "message": "private-termination-message"}}
		if current {
			status["state"] = termination
		} else {
			status["lastState"] = termination
		}
	}
	body, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	result.Stdout = string(body)
	return result
}

func TestWorkerEvidenceRetainsTerminationInStartupRecoveryAndImageFailure(t *testing.T) {
	for _, tc := range []struct {
		id   string
		boot int
	}{{"worker-startup", 1}, {"worker-recovery", 2}, {"worker-image-replacement", 4}} {
		t.Run(tc.id, func(t *testing.T) {
			root, current := workerFixture(t)
			base := newWorkerRunner(t, current, "healthy")
			runner := runnerFunc(func(ctx context.Context, request command.Request) model.CommandResult {
				result := base.Run(ctx, request)
				if request.Name == "kubectl" && containsArgument(request.Args, "pods") && containsArgument(request.Args, workerSelector(current)) && base.active && base.boot == tc.boot {
					return withWorkerTermination(t, result, false, 1)
				}
				return result
			})
			service := workerTestService(base)
			service.runner = runner
			out := service.Run(context.Background(), root, testOptions())
			evidence := evidenceByID(out.Run.Evidence, tc.id)
			if out.Run.Status != model.StatusFail || evidence == nil || evidence.Status != model.StatusFail || !evidence.Execution.Executed {
				t.Fatalf("termination metadata changed verdict: %+v", out.Run.Evidence)
			}
			values := terminationMeasurementValues(evidence.Measurements)
			if values["pod_termination_previous_reason_oom_killed"] != "1" || values["pod_termination_previous_exit_code_137"] != "1" || values["pod_termination_previous_signal_9"] != "1" || values["pod_termination_current_count"] != "0" {
				t.Fatalf("latest termination missing from %s: %+v", tc.id, evidence)
			}
			if tc.id == "worker-recovery" && (evidence.Recovery == nil || evidence.Recovery.Status != model.StatusPass || evidenceByID(out.Run.Evidence, "worker-image-replacement").Status != model.StatusPass) {
				t.Fatal("successful restoration erased failure or prevented continuation")
			}
		})
	}
}

func TestWorkerTerminationSnapshotSurvivesLaterObservationFailure(t *testing.T) {
	for _, finalTimeout := range []bool{false, true} {
		t.Run(map[bool]string{false: "error", true: "deadline-final-error"}[finalTimeout], func(t *testing.T) {
			_, current := workerFixture(t)
			base := newWorkerRunner(t, current, "missing")
			base.start()
			reads := 0
			runner := runnerFunc(func(ctx context.Context, request command.Request) model.CommandResult {
				if request.Name == "kubectl" && containsArgument(request.Args, "pods") && containsArgument(request.Args, workerSelector(current)) {
					reads++
					if reads > 1 {
						if finalTimeout && reads == 2 {
							<-ctx.Done()
						}
						return model.CommandResult{ExitCode: -1, FailureType: model.FailureTimeout}
					}
					return withWorkerTermination(t, base.Run(ctx, request), true, 0)
				}
				return base.Run(ctx, request)
			})
			service := workerTestService(base)
			result := service.waitWorker(context.Background(), kubernetes.New(runner), current, current.image, 30*time.Millisecond, model.WorkerObservation{})
			values := terminationMeasurementValues(workerEvidence("worker-startup", result, true, current).Measurements)
			if result.status != model.StatusError || values["pod_termination_current_reason_oom_killed"] != "1" || values["pod_termination_snapshot_pods"] != "1" || result.deadline != finalTimeout {
				t.Fatalf("latest successful snapshot lost: %+v measurements=%+v", result, values)
			}
			wantReads := 2
			if finalTimeout {
				wantReads = 3
			}
			if reads != wantReads {
				t.Fatalf("extra runtime observation added: %d, want %d", reads, wantReads)
			}
		})
	}
}

func TestWorkerTerminationSnapshotReplacedByLaterSuccessfulEmptyObservation(t *testing.T) {
	_, current := workerFixture(t)
	base := newWorkerRunner(t, current, "missing")
	base.start()
	reads := 0
	runner := runnerFunc(func(ctx context.Context, request command.Request) model.CommandResult {
		if request.Name == "kubectl" && containsArgument(request.Args, "pods") && containsArgument(request.Args, workerSelector(current)) {
			reads++
			if reads == 1 {
				return withWorkerTermination(t, base.Run(ctx, request), true, 0)
			}
			if reads == 2 {
				return model.CommandResult{Stdout: `{"items":[]}`}
			}
			return model.CommandResult{ExitCode: -1, FailureType: model.FailureExit}
		}
		return base.Run(ctx, request)
	})
	result := workerTestService(base).waitWorker(context.Background(), kubernetes.New(runner), current, current.image, time.Second, model.WorkerObservation{})
	values := terminationMeasurementValues(podTerminationMeasurements(result.podSnapshot))
	if result.status != model.StatusError || values["pod_termination_snapshot_pods"] != "0" || values["pod_termination_current_count"] != "0" || values["pod_termination_previous_count"] != "0" {
		t.Fatalf("old termination accumulated after an empty snapshot: %+v %+v", result, values)
	}
}
