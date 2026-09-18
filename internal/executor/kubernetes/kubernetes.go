// Package kubernetes adapts kubectl operations used by verification.
package kubernetes

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

// Client deploys and observes workloads through kubectl.
type Client struct{ runner command.Runner }

// New creates a kubectl adapter.
func New(runner command.Runner) *Client { return &Client{runner: runner} }

// Apply applies a generated manifest using the cluster's explicit context.
func (c *Client) Apply(ctx context.Context, cluster, manifestPath string) model.CommandResult {
	return c.runner.Run(ctx, command.Request{
		Name: "kubectl", Args: []string{"--context", "k3d-" + cluster, "apply", "--filename", manifestPath},
		Timeout: time.Minute, OutputLimit: 128 * 1024,
	})
}

// WaitAvailable waits until every new Deployment replica is available.
func (c *Client) WaitAvailable(ctx context.Context, cluster, namespace, deployment string) model.CommandResult {
	return c.runner.Run(ctx, command.Request{
		Name: "kubectl", Args: []string{"--context", "k3d-" + cluster, "--namespace", namespace, "rollout", "status", "deployment/" + deployment, "--timeout=120s"},
		Timeout: 130 * time.Second, OutputLimit: 128 * 1024,
	})
}

// ReadyPods returns ready pod count, total pod count, and restart count.
func (c *Client) ReadyPods(ctx context.Context, cluster, namespace, selector string) (int, int, int32, model.CommandResult, error) {
	result := c.runner.Run(ctx, command.Request{
		Name: "kubectl", Args: []string{"--context", "k3d-" + cluster, "--namespace", namespace, "get", "pods", "--selector", selector, "--output", "json"},
		Timeout: 30 * time.Second, OutputLimit: 256 * 1024,
	})
	if result.FailureType != model.FailureNone {
		return 0, 0, 0, result, nil
	}
	var pods corev1.PodList
	if err := json.Unmarshal([]byte(result.Stdout), &pods); err != nil {
		return 0, 0, 0, result, fmt.Errorf("decode pod state: %w", err)
	}
	ready := 0
	var restarts int32
	for _, pod := range pods.Items {
		if podReady(pod) {
			ready++
		}
		for _, status := range pod.Status.ContainerStatuses {
			restarts += status.RestartCount
		}
	}
	return ready, len(pods.Items), restarts, result, nil
}

func podReady(pod corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}
