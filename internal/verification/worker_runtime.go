package verification

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/noor15102002/cloud-forge/internal/executor/k3d"
	"github.com/noor15102002/cloud-forge/internal/executor/kubernetes"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

const workerRecoveryStrategy = "stop_wait_for_expiry_restore_worker_and_validate"

type workerResult struct {
	deadline      bool
	finalObserved bool
	status        model.Status
	summary       string
	observation   model.WorkerObservation
	duration      time.Duration
}

func workerSelector(current plan) string {
	return "app.kubernetes.io/name=" + current.workloadName + ",app.kubernetes.io/managed-by=cloudforge,cloudforge.dev/run-id=" + runIdentifier(current)
}
func (s *Service) workerPause(ctx context.Context) bool { return pause(ctx, s.workerPoll) }

func workerFailure(status model.Status, reason string, observation model.WorkerObservation, started time.Time) workerResult {
	return workerResult{status: status, summary: reason, observation: observation, duration: time.Since(started)}
}

// waitWorkerEmpty never deletes or overwrites the heartbeat. A replacement can
// start only after all prior pod objects are gone and Redis confirms key absence.
func (s *Service) waitWorkerEmpty(parent context.Context, client *kubernetes.Client, current plan, timeout time.Duration, observation model.WorkerObservation) workerResult {
	started := time.Now()
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	for ctx.Err() == nil {
		pods, res, err := client.ObservePods(ctx, current.clusterName, namespace, workerSelector(current))
		if failed(res) || err != nil {
			if ctx.Err() != nil && parent.Err() == nil {
				return s.workerEmptyDeadline(parent, client, current, observation, started)
			}
			return workerFailure(model.StatusError, "Worker termination could not be observed reliably.", observation, started)
		}
		running := 0
		for _, pod := range pods {
			if pod.Running {
				running++
			}
		}
		observation.RunningPods = running
		observation.MaximumRunningPods = max(observation.MaximumRunningPods, running)
		if len(pods) > 1 {
			return workerFailure(model.StatusFail, "Overlapping predecessor worker pods were observed; heartbeat attribution is unsafe.", observation, started)
		}
		if len(pods) == 0 {
			observation.PredecessorTerminated = observation.PreviousPodUID != ""
			heartbeat, res, err := client.ObserveRedisHeartbeat(ctx, current.clusterName, namespace, current.config.Worker.Heartbeat.Key, current.config.Worker.Heartbeat.TimestampField)
			if failed(res) || err != nil {
				if ctx.Err() != nil && parent.Err() == nil {
					return s.workerEmptyDeadline(parent, client, current, observation, started)
				}
				return workerFailure(model.StatusError, "Prior heartbeat absence could not be observed reliably; no new worker was started.", observation, started)
			}
			if !heartbeat.Present {
				observation.KeyAbsentBeforeStart = true
				observation.PreviousHeartbeatGone = observation.PreviousPodUID != ""
				observation.HeartbeatState = "absent"
				return workerFailure(model.StatusPass, "No predecessor pod or heartbeat remained before worker startup.", observation, started)
			}
			observation.HeartbeatState = "awaiting_prior_expiry"
		}
		if !s.workerPause(ctx) {
			break
		}
	}
	if parent.Err() != nil {
		return workerFailure(model.StatusError, "Worker transition was canceled; no replacement was started.", observation, started)
	}
	return s.workerEmptyDeadline(parent, client, current, observation, started)
}

// workerPod establishes expected controller, image, UID, replicas and dependency
// readiness independently from the application's heartbeat payload.
func (s *Service) workerPod(ctx context.Context, client *kubernetes.Client, current plan, image string, observation *model.WorkerObservation) (bool, string, error) {
	deployment, res, err := client.ObserveDeployment(ctx, current.clusterName, namespace, current.workloadName)
	if failed(res) || err != nil {
		return false, "", fmt.Errorf("deployment observation unavailable")
	}
	sets, res, err := client.RevisionReplicaSets(ctx, current.clusterName, namespace, workerSelector(current), deployment)
	if failed(res) || err != nil {
		return false, "", fmt.Errorf("revision observation unavailable")
	}
	pods, res, err := client.ObservePods(ctx, current.clusterName, namespace, workerSelector(current))
	if failed(res) || err != nil {
		return false, "", fmt.Errorf("pod observation unavailable")
	}
	running := 0
	for _, pod := range pods {
		if pod.Running {
			running++
		}
	}
	observation.RunningPods = running
	observation.MaximumRunningPods = max(observation.MaximumRunningPods, running)
	if len(pods) > 1 {
		return false, "More than one worker pod was observed; shared heartbeat attribution is unsafe.", nil
	}
	if len(pods) != 1 {
		return false, "", nil
	}
	pod := pods[0]
	if pod.UID == "" {
		return false, "", fmt.Errorf("pod identity unavailable")
	}
	if observation.PodUID != "" && observation.PodUID != pod.UID {
		return false, "Worker pod identity changed during a heartbeat observation.", nil
	}
	observation.PodUID, observation.Image, observation.ContainerRestarts = pod.UID, pod.Image, pod.Restarts
	if pod.Restarts > 0 {
		return false, "The worker container restarted instead of establishing uninterrupted heartbeat progress.", nil
	}
	if pod.Running {
		if pod.StartedAt.IsZero() {
			return false, "", fmt.Errorf("worker container start identity unavailable")
		}
		started := pod.StartedAt.UTC().Format(time.RFC3339Nano)
		if observation.ProcessStartedAt != "" && observation.ProcessStartedAt != started {
			return false, "The worker container instance changed during heartbeat qualification.", nil
		}
		observation.ProcessStartedAt = started
	}
	if pod.UID == observation.PreviousPodUID {
		return false, "The predecessor pod was observed after the required replacement boundary.", nil
	}
	if pod.Terminating || !pod.Running || pod.Image != image || deployment.Image != image || deployment.Replicas != 1 || deployment.Generation < 1 || deployment.ObservedGeneration < deployment.Generation || !sets[pod.ReplicaSetUID] {
		return false, "", nil
	}
	for _, name := range enabledProviders(current.config) {
		pods, res, err := client.ObservePods(ctx, current.clusterName, namespace, "cloudforge.dev/dependency="+name+",app.kubernetes.io/managed-by=cloudforge,cloudforge.dev/run-id="+runIdentifier(current))
		if failed(res) || err != nil {
			return false, "", fmt.Errorf("dependency observation unavailable")
		}
		if len(pods) != 1 || !pods[0].Ready || pods[0].Terminating || pods[0].Image != providerFingerprint(name).Image {
			return false, "", workerPrerequisiteError{"A required provider is not at its intended healthy image."}
		}
		if name == "clamav" {
			matched, err := s.clamFingerprintMatches(ctx, client, current, pods[0].Name)
			if err != nil {
				return false, "", err
			}
			if !matched {
				return false, "", workerPrerequisiteError{"The antivirus signature database changed or is no longer fresh."}
			}
		}
	}
	return true, "", nil
}

func (s *Service) waitWorker(parent context.Context, client *kubernetes.Client, current plan, image string, timeout time.Duration, observation model.WorkerObservation) workerResult {
	started := time.Now()
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	maxAge, _ := time.ParseDuration(current.config.Worker.Heartbeat.MaxAge)
	var previousTimestamp, previousRedisTime, lastProducerTime time.Time
	advancing := 0
	for ctx.Err() == nil {
		running, violation, err := s.workerPod(ctx, client, current, image, &observation)
		if err != nil {
			if ctx.Err() != nil && parent.Err() == nil {
				return s.workerDeadline(parent, client, current, image, observation, lastProducerTime, previousRedisTime, started)
			}
			var prerequisite workerPrerequisiteError
			if errors.As(err, &prerequisite) {
				return workerFailure(model.StatusBlocked, prerequisite.reason, observation, started)
			}
			return workerFailure(model.StatusError, "Worker image, identity, replicas or dependencies could not be observed reliably.", observation, started)
		}
		if violation != "" {
			return workerFailure(model.StatusFail, violation, observation, started)
		}
		heartbeat, res, err := client.ObserveRedisHeartbeat(ctx, current.clusterName, namespace, current.config.Worker.Heartbeat.Key, current.config.Worker.Heartbeat.TimestampField)
		if failed(res) || err != nil {
			if ctx.Err() != nil && parent.Err() == nil {
				return s.workerDeadline(parent, client, current, image, observation, lastProducerTime, previousRedisTime, started)
			}
			return workerFailure(model.StatusError, "Worker heartbeat was malformed, clock-uncertain or could not be observed reliably.", observation, started)
		}
		if !previousRedisTime.IsZero() && heartbeat.RedisTime.Before(previousRedisTime) {
			return workerFailure(model.StatusError, "Redis time moved backwards; heartbeat freshness could not be established.", observation, started)
		}
		previousRedisTime = heartbeat.RedisTime
		valid := recordWorkerHeartbeat(&observation, heartbeat, maxAge)
		if heartbeat.Present {
			if !lastProducerTime.IsZero() && heartbeat.Timestamp.Before(lastProducerTime) {
				return workerFailure(model.StatusError, "Worker heartbeat timestamp moved backwards; progress could not be established reliably.", observation, started)
			}
			lastProducerTime = heartbeat.Timestamp
		}

		if !running {
			observation.HeartbeatState = "process_baseline_unready"
			valid = false
		}
		if !valid {
			advancing = 0
			previousTimestamp = time.Time{}
		} else if previousTimestamp.IsZero() {
			previousTimestamp = heartbeat.Timestamp
			advancing = 1
		} else if heartbeat.Timestamp.After(previousTimestamp) {
			previousTimestamp = heartbeat.Timestamp
			advancing++
		}
		if advancing >= 2 {
			// The Redis read must not straddle an unobserved container restart.
			stable, violation, err := s.workerPod(ctx, client, current, image, &observation)
			if err != nil {
				var prerequisite workerPrerequisiteError
				if errors.As(err, &prerequisite) {
					return workerFailure(model.StatusBlocked, prerequisite.reason, observation, started)
				}
				return workerFailure(model.StatusError, "Container identity could not be revalidated after heartbeat progress.", observation, started)
			}
			if violation != "" {
				return workerFailure(model.StatusFail, violation, observation, started)
			}
			if stable && ctx.Err() == nil {
				observation.HeartbeatState = "advancing"
				return workerFailure(model.StatusPass, "One expected worker pod produced two advancing fresh heartbeats with bounded positive expiry. This establishes process liveness only.", observation, started)
			}
			advancing = 0
			previousTimestamp = time.Time{}
		}
		if !s.workerPause(ctx) {
			break
		}
	}
	if parent.Err() != nil {
		return workerFailure(model.StatusError, "Worker observation was canceled; partial liveness evidence is retained.", observation, started)
	}
	return s.workerDeadline(parent, client, current, image, observation, lastProducerTime, previousRedisTime, started)
}

func workerEvidence(id string, result workerResult, mutation bool, current plan) model.Evidence {
	observation := result.observation
	if observation.Samples == nil {
		observation.Samples = []model.HeartbeatSample{}
	}
	evidence := model.Evidence{ExperimentID: id, Title: id, Status: result.status, Summary: result.summary, DurationMS: elapsedMilliseconds(result.duration), Topology: current.topology, Worker: &observation, Execution: &model.ExperimentExecution{Executed: true, MutationAttempted: mutation}, Measurements: []model.Measurement{
		{Name: "worker_observation_duration_ms", Value: strconv.FormatInt(elapsedMilliseconds(result.duration), 10), Unit: "ms"},
		{Name: "maximum_running_workers", Value: strconv.Itoa(observation.MaximumRunningPods), Unit: "pods"},
		{Name: "container_restarts", Value: strconv.Itoa(int(observation.ContainerRestarts)), Unit: "restarts"},
	}}
	if result.deadline {
		evidence.Measurements = append(evidence.Measurements, model.Measurement{Name: "requirement_deadline_exceeded", Value: "true"}, model.Measurement{Name: "final_state_observed_after_deadline", Value: strconv.FormatBool(result.finalObserved)}, model.Measurement{Name: "final_observation_limit_ms", Value: "5000", Unit: "ms"})
	}
	return evidence
}

func (s *Service) workerTransition(parent context.Context, client *kubernetes.Client, current plan, image, previousUID string, restoreManifest string) workerResult {
	started := time.Now()
	ctx, cancel := context.WithTimeout(parent, s.recoveryTimeout)
	defer cancel()
	observation := model.WorkerObservation{PreviousPodUID: previousUID, Samples: []model.HeartbeatSample{}}
	failure := func(reason string) workerResult {
		return workerFailure(model.StatusError, reason, observation, started)
	}
	if failed(client.ScaleWorker(ctx, current.clusterName, namespace, current.workloadName, 0)) {
		return failure("CloudForge could not request a zero-worker transition.")
	}
	empty := s.waitWorkerEmpty(parent, client, current, time.Until(started.Add(s.recoveryTimeout)), observation)
	observation = empty.observation
	if empty.status != model.StatusPass {
		empty.duration = time.Since(started)
		return empty
	}
	if restoreManifest != "" {
		if failed(client.Apply(ctx, current.clusterName, restoreManifest)) {
			return failure("The intended worker image and configuration could not be restored.")
		}
	} else {
		if failed(client.SetImage(ctx, current.clusterName, namespace, current.workloadName, "application", image)) {
			return failure("The worker image could not be selected while all predecessors were stopped.")
		}
		if failed(client.ScaleWorker(ctx, current.clusterName, namespace, current.workloadName, 1)) {
			return failure("The single replacement worker could not be started.")
		}
	}
	result := s.waitWorker(parent, client, current, image, time.Until(started.Add(s.recoveryTimeout)), observation)
	result.duration = time.Since(started)
	return result
}

func recoveryFromWorker(result workerResult) model.RecoveryEvidence {
	status := result.status
	if status == model.StatusFail {
		status = model.StatusBlocked
	}
	observation := result.observation
	if observation.Samples == nil {
		observation.Samples = []model.HeartbeatSample{}
	}
	return model.RecoveryEvidence{Worker: &observation, Status: status, Strategy: workerRecoveryStrategy, Summary: result.summary, DurationMS: elapsedMilliseconds(result.duration), Checks: []model.BaselineCheck{
		{Name: "worker_image_identity_dependencies_and_fresh_progress", Status: status, Reason: result.summary},
	}}
}

func (s *Service) runWorkerSuite(ctx context.Context, client *kubernetes.Client, cluster *k3d.Client, current plan, manifest string, buildB *model.CommandResult, out *Outcome) {
	start := time.Now()
	initial := s.waitWorkerEmpty(ctx, client, current, s.readinessTimeout, model.WorkerObservation{Samples: []model.HeartbeatSample{}})
	if initial.status != model.StatusPass {
		if initial.status == model.StatusFail {
			initial.status = model.StatusBlocked
		}
		evidence := workerEvidence("worker-startup", initial, false, current)
		evidence.Execution.Executed = false
		out.Run.Evidence = append(out.Run.Evidence, evidence)
		finalEvidenceStatus(out)
		return
	}
	if failed(client.Apply(ctx, current.clusterName, manifest)) {
		initial.status, initial.summary = model.StatusError, "The worker deployment could not be created reliably."
	} else {
		initial = s.waitWorker(ctx, client, current, current.image, s.readinessTimeout, initial.observation)
	}
	initial.duration = time.Since(start)
	out.Run.Evidence = append(out.Run.Evidence, workerEvidence("worker-startup", initial, true, current))
	if initial.status != model.StatusPass || ctx.Err() != nil {
		finalEvidenceStatus(out)
		return
	}
	blocked := ""
	for _, id := range []string{"worker-recovery", "worker-image-replacement"} {
		if ctx.Err() != nil {
			break
		}
		if blocked != "" {
			recordNotExecuted(out, current, id, model.StatusBlocked, blocked)
			continue
		}
		baseline := s.waitWorker(ctx, client, current, current.image, s.baselineTimeout, model.WorkerObservation{Samples: []model.HeartbeatSample{}})
		if baseline.status != model.StatusPass {
			status := baseline.status
			if status == model.StatusFail {
				status = model.StatusBlocked
			}
			recordNotExecuted(out, current, id, status, "Required worker baseline could not be established.")
			blocked = "The worker baseline is unavailable; dependent experiments were not started."
			continue
		}
		image := current.image
		if id == "worker-image-replacement" {
			if buildB == nil || failed(*buildB) {
				recordNotExecuted(out, current, id, model.StatusBlocked, "Image B was not built successfully; sequential replacement could not be tested.")
				continue
			}
			if failed(cluster.ImportImage(ctx, current.clusterName, current.rolloutImage)) {
				recordNotExecuted(out, current, id, model.StatusError, "Image B could not be imported reliably; worker state was not changed.")
				continue
			}
			// Importing image B can disturb the node; establish image A's
			// baseline again before attributing a subsequent mutation result.
			baseline = s.waitWorker(ctx, client, current, current.image, s.baselineTimeout, model.WorkerObservation{Samples: []model.HeartbeatSample{}})
			if baseline.status != model.StatusPass {
				status := baseline.status
				if status == model.StatusFail {
					status = model.StatusBlocked
				}
				recordNotExecuted(out, current, id, status, "The image A baseline was not established after image B preparation; no worker replacement was attempted.")
				continue
			}
			image = current.rolloutImage
		}
		result := s.workerTransition(ctx, client, current, image, baseline.observation.PodUID, "")
		evidence := workerEvidence(id, result, true, current)
		if ctx.Err() == nil {
			previousUID := result.observation.PodUID
			if previousUID == "" {
				previousUID = baseline.observation.PodUID
			}
			restored := s.workerTransition(ctx, client, current, current.image, previousUID, manifest)
			recovery := recoveryFromWorker(restored)
			evidence.Recovery = &recovery
			if recovery.Status != model.StatusPass {
				blocked = "Worker baseline restoration failed; dependent experiments were not started."
			}
		}
		out.Run.Evidence = append(out.Run.Evidence, evidence)
	}
	finalEvidenceStatus(out)
}
