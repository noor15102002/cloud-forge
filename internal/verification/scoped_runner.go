package verification

import (
	"context"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

const backendDockerEndpoint = "unix:///var/run/docker.sock"

// scopedRunner never changes process-wide environment or the user's kubeconfig.
type scopedRunner struct {
	runner        command.Runner
	kubeconfig    string
	dockerConfig  string
	backendDocker bool
}

func (r scopedRunner) Run(ctx context.Context, req command.Request) model.CommandResult {
	if req.Name == "kubectl" || req.Name == "k3d" {
		req.Env = append(req.Env, "KUBECONFIG="+r.kubeconfig)
	}
	if r.dockerConfig != "" {
		req.Env = append(req.Env, "DOCKER_CONFIG="+r.dockerConfig)
	}
	if r.backendDocker {
		// Backend preflight qualifies this exact local endpoint. Docker's fresh
		// private config and k3d's Docker SDK must use the same daemon, regardless
		// of the user's context or TLS environment. Never change process globals.
		req.Env = append(req.Env, "DOCKER_HOST="+backendDockerEndpoint, "DOCKER_CONTEXT=", "DOCKER_TLS=", "DOCKER_TLS_VERIFY=", "DOCKER_CERT_PATH=")
	}
	return r.runner.Run(ctx, req)
}
