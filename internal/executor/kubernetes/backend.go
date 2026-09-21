package kubernetes

import (
	"context"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

// WaitDependency bounds only the explicitly supported provider startup wait.
func (c *Client) WaitDependency(ctx context.Context, cluster, namespace, deployment string, timeout time.Duration) model.CommandResult {
	if timeout < time.Second || timeout > 10*time.Minute {
		return model.CommandResult{ExitCode: 2, FailureType: model.FailureExecution}
	}
	return c.runner.Run(ctx, command.Request{Name: "kubectl", Args: []string{"--context", "k3d-" + cluster, "--namespace", namespace, "rollout", "status", "deployment/" + deployment, "--timeout=" + timeout.String()}, Timeout: timeout + 10*time.Second, OutputLimit: 64 * 1024})
}

// ClamAVVersion observes the daemon's full engine/database/date tuple. Callers
// must reject clamdscan's successful local-version fallback without a database.
func (c *Client) ClamAVVersion(ctx context.Context, cluster, namespace, pod string) model.CommandResult {
	return c.runner.Run(ctx, command.Request{Name: "kubectl", Args: []string{"--context", "k3d-" + cluster, "--namespace", namespace, "exec", pod, "--container", "clamav", "--", "clamdscan", "--config-file=/etc/cloudforge/clamd.conf", "--version"}, Timeout: 10 * time.Second, OutputLimit: 2048})
}

// NetworkPolicies reads only policy definitions, never Secrets or pod contents.
func (c *Client) NetworkPolicies(ctx context.Context, cluster, namespace string) model.CommandResult {
	return c.runner.Run(ctx, command.Request{Name: "kubectl", Args: []string{"--context", "k3d-" + cluster, "--namespace", namespace, "get", "networkpolicies", "--output", "json"}, Timeout: 15 * time.Second, OutputLimit: 64 * 1024})
}
