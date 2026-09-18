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
	"github.com/noor15102002/cloud-forge/pkg/model"
)

// Client builds application images through the Docker CLI.
type Client struct{ runner command.Runner }

// New creates a Docker CLI adapter.
func New(runner command.Runner) *Client { return &Client{runner: runner} }

// Build builds and tags a Dockerfile from root.
func (c *Client) Build(ctx context.Context, root, image string) model.CommandResult {
	return c.BuildVersion(ctx, root, image, "")
}

// BuildVersion builds and tags a Dockerfile with an optional public experiment version.
func (c *Client) BuildVersion(ctx context.Context, root, image, version string) model.CommandResult {
	args := []string{"build"}
	if version != "" {
		args = append(args, "--build-arg", "CLOUDFORGE_VERSION="+version)
	}
	args = append(args, "--tag", image, ".")
	return c.runner.Run(ctx, command.Request{
		Name: "docker", Args: args, Dir: root,
		Timeout: 10 * time.Minute, OutputLimit: 256 * 1024,
	})
}

// RemoveImage removes the uniquely tagged image created for a verification run.
func (c *Client) RemoveImage(ctx context.Context, image string) model.CommandResult {
	result := c.runner.Run(ctx, command.Request{
		Name: "docker", Args: []string{"image", "rm", "--force", image},
		Timeout: time.Minute, OutputLimit: 128 * 1024,
	})
	output := strings.ToLower(result.Stdout + " " + result.Stderr)
	if strings.Contains(output, "no such image") || strings.Contains(output, "image does not exist") {
		result.ExitCode = 0
		result.FailureType = model.FailureNone
	}
	return result
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
