// Package kubernetes adapts kubectl operations used by verification.
package kubernetes

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

// Client deploys and observes workloads through kubectl.
type Client struct{ runner command.Runner }

// PodState contains the safe pod identity and readiness data needed by experiments.
type PodState struct {
	Name     string
	Ready    bool
	Restarts int32
	Image    string
}

// HPAState contains safe autoscaler state needed by the load experiment.
type HPAState struct {
	CurrentReplicas int32
	DesiredReplicas int32
	CurrentCPU      *int32
	MetricsReady    bool
	Reason          string
}

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
	pods, result, err := c.ObservePods(ctx, cluster, namespace, selector)
	if err != nil || result.FailureType != model.FailureNone || result.ExitCode != 0 {
		return 0, 0, 0, result, err
	}
	ready := 0
	var restarts int32
	for _, pod := range pods {
		if pod.Ready {
			ready++
		}
		restarts += pod.Restarts
	}
	return ready, len(pods), restarts, result, nil
}

// ObservePods returns sorted pod identity and readiness without retaining logs or environment data.
func (c *Client) ObservePods(ctx context.Context, cluster, namespace, selector string) ([]PodState, model.CommandResult, error) {
	result := c.runner.Run(ctx, command.Request{
		Name: "kubectl", Args: []string{"--context", "k3d-" + cluster, "--namespace", namespace, "get", "pods", "--selector", selector, "--output", "json"},
		Timeout: 30 * time.Second, OutputLimit: 256 * 1024,
	})
	if result.FailureType != model.FailureNone || result.ExitCode != 0 {
		return nil, result, nil
	}
	var pods corev1.PodList
	if err := json.Unmarshal([]byte(result.Stdout), &pods); err != nil {
		return nil, result, fmt.Errorf("decode pod state: %w", err)
	}
	states := make([]PodState, 0, len(pods.Items))
	for _, pod := range pods.Items {
		state := PodState{Name: pod.Name, Ready: podReady(pod)}
		if len(pod.Spec.Containers) > 0 {
			state.Image = pod.Spec.Containers[0].Image
		}
		for _, status := range pod.Status.ContainerStatuses {
			state.Restarts += status.RestartCount
		}
		states = append(states, state)
	}
	sort.Slice(states, func(i, j int) bool { return states[i].Name < states[j].Name })
	return states, result, nil
}

// ObserveHPA returns the current autoscaler state without retaining events or pod data.
func (c *Client) ObserveHPA(ctx context.Context, cluster, namespace, name string) (HPAState, model.CommandResult, error) {
	result := c.runner.Run(ctx, command.Request{
		Name: "kubectl", Args: []string{"--context", "k3d-" + cluster, "--namespace", namespace, "get", "horizontalpodautoscaler", name, "--output", "json"},
		Timeout: 30 * time.Second, OutputLimit: 128 * 1024,
	})
	if result.FailureType != model.FailureNone || result.ExitCode != 0 {
		return HPAState{}, result, nil
	}
	var hpa autoscalingv2.HorizontalPodAutoscaler
	if err := json.Unmarshal([]byte(result.Stdout), &hpa); err != nil {
		return HPAState{}, result, fmt.Errorf("decode HPA state: %w", err)
	}
	state := HPAState{CurrentReplicas: hpa.Status.CurrentReplicas, DesiredReplicas: hpa.Status.DesiredReplicas}
	for _, metric := range hpa.Status.CurrentMetrics {
		if metric.Type == autoscalingv2.ResourceMetricSourceType && metric.Resource != nil && metric.Resource.Name == corev1.ResourceCPU && metric.Resource.Current.AverageUtilization != nil {
			value := *metric.Resource.Current.AverageUtilization
			state.CurrentCPU = &value
			state.MetricsReady = true
			break
		}
	}
	for _, condition := range hpa.Status.Conditions {
		if condition.Type == autoscalingv2.ScalingActive && condition.Status != corev1.ConditionTrue {
			state.Reason = condition.Reason
		}
	}
	return state, result, nil
}

// SetImage starts a Deployment rollout to a locally imported image.
func (c *Client) SetImage(ctx context.Context, cluster, namespace, deployment, container, image string) model.CommandResult {
	return c.runner.Run(ctx, command.Request{
		Name: "kubectl", Args: []string{
			"--context", "k3d-" + cluster, "--namespace", namespace,
			"set", "image", "deployment/" + deployment, container + "=" + image,
		},
		Timeout: 30 * time.Second, OutputLimit: 128 * 1024,
	})
}

// DeletePod removes one application pod and waits until that object is gone.
func (c *Client) DeletePod(ctx context.Context, cluster, namespace, name string) model.CommandResult {
	return c.runner.Run(ctx, command.Request{
		Name: "kubectl", Args: []string{"--context", "k3d-" + cluster, "--namespace", namespace, "delete", "pod", name, "--wait=true", "--timeout=30s"},
		Timeout: 40 * time.Second, OutputLimit: 128 * 1024,
	})
}

func podReady(pod corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}
