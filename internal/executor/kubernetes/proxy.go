package kubernetes

import (
	"context"
	"strconv"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

// PodProxy addresses a selected pod through the private cluster API. The caller
// supplies only validated control paths and generated query parameters.
func (c *Client) PodProxy(ctx context.Context, cluster, namespace, pod string, port int32, path string) model.CommandResult {
	return c.runner.Run(ctx, command.Request{Name: "kubectl", Args: []string{"--context", "k3d-" + cluster, "--request-timeout=15s", "get", "--raw", "/api/v1/namespaces/" + namespace + "/pods/" + pod + ":" + strconv.Itoa(int(port)) + "/proxy" + path}, Timeout: 20 * time.Second, OutputLimit: 16 * 1024})
}

// BeginDeletePod requests ordinary graceful termination without waiting for removal.
func (c *Client) BeginDeletePod(ctx context.Context, cluster, namespace, pod string) model.CommandResult {
	return c.runner.Run(ctx, command.Request{Name: "kubectl", Args: []string{"--context", "k3d-" + cluster, "--namespace", namespace, "delete", "pod", pod, "--wait=false"}, Timeout: 15 * time.Second, OutputLimit: 16 * 1024})
}
