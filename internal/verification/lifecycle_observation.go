package verification

import (
	"context"
	"errors"
	"time"

	"github.com/noor15102002/cloud-forge/internal/executor/kubernetes"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

const lifecycleFinalObservationLimit = 5 * time.Second

// lifecycleObservation separates the requirement deadline from one final read.
// The extra budget is shared by the final pod read and final HTTP health probe;
// it never extends mutation, traffic collection, or the time allowed to pass.
type lifecycleObservation struct {
	parent, experiment context.Context
	final              context.Context
	cancel             context.CancelFunc
}

func (o *lifecycleObservation) close() {
	if o.cancel != nil {
		o.cancel()
	}
}

func (o *lifecycleObservation) context() context.Context {
	if o.parent.Err() != nil || !errors.Is(o.experiment.Err(), context.DeadlineExceeded) {
		return o.experiment
	}
	if o.final == nil {
		o.final, o.cancel = context.WithTimeout(o.parent, lifecycleFinalObservationLimit)
	}
	return o.final
}

func (o *lifecycleObservation) pods(client *kubernetes.Client, current plan, selector string) ([]kubernetes.PodState, model.CommandResult, bool, error) {
	ctx := o.context()
	pods, result, err := client.ObservePods(ctx, current.clusterName, namespace, selector)
	// Retry only a read interrupted by our own requirement deadline. An API
	// failure, malformed response, or independent command timeout stays ERROR.
	if failed(result) && (result.FailureType == model.FailureTimeout || result.FailureType == model.FailureCanceled) &&
		ctx == o.experiment && o.parent.Err() == nil && errors.Is(o.experiment.Err(), context.DeadlineExceeded) {
		ctx = o.context()
		pods, result, err = client.ObservePods(ctx, current.clusterName, namespace, selector)
	}
	return pods, result, errors.Is(o.experiment.Err(), context.DeadlineExceeded), err
}

func lifecycleDeadlineMeasurements(expired bool) []model.Measurement {
	if !expired {
		return nil
	}
	return []model.Measurement{
		{Name: "requirement_deadline_exceeded", Value: "true"},
		{Name: "final_state_observed_after_deadline", Value: "true"},
		{Name: "final_observation_limit_ms", Value: "5000", Unit: "ms"},
	}
}

// A healthy snapshot after expiry cannot prove when the requirement was met.
func lifecycleCompletionUnobserved(id, title, code, summary string, duration int64, traffic trafficObservation, measurements []model.Measurement) recoveryOutcome {
	result := lifecycleExecutionError(id, title, code, summary, "Retry verification; the final state does not establish when the operation completed.", traffic)
	result.Evidence.Measurements = measurements
	result.Evidence.DurationMS = duration
	return result
}
