package runtimepolicy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

// DockerEndpoint is the only supported endpoint. Never silently substitute it
// for a user's selected remote, VM, rootless or nondefault socket endpoint.
const DockerEndpoint = "unix:///var/run/docker.sock"

// DockerSelection contains only bounded, non-sensitive provenance. Context
// names, supplied endpoint strings and TLS/certificate paths never escape.
type DockerSelection struct {
	Status, Origin string
	Observation    model.CommandResult
}

// ResolveDocker observes selection before a private Docker configuration is
// introduced. Docker's precedence is DOCKER_CONTEXT, DOCKER_HOST, then the
// configured current context. Only context metadata is inspected; no daemon
// is contacted during resolution.
func ResolveDocker(ctx context.Context, runner command.Runner, getenv func(string) string) DockerSelection {
	selection := DockerSelection{Status: "not_validated", Origin: "configured_context"}
	if ctx.Err() != nil {
		return selection
	}
	for _, key := range []string{"DOCKER_TLS", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH"} {
		if getenv(key) != "" {
			selection.Status = "unsupported"
			return selection
		}
	}
	endpoint := getenv("DOCKER_HOST")
	contextName := getenv("DOCKER_CONTEXT")
	if contextName != "" {
		selection.Origin = "environment_context"
	} else if endpoint != "" {
		selection.Origin = "environment_host"
	}
	// Docker's special "default" context synthesizes its endpoint from host
	// flags/environment rather than stored metadata. Never hide a remote host
	// in this case by clearing it and silently switching to the local socket.
	defaultHost := contextName == "default" && endpoint != ""
	if selection.Origin != "environment_host" && !defaultHost {
		request := command.Request{Name: "docker", Args: []string{"context", "inspect", "--format", "{{json .Endpoints.docker.Host}}"}, Timeout: 10 * time.Second, OutputLimit: 4096}
		if contextName != "" {
			// `context inspect` without an explicit name reports the synthesized
			// default when DOCKER_HOST is set, unlike normal daemon selection.
			// Inspect the selected metadata explicitly without that host override.
			request.Args = append(request.Args, "--", contextName)
			request.Env = []string{"DOCKER_HOST="}
		}
		result := runner.Run(ctx, request)
		// Retain only the safe observation result, never raw output/arguments.
		selection.Observation = model.CommandResult{ExitCode: result.ExitCode, FailureType: result.FailureType, DurationMS: result.DurationMS, Truncated: result.Truncated}
		if ctx.Err() != nil || result.FailureType != model.FailureNone || result.ExitCode != 0 || result.Truncated || json.Unmarshal([]byte(result.Stdout), &endpoint) != nil || endpoint == "" {
			return selection
		}
	}
	selection.Status = "unsupported"
	if endpoint == DockerEndpoint {
		selection.Status = "supported"
	}
	return selection
}

// PinDocker makes every consumer, including Docker SDK consumers, use the
// already approved endpoint independently of later environment/config changes.
func PinDocker(req command.Request) command.Request {
	req.Env = append(req.Env, "DOCKER_HOST="+DockerEndpoint, "DOCKER_CONTEXT=", "DOCKER_TLS=", "DOCKER_TLS_VERIFY=", "DOCKER_CERT_PATH=")
	return req
}

// BuildxVersion checks the same plugin visibility and private buildx state as
// verification. A plugin installed only inside a user's Docker config must not
// pass preflight and then disappear when verification isolates that config.
func BuildxVersion(ctx context.Context, runner command.Runner) (result model.CommandResult) {
	if ctx.Err() != nil {
		return model.CommandResult{ExitCode: -1, FailureType: model.FailureCanceled}
	}
	directory, err := os.MkdirTemp("", "cloudforge-docker-preflight-")
	if err != nil {
		return model.CommandResult{ExitCode: -1, FailureType: model.FailureExecution}
	}
	defer func() {
		if err := os.RemoveAll(directory); err != nil {
			result = model.CommandResult{ExitCode: -1, FailureType: model.FailureExecution}
		}
	}()
	return runner.Run(ctx, command.Request{Name: "docker", Args: []string{"buildx", "version"}, Timeout: 10 * time.Second, OutputLimit: 16 * 1024, Env: []string{"DOCKER_CONFIG=" + directory, "BUILDX_CONFIG=" + filepath.Join(directory, "buildx"), "BUILDX_BUILDER=", "TMPDIR=" + directory}})
}
