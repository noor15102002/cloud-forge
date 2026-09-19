// Package k3d adapts disposable k3d cluster lifecycle operations.
package k3d

import (
	"context"
	"strconv"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

// Client manages one-run k3d clusters.
type Client struct{ runner command.Runner }

// New creates a k3d CLI adapter.
func New(runner command.Runner) *Client { return &Client{runner: runner} }

// Create creates a minimal cluster, publishes one NodePort on loopback, and waits for its API.
func (c *Client) Create(ctx context.Context, name string, nodePort int) model.CommandResult {
	portMapping := "127.0.0.1:0:" + strconv.Itoa(nodePort) + "@server:0"
	return c.runner.Run(ctx, command.Request{
		Name: "k3d", Args: []string{"cluster", "create", name, "--servers-memory", "4g", "--kubeconfig-update-default=false", "--kubeconfig-switch-context=false", "--runtime-label", "cloudforge.dev/owned=true@all", "--servers", "1", "--agents", "0", "--port", portMapping, "--wait", "--timeout", "90s"},
		Timeout: 2 * time.Minute, OutputLimit: 128 * 1024,
	})
}

// ImportImage loads a local image into every node in a cluster.
func (c *Client) ImportImage(ctx context.Context, cluster, image string) model.CommandResult {
	imported := c.runner.Run(ctx, command.Request{
		Name: "k3d", Args: []string{"image", "import", image, "--cluster", cluster, "--mode", "direct"},
		Timeout: 3 * time.Minute, OutputLimit: 128 * 1024,
	})
	if imported.ExitCode != 0 || imported.FailureType != model.FailureNone {
		return imported
	}
	// Use the direct importer because k3d 5.9's tools importer can log a node
	// import error but return success. Independently confirm the image in the
	// single server's CRI before deploying either image.
	// The generated cluster name targets only this run's node; no shell is used.
	checked := c.runner.Run(ctx, command.Request{
		Name: "docker", Args: []string{"exec", "k3d-" + cluster + "-server-0", "crictl", "inspecti", image},
		Timeout: 15 * time.Second, OutputLimit: 64 * 1024,
	})
	checked.DurationMS += imported.DurationMS
	return checked
}

// Delete removes a CloudForge-owned cluster.
func (c *Client) Delete(ctx context.Context, name string) model.CommandResult {
	return c.runner.Run(ctx, command.Request{
		Name: "k3d", Args: []string{"cluster", "delete", name},
		Timeout: 2 * time.Minute, OutputLimit: 128 * 1024,
	})
}

// Kubeconfig returns credentials for this run only; callers must not publish output.
func (c *Client) Kubeconfig(ctx context.Context, name string) model.CommandResult {
	return c.runner.Run(ctx, command.Request{Name: "k3d", Args: []string{"kubeconfig", "get", name}, Timeout: 15 * time.Second, OutputLimit: 128 * 1024})
}
