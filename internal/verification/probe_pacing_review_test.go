package verification

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/internal/executor/kubernetes"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

func TestProbePacingUnissuedFinalHealthRetainsObservedLifecycleFailure(t *testing.T) {
	for _, name := range []string{"graceful-shutdown", "pod-recovery", "rolling-deployment"} {
		t.Run(name, func(t *testing.T) {
			var mutated, finalPodsObserved atomic.Bool
			var finalHealthAttempts atomic.Int32
			var deletionContext context.Context
			waitedForDeadline := false
			service := fixedService(runnerFunc(func(ctx context.Context, request command.Request) model.CommandResult {
				result := successfulCommand(request)
				if request.Name == "kubectl" && (containsArgument(request.Args, "delete") || containsArgument(request.Args, "set")) {
					mutated.Store(true)
					if containsArgument(request.Args, "delete") {
						deletionContext = ctx
					}
				}
				if mutated.Load() && request.Name == "kubectl" && containsArgument(request.Args, "pods") {
					// Keep deletion's concurrent readiness samples responsive,
					// then expire the first recovery read to exercise the fresh
					// bounded final observation in each lifecycle experiment.
					if ctx != deletionContext && !waitedForDeadline {
						waitedForDeadline = true
						<-ctx.Done()
						result.ExitCode, result.FailureType = -1, model.FailureTimeout
						return result
					}
					result.Stdout = degradedPodList
					if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) > time.Second {
						finalPodsObserved.Store(true)
					}
				}
				return result
			}))
			current := experimentTestPlan(t)
			service.recoveryTimeout, service.rolloutTimeout = 100*time.Millisecond, 100*time.Millisecond
			service.probe = func(_ context.Context, url string) (int, error) {
				if url == current.healthURL {
					finalHealthAttempts.Add(1)
					// Inject the pacer's unissued-request result. The real gate's
					// cancellation and request-start behavior have separate tests.
					return 0, errors.Join(errProbePacingInterrupted, context.DeadlineExceeded)
				}
				return http.StatusOK, nil
			}
			result := runTestLifecycle(t, name, service, current)
			if result.Evidence.Status != model.StatusFail || result.ExitCode != 1 || !result.MutationAttempted || result.Finding == nil || result.Finding.Status != model.StatusFail {
				t.Fatalf("unissued health request erased the observed lifecycle failure: %+v", result)
			}
			if !finalPodsObserved.Load() || finalHealthAttempts.Load() != 1 {
				t.Fatalf("test did not observe final unhealthy pods followed by one unissued health request: final pods=%t health attempts=%d", finalPodsObserved.Load(), finalHealthAttempts.Load())
			}
			for key, expected := range map[string]string{
				"final_http_request_started": "false", "final_http_status": "0",
				"requirement_deadline_exceeded": "true", "final_state_observed_after_deadline": "true",
				"minimum_ready_pods": "1", "total_pods": "2",
			} {
				if got := measurementValue(result.Evidence.Measurements, key); got != expected {
					t.Fatalf("lost completed observation %s: got %q, want %q", key, got, expected)
				}
			}
			failures, readiness := "failed_requests", "ready_pods"
			if name == "graceful-shutdown" {
				failures = "dropped_requests"
			}
			if name == "rolling-deployment" {
				readiness = "target_ready_pods"
			}
			if measurementValue(result.Evidence.Measurements, failures) != "0" || measurementValue(result.Evidence.Measurements, "probe_http_status_200") == "" {
				t.Fatal("an unissued probe invented a failed request or erased completed healthy traffic")
			}
			if got := measurementValue(result.Evidence.Measurements, readiness); got == "" || got == "2" {
				t.Fatal("the observed replica shortfall was lost")
			}
		})
	}
}

func TestProbePacingBaselineDeadlinePreservesUnhealthyChecksAndCancellation(t *testing.T) {
	for _, cancelParent := range []bool{false, true} {
		name := "own-deadline"
		if cancelParent {
			name = "parent-cancellation"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var deployments, requests atomic.Int32
			secondAttempt := make(chan struct{})
			service := fixedService(runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
				if request.Name == "kubectl" && containsArgument(request.Args, "deployment") && deployments.Add(1) == 2 {
					close(secondAttempt)
				}
				return successfulCommand(request)
			}))
			service.probePacer = newProbePacer(5 * time.Second)
			service.probe = func(context.Context, string) (int, error) {
				requests.Add(1)
				return http.StatusServiceUnavailable, nil
			}
			cancellationDone := make(chan struct{})
			if cancelParent {
				go func() {
					defer close(cancellationDone)
					select {
					case <-secondAttempt:
					case <-ctx.Done():
						return
					}
					// The second baseline attempt has completed its Kubernetes
					// reads once it holds the shared gate awaiting the interval.
					for len(service.probePacer.gate) != 0 {
						if !pause(ctx, time.Millisecond) {
							return
						}
					}
					cancel()
				}()
			}
			budget := 60 * time.Millisecond
			if cancelParent {
				budget = time.Second
			}
			result := service.validateBaselineFor(ctx, kubernetes.New(service.runner), experimentTestPlan(t), budget)
			if cancelParent {
				cancel()
				<-cancellationDone
			}
			expected := model.StatusBlocked
			if cancelParent {
				expected = model.StatusError
			}
			if result.Status != expected || requests.Load() != 1 || deployments.Load() < 2 {
				t.Fatalf("pacing changed baseline classification or issued another request: result=%+v requests=%d attempts=%d", result, requests.Load(), deployments.Load())
			}
			var serviceCheck *model.BaselineCheck
			for i := range result.Checks {
				if result.Checks[i].Name == "service_reachable" {
					serviceCheck = &result.Checks[i]
				}
			}
			if serviceCheck == nil || serviceCheck.Status != model.StatusBlocked || !strings.Contains(serviceCheck.Reason, "503") {
				t.Fatalf("last completed unhealthy HTTP observation was erased: %+v", result.Checks)
			}
		})
	}
}
