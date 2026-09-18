// Package docker adapts Docker CLI operations used by verification.
package docker

import (
	"context"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

// Client builds application images through the Docker CLI.
type Client struct{ runner command.Runner }

// New creates a Docker CLI adapter.
func New(runner command.Runner) *Client { return &Client{runner: runner} }

// Build builds and tags a Dockerfile from root.
func (c *Client) Build(ctx context.Context, root, image string) model.CommandResult {
	return c.runner.Run(ctx, command.Request{
		Name: "docker", Args: []string{"build", "--tag", image, "."}, Dir: root,
		Timeout: 10 * time.Minute, OutputLimit: 256 * 1024,
	})
}

// RemoveImage removes the uniquely tagged image created for a verification run.
func (c *Client) RemoveImage(ctx context.Context, image string) model.CommandResult {
	return c.runner.Run(ctx, command.Request{
		Name: "docker", Args: []string{"image", "rm", "--force", image},
		Timeout: time.Minute, OutputLimit: 128 * 1024,
	})
}
