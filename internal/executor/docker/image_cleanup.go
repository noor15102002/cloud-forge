package docker

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

var imageIdentifier = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// PrepareBuild establishes absence before any builder or image name is used.
// Buildx metadata lives in the run's private Docker configuration; its runtime
// container and cache volume also need an explicit collision check.
func (c *Client) PrepareBuild(ctx context.Context, images ...string) model.CommandResult {
	if !builderRunName.MatchString(c.builder) || len(c.ownershipToken) != 32 {
		return cleanupError(model.CommandResult{}, "A private resource ownership identity could not be established.")
	}
	for _, kind := range []string{"container", "volume"} {
		name := c.builderContainerName()
		if kind == "volume" {
			name += "_state"
		}
		resources, result := c.inventory(ctx, kind, name, false)
		if builderCleanupFailed(result) {
			return result
		}
		if len(resources) != 0 {
			return cleanupError(result, "A same-name builder resource already exists; creation and cleanup were refused.")
		}
	}
	for _, image := range images {
		if result := c.prepareImage(ctx, image); builderCleanupFailed(result) {
			return result
		}
	}
	return model.CommandResult{}
}

func (c *Client) prepareImage(ctx context.Context, image string) model.CommandResult {
	result := c.runner.Run(ctx, command.Request{Name: "docker", Args: []string{"image", "ls", "--no-trunc", "--filter", "reference=" + image, "--format", "{{.ID}} {{.Repository}}:{{.Tag}}"}, Timeout: 15 * time.Second, OutputLimit: 4096})
	if builderCleanupFailed(result) {
		return cleanupError(result, "Image name availability could not be observed reliably; the build was refused.")
	}
	if result.Stdout != "" && !strings.HasSuffix(result.Stdout, "\n") {
		return cleanupError(result, "The image inventory ended before its record terminator; the build was refused.")
	}
	if result.Stdout == "" {
		return model.CommandResult{}
	}
	for _, line := range strings.Split(strings.TrimSuffix(result.Stdout, "\n"), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || !imageIdentifier.MatchString(fields[0]) || fields[1] != image {
			return cleanupError(result, "The image name inventory was malformed; the build was refused.")
		}
	}
	return cleanupError(result, "A same-name image already exists; the build and image cleanup were refused.")
}

func (c *Client) inspectOwnedImage(ctx context.Context, image string) (string, bool, model.CommandResult) {
	format := `{{json .Id}} {{json (eq (index .Config.Labels "cloudforge.dev/ownership") "` + c.ownershipToken + `")}}`
	result := c.runner.Run(ctx, command.Request{Name: "docker", Args: []string{"image", "inspect", "--format", format, image}, Timeout: 5 * time.Second, OutputLimit: 4096})
	if builderObjectMissing(result, "image", image) {
		return "", false, model.CommandResult{}
	}
	if builderCleanupFailed(result) {
		return "", false, cleanupError(result, "Temporary image ownership could not be observed reliably; removal was refused.")
	}
	var id string
	var owned bool
	if !decodeBuilderFields(result.Stdout, &id, &owned) || !imageIdentifier.MatchString(id) || !owned {
		return "", false, cleanupError(result, "The temporary image did not match this run's ownership; removal was refused.")
	}
	return id, true, model.CommandResult{}
}

// RemoveImage removes only a run-labelled image whose tag was absent before
// this invocation attempted its build. An exact not-found observation proves
// idempotent absence; arbitrary stderr text and exit zero do not.
func (c *Client) RemoveImage(ctx context.Context, image string) model.CommandResult {
	if !c.attemptedImages[image] {
		return model.CommandResult{}
	}
	id, exists, result := c.inspectOwnedImage(ctx, image)
	if builderCleanupFailed(result) || !exists {
		return result
	}
	result = c.runner.Run(ctx, command.Request{Name: "docker", Args: []string{"image", "rm", id}, Timeout: time.Minute, OutputLimit: 4096})
	if builderCleanupFailed(result) && !builderObjectMissing(result, "image", id) {
		return cleanupError(result, "The verified temporary image could not be removed.")
	}
	_, exists, result = c.inspectOwnedImage(ctx, image)
	if builderCleanupFailed(result) {
		return result
	}
	if exists {
		return cleanupError(result, "The temporary image remained after removal.")
	}
	_, exists, result = c.inspectOwnedImage(ctx, id)
	if builderCleanupFailed(result) {
		return result
	}
	if exists {
		return cleanupError(result, "The temporary image object remained after its tag was removed.")
	}
	return model.CommandResult{}
}

func (c *Client) ownedImageIDs(ctx context.Context) ([]string, model.CommandResult) {
	if len(c.ownershipToken) != 32 {
		return nil, cleanupError(model.CommandResult{}, "Image cleanup has no invocation ownership identity.")
	}
	result := c.runner.Run(ctx, command.Request{Name: "docker", Args: []string{"image", "ls", "--all", "--no-trunc", "--filter", "label=cloudforge.dev/ownership=" + c.ownershipToken, "--format", "{{.ID}}"}, Timeout: 15 * time.Second, OutputLimit: 32 * 1024})
	if builderCleanupFailed(result) || (result.Stdout != "" && !strings.HasSuffix(result.Stdout, "\n")) {
		return nil, cleanupError(result, "The owned image inventory was incomplete; absence could not be established.")
	}
	if result.Stdout == "" {
		return nil, model.CommandResult{}
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, id := range strings.Split(strings.TrimSuffix(result.Stdout, "\n"), "\n") {
		if !imageIdentifier.MatchString(id) {
			return nil, cleanupError(result, "The owned image inventory was malformed; absence could not be established.")
		}
		// Docker can list the same image once per tag; identity remains immutable.
		if !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	return ids, model.CommandResult{}
}

// RemoveOwnedImages also accounts for untagged image objects. The unpredictable
// ownership label selects this invocation; every immutable ID is inspected
// again before non-forced removal, and a final complete list must be empty.
func (c *Client) RemoveOwnedImages(ctx context.Context) model.CommandResult {
	ids, result := c.ownedImageIDs(ctx)
	if builderCleanupFailed(result) {
		return result
	}
	for _, id := range ids {
		observed, exists, result := c.inspectOwnedImage(ctx, id)
		if builderCleanupFailed(result) {
			return result
		}
		if !exists {
			continue
		}
		if observed != id {
			return cleanupError(result, "The image inventory and ownership inspection identities disagreed; removal was refused.")
		}
		removed := c.runner.Run(ctx, command.Request{Name: "docker", Args: []string{"image", "rm", id}, Timeout: time.Minute, OutputLimit: 4096})
		if builderCleanupFailed(removed) && !builderObjectMissing(removed, "image", id) {
			return cleanupError(removed, "A verified untagged temporary image could not be removed.")
		}
	}
	remaining, result := c.ownedImageIDs(ctx)
	if builderCleanupFailed(result) {
		return result
	}
	if len(remaining) != 0 {
		return cleanupError(result, "The final owned image inventory still contains temporary image objects.")
	}
	return model.CommandResult{}
}
