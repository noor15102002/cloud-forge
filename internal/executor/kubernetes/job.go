package kubernetes

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
)

// JobPodState retains only the bounded execution observations needed for a
// preparation result. Environment, arguments, logs and status messages are omitted.
type JobPodState struct {
	Image      string
	Started    bool
	Terminated bool
	ExitCode   int32
	Signal     int32
	Restarts   int32
	Reason     string
}

// JobState describes one owned single-execution Job and its controller-owned pod.
type JobState struct {
	Image            string
	Complete         bool
	Failed           bool
	DeadlineExceeded bool
	Pods             []JobPodState
}

// ObserveJob reads a Job and its pods through structured bounded API responses.
// It checks controller ownership instead of trusting a copied job-name label.
// Returned command results intentionally omit both raw streams, including errors.
func (c *Client) ObserveJob(ctx context.Context, cluster, namespace, name string) (state JobState, result model.CommandResult, err error) {
	defer func() {
		result.Stdout = ""
		result.Stderr = ""
	}()
	result = c.runner.Run(ctx, command.Request{
		Name: "kubectl", Args: []string{"--context", "k3d-" + cluster, "--namespace", namespace, "--request-timeout=5s", "get", "job", name, "--output=json"},
		Timeout: 6 * time.Second, OutputLimit: 128 * 1024,
	})
	if result.ExitCode != 0 || result.FailureType != model.FailureNone {
		return state, result, nil
	}
	if result.Truncated {
		return state, result, errors.New("preparation Job observation exceeded its output bound")
	}
	var job batchv1.Job
	if json.Unmarshal([]byte(result.Stdout), &job) != nil || job.Name != name || job.Namespace != namespace || job.UID == "" || len(job.Spec.Template.Spec.Containers) != 1 {
		return state, result, errors.New("preparation Job observation was invalid")
	}
	state.Image = job.Spec.Template.Spec.Containers[0].Image
	if state.Image == "" || job.Spec.Template.Spec.Containers[0].Name == "" {
		return state, result, errors.New("preparation Job image identity was absent")
	}
	for _, condition := range job.Status.Conditions {
		if condition.Status != corev1.ConditionTrue {
			continue
		}
		switch condition.Type {
		case batchv1.JobComplete:
			state.Complete = true
		case batchv1.JobFailed, batchv1.JobFailureTarget:
			state.Failed = true
			state.DeadlineExceeded = state.DeadlineExceeded || condition.Reason == "DeadlineExceeded"
		}
	}
	if state.Complete && state.Failed {
		return state, result, errors.New("preparation Job reported contradictory outcomes")
	}
	jobDuration := result.DurationMS
	result = c.runner.Run(ctx, command.Request{
		Name: "kubectl", Args: []string{"--context", "k3d-" + cluster, "--namespace", namespace, "--request-timeout=5s", "get", "pods", "--selector=batch.kubernetes.io/job-name=" + name, "--output=json"},
		Timeout: 6 * time.Second, OutputLimit: 256 * 1024,
	})
	result.DurationMS += jobDuration
	if result.ExitCode != 0 || result.FailureType != model.FailureNone {
		return state, result, nil
	}
	if result.Truncated {
		return state, result, errors.New("preparation pod observation exceeded its output bound")
	}
	var pods corev1.PodList
	if json.Unmarshal([]byte(result.Stdout), &pods) != nil || len(pods.Items) > 1 {
		return state, result, errors.New("preparation did not have one unambiguous pod execution")
	}
	for _, pod := range pods.Items {
		owned := false
		for _, owner := range pod.OwnerReferences {
			if owner.Kind == "Job" && owner.Name == job.Name && owner.UID == job.UID && owner.Controller != nil && *owner.Controller {
				owned = true
			}
		}
		if !owned || pod.Namespace != namespace || len(pod.Spec.Containers) != 1 || pod.Spec.Containers[0].Name != job.Spec.Template.Spec.Containers[0].Name || pod.Spec.Containers[0].Image != state.Image || len(pod.Spec.InitContainers) != 0 {
			return state, result, errors.New("preparation pod ownership or image did not match the intended Job")
		}
		observation := JobPodState{Image: pod.Spec.Containers[0].Image, Reason: podReason(pod)}
		if len(pod.Status.ContainerStatuses) > 1 {
			return state, result, errors.New("preparation pod contained ambiguous container status")
		}
		for _, status := range pod.Status.ContainerStatuses {
			if status.Name != pod.Spec.Containers[0].Name || status.RestartCount != 0 {
				return state, result, errors.New("preparation pod reported an unexpected or repeated execution")
			}
			observation.Restarts = status.RestartCount
			if status.State.Running != nil {
				observation.Started = !status.State.Running.StartedAt.IsZero()
			}
			if status.State.Terminated != nil {
				termination := status.State.Terminated
				observation.Started = !termination.StartedAt.IsZero()
				observation.Terminated = !termination.FinishedAt.IsZero()
				observation.ExitCode = termination.ExitCode
				observation.Signal = termination.Signal
			}
		}
		state.Pods = append(state.Pods, observation)
	}
	return state, result, nil
}
