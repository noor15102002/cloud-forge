package verification

import (
	"context"
	"errors"
	"time"

	"github.com/noor15102002/cloud-forge/internal/executor/kubernetes"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

type workerPrerequisiteError struct{ reason string }

func (e workerPrerequisiteError) Error() string { return e.reason }

// A separate five-second read resolves deadline-boundary uncertainty without
// extending mutation or accepting heartbeat progress completed after the limit.
func workerDeadlineResult(status model.Status, summary string, observation model.WorkerObservation, started time.Time, observed bool) workerResult {
	result := workerFailure(status, summary, observation, started)
	result.deadline, result.finalObserved = true, observed
	return result
}

func (s *Service) workerEmptyDeadline(parent context.Context, client *kubernetes.Client, current plan, observation model.WorkerObservation, started time.Time) workerResult {
	ctx, cancel := context.WithTimeout(parent, lifecycleFinalObservationLimit)
	defer cancel()
	pods, res, err := client.ObservePods(ctx, current.clusterName, namespace, workerSelector(current))
	if failed(res) || err != nil {
		return workerDeadlineResult(model.StatusError, "Prior worker state could not be observed after the transition deadline.", observation, started, false)
	}
	observation.RunningPods = 0
	for _, pod := range pods {
		if pod.Running {
			observation.RunningPods++
		}
	}
	observation.MaximumRunningPods = max(observation.MaximumRunningPods, observation.RunningPods)
	if len(pods) != 0 {
		return workerDeadlineResult(model.StatusFail, "A predecessor worker remained after the stop deadline; no replacement was started.", observation, started, true)
	}
	observation.PredecessorTerminated = observation.PreviousPodUID != ""
	heartbeat, res, err := client.ObserveRedisHeartbeat(ctx, current.clusterName, namespace, current.config.Worker.Heartbeat.Key, current.config.Worker.Heartbeat.TimestampField)
	if failed(res) || err != nil {
		return workerDeadlineResult(model.StatusError, "Prior heartbeat expiry could not be observed reliably after the deadline.", observation, started, false)
	}
	maxAge, _ := time.ParseDuration(current.config.Worker.Heartbeat.MaxAge)
	recordWorkerHeartbeat(&observation, heartbeat, maxAge)
	if heartbeat.Present {
		return workerDeadlineResult(model.StatusFail, "The prior heartbeat remained after the expiry deadline; no replacement was started.", observation, started, true)
	}
	observation.HeartbeatState = "absent_after_deadline"
	return workerDeadlineResult(model.StatusError, "Worker and heartbeat absence were observed only after the deadline; their completion time is unknown and no replacement was started.", observation, started, true)
}

func (s *Service) workerDeadline(parent context.Context, client *kubernetes.Client, current plan, image string, observation model.WorkerObservation, previousTimestamp, previousRedisTime time.Time, started time.Time) workerResult {
	ctx, cancel := context.WithTimeout(parent, lifecycleFinalObservationLimit)
	defer cancel()
	running, violation, err := s.workerPod(ctx, client, current, image, &observation)
	if err != nil {
		var prerequisite workerPrerequisiteError
		if errors.As(err, &prerequisite) {
			return workerDeadlineResult(model.StatusBlocked, prerequisite.reason, observation, started, true)
		}
		return workerDeadlineResult(model.StatusError, "Final worker state could not be observed reliably after the requirement deadline.", observation, started, false)
	}
	if violation != "" {
		return workerDeadlineResult(model.StatusFail, violation, observation, started, true)
	}
	if !running {
		return workerDeadlineResult(model.StatusFail, "The intended single-worker image and process state remained unready after the deadline.", observation, started, true)
	}
	heartbeat, res, err := client.ObserveRedisHeartbeat(ctx, current.clusterName, namespace, current.config.Worker.Heartbeat.Key, current.config.Worker.Heartbeat.TimestampField)
	if failed(res) || err != nil {
		return workerDeadlineResult(model.StatusError, "Final heartbeat observation was unavailable, malformed or clock-uncertain.", observation, started, false)
	}
	maxAge, _ := time.ParseDuration(current.config.Worker.Heartbeat.MaxAge)
	valid := recordWorkerHeartbeat(&observation, heartbeat, maxAge)
	if (!previousRedisTime.IsZero() && heartbeat.RedisTime.Before(previousRedisTime)) || (heartbeat.Present && !previousTimestamp.IsZero() && heartbeat.Timestamp.Before(previousTimestamp)) {
		return workerDeadlineResult(model.StatusError, "Heartbeat or Redis time moved backwards in the final observation.", observation, started, true)
	}
	if !valid || (!previousTimestamp.IsZero() && heartbeat.Timestamp.Equal(previousTimestamp)) {
		if valid {
			observation.HeartbeatState = "not_advancing"
		}
		return workerDeadlineResult(model.StatusFail, "The configured fresh, expiring and advancing heartbeat remained unsatisfied after the requirement deadline.", observation, started, true)
	}
	observation.HeartbeatState = "late_progress"
	return workerDeadlineResult(model.StatusError, "Fresh heartbeat progress was observed only after the deadline; completion within the tested time bound was not established.", observation, started, true)
}

func recordWorkerHeartbeat(observation *model.WorkerObservation, heartbeat kubernetes.RedisHeartbeat, maxAge time.Duration) bool {
	observation.RedisObservedAt = heartbeat.RedisTime.UTC().Format(time.RFC3339Nano)
	observation.HeartbeatState = "missing"
	if !heartbeat.Present {
		return false
	}
	age := heartbeat.RedisTime.Sub(heartbeat.Timestamp)
	sample := model.HeartbeatSample{RedisTime: observation.RedisObservedAt, Timestamp: heartbeat.Timestamp.UTC().Format(time.RFC3339Nano), AgeMS: max(int64(0), age.Milliseconds()), TTLMS: heartbeat.TTLMS}
	if len(observation.Samples) > 0 && observation.Samples[len(observation.Samples)-1].Timestamp == sample.Timestamp {
		observation.Samples[len(observation.Samples)-1] = sample
	} else {
		observation.Samples = append(observation.Samples, sample)
	}
	if len(observation.Samples) > 2 {
		observation.Samples = observation.Samples[len(observation.Samples)-2:]
	}
	if heartbeat.TTLMS <= 0 || heartbeat.TTLMS > 60_000 {
		observation.HeartbeatState = "invalid_expiry"
		return false
	}
	if age > maxAge {
		observation.HeartbeatState = "stale"
		return false
	}
	observation.HeartbeatState = "fresh"
	return true
}
