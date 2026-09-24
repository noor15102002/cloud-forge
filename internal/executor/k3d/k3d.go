// Package k3d adapts disposable k3d cluster lifecycle operations.
package k3d

import (
	"context"
	"encoding/json"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

// KubernetesVersion is the explicit server selected for disposable runs.
const KubernetesVersion = "1.35.5+k3s1"

// NodeImage fixes the runtime independently of a local k3d default.
const NodeImage = "rancher/k3s:v1.35.5-k3s1"

const imageIdentityLimit = 4096

var imageIdentifier = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
var importErrorLog = regexp.MustCompile(`(?m)(?:^|[[:space:]])level=(?:error|fatal|panic)(?:[[:space:]]|$)`)

// Client manages one-run k3d clusters.
type Client struct {
	runner         command.Runner
	ownershipToken string
}

// New creates a k3d CLI adapter.
func New(runner command.Runner) *Client { return &Client{runner: runner} }

// SetOwnership adds this invocation's private token to every created runtime node.
func (c *Client) SetOwnership(token string) { c.ownershipToken = token }

// Create creates a minimal cluster, publishes one NodePort on loopback, and waits for its API.
func (c *Client) Create(ctx context.Context, name string, nodePort int) model.CommandResult {
	return c.CreateWithMemory(ctx, name, nodePort, "4g")
}

// CreateWithMemory selects one of the fixed, qualified cluster memory budgets.
// Arbitrary Docker resource flags are never accepted through this adapter.
func (c *Client) CreateWithMemory(ctx context.Context, name string, nodePort int, memory string) model.CommandResult {
	// k3d 5.9 reserves room for node suffixes and limits cluster names to 32.
	if len(name) > 32 {
		return model.CommandResult{Command: "k3d", ExitCode: -1, FailureType: model.FailureExecution, Stderr: "Cluster name exceeds k3d's 32-character limit."}
	}
	if memory != "4g" && memory != "6g" {
		return model.CommandResult{Command: "k3d", ExitCode: -1, FailureType: model.FailureExecution, Stderr: "Unsupported bounded cluster memory profile."}
	}
	portMapping := "127.0.0.1:0:" + strconv.Itoa(nodePort) + "@server:0"
	args := []string{"cluster", "create", name, "--image", NodeImage, "--servers-memory", memory, "--kubeconfig-update-default=false", "--kubeconfig-switch-context=false", "--runtime-label", "cloudforge.dev/owned=true@all", "--runtime-label", "cloudforge.dev/run-id=" + name + "@all", "--servers", "1", "--agents", "0"}
	if c.ownershipToken != "" {
		args = append(args, "--runtime-label", "cloudforge.dev/ownership="+c.ownershipToken+"@all")
	}
	if nodePort > 0 {
		args = append(args, "--port", portMapping)
	}
	args = append(args, "--wait", "--timeout", "90s")
	return c.runner.Run(ctx, command.Request{
		Name: "k3d", Args: args,
		Timeout: 2 * time.Minute, OutputLimit: 128 * 1024,
	})
}

// ImportImage loads a local image into every node in a cluster.
func (c *Client) ImportImage(ctx context.Context, cluster, image string) model.CommandResult {
	if result, stopped := importCanceled(ctx); stopped {
		return result
	}
	// Observe only the source image's immutable config digest. Inspecting an
	// entire image could collect application environment or other private data.
	source := c.runner.Run(ctx, command.Request{
		Name: "docker", Args: []string{"image", "inspect", "--format", "{{json .Id}}", image},
		Timeout: 10 * time.Second, OutputLimit: imageIdentityLimit,
	})
	if source.ExitCode != 0 || source.FailureType != model.FailureNone {
		return source
	}
	expected, valid := decodeImageIdentity(source.Stdout, false)
	if source.Truncated || !valid {
		return importObservationError(source, "The source image identity could not be observed completely.")
	}
	if result, stopped := importCanceled(ctx); stopped {
		return result
	}
	imported := c.runner.Run(ctx, command.Request{
		Name: "k3d", Args: []string{"image", "import", image, "--cluster", cluster, "--mode", "tools-node"},
		Timeout: 3 * time.Minute, OutputLimit: 128 * 1024,
		// k3d 5.9's initLogging supports these environment variables. Its
		// default text formatter emits level=error/fatal/panic without color.
		// Pin info so an ambient panic-only threshold cannot hide errors.
		Env: []string{"LOG_LEVEL=info", "LOG_COLORS=false", "LOG_TIMESTAMPS="},
	})
	if imported.ExitCode != 0 || imported.FailureType != model.FailureNone {
		return imported
	}
	if imported.Truncated {
		imported.FailureType = model.FailureExecution
		return imported
	}
	if importErrorLog.MatchString(imported.Stdout) || importErrorLog.MatchString(imported.Stderr) {
		// The tools importer can log a failed import or cleanup and exit zero.
		// Preserve its original output while classifying the observation as an
		// execution error, even if the image happened to become available.
		imported.FailureType = model.FailureExecution
		return imported
	}
	if result, stopped := importCanceled(ctx); stopped {
		return result
	}
	// Avoid the direct importer's unsynchronized stdin pipe lifetime. The
	// tools-node path stages a file in the already owned cluster image volume;
	// neither its zero exit nor a pre-existing image tag proves correct import.
	// Ask CRI for only the config digest and require the exact Docker identity.
	checked := c.runner.Run(ctx, command.Request{
		Name: "docker", Args: []string{"exec", "k3d-" + cluster + "-server-0", "crictl", "inspecti", "--quiet", "--output", "go-template", "--template", `{"id":{{printf "%q" .status.id}}}`, image},
		Timeout: 15 * time.Second, OutputLimit: imageIdentityLimit,
	})
	checked.DurationMS += source.DurationMS + imported.DurationMS
	if checked.ExitCode != 0 || checked.FailureType != model.FailureNone {
		return checked
	}
	if result, stopped := importCanceled(ctx); stopped {
		return result
	}
	observed, valid := decodeImageIdentity(checked.Stdout, true)
	if checked.Truncated || !valid || observed != expected {
		return importObservationError(checked, "The imported image identity did not match the completely observed source image.")
	}
	return checked
}

// decodeImageIdentity accepts only one bounded JSON identity. The CRI template
// has exactly one field, so duplicate fields and extra objects fail closed.
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

func importCanceled(ctx context.Context) (model.CommandResult, bool) {
	if ctx.Err() == nil {
		return model.CommandResult{}, false
	}
	failure := model.FailureCanceled
	if ctx.Err() == context.DeadlineExceeded {
		failure = model.FailureTimeout
	}
	return model.CommandResult{Command: "k3d", ExitCode: -1, FailureType: failure}, true
}

// Delete removes a CloudForge-owned cluster.
func (c *Client) Delete(ctx context.Context, name string) model.CommandResult {
	return c.runner.Run(ctx, command.Request{
		Name: "k3d", Args: []string{"cluster", "delete", name},
		Timeout: 2 * time.Minute, OutputLimit: 128 * 1024,
	})
}

// Kubeconfig returns credentials for this run only; callers must not publish output.
func (c *Client) Kubeconfig(ctx context.Context, name string) model.CommandResult {
	return c.runner.Run(ctx, command.Request{Name: "k3d", Args: []string{"kubeconfig", "get", name}, Timeout: 15 * time.Second, OutputLimit: 128 * 1024})
}
