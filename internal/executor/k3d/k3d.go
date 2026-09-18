// Package k3d adapts disposable k3d cluster lifecycle operations.
package k3d

import (
	"context"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

// Client manages one-run k3d clusters.
type Client struct{ runner command.Runner }

// New creates a k3d CLI adapter.
func New(runner command.Runner) *Client { return &Client{runner: runner} }

// Create creates a minimal cluster and waits for its API.
func (c *Client) Create(ctx context.Context, name string) model.CommandResult {
	return c.runner.Run(ctx, command.Request{
		Name: "k3d", Args: []string{"cluster", "create", name, "--servers", "1", "--agents", "0", "--no-lb", "--wait", "--timeout", "90s"},
		Timeout: 2 * time.Minute, OutputLimit: 128 * 1024,
	})
}

// ImportImage loads a local image into every node in a cluster.
func (c *Client) ImportImage(ctx context.Context, cluster, image string) model.CommandResult {
	return c.runner.Run(ctx, command.Request{
		Name: "k3d", Args: []string{"image", "import", image, "--cluster", cluster},
		Timeout: 3 * time.Minute, OutputLimit: 128 * 1024,
	})
}

// Delete removes a CloudForge-owned cluster.
func (c *Client) Delete(ctx context.Context, name string) model.CommandResult {
	return c.runner.Run(ctx, command.Request{
		Name: "k3d", Args: []string{"cluster", "delete", name},
		Timeout: 2 * time.Minute, OutputLimit: 128 * 1024,
	})
}
