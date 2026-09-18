package verification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/noor15102002/cloud-forge/internal/executor/kubernetes"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

type controlState struct {
	Pod             string   `json:"pod"`
	Ready           bool     `json:"ready"`
	Active          []string `json:"active"`
	Completed       string   `json:"completed"`
	SIGTERMReceived bool     `json:"sigterm_received"`
}

func controlResult(result model.CommandResult) (controlState, error) {
	var state controlState
	if failed(result) || result.Truncated {
		return state, errors.New("control request did not complete")
	}
	if json.Unmarshal([]byte(result.Stdout), &state) != nil || state.Pod == "" {
		return state, errors.New("control protocol response was invalid")
	}
	return state, nil
}

func serviceIdentity(ctx context.Context, endpoint string) (controlState, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return controlState{}, err
	}
	request.Close = true // Each observation must select a Service backend anew.
	response, err := directHTTPClient().Do(request)
	if err != nil {
		return controlState{}, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return controlState{}, errors.New("identity endpoint was unsuccessful")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 16*1024+1))
	if err != nil || len(data) > 16*1024 {
		return controlState{}, errors.New("identity response exceeded its bound")
	}
	return controlResult(model.CommandResult{Stdout: string(data)})
}

func controlledSkip(id, title string) recoveryOutcome {
	return recoveryOutcome{Evidence: model.Evidence{ExperimentID: id, Title: title, Status: model.StatusSkipped, Summary: "Requires explicit experiments.control_path implementing the documented pilot protocol; no behavioral proof is inferred."}}
}

func (s *Service) runReadinessGating(ctx context.Context, client *kubernetes.Client, current plan) recoveryOutcome {
	const id, title = "readiness-gating", "Service readiness gating"
	base := current.config.Experiments.ControlPath
	if base == "" || current.desiredReplicas < 2 {
		return controlledSkip(id, title)
	}
	ctx, cancel := context.WithTimeout(ctx, s.recoveryTimeout)
	defer cancel()
	pods, result, err := client.ObservePods(ctx, current.clusterName, namespace, "app.kubernetes.io/name="+current.workloadName)
	if err != nil || failed(result) {
		return controlError(id, title, "Could not inspect pods before readiness control.")
	}
	target := firstReadyPod(pods)
	if target == "" {
		return controlError(id, title, "No ready pod was available for readiness control.")
	}
	state, err := controlResult(client.PodProxy(ctx, current.clusterName, namespace, target, current.config.Runtime.Port, base+"/identity"))
	if err != nil || state.Pod != target {
		return controlError(id, title, "The control endpoint did not identify the selected pod.")
	}
	endpoint, _ := url.Parse(current.readinessURL)
	endpoint.Path = base + "/identity"
	endpoint.RawQuery = ""
	// Establish that this Service routed to the target before changing readiness.
	seen := false
	for i := 0; i < 100; i++ {
		sample, e := serviceIdentity(ctx, endpoint.String())
		if e != nil {
			return controlError(id, title, "Service identity traffic failed before the transition.")
		}
		if sample.Pod == target {
			seen = true
			break
		}
		if !pause(ctx, s.poll) {
			break
		}
	}
	if !seen {
		outcome := controlledSkip(id, title)
		outcome.Evidence.Summary = "The Service did not route to the selected ready pod during the bounded precondition window; readiness-gating evidence is unavailable."
		return outcome
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = client.PodProxy(cleanupCtx, current.clusterName, namespace, target, current.config.Runtime.Port, base+"/ready")
	}()
	state, err = controlResult(client.PodProxy(ctx, current.clusterName, namespace, target, current.config.Runtime.Port, base+"/unready"))
	if err != nil || state.Pod != target || state.Ready {
		return controlError(id, title, "The selected pod did not acknowledge its controlled unready state.")
	}
	unready := false
	for ctx.Err() == nil {
		pods, result, err = client.ObservePods(ctx, current.clusterName, namespace, "app.kubernetes.io/name="+current.workloadName)
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			break
		}
		if err != nil || failed(result) {
			return controlError(id, title, "Could not observe the controlled readiness transition.")
		}
		for _, pod := range pods {
			if pod.Name == target && !pod.Ready {
				unready = true
			}
		}
		if unready {
			break
		}
		if !pause(ctx, s.poll) {
			break
		}
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return controlError(id, title, "Readiness control was interrupted.")
	}
	if !unready {
		return lifecycleFailure(id, title, "runtime."+id, "The pod remained Kubernetes-ready after the controlled unready transition.", "Make readinessProbe reflect the application's readiness state.", 0, trafficObservation{}, nil)
	}
	// Allow bounded EndpointSlice/kube-proxy propagation, then require a sustained
	// sample with no traffic to the unready pod. A broken readiness fixture fails
	// above; routing mistakes fail below.
	if !pause(ctx, time.Second) {
		return controlError(id, title, "Readiness gating was interrupted.")
	}
	requests, misrouted := 0, 0
	for i := 0; i < 50; i++ {
		sample, e := serviceIdentity(ctx, endpoint.String())
		if e != nil {
			return controlError(id, title, "Service identity traffic failed during the unready window.")
		}
		requests++
		if sample.Pod == target {
			misrouted++
		}
		if !pause(ctx, 20*time.Millisecond) {
			return controlError(id, title, "Readiness gating was interrupted.")
		}
	}
	measurements := []model.Measurement{{Name: "service_requests", Value: strconv.Itoa(requests)}, {Name: "requests_to_unready_pod", Value: strconv.Itoa(misrouted)}, {Name: "target_pod", Value: target}}
	if misrouted > 0 {
		return lifecycleFailure(id, title, "runtime."+id, "Service traffic reached a pod observed as unready.", "Inspect readiness and Service routing.", 0, trafficObservation{}, measurements)
	}
	state, err = controlResult(client.PodProxy(ctx, current.clusterName, namespace, target, current.config.Runtime.Port, base+"/ready"))
	if err != nil || !state.Ready {
		return controlError(id, title, "The controlled pod did not acknowledge restored readiness.")
	}
	for ctx.Err() == nil {
		sample, e := serviceIdentity(ctx, endpoint.String())
		if e == nil && sample.Pod == target {
			return lifecycleSuccess(id, title, "runtime."+id, "Service routing excluded the unready pod and resumed after readiness was restored.", 0, "controlled readiness transition", measurements)
		}
		if !pause(ctx, s.poll) {
			break
		}
	}
	return lifecycleFailure(id, title, "runtime."+id, "Service routing did not resume to the restored pod before the deadline.", "Inspect readiness recovery and Service routing.", 0, trafficObservation{}, measurements)
}

func (s *Service) runInFlightShutdown(ctx context.Context, client *kubernetes.Client, current plan) recoveryOutcome {
	const id, title = "inflight-shutdown", "Targeted in-flight shutdown"
	base := current.config.Experiments.ControlPath
	if base == "" {
		return controlledSkip(id, title)
	}
	ctx, cancel := context.WithTimeout(ctx, s.recoveryTimeout)
	defer cancel()
	pods, result, err := client.ObservePods(ctx, current.clusterName, namespace, "app.kubernetes.io/name="+current.workloadName)
	if err != nil || failed(result) {
		return controlError(id, title, "Could not inspect pods before the targeted request.")
	}
	target := firstReadyPod(pods)
	if target == "" {
		return controlError(id, title, "No ready pod was available for the targeted request.")
	}
	requestID := strings.TrimPrefix(current.clusterName, "cloudforge-") + "-shutdown"
	type completedRequest struct {
		result    model.CommandResult
		completed time.Time
	}
	done := make(chan completedRequest, 1)
	go func() {
		result := client.PodProxy(ctx, current.clusterName, namespace, target, current.config.Runtime.Port, base+"/slow?id="+url.QueryEscape(requestID))
		done <- completedRequest{result: result, completed: time.Now()}
	}()
	// Always join the request before cleanup, including observation failures.
	joined := false
	defer func() {
		cancel()
		if !joined {
			<-done
		}
	}()
	active := false
	for ctx.Err() == nil {
		state, e := controlResult(client.PodProxy(ctx, current.clusterName, namespace, target, current.config.Runtime.Port, base+"/state"))
		if e == nil && state.Pod == target {
			for _, value := range state.Active {
				if value == requestID {
					active = true
				}
			}
		}
		if active {
			break
		}
		if !pause(ctx, s.poll) {
			break
		}
	}
	if !active {
		return controlError(id, title, "The selected pod never acknowledged the specific active request.")
	}
	select {
	case <-done:
		joined = true
		return controlError(id, title, "The controlled request finished before termination began.")
	default:
	}
	if result := client.BeginDeletePod(ctx, current.clusterName, namespace, target); failed(result) {
		return controlError(id, title, "Could not request graceful termination of the selected pod.")
	}
	terminating := false
	var terminationObserved time.Time
	for ctx.Err() == nil {
		pods, result, err = client.ObservePods(ctx, current.clusterName, namespace, "app.kubernetes.io/name="+current.workloadName)
		if err != nil || failed(result) {
			return controlError(id, title, "Could not observe the targeted pod's termination.")
		}
		present := false
		for _, pod := range pods {
			if pod.Name == target {
				present = true
				terminating = pod.Terminating
			}
		}
		if terminating || !present {
			if terminating {
				terminationObserved = time.Now()
			}
			break
		}
		if !pause(ctx, s.poll) {
			break
		}
	}
	response := <-done
	joined = true
	if ctx.Err() != nil {
		return controlError(id, title, "The targeted shutdown experiment was interrupted or timed out.")
	}
	state, err := controlResult(response.result)
	measurements := []model.Measurement{{Name: "target_pod", Value: target}, {Name: "request_id", Value: requestID}, {Name: "active_before_delete", Value: "true"}, {Name: "termination_observed", Value: strconv.FormatBool(terminating)}}
	if err != nil || state.Pod != target || state.Completed != requestID {
		return lifecycleFailure(id, title, "runtime."+id, "The specific request active on the terminated pod did not complete.", "Drain active requests during SIGTERM within terminationGracePeriodSeconds.", 0, trafficObservation{}, measurements)
	}
	if !terminating || response.completed.Before(terminationObserved) {
		return controlError(id, title, "Request completed, but termination overlap could not be established.")
	}
	if !state.SIGTERMReceived {
		return controlError(id, title, "Request completed, but the application did not acknowledge SIGTERM while that request was active.")
	}
	measurements = append(measurements, model.Measurement{Name: "sigterm_received", Value: "true"})
	// Restore the requested replica count before the next experiment.
	for ctx.Err() == nil {
		ready, total, _, res, e := client.ReadyPods(ctx, current.clusterName, namespace, "app.kubernetes.io/name="+current.workloadName)
		if e == nil && !failed(res) && ready == int(current.desiredReplicas) && total == ready {
			return lifecycleSuccess(id, title, "runtime."+id, "The identified request remained active at SIGTERM, completed on the targeted pod, and replicas recovered.", 0, "targeted request completed", measurements)
		}
		if !pause(ctx, s.poll) {
			break
		}
	}
	return controlError(id, title, "Replicas did not recover after the targeted shutdown experiment.")
}

func pause(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
func controlError(id, title, message string) recoveryOutcome {
	return lifecycleExecutionError(id, title, "control_evidence_unavailable", message, fmt.Sprintf("Check the documented control protocol for %s; this result is not an application pass.", id), trafficObservation{})
}
