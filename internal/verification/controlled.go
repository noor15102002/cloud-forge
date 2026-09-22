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

func (s *Service) runReadinessGating(ctx context.Context, client *kubernetes.Client, current plan) (outcome recoveryOutcome) {
	mutated := false
	defer func() { outcome.MutationAttempted = mutated; qualifyTopology(&outcome, current) }()
	const id, title = "readiness-gating", "Service readiness gating"
	base := current.config.Experiments.ControlPath
	if base == "" || current.desiredReplicas < 2 {
		return controlledSkip(id, title)
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(parent, s.recoveryTimeout)
	defer cancel()
	observation := lifecycleObservation{parent: parent, experiment: ctx}
	defer observation.close()
	pods, result, err := client.ObservePods(ctx, current.clusterName, namespace, "app.kubernetes.io/name="+current.workloadName)
	if err != nil || failed(result) {
		return controlError(id, title, "Could not inspect pods before readiness control.")
	}
	target := firstReadyPod(pods)
	if target == "" {
		return lifecycleBlocked(id, title, "No ready pod was available for readiness control.")
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
	mutated = true
	state, err = controlResult(client.PodProxy(ctx, current.clusterName, namespace, target, current.config.Runtime.Port, base+"/unready"))
	if err != nil || state.Pod != target || state.Ready {
		return controlError(id, title, "The selected pod did not acknowledge its controlled unready state.")
	}
	unready := false
	var deadlineReached bool
	for {
		if parent.Err() != nil {
			return controlError(id, title, "Readiness control was interrupted.")
		}
		pods, result, deadlineReached, err = observation.pods(client, current, "app.kubernetes.io/name="+current.workloadName)
		if parent.Err() != nil {
			return controlError(id, title, "Readiness control was interrupted.")
		}
		if err != nil || failed(result) {
			return controlError(id, title, "Could not observe the controlled readiness transition.")
		}
		present := false
		for _, pod := range pods {
			if pod.Name == target {
				present = true
				unready = !pod.Ready
			}
		}
		if !present {
			return controlError(id, title, "The selected pod disappeared; its controlled readiness transition could not be observed.")
		}
		if unready || deadlineReached {
			break
		}
		pause(ctx, s.poll)
	}
	transitionMeasurements := append([]model.Measurement{{Name: "target_pod", Value: target}, {Name: "target_ready", Value: strconv.FormatBool(!unready)}}, lifecycleDeadlineMeasurements(deadlineReached)...)
	if !unready {
		failure := lifecycleFailure(id, title, "runtime."+id, "The controlled target was still Kubernetes-ready in the final observation; no unready transition was observed before the deadline.", "Make readinessProbe reflect the application's readiness state.", 0, trafficObservation{}, transitionMeasurements)
		failure.Finding.Observed = "The selected target was observed Ready after acknowledging the unready control request."
		failure.Finding.Expected = "The selected target is observed Kubernetes-unready before the deadline."
		return failure
	}
	if deadlineReached {
		return lifecycleCompletionUnobserved(id, title, "readiness_transition_unobserved", "The target was observed unready only after the deadline; timely readiness gating was not established.", 0, trafficObservation{}, transitionMeasurements)
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
		if e != nil {
			return controlError(id, title, "Service identity traffic could not be observed after readiness restoration.")
		}
		if ctx.Err() != nil {
			return controlError(id, title, "Service routing completion was not observed before the readiness-gating deadline.")
		}
		if sample.Pod == target {
			return lifecycleSuccess(id, title, "runtime."+id, "Service routing excluded the unready pod and resumed after readiness was restored.", 0, "controlled readiness transition", measurements)
		}
		if !pause(ctx, s.poll) {
			break
		}
	}
	if parent.Err() != nil {
		return controlError(id, title, "Readiness gating was interrupted.")
	}
	return lifecycleFailure(id, title, "runtime."+id, "Service routing did not resume to the restored pod before the deadline.", "Inspect readiness recovery and Service routing.", 0, trafficObservation{}, measurements)
}

func (s *Service) runInFlightShutdown(ctx context.Context, client *kubernetes.Client, current plan) (outcome recoveryOutcome) {
	mutated := false
	defer func() { outcome.MutationAttempted = mutated; qualifyTopology(&outcome, current) }()
	const id, title = "inflight-shutdown", "Targeted in-flight shutdown"
	base := current.config.Experiments.ControlPath
	if base == "" {
		return controlledSkip(id, title)
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(parent, s.recoveryTimeout)
	defer cancel()
	observation := lifecycleObservation{parent: parent, experiment: ctx}
	defer observation.close()
	pods, result, err := client.ObservePods(ctx, current.clusterName, namespace, "app.kubernetes.io/name="+current.workloadName)
	if err != nil || failed(result) {
		return controlError(id, title, "Could not inspect pods before the targeted request.")
	}
	target := firstReadyPod(pods)
	if target == "" {
		return lifecycleBlocked(id, title, "No ready pod was available for the targeted request.")
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
	mutated = true
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
	unobservableRecovery := func(message string) recoveryOutcome {
		failure := controlError(id, title, message)
		failure.Evidence.Measurements = measurements
		return failure
	}
	// Observe recovery within the requirement deadline; a final bounded read can
	// distinguish an unmet requirement from unavailable observation, never extend
	// the time allowed for this experiment to pass.
	for {
		if parent.Err() != nil {
			return unobservableRecovery("Targeted shutdown recovery was interrupted.")
		}
		pods, res, expired, e := observation.pods(client, current, "app.kubernetes.io/name="+current.workloadName)
		if parent.Err() != nil {
			return unobservableRecovery("Targeted shutdown recovery was interrupted.")
		}
		if e != nil || failed(res) {
			return unobservableRecovery("Replica recovery could not be observed after the targeted request completed.")
		}
		ready, total, _ := summarizePods(pods)
		if ready == int(current.desiredReplicas) && total == ready && !expired {
			return lifecycleSuccess(id, title, "runtime."+id, "The identified request remained active at SIGTERM, completed on the targeted pod, and replicas recovered.", 0, "targeted request completed", measurements)
		}
		if expired {
			measurements = append(measurements, model.Measurement{Name: "ready_pods", Value: strconv.Itoa(ready)}, model.Measurement{Name: "total_pods", Value: strconv.Itoa(total)}, model.Measurement{Name: "expected_replicas", Value: strconv.Itoa(int(current.desiredReplicas))})
			measurements = append(measurements, lifecycleDeadlineMeasurements(true)...)
			if ready == int(current.desiredReplicas) && total == ready {
				return lifecycleCompletionUnobserved(id, title, "targeted_recovery_completion_unobserved", "Replicas were observed recovered only after the deadline; timely recovery was not established.", 0, trafficObservation{}, measurements)
			}
			failure := lifecycleFailure(id, title, "runtime."+id, "The targeted request completed, but the final observed replicas did not meet the recovery requirement.", "Inspect application readiness and replica replacement within the configured recovery deadline.", 0, trafficObservation{}, measurements)
			failure.Finding.Observed = fmt.Sprintf("The targeted request completed; final ready replicas %d/%d, total pods %d.", ready, current.desiredReplicas, total)
			failure.Finding.Expected = "The targeted request completes and the requested replicas recover before the deadline."
			return failure
		}
		pause(ctx, s.poll)
	}
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
