package verification

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/noor15102002/cloud-forge/internal/executor/k3d"
	"github.com/noor15102002/cloud-forge/internal/executor/kubernetes"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

type httpObservation struct {
	Status     int
	Attempts   int
	Failures   int
	DurationMS int64
	Success    bool
}

type trafficObservation struct {
	Requests       int
	Failures       int
	MaxDowntimeMS  int64
	LastHTTPStatus int
	Samples        []trafficSample
}

type trafficSample struct {
	StartedAt   time.Time
	CompletedAt time.Time
	Failed      bool
}

type recoveryOutcome struct {
	MutationAttempted bool
	Evidence          model.Evidence
	Finding           *model.Finding
	Diagnostic        *model.Diagnostic
	ExitCode          int
}

func (s *Service) waitForHTTP(ctx context.Context, url string) httpObservation {
	started := time.Now()
	result := httpObservation{}
	ticker := time.NewTicker(s.poll)
	defer ticker.Stop()
	for {
		status, err := s.probe(ctx, url)
		result.Attempts++
		// Retain the last HTTP response. A final canceled transport attempt has
		// no HTTP status and must not erase an observed 200/degraded response.
		if status > 0 {
			result.Status = status
		}
		if err == nil && status >= 200 && status < 300 {
			result.Success = true
			result.DurationMS = elapsedMilliseconds(time.Since(started))
			return result
		}
		result.Failures++
		select {
		case <-ctx.Done():
			result.DurationMS = elapsedMilliseconds(time.Since(started))
			return result
		case <-ticker.C:
		}
	}
}

func (s *Service) runGracefulShutdown(ctx context.Context, client *kubernetes.Client, current plan) (outcome recoveryOutcome) {
	mutated := false
	defer func() { outcome.MutationAttempted = mutated; qualifyTopology(&outcome, current) }()
	parent := ctx
	ctx, cancelExperiment := context.WithTimeout(ctx, s.recoveryTimeout)
	defer cancelExperiment()
	selector := "app.kubernetes.io/name=" + current.workloadName
	pods, commandResult, err := client.ObservePods(ctx, current.clusterName, namespace, selector)
	if err != nil || failed(commandResult) {
		return lifecycleExecutionError("graceful-shutdown", "Graceful shutdown under traffic", "shutdown_observation_failed", "CloudForge could not inspect a pod before the shutdown experiment.", commandGuidance(commandResult, err), trafficObservation{})
	}
	podName := firstReadyPod(pods)
	if podName == "" {
		return lifecycleBlocked("graceful-shutdown", "Graceful shutdown under traffic", "No ready application pod was available for controlled termination.")
	}

	trafficCtx, stopTraffic := context.WithCancel(ctx)
	sampled := make(chan trafficSample, 1)
	started := make(chan time.Time, 1)
	trafficDone := make(chan trafficObservation, 1)
	go func() { trafficDone <- s.collectTraffic(trafficCtx, current.readinessURL, sampled, started) }()
	initialSample, ok := waitForSample(ctx, sampled, time.Time{})
	if !ok {
		stopTraffic()
		traffic := <-trafficDone
		return lifecycleExecutionError("graceful-shutdown", "Graceful shutdown under traffic", "shutdown_canceled", "Graceful shutdown was canceled before traffic started.", ctx.Err().Error(), traffic)
	}

	terminationStarted, ok := waitForRequestStart(ctx, started, initialSample.CompletedAt)
	if !ok {
		stopTraffic()
		traffic := <-trafficDone
		return lifecycleExecutionError("graceful-shutdown", "Graceful shutdown under traffic", "shutdown_canceled", "Graceful shutdown was canceled before an in-flight request started.", ctx.Err().Error(), traffic)
	}
	mutated = true
	deleteResult := client.DeletePod(ctx, current.clusterName, namespace, podName)
	terminationCompleted := time.Now()
	if failed(deleteResult) {
		stopTraffic()
		traffic := <-trafficDone
		return lifecycleExecutionError("graceful-shutdown", "Graceful shutdown under traffic", "shutdown_delete_failed", "kubectl could not terminate the selected application pod.", commandGuidance(deleteResult, nil), traffic)
	}

	recoveryCtx, cancelRecovery := context.WithTimeout(ctx, s.recoveryTimeout)
	defer cancelRecovery()
	ready, total := 0, 0
	var restarts int32
	recovered := false
	for {
		observed, result, observeErr := client.ObservePods(recoveryCtx, current.clusterName, namespace, selector)
		if recoveryCtx.Err() != nil && parent.Err() == nil {
			goto shutdownComplete
		}
		if observeErr != nil || failed(result) {
			stopTraffic()
			traffic := <-trafficDone
			return lifecycleExecutionError("graceful-shutdown", "Graceful shutdown under traffic", "shutdown_recovery_observation_failed", "CloudForge could not inspect the replacement pod after SIGTERM.", commandGuidance(result, observeErr), traffic)
		}
		ready, total, restarts = summarizePods(observed)
		if ready == int(current.desiredReplicas) && total == int(current.desiredReplicas) {
			recovered = true
			break
		}
		select {
		case <-recoveryCtx.Done():
			goto shutdownComplete
		case <-time.After(s.poll):
		}
	}

shutdownComplete:
	_, _ = waitForSample(ctx, sampled, terminationCompleted)
	stopTraffic()
	traffic := <-trafficDone
	duration := elapsedMilliseconds(time.Since(terminationStarted))
	if parent.Err() != nil {
		return lifecycleExecutionError("graceful-shutdown", "Graceful shutdown under traffic", "shutdown_canceled", "Graceful shutdown was canceled before completion.", parent.Err().Error(), traffic)
	}
	finalStatus, finalErr := s.probe(ctx, current.healthURL)
	if finalErr != nil {
		finalStatus = 0
	}
	inFlight := overlappingRequests(traffic.Samples, terminationStarted, terminationCompleted)
	measurements := []model.Measurement{
		{Name: "request_count", Value: strconv.Itoa(traffic.Requests), Unit: "requests"},
		{Name: "requests_overlapping_deletion", Value: strconv.Itoa(inFlight), Unit: "requests"},
		{Name: "dropped_requests", Value: strconv.Itoa(traffic.Failures), Unit: "requests"},
		{Name: "downtime_ms", Value: strconv.FormatInt(traffic.MaxDowntimeMS, 10), Unit: "ms"},
		{Name: "termination_duration_ms", Value: strconv.FormatInt(elapsedMilliseconds(terminationCompleted.Sub(terminationStarted)), 10), Unit: "ms"},
		{Name: "replacement_duration_ms", Value: strconv.FormatInt(duration, 10), Unit: "ms"},
		{Name: "ready_pods", Value: strconv.Itoa(ready), Unit: "pods"},
		{Name: "total_pods", Value: strconv.Itoa(total), Unit: "pods"},
		{Name: "container_restarts", Value: strconv.FormatInt(int64(restarts), 10), Unit: "restarts"},
		{Name: "final_http_status", Value: strconv.Itoa(finalStatus)},
	}
	if !recovered || traffic.Failures > 0 || finalStatus < 200 || finalStatus >= 300 {
		return lifecycleFailure("graceful-shutdown", "Graceful shutdown under traffic", "runtime.graceful-shutdown", "The application dropped traffic or did not recover cleanly after SIGTERM.", "Inspect SIGTERM handling, readiness removal, connection draining, replica count, and termination grace period.", duration, traffic, measurements)
	}
	return lifecycleSuccess("graceful-shutdown", "Graceful shutdown under traffic", "runtime.graceful-shutdown", "The application remained healthy while Kubernetes terminated and replaced a pod.", duration, fmt.Sprintf("%d requests overlapping deletion, %d dropped requests", inFlight, traffic.Failures), measurements)
}

func (s *Service) runRollingDeployment(ctx context.Context, k3dClient *k3d.Client, client *kubernetes.Client, current plan, buildResult model.CommandResult) (outcome recoveryOutcome) {
	mutated := false
	defer func() { outcome.MutationAttempted = mutated; qualifyTopology(&outcome, current) }()
	parent := ctx
	ctx, cancelExperiment := context.WithTimeout(ctx, s.rolloutTimeout)
	defer cancelExperiment()
	title := "Rolling deployment under traffic"
	if failed(buildResult) {
		if isApplicationBuildFailure(buildResult) {
			return rolloutImageBuildFailure(title, buildResult)
		}
		return lifecycleExecutionError("rolling-deployment", title, "rollout_image_build_failed", "CloudForge could not build the version B image.", commandGuidance(buildResult, nil), trafficObservation{}, model.Measurement{Name: "version_b_build_duration_ms", Value: strconv.FormatInt(buildResult.DurationMS, 10), Unit: "ms"})
	}
	if result := k3dClient.ImportImage(ctx, current.clusterName, current.rolloutImage); failed(result) {
		return lifecycleExecutionError("rolling-deployment", title, "rollout_image_import_failed", "k3d could not import the version B image.", commandGuidance(result, nil), trafficObservation{})
	}
	selector := "app.kubernetes.io/name=" + current.workloadName
	before, beforeResult, beforeErr := client.ObservePods(ctx, current.clusterName, namespace, selector)
	if beforeErr != nil || failed(beforeResult) {
		return lifecycleExecutionError("rolling-deployment", title, "rollout_observation_failed", "CloudForge could not inspect version A before rollout.", commandGuidance(beforeResult, beforeErr), trafficObservation{})
	}
	baselineReady, _, _ := summarizePods(before)

	trafficCtx, stopTraffic := context.WithCancel(ctx)
	sampled := make(chan trafficSample, 1)
	trafficDone := make(chan trafficObservation, 1)
	go func() { trafficDone <- s.collectTraffic(trafficCtx, current.readinessURL, sampled, nil) }()
	if _, ok := waitForSample(ctx, sampled, time.Time{}); !ok {
		stopTraffic()
		traffic := <-trafficDone
		return lifecycleExecutionError("rolling-deployment", title, "rollout_canceled", "Rolling deployment was canceled before traffic started.", ctx.Err().Error(), traffic)
	}

	rolloutStarted := time.Now()
	mutated = true
	setResult := client.SetImage(ctx, current.clusterName, namespace, current.workloadName, "application", current.rolloutImage)
	if failed(setResult) {
		stopTraffic()
		traffic := <-trafficDone
		return lifecycleExecutionError("rolling-deployment", title, "rollout_update_failed", "kubectl could not start the version B rollout.", commandGuidance(setResult, nil), traffic)
	}

	rolloutCtx, cancelRollout := context.WithTimeout(ctx, s.rolloutTimeout)
	defer cancelRollout()
	var ready, total, targetReady int
	readinessTransitions, versionTransitions := 0, 0
	minimumReady := baselineReady
	previousReady, previousTarget := baselineReady, 0
	completed := false
	for {
		pods, result, observeErr := client.ObservePods(rolloutCtx, current.clusterName, namespace, selector)
		if rolloutCtx.Err() != nil && parent.Err() == nil {
			goto rolloutComplete
		}
		if observeErr != nil || failed(result) {
			stopTraffic()
			traffic := <-trafficDone
			return lifecycleExecutionError("rolling-deployment", title, "rollout_observation_failed", "CloudForge could not inspect the rolling Deployment.", commandGuidance(result, observeErr), traffic)
		}
		ready, total, _ = summarizePods(pods)
		targetReady = readyPodsWithImage(pods, current.rolloutImage)
		if ready != previousReady {
			readinessTransitions++
			previousReady = ready
		}
		if targetReady != previousTarget {
			versionTransitions++
			previousTarget = targetReady
		}
		if ready < minimumReady {
			minimumReady = ready
		}
		if total == int(current.desiredReplicas) && ready == int(current.desiredReplicas) && targetReady == int(current.desiredReplicas) {
			completed = true
			break
		}
		select {
		case <-rolloutCtx.Done():
			goto rolloutComplete
		case <-time.After(s.poll):
		}
	}

rolloutComplete:
	completedAt := time.Now()
	_, _ = waitForSample(ctx, sampled, completedAt)
	stopTraffic()
	traffic := <-trafficDone
	duration := elapsedMilliseconds(completedAt.Sub(rolloutStarted))
	if parent.Err() != nil {
		return lifecycleExecutionError("rolling-deployment", title, "rollout_canceled", "Rolling deployment was canceled before completion.", parent.Err().Error(), traffic)
	}
	finalStatus, finalErr := s.probe(ctx, current.healthURL)
	if finalErr != nil {
		finalStatus = 0
	}
	measurements := []model.Measurement{
		{Name: "source_version", Value: "a"},
		{Name: "target_version", Value: "b"},
		{Name: "version_b_build_duration_ms", Value: strconv.FormatInt(buildResult.DurationMS, 10), Unit: "ms"},
		{Name: "rollout_duration_ms", Value: strconv.FormatInt(duration, 10), Unit: "ms"},
		{Name: "readiness_transitions", Value: strconv.Itoa(readinessTransitions), Unit: "transitions"},
		{Name: "version_transitions", Value: strconv.Itoa(versionTransitions), Unit: "transitions"},
		{Name: "minimum_ready_pods", Value: strconv.Itoa(minimumReady), Unit: "pods"},
		{Name: "target_ready_pods", Value: strconv.Itoa(targetReady), Unit: "pods"},
		{Name: "total_pods", Value: strconv.Itoa(total), Unit: "pods"},
		{Name: "request_count", Value: strconv.Itoa(traffic.Requests), Unit: "requests"},
		{Name: "failed_requests", Value: strconv.Itoa(traffic.Failures), Unit: "requests"},
		{Name: "downtime_ms", Value: strconv.FormatInt(traffic.MaxDowntimeMS, 10), Unit: "ms"},
		{Name: "final_http_status", Value: strconv.Itoa(finalStatus)},
	}
	if !completed || traffic.Failures > 0 || finalStatus < 200 || finalStatus >= 300 {
		return lifecycleFailure("rolling-deployment", title, "runtime.rolling-deployment", "Version B did not roll out without failed traffic.", "Inspect version B readiness, rolling update strategy, capacity, and application startup behavior.", duration, traffic, measurements)
	}
	return lifecycleSuccess("rolling-deployment", title, "runtime.rolling-deployment", "Version B became ready without interrupting application traffic.", duration, fmt.Sprintf("%d/%d version B pods ready with %d failed requests", targetReady, total, traffic.Failures), measurements)
}

func (s *Service) runPodRecovery(ctx context.Context, client *kubernetes.Client, current plan) (outcome recoveryOutcome) {
	mutated := false
	defer func() { outcome.MutationAttempted = mutated; qualifyTopology(&outcome, current) }()
	parent := ctx
	ctx, cancelExperiment := context.WithTimeout(ctx, s.recoveryTimeout)
	defer cancelExperiment()
	selector := "app.kubernetes.io/name=" + current.workloadName
	pods, commandResult, err := client.ObservePods(ctx, current.clusterName, namespace, selector)
	if err != nil || failed(commandResult) {
		return recoveryExecutionError("pod_recovery_observation_failed", "CloudForge could not inspect a pod before the recovery experiment.", commandGuidance(commandResult, err))
	}
	podName := ""
	for _, pod := range pods {
		if pod.Ready {
			podName = pod.Name
			break
		}
	}
	if podName == "" {
		return lifecycleBlocked("pod-recovery", "Pod recovery under traffic", "No ready application pod was available for controlled deletion.")
	}

	trafficCtx, stopTraffic := context.WithCancel(ctx)
	sampled := make(chan trafficSample, 1)
	trafficDone := make(chan trafficObservation, 1)
	go func() { trafficDone <- s.collectTraffic(trafficCtx, current.readinessURL, sampled, nil) }()
	select {
	case <-sampled:
	case <-ctx.Done():
		stopTraffic()
		traffic := <-trafficDone
		return recoveryExecutionError("pod_recovery_canceled", "Pod recovery was canceled before traffic started.", ctx.Err().Error(), traffic)
	}

	recoveryStarted := time.Now()
	mutated = true
	deleteResult := client.DeletePod(ctx, current.clusterName, namespace, podName)
	if failed(deleteResult) {
		stopTraffic()
		traffic := <-trafficDone
		return recoveryExecutionError("pod_delete_failed", "kubectl could not delete the selected application pod.", commandGuidance(deleteResult, nil), traffic)
	}
	deletedAt := time.Now()
	for {
		select {
		case sample := <-sampled:
			if !sample.StartedAt.Before(deletedAt) {
				goto trafficContinued
			}
		case <-ctx.Done():
			stopTraffic()
			traffic := <-trafficDone
			return recoveryExecutionError("pod_recovery_canceled", "Pod recovery was canceled while traffic was running.", ctx.Err().Error(), traffic)
		}
	}

trafficContinued:
	recoveryCtx, cancelRecovery := context.WithTimeout(ctx, s.recoveryTimeout)
	defer cancelRecovery()
	ready, total := 0, 0
	var restarts int32
	recovered := false
	for {
		observed, result, observeErr := client.ObservePods(recoveryCtx, current.clusterName, namespace, selector)
		if recoveryCtx.Err() != nil && parent.Err() == nil {
			goto complete
		}
		if observeErr != nil || failed(result) {
			stopTraffic()
			traffic := <-trafficDone
			return recoveryExecutionError("pod_recovery_observation_failed", "CloudForge could not inspect the replacement pod.", commandGuidance(result, observeErr), traffic)
		}
		ready, total, restarts = summarizePods(observed)
		if ready == int(current.desiredReplicas) && total == int(current.desiredReplicas) {
			recovered = true
			break
		}
		select {
		case <-recoveryCtx.Done():
			goto complete
		case <-time.After(s.poll):
		}
	}

complete:
	replacementDuration := elapsedMilliseconds(time.Since(recoveryStarted))
	stopTraffic()
	traffic := <-trafficDone
	if parent.Err() != nil {
		return recoveryExecutionError("pod_recovery_canceled", "Pod recovery was canceled before completion.", parent.Err().Error(), traffic)
	}
	finalStatus, finalErr := s.probe(ctx, current.healthURL)
	if finalErr != nil {
		finalStatus = 0
	}
	status := model.StatusPass
	summary := "The Deployment replaced a deleted pod while serving healthy traffic."
	if !recovered || traffic.Failures > 0 || finalStatus < 200 || finalStatus >= 300 {
		status = model.StatusFail
		summary = "Pod recovery did not preserve healthy application traffic."
	}
	evidence := model.Evidence{
		ExperimentID: "pod-recovery", Title: "Pod recovery under traffic", Status: status, Summary: summary,
		DurationMS: replacementDuration,
		Measurements: []model.Measurement{
			{Name: "request_count", Value: strconv.Itoa(traffic.Requests), Unit: "requests"},
			{Name: "failed_requests", Value: strconv.Itoa(traffic.Failures), Unit: "requests"},
			{Name: "downtime_ms", Value: strconv.FormatInt(traffic.MaxDowntimeMS, 10), Unit: "ms"},
			{Name: "replacement_duration_ms", Value: strconv.FormatInt(replacementDuration, 10), Unit: "ms"},
			{Name: "final_http_status", Value: strconv.Itoa(finalStatus)},
			{Name: "ready_pods", Value: strconv.Itoa(ready), Unit: "pods"},
			{Name: "total_pods", Value: strconv.Itoa(total), Unit: "pods"},
			{Name: "container_restarts", Value: strconv.FormatInt(int64(restarts), 10), Unit: "restarts"},
		},
	}
	finding := model.Finding{
		ID: "runtime.pod-recovery", Category: "reliability", Status: status, Severity: model.SeverityHigh,
		Summary: summary, Observed: fmt.Sprintf("%d failed requests, %d ms downtime, HTTP %d", traffic.Failures, traffic.MaxDowntimeMS, finalStatus),
		Expected: "zero failed requests, zero downtime, and a healthy replacement pod", DurationMS: replacementDuration,
	}
	outcome = recoveryOutcome{Evidence: evidence, Finding: &finding}
	if status == model.StatusFail {
		outcome.ExitCode = 1
		outcome.Diagnostic = &model.Diagnostic{
			Code: "pod_recovery_failed", Status: model.StatusFail, Message: summary,
			Guidance: "Inspect readiness behavior, replica count, termination handling, and replacement startup time.",
		}
		finding.Remediation = outcome.Diagnostic.Guidance
		outcome.Finding = &finding
	}
	return outcome
}

func (s *Service) collectTraffic(ctx context.Context, url string, sampled chan<- trafficSample, started chan<- time.Time) trafficObservation {
	result := trafficObservation{}
	ticker := time.NewTicker(s.trafficPoll)
	defer ticker.Stop()
	var failureStarted time.Time
	for {
		requestStarted := time.Now()
		if started != nil {
			select {
			case started <- requestStarted:
			default:
			}
		}
		status, err := s.probe(ctx, url)
		now := time.Now()
		if ctx.Err() != nil {
			if !failureStarted.IsZero() {
				result.MaxDowntimeMS = maxInt64(result.MaxDowntimeMS, elapsedMilliseconds(now.Sub(failureStarted)))
			}
			return result
		}
		result.Requests++
		result.LastHTTPStatus = status
		requestFailed := err != nil || status < 200 || status >= 300
		if requestFailed {
			result.Failures++
			if failureStarted.IsZero() {
				failureStarted = now
			}
		} else if !failureStarted.IsZero() {
			result.MaxDowntimeMS = maxInt64(result.MaxDowntimeMS, elapsedMilliseconds(now.Sub(failureStarted)))
			failureStarted = time.Time{}
		}
		sample := trafficSample{StartedAt: requestStarted, CompletedAt: now, Failed: requestFailed}
		result.Samples = append(result.Samples, sample)
		select {
		case sampled <- sample:
		default:
		}
		select {
		case <-ctx.Done():
			if !failureStarted.IsZero() {
				result.MaxDowntimeMS = maxInt64(result.MaxDowntimeMS, elapsedMilliseconds(time.Since(failureStarted)))
			}
			return result
		case <-ticker.C:
		}
	}
}

func waitForRequestStart(ctx context.Context, started <-chan time.Time, after time.Time) (time.Time, bool) {
	for {
		select {
		case value := <-started:
			if value.After(after) {
				return value, true
			}
		case <-ctx.Done():
			return time.Time{}, false
		}
	}
}

func waitForSample(ctx context.Context, sampled <-chan trafficSample, notBefore time.Time) (trafficSample, bool) {
	for {
		select {
		case sample := <-sampled:
			if notBefore.IsZero() || !sample.StartedAt.Before(notBefore) {
				return sample, true
			}
		case <-ctx.Done():
			return trafficSample{}, false
		}
	}
}

func firstReadyPod(pods []kubernetes.PodState) string {
	for _, pod := range pods {
		if pod.Ready && !pod.Terminating {
			return pod.Name
		}
	}
	return ""
}

func overlappingRequests(samples []trafficSample, started, completed time.Time) int {
	count := 0
	for _, sample := range samples {
		if !sample.StartedAt.After(completed) && !sample.CompletedAt.Before(started) {
			count++
		}
	}
	return count
}

func readyPodsWithImage(pods []kubernetes.PodState, image string) int {
	count := 0
	for _, pod := range pods {
		if pod.Ready && pod.Image == image {
			count++
		}
	}
	return count
}

func lifecycleSuccess(experimentID, title, findingID, summary string, duration int64, observed string, measurements []model.Measurement) recoveryOutcome {
	evidence := model.Evidence{ExperimentID: experimentID, Title: title, Status: model.StatusPass, Summary: summary, DurationMS: duration, Measurements: measurements}
	finding := model.Finding{
		ID: findingID, Category: "reliability", Status: model.StatusPass, Severity: model.SeverityInfo,
		Summary: summary, Observed: observed, Expected: "no dropped requests and a healthy final state", DurationMS: duration,
	}
	return recoveryOutcome{Evidence: evidence, Finding: &finding}
}

func lifecycleFailure(experimentID, title, findingID, summary, guidance string, duration int64, traffic trafficObservation, measurements []model.Measurement) recoveryOutcome {
	if measurements == nil {
		measurements = []model.Measurement{
			{Name: "request_count", Value: strconv.Itoa(traffic.Requests), Unit: "requests"},
			{Name: "failed_requests", Value: strconv.Itoa(traffic.Failures), Unit: "requests"},
			{Name: "downtime_ms", Value: strconv.FormatInt(traffic.MaxDowntimeMS, 10), Unit: "ms"},
		}
	}
	evidence := model.Evidence{ExperimentID: experimentID, Title: title, Status: model.StatusFail, Summary: summary, DurationMS: duration, Measurements: measurements}
	finding := model.Finding{
		ID: findingID, Category: "reliability", Status: model.StatusFail, Severity: model.SeverityHigh,
		Summary: summary, Observed: fmt.Sprintf("%d failed requests and %d ms downtime", traffic.Failures, traffic.MaxDowntimeMS),
		Expected: "no dropped requests and a healthy final state", Remediation: guidance, DurationMS: duration,
	}
	diagnostic := model.Diagnostic{Code: strings.ReplaceAll(experimentID, "-", "_") + "_failed", Status: model.StatusFail, Message: summary, Guidance: guidance}
	return recoveryOutcome{Evidence: evidence, Finding: &finding, Diagnostic: &diagnostic, ExitCode: 1}
}

func lifecycleExecutionError(experimentID, title, code, summary, guidance string, traffic trafficObservation, additional ...model.Measurement) recoveryOutcome {
	measurements := []model.Measurement{
		{Name: "request_count", Value: strconv.Itoa(traffic.Requests), Unit: "requests"},
		{Name: "failed_requests", Value: strconv.Itoa(traffic.Failures), Unit: "requests"},
		{Name: "downtime_ms", Value: strconv.FormatInt(traffic.MaxDowntimeMS, 10), Unit: "ms"},
	}
	measurements = append(measurements, additional...)
	evidence := model.Evidence{
		ExperimentID: experimentID, Title: title, Status: model.StatusError, Summary: summary,
		Measurements: measurements,
	}
	diagnostic := model.Diagnostic{Code: code, Status: model.StatusError, Message: summary, Guidance: guidance}
	return recoveryOutcome{Evidence: evidence, Diagnostic: &diagnostic, ExitCode: 2}
}

func rolloutImageBuildFailure(title string, result model.CommandResult) recoveryOutcome {
	summary := "The application version B image did not build."
	guidance := "Run the Docker build with CLOUDFORGE_VERSION=b, correct the failing instruction, and retry verification."
	evidence := model.Evidence{
		ExperimentID: "rolling-deployment", Title: title, Status: model.StatusFail, Summary: summary,
		DurationMS:   result.DurationMS,
		Measurements: []model.Measurement{{Name: "version_b_build_duration_ms", Value: strconv.FormatInt(result.DurationMS, 10), Unit: "ms"}},
	}
	finding := model.Finding{
		ID: "container.rollout-build", Category: "container", Status: model.StatusFail, Severity: model.SeverityHigh,
		Summary: summary, Observed: "fail", Expected: "version B image builds successfully", Remediation: guidance,
		DurationMS: result.DurationMS, Source: &model.SourceReference{Path: "Dockerfile"},
	}
	diagnostic := model.Diagnostic{Code: "rollout_image_build_failed", Status: model.StatusFail, Message: summary, Guidance: guidance}
	return recoveryOutcome{Evidence: evidence, Finding: &finding, Diagnostic: &diagnostic, ExitCode: 1}
}

func summarizePods(pods []kubernetes.PodState) (int, int, int32) {
	ready := 0
	var restarts int32
	for _, pod := range pods {
		if pod.Ready {
			ready++
		}
		restarts += pod.Restarts
	}
	return ready, len(pods), restarts
}

func recoveryExecutionError(code, summary, guidance string, traffic ...trafficObservation) recoveryOutcome {
	observed := trafficObservation{}
	if len(traffic) > 0 {
		observed = traffic[0]
	}
	diagnostic := model.Diagnostic{Code: code, Status: model.StatusError, Message: summary, Guidance: guidance}
	evidence := model.Evidence{
		ExperimentID: "pod-recovery", Title: "Pod recovery under traffic", Status: model.StatusError, Summary: summary,
		Measurements: []model.Measurement{
			{Name: "request_count", Value: strconv.Itoa(observed.Requests), Unit: "requests"},
			{Name: "failed_requests", Value: strconv.Itoa(observed.Failures), Unit: "requests"},
			{Name: "downtime_ms", Value: strconv.FormatInt(observed.MaxDowntimeMS, 10), Unit: "ms"},
		},
	}
	return recoveryOutcome{Evidence: evidence, Diagnostic: &diagnostic, ExitCode: 2}
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}

func lifecycleBlocked(id, title, reason string) recoveryOutcome {
	return recoveryOutcome{Evidence: model.Evidence{ExperimentID: id, Title: title, Status: model.StatusBlocked, Summary: reason}, ExitCode: 1}
}
