package verification

import (
	"context"
	"path/filepath"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/internal/runtimepolicy"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

const backendDockerEndpoint = runtimepolicy.DockerEndpoint

// scopedRunner never changes process-wide environment or the user's kubeconfig.
type scopedRunner struct {
	runner       command.Runner
	kubeconfig   string
	dockerConfig string
	tempDir      string
	dockerPinned bool
}

func (r scopedRunner) Run(ctx context.Context, req command.Request) model.CommandResult {
	if req.Name == "kubectl" || req.Name == "k3d" {
		req.Env = append(req.Env, "KUBECONFIG="+r.kubeconfig)
	}
	if r.dockerConfig != "" {
		req.Env = append(req.Env, "DOCKER_CONFIG="+r.dockerConfig, "BUILDX_CONFIG="+filepath.Join(r.dockerConfig, "buildx"), "BUILDX_BUILDER=")
	}
	if r.tempDir != "" && !req.ClearEnv {
		// Runtime tools may leave their own temporary files after succeeding.
		// Keep those files inside the already owned cleanup boundary. A tool
		// with a controlled environment (Trivy) keeps its stricter private temp.
		req.Env = append(req.Env, "TMPDIR="+r.tempDir)
	}
	if r.dockerPinned {
		req = runtimepolicy.PinDocker(req)
	}

	return r.runner.Run(ctx, req)
}
