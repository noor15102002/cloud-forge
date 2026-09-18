package verification

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/internal/executor/kubernetes"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

func TestTargetedShutdownRequiresSignalDuringSpecificRequest(t *testing.T) {
	for _, signalReceived := range []bool{true, false} {
		t.Run(fmt.Sprint(signalReceived), func(t *testing.T) {
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
					// The response completes after the Kubernetes observation returns.
					time.Sleep(20 * time.Millisecond)
					return model.CommandResult{Stdout: fmt.Sprintf(`{"pod":"target","completed":"test-shutdown","sigterm_received":%t}`, signalReceived)}
				case strings.Contains(args, "/state"):
					<-requestStarted
					return model.CommandResult{Stdout: `{"pod":"target","active":["test-shutdown"]}`}
				case strings.Contains(args, "get pods"):
					if observations.Add(1) == 2 {
						close(terminationObserved)
						return model.CommandResult{Stdout: `{"items":[{"metadata":{"name":"target","deletionTimestamp":"2026-09-18T00:00:00Z"},"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}`}
					}
					return model.CommandResult{Stdout: `{"items":[{"metadata":{"name":"target"},"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}`}
				default:
					return model.CommandResult{}
				}
			})
			service := New(runner)
			service.recoveryTimeout = time.Second
			current := plan{clusterName: "cloudforge-test", desiredReplicas: 1, config: model.RuntimeConfiguration{Runtime: model.RuntimeSettings{Port: 8080}, Experiments: model.ExperimentSettings{ControlPath: "/_test"}}}
			outcome := service.runInFlightShutdown(context.Background(), kubernetes.New(runner), current)
			want := model.StatusPass
			if !signalReceived {
				want = model.StatusError
			}
			if outcome.Evidence.Status != want {
				t.Fatalf("signal overlap %t: %#v", signalReceived, outcome)
			}
		})
	}
}
