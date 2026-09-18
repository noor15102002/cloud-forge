package kubernetes

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

// Only recognized state codes are retained. Kubernetes messages and custom
// reasons may contain arbitrary application data and are never copied.
func podReason(pod corev1.Pod) string {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodScheduled && condition.Status == corev1.ConditionFalse {
			return "unscheduled"
		}
	}
	for _, status := range pod.Status.ContainerStatuses {
		if status.State.Waiting != nil {
			switch status.State.Waiting.Reason {
			case "ErrImageNeverPull", "ImagePullBackOff", "ErrImagePull":
				return "image_unavailable"
			case "ContainerCreating":
				return "container_creating"
			case "CrashLoopBackOff":
				return "crash_loop"
			case "CreateContainerConfigError", "CreateContainerError", "RunContainerError":
				return "container_start_error"
			}
		}
		if status.State.Terminated != nil {
			if status.State.Terminated.Reason == "OOMKilled" {
				return "out_of_memory"
			}
			return "terminated"
		}
	}
	switch pod.Status.Phase {
	case corev1.PodPending:
		return "pending"
	case corev1.PodRunning:
		return "running"
	case corev1.PodFailed:
		return "failed"
	case corev1.PodSucceeded:
		return "succeeded"
	default:
		return "unknown"
	}
}

// NodeProblems returns fixed condition codes, never node addresses or messages.
func (c *Client) NodeProblems(ctx context.Context, cluster string) ([]string, model.CommandResult, error) {
	result := c.runner.Run(ctx, command.Request{Name: "kubectl", Args: []string{"--context", "k3d-" + cluster, "get", "nodes", "--output", "json"}, Timeout: 15 * time.Second, OutputLimit: 256 * 1024})
	if result.FailureType != model.FailureNone || result.ExitCode != 0 {
		return nil, result, nil
	}
	var nodes corev1.NodeList
	if err := json.Unmarshal([]byte(result.Stdout), &nodes); err != nil {
		return nil, result, fmt.Errorf("decode node conditions: %w", err)
	}
	set := map[string]bool{}
	for _, node := range nodes.Items {
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status != corev1.ConditionTrue {
				set["node_not_ready"] = true
			}
			if condition.Status == corev1.ConditionTrue {
				switch condition.Type {
				case corev1.NodeDiskPressure:
					set["node_disk_pressure"] = true
				case corev1.NodeMemoryPressure:
					set["node_memory_pressure"] = true
				case corev1.NodePIDPressure:
					set["node_pid_pressure"] = true
				}
			}
		}
	}
	problems := make([]string, 0, len(set))
	for problem := range set {
		problems = append(problems, problem)
	}
	sort.Strings(problems)
	return problems, result, nil
}
