// Package docker adapts Docker CLI operations used by verification.
package docker

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/internal/selection"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

// Client builds application images through the Docker CLI.
type Client struct {
	runner            command.Runner
	builder           string
	ownershipToken    string
	builderOwnership  *builderOwnership
	clusterPrepared   string
	clusterCreated    bool
	clusterProven     bool
	clusterNetworkID  string
	clusterVolumeName string
	clusterResources  map[string]string
	attemptedImages   map[string]bool
}

// New creates a Docker CLI adapter.
func New(runner command.Runner) *Client { return &Client{runner: runner} }

// Build builds and tags a Dockerfile from root.
func (c *Client) Build(ctx context.Context, root, image string) model.CommandResult {
	return c.BuildVersion(ctx, root, image, "")
}

// BuildVersion builds and tags a Dockerfile with an optional public experiment version.
func (c *Client) BuildVersion(ctx context.Context, root, image, version string) model.CommandResult {
	return c.BuildSelected(ctx, root, image, version, nil)
}

// BuildSelected uses one repository-relative build tuple for every image.
// Revalidation catches selected paths replaced since planning, before Docker runs.
func (c *Client) BuildSelected(ctx context.Context, root, image, version string, build *model.BuildSelection) model.CommandResult {
	if err := ctx.Err(); err != nil {
		failure := model.FailureCanceled
		if err == context.DeadlineExceeded {
			failure = model.FailureTimeout
		}
		return model.CommandResult{Command: "docker", ExitCode: -1, FailureType: failure}
	}
	buildContext := "."
	if build != nil {
		resolved, err := selection.Resolve(root, *build)
		if err != nil {
			return model.CommandResult{Command: "docker", ExitCode: -1, FailureType: model.FailureExecution, Stderr: "Build selection changed or became unsafe after planning."}
		}
		build = &resolved
		buildContext = "./" + build.Context
	}
	if c.builder != "" {
		if result := c.prepareImage(ctx, image); builderCleanupFailed(result) {
			return result
		}
		if c.attemptedImages == nil {
			c.attemptedImages = map[string]bool{}
		}
		c.attemptedImages[image] = true
	}
	args := []string{"build"}
	if c.builder != "" {
		args = []string{"buildx", "build", "--builder", c.builder, "--load", "--provenance=false", "--label", "cloudforge.dev/run-id=" + c.builder, "--label", "cloudforge.dev/ownership=" + c.ownershipToken}
	}
	if version != "" {
		if c.builder != "" {
			// Distinct image configuration keeps same-source A/B builds independently
			// removable even when the Dockerfile ignores the public build argument.
			args = append(args, "--label", "cloudforge.dev/build-version="+version)
		}
		args = append(args, "--build-arg", "CLOUDFORGE_VERSION="+version)
	}
	if build != nil {
		args = append(args, "--file", "./"+build.Dockerfile)
	}
	args = append(args, "--tag", image, buildContext)
	return c.runner.Run(ctx, command.Request{
		Name: "docker", Args: args, Dir: root,
		Timeout: 10 * time.Minute, OutputLimit: 256 * 1024,
	})
}

// PublishedPort returns Docker's dynamically selected loopback port for a k3d load balancer.
func (c *Client) PublishedPort(ctx context.Context, cluster string, containerPort int) (int, model.CommandResult, error) {
	result := c.runner.Run(ctx, command.Request{
		Name: "docker", Args: []string{"port", "k3d-" + cluster + "-serverlb", strconv.Itoa(containerPort) + "/tcp"},
		Timeout: 15 * time.Second, OutputLimit: 16 * 1024,
	})
	if result.FailureType != model.FailureNone || result.ExitCode != 0 {
		return 0, result, nil
	}
	for _, line := range strings.Split(result.Stdout, "\n") {
		host, portText, err := net.SplitHostPort(strings.TrimSpace(line))
		if err != nil || host != "127.0.0.1" {
			continue
		}
		port, err := strconv.Atoi(portText)
		if err == nil && port > 0 && port <= 65535 {
			return port, result, nil
		}
	}
	return 0, result, fmt.Errorf("docker did not report a loopback port for %d/tcp", containerPort)
}
