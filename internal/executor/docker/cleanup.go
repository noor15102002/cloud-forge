package docker

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

var resourceID = regexp.MustCompile(`^[a-f0-9]{12,64}$`)
var ownedName = regexp.MustCompile(`^k3d-cloudforge-[a-f0-9]+(?:-[a-zA-Z0-9_.-]+)?$`)

// RemoveClusterRemnants covers interrupted creation before k3d records a full
// cluster. Both a k3d ownership label and this run's exact name/prefix are required.
func (c *Client) RemoveClusterRemnants(ctx context.Context, cluster, kind string) model.CommandResult {
	prefix := "k3d-" + cluster
	var args []string
	switch kind {
	case "container":
		args = []string{"container", "ls", "--all", "--filter", "label=app=k3d", "--filter", "label=k3d.cluster=" + cluster, "--format", "{{.ID}} {{.Names}}"}
	case "network":
		args = []string{"network", "ls", "--filter", "label=app=k3d", "--filter", "name=" + prefix, "--format", "{{.ID}} {{.Name}}"}
	case "volume":
		args = []string{"volume", "ls", "--filter", "label=app=k3d", "--filter", "name=" + prefix, "--format", "{{.Name}}"}
	default:
		return model.CommandResult{FailureType: model.FailureExecution, ExitCode: -1}
	}
	result := c.runner.Run(ctx, command.Request{Name: "docker", Args: args, Timeout: 30 * time.Second, OutputLimit: 32 * 1024})
	if result.ExitCode != 0 || result.FailureType != model.FailureNone || result.Truncated {
		return result
	}
	var last model.CommandResult
	for _, line := range strings.Split(result.Stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		name := fields[len(fields)-1]
		if !ownedName.MatchString(name) || (name != prefix && !strings.HasPrefix(name, prefix+"-")) {
			continue
		}
		if kind == "network" && name != prefix {
			continue
		}
		identifier := fields[0]
		if kind != "volume" && (len(fields) != 2 || !resourceID.MatchString(identifier)) {
			continue
		}
		remove := []string{kind, "rm"}
		if kind == "container" {
			remove = append(remove, "--force")
		}
		remove = append(remove, identifier)
		removed := c.runner.Run(ctx, command.Request{Name: "docker", Args: remove, Timeout: 30 * time.Second, OutputLimit: 32 * 1024})
		if removed.ExitCode != 0 || removed.FailureType != model.FailureNone {
			last = removed
		}
	}
	return last
}
