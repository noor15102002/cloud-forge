// Package k3d adapts disposable k3d cluster lifecycle operations.
package k3d

import (
	"context"
	"strconv"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

// KubernetesVersion is the explicit server selected for disposable runs.
const KubernetesVersion = "1.35.5+k3s1"

// NodeImage fixes the runtime independently of a local k3d default.
const NodeImage = "rancher/k3s:v1.35.5-k3s1"

// Client manages one-run k3d clusters.
type Client struct {
	runner         command.Runner
	ownershipToken string
}

// New creates a k3d CLI adapter.
func New(runner command.Runner) *Client { return &Client{runner: runner} }

// SetOwnership adds this invocation's private token to every created runtime node.
func (c *Client) SetOwnership(token string) { c.ownershipToken = token }

// Create creates a minimal cluster, publishes one NodePort on loopback, and waits for its API.
func (c *Client) Create(ctx context.Context, name string, nodePort int) model.CommandResult {
	return c.CreateWithMemory(ctx, name, nodePort, "4g")
}

// CreateWithMemory selects one of the fixed, qualified cluster memory budgets.
// Arbitrary Docker resource flags are never accepted through this adapter.
func (c *Client) CreateWithMemory(ctx context.Context, name string, nodePort int, memory string) model.CommandResult {
	if memory != "4g" && memory != "6g" {
		return model.CommandResult{Command: "k3d", ExitCode: -1, FailureType: model.FailureExecution, Stderr: "Unsupported bounded cluster memory profile."}
	}
	portMapping := "127.0.0.1:0:" + strconv.Itoa(nodePort) + "@server:0"
	args := []string{"cluster", "create", name, "--image", NodeImage, "--servers-memory", memory, "--kubeconfig-update-default=false", "--kubeconfig-switch-context=false", "--runtime-label", "cloudforge.dev/owned=true@all", "--runtime-label", "cloudforge.dev/run-id=" + name + "@all", "--servers", "1", "--agents", "0"}
	if c.ownershipToken != "" {
		args = append(args, "--runtime-label", "cloudforge.dev/ownership="+c.ownershipToken+"@all")
	}
	if nodePort > 0 {
		args = append(args, "--port", portMapping)
	}
	args = append(args, "--wait", "--timeout", "90s")
	return c.runner.Run(ctx, command.Request{
		Name: "k3d", Args: args,
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
