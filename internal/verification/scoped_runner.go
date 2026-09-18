package verification

import (
	"context"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

// scopedRunner never changes process-wide environment or the user's kubeconfig.
type scopedRunner struct {
	runner       command.Runner
	kubeconfig   string
	dockerConfig string
}

func (r scopedRunner) Run(ctx context.Context, req command.Request) model.CommandResult {
	if req.Name == "kubectl" || req.Name == "k3d" {
		req.Env = append(req.Env, "KUBECONFIG="+r.kubeconfig)
	}
	if r.dockerConfig != "" {
		req.Env = append(req.Env, "DOCKER_CONFIG="+r.dockerConfig)
	}
	return r.runner.Run(ctx, req)
}
