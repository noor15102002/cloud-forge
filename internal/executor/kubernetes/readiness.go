package kubernetes

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

// WaitReady separates cluster bootstrap from application startup. k3d returning
// successfully does not guarantee that the API and scheduler are ready yet.
// The returned reason is a fixed code, never raw server output or credentials.
func (c *Client) WaitReady(ctx context.Context, cluster string) (model.CommandResult, string) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	started := time.Now()
	reason := "api_not_ready"
	for ctx.Err() == nil {
		result := c.runner.Run(ctx, command.Request{Name: "kubectl", Args: []string{"--context", "k3d-" + cluster, "--request-timeout=5s", "get", "--raw", "/readyz"}, Timeout: 6 * time.Second, OutputLimit: 1024})
		if result.FailureType == model.FailureNone && result.ExitCode == 0 && !result.Truncated && strings.TrimSpace(result.Stdout) == "ok" {
			result = c.runner.Run(ctx, command.Request{Name: "kubectl", Args: []string{"--context", "k3d-" + cluster, "get", "nodes", "--output", "json", "--request-timeout=5s"}, Timeout: 6 * time.Second, OutputLimit: 256 * 1024})
			if result.FailureType == model.FailureNone && result.ExitCode == 0 && !result.Truncated {
				var nodes corev1.NodeList
				if json.Unmarshal([]byte(result.Stdout), &nodes) != nil {
					return model.CommandResult{FailureType: model.FailureExecution, ExitCode: -1}, "invalid_node_state"
				}
				reason = readinessProblem(nodes)
				if reason == "" {
					return model.CommandResult{DurationMS: time.Since(started).Milliseconds()}, ""
				}
			} else {
				reason = "node_state_unavailable"
			}
		} else {
			reason = "api_not_ready"
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	failure := model.FailureTimeout
	if ctx.Err() == context.Canceled {
		failure = model.FailureCanceled
	}
	return model.CommandResult{FailureType: failure, ExitCode: -1, DurationMS: time.Since(started).Milliseconds()}, reason
}

func readinessProblem(nodes corev1.NodeList) string {
	if len(nodes.Items) == 0 {
		return "node_not_registered"
	}
	for _, node := range nodes.Items {
		ready := false
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
				ready = true
			}
			if condition.Status == corev1.ConditionTrue {
				switch condition.Type {
				case corev1.NodeDiskPressure:
					return "node_disk_pressure"
				case corev1.NodeMemoryPressure:
					return "node_memory_pressure"
				case corev1.NodePIDPressure:
					return "node_pid_pressure"
				}
			}
		}
		if !ready {
			return "node_not_ready"
		}
	}
	return ""
}
