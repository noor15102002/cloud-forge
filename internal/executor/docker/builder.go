package docker

import (
	"context"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

// IsolateBuild selects a run-owned BuildKit container with enforced cgroup limits.
func (c *Client) IsolateBuild(name string) { c.builder = name }

// CreateBuilder provisions a bounded builder without changing the user's default builder.
func (c *Client) CreateBuilder(ctx context.Context) model.CommandResult {
	return c.runner.Run(ctx, command.Request{Name: "docker", Args: []string{"buildx", "create", "--name", c.builder, "--driver", "docker-container", "--driver-opt", "memory=2g,cpu-period=100000,cpu-quota=200000"}, Timeout: time.Minute, OutputLimit: 64 * 1024})
}

// RemoveBuilder removes only the named run builder and its build cache.
func (c *Client) RemoveBuilder(ctx context.Context) model.CommandResult {
	return c.runner.Run(ctx, command.Request{Name: "docker", Args: []string{"buildx", "rm", "--force", c.builder}, Timeout: time.Minute, OutputLimit: 64 * 1024})
}
