package docker

import (
	"context"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

// IsolateBuild selects a run-owned BuildKit container with enforced cgroup limits.
func (c *Client) IsolateBuild(name string) {
	c.builder = name
	c.builderOwnership = nil
}

// CreateBuilder provisions a bounded builder without changing the user's default builder.
func (c *Client) CreateBuilder(ctx context.Context) model.CommandResult {
	if !builderRunName.MatchString(c.builder) {
		return builderCleanupError(model.CommandResult{}, "The builder run identity is invalid.")
	}
	return c.runner.Run(ctx, command.Request{Name: "docker", Args: []string{"buildx", "create", "--name", c.builder, "--driver", "docker-container", "--driver-opt", "memory=2g,cpu-period=100000,cpu-quota=200000,env.CLOUDFORGE_RUN_ID=" + c.builder}, Timeout: time.Minute, OutputLimit: 64 * 1024})
}

// RemoveBuilder snapshots ownership before Buildx can remove the container and
// lose its volume relationship. Inspection and the original removal share one
// minute. A removal failure is never replaced by a later fallback success.
func (c *Client) RemoveBuilder(ctx context.Context) model.CommandResult {
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if result := c.captureBuilderOwnership(ctx); builderCleanupFailed(result) {
		result.DurationMS = time.Since(started).Milliseconds()
		return result
	}
	result := c.runner.Run(ctx, command.Request{Name: "docker", Args: []string{"buildx", "rm", "--force", c.builder}, Timeout: time.Minute, OutputLimit: 64 * 1024})
	result.DurationMS = time.Since(started).Milliseconds()
	return result
}
