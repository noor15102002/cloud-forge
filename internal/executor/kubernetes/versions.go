package kubernetes

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/internal/doctor"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

const supportedKubectlSkew = 1

// CompatibleClientServer confirms kubectl's client minor is within Kubernetes'
// supported version-skew range of the cluster server. A mismatch is an
// execution error, not an application failure.
func (c *Client) CompatibleClientServer(ctx context.Context, cluster string) (model.CommandResult, string) {
	result := c.runner.Run(ctx, command.Request{
		Name: "kubectl", Args: []string{"--context", "k3d-" + cluster, "version", "--output=json"},
		Timeout: 15 * time.Second, OutputLimit: 64 * 1024,
	})
	if result.FailureType != model.FailureNone || result.ExitCode != 0 || result.Truncated {
		if result.FailureType == model.FailureNone {
			result.FailureType = model.FailureExit
			result.ExitCode = 1
		}
		return result, "kubectl_version_unavailable"
	}
	var versions struct {
		ClientVersion struct {
			GitVersion string `json:"gitVersion"`
		} `json:"clientVersion"`
		ServerVersion struct {
			GitVersion string `json:"gitVersion"`
		} `json:"serverVersion"`
	}
	if json.Unmarshal([]byte(result.Stdout), &versions) != nil {
		return model.CommandResult{FailureType: model.FailureExecution, ExitCode: -1, Command: result.Command, Arguments: result.Arguments, DurationMS: result.DurationMS}, "kubectl_version_unrecognized"
	}
	clientMajor, clientMinor, clientOK := kubernetesMajorMinor(versions.ClientVersion.GitVersion)
	serverMajor, serverMinor, serverOK := kubernetesMajorMinor(versions.ServerVersion.GitVersion)
	if !clientOK || !serverOK {
		return model.CommandResult{FailureType: model.FailureExecution, ExitCode: -1, Command: result.Command, Arguments: result.Arguments, DurationMS: result.DurationMS}, "kubectl_version_unrecognized"
	}
	if clientMajor != serverMajor {
		return model.CommandResult{FailureType: model.FailureExecution, ExitCode: -1, Command: result.Command, Arguments: result.Arguments, DurationMS: result.DurationMS}, "kubectl_version_skew"
	}
	skew := clientMinor - serverMinor
	if skew < 0 {
		skew = -skew
	}
	if skew > supportedKubectlSkew {
		return model.CommandResult{FailureType: model.FailureExecution, ExitCode: -1, Command: result.Command, Arguments: result.Arguments, DurationMS: result.DurationMS}, "kubectl_version_skew"
	}
	return result, ""
}

func kubernetesMajorMinor(gitVersion string) (int, int, bool) {
	parsed := doctor.ParsedVersion(gitVersion)
	if parsed == "" {
		return 0, 0, false
	}
	if cut := strings.IndexAny(parsed, "+-"); cut >= 0 {
		parsed = parsed[:cut]
	}
	parts := strings.Split(parsed, ".")
	if len(parts) < 2 {
		return 0, 0, false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, false
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, false
	}
	return major, minor, true
}
