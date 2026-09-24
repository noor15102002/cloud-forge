package verification

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/internal/executor/kubernetes"
	"github.com/noor15102002/cloud-forge/internal/regression"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

func TestRolloutDeadlineUsesFreshObservationWithoutInventingSuccess(t *testing.T) {
	for _, final := range []string{"unready", "ready", "timeout", "malformed", "canceled", "independent-timeout"} {
		t.Run(final, func(t *testing.T) {
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			var mutated atomic.Bool
			calls := 0
			service := fixedService(runnerFunc(func(ctx context.Context, request command.Request) model.CommandResult {
				result := successfulCommand(request)
				if request.Name == "kubectl" && containsArgument(request.Args, "set") {
					mutated.Store(true)
				}
				if !mutated.Load() || request.Name != "kubectl" || !containsArgument(request.Args, "pods") {
					return result
				}
				calls++
				if calls == 1 {
					if final != "independent-timeout" {
						<-ctx.Done()
					}
					result.ExitCode, result.FailureType = -1, model.FailureTimeout
					return result
				}
				deadline, ok := ctx.Deadline()
				if !ok || ctx.Err() != nil || time.Until(deadline) > lifecycleFinalObservationLimit || time.Until(deadline) < time.Second {
					t.Errorf("final observation did not receive a fresh bounded context: deadline=%v error=%v", deadline, ctx.Err())
				}
				switch final {
				case "unready":
					result.Stdout = degradedPodList
				case "ready":
					result.Stdout = readyPodList
				case "timeout":
					result.ExitCode, result.FailureType = -1, model.FailureTimeout
				case "malformed":
					result.Stdout = "{"
				case "canceled":
					cancel()
					<-ctx.Done()
					result.ExitCode, result.FailureType = -1, model.FailureCanceled
				}
				return result
			}))
			service.rolloutTimeout = 50 * time.Millisecond
			current := experimentTestPlan(t)
			result := service.runRollingDeployment(parent, testImportClient(t, service.runner), kubernetes.New(service.runner), current, model.CommandResult{})
			expected, expectedCalls := model.StatusError, 2
			if final == "unready" {
				expected = model.StatusFail
			}
			if final == "independent-timeout" {
				expectedCalls = 1
			}
			if result.Evidence.Status != expected || calls != expectedCalls || !result.MutationAttempted {
				t.Fatalf("deadline classification changed: final=%s calls=%d result=%+v", final, calls, result)
			}
			if expected == model.StatusError && result.Finding != nil {
				t.Fatal("observation failure became an application finding")
			}
			if measurementValue(result.Evidence.Measurements, "probe_http_status_200") == "" {
				t.Fatal("discarded valid traffic evidence")
			}
			if final == "unready" || final == "ready" {
				if measurementValue(result.Evidence.Measurements, "requirement_deadline_exceeded") != "true" || measurementValue(result.Evidence.Measurements, "final_state_observed_after_deadline") != "true" || measurementValue(result.Evidence.Measurements, "final_http_status") != "200" {
					t.Fatalf("missing final observation provenance: %+v", result.Evidence.Measurements)
				}
			}
		})
	}
}

func TestFinalObservationSharesBudgetAndHonorsParentCancellation(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	experiment, cancelExperiment := context.WithDeadline(parent, time.Now().Add(-time.Second))
	defer cancelExperiment()
	observation := lifecycleObservation{parent: parent, experiment: experiment}
	defer observation.close()
	first := observation.context()
	firstDeadline, _ := first.Deadline()
	second := observation.context()
	secondDeadline, _ := second.Deadline()
	if first != second || !firstDeadline.Equal(secondDeadline) || time.Until(firstDeadline) > lifecycleFinalObservationLimit {
		t.Fatal("final observations renewed their total budget")
	}
	cancelParent()
	if first.Err() != context.Canceled || observation.context().Err() == nil {
		t.Fatal("final observation detached from parent cancellation")
	}
}

func TestRolloutDeadlineFailureRestoresContinuesAndLoadsStrictReport(t *testing.T) {
	var rolling, cleaned atomic.Bool
	reads := 0
	service := fixedService(runnerFunc(func(ctx context.Context, request command.Request) model.CommandResult {
		result := successfulCommand(request)
		if request.Name == "kubectl" && containsArgument(request.Args, "set") {
			rolling.Store(true)
		}
		if request.Name == "kubectl" && containsArgument(request.Args, "apply") {
			rolling.Store(false)
		}
		if request.Name == "k3d" && containsArgument(request.Args, "delete") {
			cleaned.Store(true)
		}
		if rolling.Load() && request.Name == "kubectl" && containsArgument(request.Args, "pods") {
			reads++
			if reads == 1 {
				<-ctx.Done()
				result.ExitCode, result.FailureType = -1, model.FailureTimeout
			} else {
				result.Stdout = degradedPodList
			}
		}
		return result
	}))
	service.rolloutTimeout = 50 * time.Millisecond
	out := service.Run(context.Background(), fixturePath(t), testOptions())
	rollout := evidenceByID(out.Run.Evidence, "rolling-deployment")
	load := evidenceByID(out.Run.Evidence, "load-profile")
	if out.ExitCode != 1 || out.Run.Status != model.StatusFail || rollout.Status != model.StatusFail || rollout.Recovery == nil || rollout.Recovery.Status != model.StatusPass || load.Status != model.StatusPass || !cleaned.Load() || reads != 2 {
		t.Fatalf("deadline failure was lost or stopped later evidence: rollout=%+v load=%+v status=%s reads=%d cleanup=%t", rollout, load, out.Run.Status, reads, cleaned.Load())
	}
	data, err := json.Marshal(out.Run)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := regression.Load(path)
	if err != nil {
		t.Fatalf("strict report contract rejected final observations: %v", err)
	}
	if loaded.Status != model.StatusFail || evidenceByID(loaded.Evidence, "rolling-deployment").Status != model.StatusFail || evidenceByID(loaded.Evidence, "load-profile").Status != model.StatusPass {
		t.Fatal("saved report lost failure or continuation")
	}
}
