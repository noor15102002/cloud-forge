package verification

import (
	"context"
	"fmt"
	"strconv"
	"time"

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
}

type trafficSample struct {
	StartedAt time.Time
}

type recoveryOutcome struct {
	Evidence   model.Evidence
	Finding    *model.Finding
	Diagnostic *model.Diagnostic
	ExitCode   int
}

func (s *Service) waitForHTTP(ctx context.Context, url string) httpObservation {
	started := time.Now()
	result := httpObservation{}
	ticker := time.NewTicker(s.poll)
	defer ticker.Stop()
	for {
		status, err := s.probe(ctx, url)
		result.Attempts++
		result.Status = status
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

func (s *Service) runPodRecovery(ctx context.Context, client *kubernetes.Client, current plan) recoveryOutcome {
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
		return recoveryFailure("No ready application pod was available for controlled deletion.", "Verify that the Deployment has at least one ready replica before running recovery.", 0, trafficObservation{}, 0, 0, 0)
	}

	trafficCtx, stopTraffic := context.WithCancel(ctx)
	sampled := make(chan trafficSample, 1)
	trafficDone := make(chan trafficObservation, 1)
	go func() { trafficDone <- s.collectTraffic(trafficCtx, current.readinessURL, sampled) }()
	select {
	case <-sampled:
	case <-ctx.Done():
		stopTraffic()
		traffic := <-trafficDone
		return recoveryExecutionError("pod_recovery_canceled", "Pod recovery was canceled before traffic started.", ctx.Err().Error(), traffic)
	}

	recoveryStarted := time.Now()
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
	if ctx.Err() != nil {
		return recoveryExecutionError("pod_recovery_canceled", "Pod recovery was canceled before completion.", ctx.Err().Error(), traffic)
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
	outcome := recoveryOutcome{Evidence: evidence, Finding: &finding}
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

func (s *Service) collectTraffic(ctx context.Context, url string, sampled chan<- trafficSample) trafficObservation {
	result := trafficObservation{}
	ticker := time.NewTicker(s.poll)
	defer ticker.Stop()
	var failureStarted time.Time
	for {
		requestStarted := time.Now()
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
		if err != nil || status < 200 || status >= 300 {
			result.Failures++
			if failureStarted.IsZero() {
				failureStarted = now
			}
		} else if !failureStarted.IsZero() {
			result.MaxDowntimeMS = maxInt64(result.MaxDowntimeMS, elapsedMilliseconds(now.Sub(failureStarted)))
			failureStarted = time.Time{}
		}
		select {
		case sampled <- trafficSample{StartedAt: requestStarted}:
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

func recoveryFailure(summary, guidance string, duration int64, traffic trafficObservation, ready, total int, restarts int32) recoveryOutcome {
	evidence := model.Evidence{
		ExperimentID: "pod-recovery", Title: "Pod recovery under traffic", Status: model.StatusFail, Summary: summary, DurationMS: duration,
		Measurements: []model.Measurement{
			{Name: "request_count", Value: strconv.Itoa(traffic.Requests), Unit: "requests"},
			{Name: "failed_requests", Value: strconv.Itoa(traffic.Failures), Unit: "requests"},
			{Name: "downtime_ms", Value: strconv.FormatInt(traffic.MaxDowntimeMS, 10), Unit: "ms"},
			{Name: "ready_pods", Value: strconv.Itoa(ready), Unit: "pods"},
			{Name: "total_pods", Value: strconv.Itoa(total), Unit: "pods"},
			{Name: "container_restarts", Value: strconv.FormatInt(int64(restarts), 10), Unit: "restarts"},
		},
	}
	finding := model.Finding{ID: "runtime.pod-recovery", Category: "reliability", Status: model.StatusFail, Severity: model.SeverityHigh, Summary: summary, Expected: "a healthy replacement pod", Remediation: guidance, DurationMS: duration}
	diagnostic := model.Diagnostic{Code: "pod_recovery_failed", Status: model.StatusFail, Message: summary, Guidance: guidance}
	return recoveryOutcome{Evidence: evidence, Finding: &finding, Diagnostic: &diagnostic, ExitCode: 1}
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
