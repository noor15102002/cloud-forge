package verification

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	k6executor "github.com/noor15102002/cloud-forge/internal/executor/k6"
	"github.com/noor15102002/cloud-forge/internal/executor/kubernetes"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

func (s *Service) runLoadAndAutoscaling(ctx context.Context, loadClient *k6executor.Client, client *kubernetes.Client, current plan, workspace, hpaManifestPath string) (recoveryOutcome, recoveryOutcome) {
	loadURL := current.loadURL
	if loadURL == "" {
		return skippedLoad("No explicit endpoints.load was configured."), skippedAutoscaling("A representative load endpoint is required to test autoscaling.")
	}

	var starting kubernetes.HPAState
	metricsReady := false
	metricsReason := ""
	if hpaManifestPath != "" {
		if result := client.Apply(ctx, current.clusterName, hpaManifestPath); failed(result) {
			failure := lifecycleExecutionError("horizontal-autoscaling", "Horizontal autoscaling under load", "hpa_apply_failed", "CloudForge could not apply the generated HPA.", commandGuidance(result, nil), trafficObservation{})
			return skippedLoad("The load profile was not started because the HPA could not be applied."), failure
		}
		metricsCtx, cancel := context.WithTimeout(ctx, s.hpaMetricsTimeout)
		defer cancel()
		for {
			if errors.Is(metricsCtx.Err(), context.DeadlineExceeded) {
				goto metricsComplete
			}
			state, result, observeErr := client.ObserveHPA(metricsCtx, current.clusterName, namespace, current.hpaName)
			if observeErr != nil || failed(result) {
				if errors.Is(metricsCtx.Err(), context.DeadlineExceeded) {
					goto metricsComplete
				}
				failure := lifecycleExecutionError("horizontal-autoscaling", "Horizontal autoscaling under load", "hpa_observation_failed", "CloudForge could not inspect the HPA.", commandGuidance(result, observeErr), trafficObservation{})
				return skippedLoad("The load profile was not started because HPA state could not be inspected."), failure
			}
			starting = state
			metricsReason = state.Reason
			if state.MetricsReady {
				metricsReady = true
				break
			}
			select {
			case <-metricsCtx.Done():
				goto metricsComplete
			case <-time.After(s.poll):
			}
		}
	}

metricsComplete:
	if hpaManifestPath == "" || !metricsReady {
		execution := executeLoad(ctx, loadClient, workspace, loadURL, s.loadProfile)
		loadOutcome := loadOutcomeForExecution(execution, s.loadProfile)
		if loadOutcome.ExitCode != 0 {
			return loadOutcome, skippedAutoscaling("Autoscaling evidence is unavailable because the load profile did not complete successfully.")
		}
		if hpaManifestPath == "" {
			return loadOutcome, skippedAutoscaling(current.hpaSkipReason)
		}
		reason := metricsReason
		if reason == "" {
			reason = "CPU metrics did not become available before the bounded observation deadline."
		}
		return loadOutcome, skippedAutoscalingWithDiagnostic(reason)
	}

	loadStarted := time.Now()
	startReplicas := starting.CurrentReplicas
	if startReplicas == 0 {
		startReplicas = current.desiredReplicas
	}
	startDesiredReplicas := starting.DesiredReplicas
	peakReplicas := startReplicas
	peakDesiredReplicas := startDesiredReplicas
	peakCPU := int32(0)
	if starting.CurrentCPU != nil {
		peakCPU = *starting.CurrentCPU
	}
	scaleDuration := int64(0)
	scaleObserved := false
	peakReady := startReplicas
	scaleExpected := false
	var demandSince time.Time
	scaleCtx, cancelScale := context.WithTimeout(ctx, s.hpaScaleTimeout)
	defer cancelScale()
	loadCtx, cancelLoad := context.WithCancel(ctx)
	defer cancelLoad()
	loadDone := make(chan loadExecution, 1)
	go func() { loadDone <- executeLoad(loadCtx, loadClient, workspace, loadURL, s.loadProfile) }()
	var execution loadExecution
	loadFinished := false
	for {
		state, result, observeErr := client.ObserveHPA(scaleCtx, current.clusterName, namespace, current.hpaName)
		if observeErr != nil || failed(result) {
			cancelLoad()
			<-loadDone
			return skippedLoad("The load profile was canceled because HPA state could not be inspected."), lifecycleExecutionError("horizontal-autoscaling", "Horizontal autoscaling under load", "hpa_observation_failed", "CloudForge could not inspect HPA behavior during load.", commandGuidance(result, observeErr), trafficObservation{})
		}
		if state.DesiredReplicas > startReplicas {
			scaleExpected = true
		}
		if !loadFinished && state.CurrentCPU != nil && float64(*state.CurrentCPU) > float64(current.hpaTargetCPU)*1.1 {
			if demandSince.IsZero() {
				demandSince = time.Now()
			}
			if !current.hpaScaleDisabled && time.Since(demandSince) >= max(15*time.Second, current.hpaDemandWindow) {
				scaleExpected = true
			}
		} else {
			demandSince = time.Time{}
		}
		peakReplicas = maxInt32(peakReplicas, state.CurrentReplicas)
		peakDesiredReplicas = maxInt32(peakDesiredReplicas, state.DesiredReplicas)
		if state.CurrentCPU != nil {
			peakCPU = maxInt32(peakCPU, *state.CurrentCPU)
		}
		if peakReplicas > startReplicas {
			ready, _, _, readyResult, readyErr := client.ReadyPods(scaleCtx, current.clusterName, namespace, "app.kubernetes.io/name="+current.workloadName)
			if readyErr != nil || failed(readyResult) || ready < 0 || ready > 10 {
				cancelLoad()
				if !loadFinished {
					<-loadDone
				}
				return skippedLoad("Autoscaling observation could not complete."), lifecycleExecutionError("horizontal-autoscaling", "Horizontal autoscaling under load", "hpa_readiness_unavailable", "CloudForge could not inspect scaled pod readiness.", "Inspect the disposable cluster.", trafficObservation{})
			}
			peakReady = maxInt32(peakReady, int32(ready))
		}
		if peakReady > startReplicas {
			if !scaleObserved {
				scaleDuration = elapsedMilliseconds(time.Since(loadStarted))
				scaleObserved = true
			}
			if loadFinished {
				break
			}
		}
		select {
		case execution = <-loadDone:
			loadFinished = true
			if peakReady > startReplicas {
				goto scaleComplete
			}
		case <-scaleCtx.Done():
			if !loadFinished {
				execution = <-loadDone
			}
			goto scaleComplete
		case <-time.After(s.poll):
		}
	}

scaleComplete:
	loadOutcome := loadOutcomeForExecution(execution, s.loadProfile)
	if loadOutcome.ExitCode != 0 {
		return loadOutcome, skippedAutoscaling("Autoscaling evidence is unavailable because the load profile did not complete successfully.")
	}
	summary := execution.summary
	measurements := []model.Measurement{
		{Name: "starting_replicas", Value: strconv.FormatInt(int64(startReplicas), 10), Unit: "pods"},
		{Name: "peak_ready_replicas", Value: strconv.FormatInt(int64(peakReady), 10), Unit: "pods"},
		{Name: "peak_replicas", Value: strconv.FormatInt(int64(peakReplicas), 10), Unit: "pods"},
		{Name: "starting_desired_replicas", Value: strconv.FormatInt(int64(startDesiredReplicas), 10), Unit: "pods"},
		{Name: "peak_desired_replicas", Value: strconv.FormatInt(int64(peakDesiredReplicas), 10), Unit: "pods"},
		{Name: "minimum_replicas", Value: strconv.FormatInt(int64(current.hpaMinReplicas), 10), Unit: "pods"},
		{Name: "maximum_replicas", Value: strconv.FormatInt(int64(current.hpaMaxReplicas), 10), Unit: "pods"},
		{Name: "scale_up_duration_ms", Value: strconv.FormatInt(scaleDuration, 10), Unit: "ms"},
		{Name: "target_cpu_percent", Value: strconv.FormatInt(int64(current.hpaTargetCPU), 10), Unit: "percent"},
		{Name: "peak_cpu_percent", Value: strconv.FormatInt(int64(peakCPU), 10), Unit: "percent"},
		{Name: "request_count", Value: strconv.FormatInt(summary.RequestCount, 10), Unit: "requests"},
		{Name: "failed_requests", Value: strconv.FormatInt(failedRequests(summary), 10), Unit: "requests"},
		{Name: "latency_p95_ms", Value: decimal(summary.P95MS), Unit: "ms"},
	}
	if peakReady <= startReplicas && !scaleExpected {
		outcome := skippedAutoscaling("Insufficient scaling demand was observed; remaining at the starting replica count is not an application failure.")
		outcome.Evidence.Measurements = measurements
		return loadOutcome, outcome
	}
	if peakReady <= startReplicas {
		return loadOutcome, lifecycleFailure("horizontal-autoscaling", "Horizontal autoscaling under load", "runtime.horizontal-autoscaling", "The HPA received CPU metrics but did not scale the Deployment.", "Review CPU requests, the utilization target, metrics-server, and the load profile.", elapsedMilliseconds(time.Since(loadStarted)), trafficObservation{}, measurements)
	}
	return loadOutcome, lifecycleSuccess("horizontal-autoscaling", "Horizontal autoscaling under load", "runtime.horizontal-autoscaling", "The HPA produced additional ready replicas under bounded load.", elapsedMilliseconds(time.Since(loadStarted)), fmt.Sprintf("replicas increased from %d to %d", startReplicas, peakReplicas), measurements)
}

type loadExecution struct {
	summary k6executor.Summary
	result  model.CommandResult
	err     error
}

func executeLoad(ctx context.Context, client *k6executor.Client, workspace, endpoint string, profile k6executor.Profile) loadExecution {
	summary, result, err := client.Run(ctx, workspace, endpoint, profile)
	return loadExecution{summary: summary, result: result, err: err}
}

func loadOutcomeForExecution(execution loadExecution, profile k6executor.Profile) recoveryOutcome {
	if execution.err != nil {
		return lifecycleExecutionError("load-profile", "Bounded HTTP load profile", "load_output_invalid", "CloudForge could not read k6's summary.", execution.err.Error(), trafficObservation{})
	}
	if failed(execution.result) {
		return lifecycleExecutionError("load-profile", "Bounded HTTP load profile", "load_execution_failed", "k6 could not complete the bounded load profile.", commandGuidance(execution.result, nil), trafficObservation{})
	}
	return normalizedLoadOutcome(execution.summary, execution.result.DurationMS, profile)
}

func normalizedLoadOutcome(summary k6executor.Summary, duration int64, profile k6executor.Profile) recoveryOutcome {
	measurements := []model.Measurement{
		{Name: "request_count", Value: strconv.FormatInt(summary.RequestCount, 10), Unit: "requests"},
		{Name: "throughput_rps", Value: decimal(summary.ThroughputRPS), Unit: "requests/second"},
		{Name: "error_rate", Value: decimal(summary.ErrorRate), Unit: "ratio"},
		{Name: "failed_requests", Value: strconv.FormatInt(failedRequests(summary), 10), Unit: "requests"},
		{Name: "latency_p50_ms", Value: decimal(summary.P50MS), Unit: "ms"},
		{Name: "latency_p95_ms", Value: decimal(summary.P95MS), Unit: "ms"},
		{Name: "latency_p99_ms", Value: decimal(summary.P99MS), Unit: "ms"},
		{Name: "virtual_users", Value: strconv.Itoa(profile.VirtualUsers), Unit: "users"},
		{Name: "duration_ms", Value: strconv.FormatInt(profile.Duration.Milliseconds(), 10), Unit: "ms"},
	}
	if summary.ErrorRate > 0 {
		return lifecycleFailure("load-profile", "Bounded HTTP load profile", "runtime.load-profile", "The application returned failed requests during the bounded load profile.", "Inspect application capacity, error handling, and resource limits.", duration, trafficObservation{}, measurements)
	}
	return lifecycleSuccess("load-profile", "Bounded HTTP load profile", "runtime.load-profile", "The application completed the bounded load profile without failed requests.", duration, fmt.Sprintf("%d requests at %s requests/second", summary.RequestCount, decimal(summary.ThroughputRPS)), measurements)
}

func skippedLoad(reason string) recoveryOutcome {
	return recoveryOutcome{Evidence: model.Evidence{ExperimentID: "load-profile", Title: "Bounded HTTP load profile", Status: model.StatusSkipped, Summary: reason}}
}
func skippedAutoscaling(reason string) recoveryOutcome {
	return recoveryOutcome{Evidence: model.Evidence{ExperimentID: "horizontal-autoscaling", Title: "Horizontal autoscaling under load", Status: model.StatusSkipped, Summary: reason}}
}
func skippedAutoscalingWithDiagnostic(reason string) recoveryOutcome {
	result := skippedAutoscaling("CPU metrics were unavailable, so CloudForge skipped the HPA scale assertion.")
	result.Diagnostic = &model.Diagnostic{Code: "hpa_metrics_unavailable", Status: model.StatusWarn, Message: result.Evidence.Summary, Guidance: "Observed cause: " + reason + " Check that metrics-server is healthy and that the Deployment declares CPU requests."}
	return result
}
func failedRequests(summary k6executor.Summary) int64 {
	return int64(summary.ErrorRate*float64(summary.RequestCount) + 0.5)
}
func decimal(value float64) string { return strconv.FormatFloat(value, 'f', 3, 64) }
func maxInt32(left, right int32) int32 {
	if left > right {
		return left
	}
	return right
}
