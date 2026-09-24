package k3d

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

const (
	imageIdentityLimit = 4096
	imageArchiveLimit  = int64(4 << 30)
	imageImportTimeout = 3 * time.Minute
)

// ImportCleanupWarning is safe diagnostic text; it contains no
// archive paths, image contents, node identity or invocation ownership token.
// #nosec G101 -- public diagnostic text, not a credential.
const ImportCleanupWarning = "Image-import staging cleanup was incomplete; the verification environment still requires cleanup."

var (
	imageIdentifier   = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	nodeIdentifier    = regexp.MustCompile(`^[a-f0-9]{64}$`)
	importClusterName = regexp.MustCompile(`^cloudforge-[a-f0-9]{8,20}$`)
	importOwnership   = regexp.MustCompile(`^[a-f0-9]{32}$`)
)

// ImportImage stages one bounded archive and imports it through ctr's local
// client, avoiding the transfer service and k3d's unreliable success wrapper.
// Every command targets this invocation's exact owned, immutable server ID.
func (c *Client) ImportImage(parent context.Context, cluster, image string) (result model.CommandResult) {
	ctx, cancel := context.WithTimeout(parent, imageImportTimeout)
	defer cancel()
	if stopped, canceled := importCanceled(ctx); canceled {
		return stopped
	}
	if !importClusterName.MatchString(cluster) || !importOwnership.MatchString(c.ownershipToken) || !filepath.IsAbs(c.workspace) {
		return importSetupError("Image import requires a generated cluster, invocation ownership and private workspace.")
	}
	workspace, err := os.Lstat(c.workspace)
	if err != nil || !workspace.IsDir() || workspace.Mode().Perm()&0o077 != 0 {
		return importSetupError("The private image-import workspace is unavailable.")
	}
	staging, err := os.MkdirTemp(c.workspace, "cloudforge-import-")
	if err != nil {
		return importSetupError("The private image-import archive could not be allocated.")
	}
	var duration int64
	defer func() {
		if err := os.RemoveAll(staging); err != nil {
			if !importFailed(result) {
				result = importObservationError(result, "The private image-import archive could not be removed.")
			}
			annotateImportCleanup(&result)
		}
		if !importFailed(result) {
			if stopped, canceled := importCanceled(parent); canceled {
				result = stopped
			}
		}
		result.DurationMS = duration
	}()
	run := func(request command.Request) model.CommandResult {
		if stopped, canceled := importCanceled(ctx); canceled {
			return stopped
		}
		observed := c.runner.Run(ctx, request)
		duration += observed.DurationMS
		return observed
	}

	source := run(command.Request{Name: "docker", Args: []string{"image", "inspect", "--format", "{{json .Id}}", image}, Timeout: 10 * time.Second, OutputLimit: imageIdentityLimit})
	if importFailed(source) {
		return source
	}
	expected, valid := decodeImageIdentity(source.Stdout, false)
	if source.Truncated || !valid {
		return importObservationError(source, "The source image identity could not be observed completely.")
	}
	node := run(command.Request{Name: "docker", Args: []string{"container", "inspect", "--format", c.importNodeTemplate(cluster), "k3d-" + cluster + "-server-0"}, Timeout: 10 * time.Second, OutputLimit: imageIdentityLimit})
	if importFailed(node) {
		return node
	}
	nodeID, valid := decodeImportNode(node.Stdout)
	if node.Truncated || !valid {
		return importObservationError(node, "The image-import node's exact identity and private ownership could not be confirmed.")
	}

	archive := filepath.Join(staging, "image.tar")
	saved := run(command.Request{Name: "docker", Args: []string{"image", "save", image}, Timeout: imageImportTimeout, OutputLimit: 128 * 1024,
		StdoutFile: &command.OutputFile{Path: archive, MaxBytes: imageArchiveLimit}})
	if importFailed(saved) {
		return saved
	}
	file, err := os.Lstat(archive)
	if saved.Truncated || err != nil || !file.Mode().IsRegular() || file.Mode().Perm() != 0o600 || file.Size() <= 0 || file.Size() > imageArchiveLimit {
		return importObservationError(saved, "A complete regular image archive within the 4 GiB import limit was not produced.")
	}
	nodeDirectory := "/tmp/" + filepath.Base(staging)
	nodeArchive := nodeDirectory + "/image.tar"
	created := run(command.Request{Name: "docker", Args: []string{"exec", nodeID, "mkdir", "-m", "700", nodeDirectory}, Timeout: 10 * time.Second, OutputLimit: imageIdentityLimit})
	if importFailed(created) || created.Truncated {
		if !importFailed(created) {
			created.FailureType = model.FailureExecution
		}
		return created
	}
	// mkdir without -p establishes an exclusive staging directory. If it did
	// not succeed, do not remove a path whose creation was never established.
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		for _, args := range [][]string{{"exec", nodeID, "rm", "-f", nodeArchive}, {"exec", nodeID, "rmdir", nodeDirectory}} {
			cleaned := c.runner.Run(cleanupCtx, command.Request{Name: "docker", Args: args, Timeout: 15 * time.Second, OutputLimit: imageIdentityLimit})
			duration += cleaned.DurationMS
			if importFailed(cleaned) || cleaned.Truncated {
				if !importFailed(result) {
					if !importFailed(cleaned) {
						cleaned.FailureType = model.FailureExecution
					}
					result = cleaned
				}
				annotateImportCleanup(&result)
			}
		}
	}()
	copied := run(command.Request{Name: "docker", Args: []string{"cp", archive, nodeID + ":" + nodeArchive}, Timeout: 45 * time.Second, OutputLimit: 128 * 1024})
	if importFailed(copied) || copied.Truncated {
		if !importFailed(copied) {
			copied.FailureType = model.FailureExecution
		}
		return copied
	}
	imported := run(command.Request{Name: "docker", Args: []string{"exec", nodeID, "ctr", "--address", "/run/k3s/containerd/containerd.sock", "--namespace", "k8s.io", "images", "import", "--local", "--all-platforms", nodeArchive}, Timeout: imageImportTimeout, OutputLimit: 128 * 1024})
	if importFailed(imported) || imported.Truncated {
		if !importFailed(imported) {
			imported.FailureType = model.FailureExecution
		}
		return imported
	}
	checked := run(command.Request{Name: "docker", Args: []string{"exec", nodeID, "crictl", "inspecti", "--quiet", "--output", "go-template", "--template", `{"id":{{printf "%q" .status.id}}}`, image}, Timeout: 15 * time.Second, OutputLimit: imageIdentityLimit})
	if importFailed(checked) {
		return checked
	}
	observed, valid := decodeImageIdentity(checked.Stdout, true)
	if checked.Truncated || !valid || observed != expected {
		return importObservationError(checked, "The imported image identity did not match the completely observed source image.")
	}
	if stopped, canceled := importCanceled(ctx); canceled {
		return stopped
	}
	return checked
}

func (c *Client) importNodeTemplate(cluster string) string {
	return `{"id":{{json .Id}},"name":{{json (eq .Name "/k3d-` + cluster + `-server-0")}},"owner":{{json (eq (index .Config.Labels "cloudforge.dev/ownership") "` + c.ownershipToken + `")}},"run":{{json (eq (index .Config.Labels "cloudforge.dev/run-id") "` + cluster + `")}},"cluster":{{json (eq (index .Config.Labels "k3d.cluster") "` + cluster + `")}},"role":{{json (eq (index .Config.Labels "k3d.role") "server")}},"running":{{json .State.Running}}}`
}

func decodeImportNode(output string) (string, bool) {
	if len(output) > imageIdentityLimit {
		return "", false
	}
	decoder := json.NewDecoder(strings.NewReader(output))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return "", false
	}
	seen := map[string]bool{}
	var id string
	for decoder.More() {
		key, err := decoder.Token()
		name, ok := key.(string)
		if err != nil || !ok || seen[name] {
			return "", false
		}
		seen[name] = true
		switch name {
		case "id":
			if decoder.Decode(&id) != nil || !nodeIdentifier.MatchString(id) {
				return "", false
			}
		case "name", "owner", "run", "cluster", "role", "running":
			var matches bool
			if decoder.Decode(&matches) != nil || !matches {
				return "", false
			}
		default:
			return "", false
		}
	}
	end, err := decoder.Token()
	var extra any
	return id, err == nil && end == json.Delim('}') && len(seen) == 7 && decoder.Decode(&extra) == io.EOF
}

// decodeImageIdentity accepts only one complete bounded JSON identity.
func decodeImageIdentity(output string, wrapped bool) (string, bool) {
	if len(output) > imageIdentityLimit {
		return "", false
	}
	decoder := json.NewDecoder(strings.NewReader(output))
	if wrapped {
		start, err := decoder.Token()
		if err != nil || start != json.Delim('{') {
			return "", false
		}
		key, err := decoder.Token()
		if err != nil || key != "id" {
			return "", false
		}
	}
	var id string
	if decoder.Decode(&id) != nil || !imageIdentifier.MatchString(id) {
		return "", false
	}
	if wrapped {
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return "", false
		}
	}
	var extra any
	return id, decoder.Decode(&extra) == io.EOF
}

func importObservationError(result model.CommandResult, message string) model.CommandResult {
	result.FailureType = model.FailureExecution
	result.Stdout, result.Stderr = "", message
	return result
}

func importSetupError(message string) model.CommandResult {
	return model.CommandResult{Command: "docker", ExitCode: -1, FailureType: model.FailureExecution, Stderr: message}
}

func annotateImportCleanup(result *model.CommandResult) {
	if strings.Contains(result.Stderr, ImportCleanupWarning) {
		return
	}
	if result.Stderr != "" {
		result.Stderr += "\n"
	}
	result.Stderr += ImportCleanupWarning
}

func importFailed(result model.CommandResult) bool {
	return result.ExitCode != 0 || result.FailureType != model.FailureNone
}

func importCanceled(ctx context.Context) (model.CommandResult, bool) {
	if ctx.Err() == nil {
		return model.CommandResult{}, false
	}
	failure := model.FailureCanceled
	if ctx.Err() == context.DeadlineExceeded {
		failure = model.FailureTimeout
	}
	return model.CommandResult{Command: "docker", ExitCode: -1, FailureType: failure}, true
}
