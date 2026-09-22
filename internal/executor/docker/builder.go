package docker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

// IsolateBuild selects a run-owned BuildKit container with enforced cgroup limits.
func (c *Client) IsolateBuild(name string) {
	c.builder = name
	c.ownershipToken = ""
	var token [16]byte
	if _, err := rand.Read(token[:]); err == nil {
		c.ownershipToken = hex.EncodeToString(token[:])
	}
	c.builderOwnership = nil
	c.attemptedImages = nil
}

// CreateBuilder provisions a bounded builder without changing the user's default builder.
func (c *Client) CreateBuilder(ctx context.Context) model.CommandResult {
	if !builderRunName.MatchString(c.builder) || len(c.ownershipToken) != 32 {
		return builderCleanupError(model.CommandResult{}, "The builder run identity is invalid.")
	}
	return c.runner.Run(ctx, command.Request{Name: "docker", Args: []string{"buildx", "create", "--name", c.builder, "--driver", "docker-container", "--driver-opt", "memory=2g,cpu-period=100000,cpu-quota=200000,env.CLOUDFORGE_RUN_ID=" + c.builder + ",env.CLOUDFORGE_OWNER_ID=" + c.ownershipToken}, Timeout: time.Minute, OutputLimit: 64 * 1024})
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
	if !builderCleanupFailed(result) {
		// Every caller, including early backend shutdown, must establish runtime
		// and cache absence before it may disarm subsequent cleanup.
		result = c.RemoveBuilderRemnants(ctx)
	}
	result.DurationMS = time.Since(started).Milliseconds()
	return result
}

// OwnershipToken is passed only to the disposable runtime, never report metadata.
func (c *Client) OwnershipToken() string { return c.ownershipToken }
