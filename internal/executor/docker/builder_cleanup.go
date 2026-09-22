package docker

import (
	"context"
	"encoding/json"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

var builderRunName = regexp.MustCompile(`^cloudforge-(?:[a-f0-9]{8}|[a-f0-9]{32})$`)
var builderContainerID = regexp.MustCompile(`^[a-f0-9]{64}$`)

// The Docker-container Buildx driver supports env.<NAME>, names the first node
// <builder>0, and mounts <container>_state at /var/lib/buildkit. It supplies no
// ownership label. These contracts are verified against Buildx v0.23.0 commit
// 28c90eadc4c12cc78155ad59ca5f486220241d2a, driver/docker-container/{factory,driver}.go.
// Keep this proof private: neither inspected environment nor volume metadata is
// application evidence. A Docker volume has no immutable ID, so its creation
// identity must also match before removing the captured named mount.
type builderOwnership struct {
	containerID   string
	containerName string
	volumeName    string
	volumeCreated string
	volumeDriver  string
	volumeScope   string
}

func (c *Client) builderContainerName() string { return "buildx_buildkit_" + c.builder + "0" }

func builderCleanupFailed(result model.CommandResult) bool {
	return result.ExitCode != 0 || result.FailureType != model.FailureNone || result.Truncated
}

func builderCleanupError(result model.CommandResult, reason string) model.CommandResult {
	result.Command, result.Arguments = "docker", nil
	result.Stdout, result.Stderr = "", reason
	if result.FailureType == model.FailureNone {
		result.FailureType = model.FailureExecution
	}
	if result.ExitCode == 0 {
		result.ExitCode = -1
	}
	return result
}

func builderContextError(ctx context.Context) model.CommandResult {
	if ctx.Err() == nil {
		return model.CommandResult{}
	}
	failure := model.FailureCanceled
	if ctx.Err() == context.DeadlineExceeded {
		failure = model.FailureTimeout
	}
	return builderCleanupError(model.CommandResult{FailureType: failure, ExitCode: -1}, "The bounded builder cleanup context ended.")
}

func decodeBuilderFields(raw string, values ...any) bool {
	decoder := json.NewDecoder(strings.NewReader(raw))
	for _, value := range values {
		if decoder.Decode(value) != nil {
			return false
		}
	}
	return decoder.Decode(new(any)) == io.EOF
}

// Only the exact Docker not-found diagnostic is absence. A daemon error,
// successful empty response, timeout, or truncated response cannot prove it.
func builderObjectMissing(result model.CommandResult, kind, name string) bool {
	if result.Truncated || result.ExitCode != 1 || result.FailureType != model.FailureExit || strings.TrimSpace(result.Stdout) != "" {
		return false
	}
	message := strings.TrimSpace(result.Stderr)
	if kind == "volume" && message == "Error response from daemon: get "+name+": no such volume" {
		return true
	}
	for _, prefix := range []string{"Error: No such " + kind + ": ", "Error response from daemon: No such " + kind + ": ", "Error: No such object: "} {
		if message == prefix+name {
			return true
		}
	}
	return false
}

func (c *Client) inspectBuilderContainer(ctx context.Context, reference string) (builderOwnership, bool, model.CommandResult) {
	if result := builderContextError(ctx); builderCleanupFailed(result) {
		return builderOwnership{}, false, result
	}
	// Emit only a marker boolean, never the environment array or other mounts.
	format := `{{json .Id}} {{json .Name}} {{$owned := false}}{{range .Config.Env}}{{if eq . "CLOUDFORGE_OWNER_ID=` + c.ownershipToken + `"}}{{$owned = true}}{{end}}{{end}}{{json $owned}} {{range .Mounts}}{{if eq .Destination "/var/lib/buildkit"}}{{json .Type}} {{json .Name}}{{end}}{{end}}`
	result := c.runner.Run(ctx, command.Request{Name: "docker", Args: []string{"container", "inspect", "--format", format, reference}, Timeout: 5 * time.Second, OutputLimit: 4096})
	if builderObjectMissing(result, "container", reference) {
		return builderOwnership{}, false, model.CommandResult{}
	}
	if builderCleanupFailed(result) {
		return builderOwnership{}, false, builderCleanupError(result, "The builder container ownership could not be observed reliably.")
	}
	var proof builderOwnership
	var marker bool
	var mountType string
	if !decodeBuilderFields(result.Stdout, &proof.containerID, &proof.containerName, &marker, &mountType, &proof.volumeName) ||
		!builderContainerID.MatchString(proof.containerID) || proof.containerName != "/"+c.builderContainerName() ||
		!marker || mountType != "volume" || proof.volumeName != c.builderContainerName()+"_state" {
		return builderOwnership{}, false, builderCleanupError(result, "The builder container ownership did not match this run; removal was refused.")
	}
	return proof, true, model.CommandResult{}
}

func (c *Client) inspectBuilderVolume(ctx context.Context, name string) (builderOwnership, bool, model.CommandResult) {
	if result := builderContextError(ctx); builderCleanupFailed(result) {
		return builderOwnership{}, false, result
	}
	format := `{{json .Name}} {{json .Driver}} {{json .Scope}} {{json .CreatedAt}}`
	result := c.runner.Run(ctx, command.Request{Name: "docker", Args: []string{"volume", "inspect", "--format", format, name}, Timeout: 5 * time.Second, OutputLimit: 4096})
	if builderObjectMissing(result, "volume", name) {
		return builderOwnership{}, false, model.CommandResult{}
	}
	if builderCleanupFailed(result) {
		return builderOwnership{}, false, builderCleanupError(result, "The builder cache volume could not be observed reliably.")
	}
	var proof builderOwnership
	if !decodeBuilderFields(result.Stdout, &proof.volumeName, &proof.volumeDriver, &proof.volumeScope, &proof.volumeCreated) ||
		proof.volumeName != name || proof.volumeDriver != "local" || proof.volumeScope != "local" {
		return builderOwnership{}, false, builderCleanupError(result, "The builder cache volume identity was invalid; removal was refused.")
	}
	if _, err := time.Parse(time.RFC3339Nano, proof.volumeCreated); err != nil {
		return builderOwnership{}, false, builderCleanupError(result, "The builder cache volume creation identity was unavailable; removal was refused.")
	}
	return proof, true, model.CommandResult{}
}

func (c *Client) captureBuilderOwnership(ctx context.Context) model.CommandResult {
	if !builderRunName.MatchString(c.builder) {
		return builderCleanupError(model.CommandResult{}, "The builder run identity is invalid.")
	}
	proof, exists, result := c.inspectBuilderContainer(ctx, c.builderContainerName())
	if builderCleanupFailed(result) {
		return result
	}
	if !exists {
		volume, exists, result := c.inspectBuilderVolume(ctx, c.builderContainerName()+"_state")
		if builderCleanupFailed(result) || !exists {
			return result
		}
		if c.builderOwnership == nil || !sameBuilderVolume(*c.builderOwnership, volume) {
			return builderCleanupError(result, "The remaining builder cache has no matching prior container-to-volume ownership proof; removal was refused.")
		}
		return model.CommandResult{}
	}
	volume, exists, result := c.inspectBuilderVolume(ctx, proof.volumeName)
	if builderCleanupFailed(result) {
		return result
	}
	if !exists {
		return builderCleanupError(result, "The builder's mounted cache volume was not observable; removal was refused.")
	}
	proof.volumeCreated, proof.volumeDriver, proof.volumeScope = volume.volumeCreated, volume.volumeDriver, volume.volumeScope
	if c.builderOwnership != nil && *c.builderOwnership != proof {
		return builderCleanupError(result, "The builder identity changed after its ownership was established; removal was refused.")
	}
	c.builderOwnership = &proof
	return model.CommandResult{}
}

func sameBuilderVolume(proof, volume builderOwnership) bool {
	return volume.volumeName == proof.volumeName && volume.volumeCreated == proof.volumeCreated && volume.volumeDriver == proof.volumeDriver && volume.volumeScope == proof.volumeScope
}

// RemoveBuilderRemnants is a separate, bounded cleanup attempt after ordinary
// Buildx removal and cluster teardown. The caller supplies a fresh cleanup
// context. The entire fallback has at most thirty seconds and never erases the
// original removal result. Only previously verified IDs and mount identities
// may be removed; no prefix search, prune, or forced volume removal is used.
func (c *Client) RemoveBuilderRemnants(ctx context.Context) (result model.CommandResult) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	defer func() { result.DurationMS = time.Since(started).Milliseconds() }()
	if !builderRunName.MatchString(c.builder) {
		return builderCleanupError(model.CommandResult{}, "The builder run identity is invalid.")
	}
	proof := c.builderOwnership
	if proof == nil {
		_, exists, observed := c.inspectBuilderContainer(ctx, c.builderContainerName())
		if builderCleanupFailed(observed) {
			return observed
		}
		if exists {
			return builderCleanupError(observed, "No prior ownership proof exists for the remaining builder container; removal was refused.")
		}
		_, exists, observed = c.inspectBuilderVolume(ctx, c.builderContainerName()+"_state")
		if builderCleanupFailed(observed) {
			return observed
		}
		if exists {
			return builderCleanupError(observed, "No prior container-to-volume ownership proof exists for the remaining builder cache; removal was refused.")
		}
		return model.CommandResult{}
	}
	container, exists, observed := c.inspectBuilderContainer(ctx, proof.containerID)
	if builderCleanupFailed(observed) {
		return observed
	}
	if exists {
		if container.containerID != proof.containerID || container.containerName != proof.containerName || container.volumeName != proof.volumeName {
			return builderCleanupError(observed, "The remaining builder container did not match its captured identity; removal was refused.")
		}
		removed := c.runner.Run(ctx, command.Request{Name: "docker", Args: []string{"container", "rm", "--force", proof.containerID}, Timeout: 10 * time.Second, OutputLimit: 4096})
		if builderCleanupFailed(removed) && !builderObjectMissing(removed, "container", proof.containerID) {
			return builderCleanupError(removed, "The verified builder container could not be removed.")
		}
		_, exists, observed = c.inspectBuilderContainer(ctx, proof.containerID)
		if builderCleanupFailed(observed) {
			return observed
		}
		if exists {
			return builderCleanupError(observed, "The verified builder container remained after removal.")
		}
	}
	volume, exists, observed := c.inspectBuilderVolume(ctx, proof.volumeName)
	if builderCleanupFailed(observed) || !exists {
		return observed
	}
	if !sameBuilderVolume(*proof, volume) {
		return builderCleanupError(observed, "The cache volume changed after its ownership was captured; removal was refused.")
	}
	removed := c.runner.Run(ctx, command.Request{Name: "docker", Args: []string{"volume", "rm", proof.volumeName}, Timeout: 10 * time.Second, OutputLimit: 4096})
	if builderCleanupFailed(removed) && !builderObjectMissing(removed, "volume", proof.volumeName) {
		return builderCleanupError(removed, "The verified builder cache volume could not be removed.")
	}
	_, exists, observed = c.inspectBuilderVolume(ctx, proof.volumeName)
	if builderCleanupFailed(observed) {
		return observed
	}
	if exists {
		return builderCleanupError(observed, "The verified builder cache volume remained after removal.")
	}
	return model.CommandResult{}
}
