package docker

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

var resourceID = regexp.MustCompile(`^[a-f0-9]{64}$`)
var ownedName = regexp.MustCompile(`^k3d-cloudforge-[a-f0-9]+(?:-[a-zA-Z0-9_.-]+)?$`)
var dockerName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

type clusterResource struct{ id, name string }

func cleanupError(result model.CommandResult, reason string) model.CommandResult {
	return builderCleanupError(result, reason)
}

func (c *Client) inventory(ctx context.Context, kind, prefix string, owned bool) ([]clusterResource, model.CommandResult) {
	args := []string{kind, "ls"}
	if kind == "container" {
		args = append(args, "--all")
	}
	if kind != "volume" {
		args = append(args, "--no-trunc")
	}
	if owned {
		args = append(args, "--filter", "label=k3d.cluster="+strings.TrimPrefix(prefix, "k3d-"))
	} else {
		args = append(args, "--filter", "name="+prefix)
	}
	format := "{{.ID}} {{.Name}}"
	if kind == "container" {
		format = "{{.ID}} {{.Names}}"
	} else if kind == "volume" {
		format = "{{.Name}}"
	} else if kind != "network" {
		return nil, cleanupError(model.CommandResult{}, "The cleanup resource class is unsupported.")
	}
	args = append(args, "--format", format)
	result := c.runner.Run(ctx, command.Request{Name: "docker", Args: args, Timeout: 30 * time.Second, OutputLimit: 32 * 1024})
	if builderCleanupFailed(result) {
		return nil, cleanupError(result, "The Docker resource inventory was incomplete or unavailable; absence could not be established.")
	}
	if result.Stdout != "" && !strings.HasSuffix(result.Stdout, "\n") {
		return nil, cleanupError(result, "The Docker inventory ended before its record terminator; absence could not be established.")
	}
	var resources []clusterResource
	seen := map[string]bool{}
	if result.Stdout == "" {
		return nil, model.CommandResult{}
	}
	for _, line := range strings.Split(strings.TrimSuffix(result.Stdout, "\n"), "\n") {
		fields := strings.Fields(line)
		count := 2
		if kind == "volume" {
			count = 1
		}
		if len(fields) != count || !dockerName.MatchString(fields[count-1]) || (kind != "volume" && !resourceID.MatchString(fields[0])) || seen[fields[0]] {
			return nil, cleanupError(result, "The Docker resource inventory was malformed; absence could not be established.")
		}
		seen[fields[0]] = true
		name := fields[count-1]
		// Docker's name filter is a substring filter; exact scope is checked here.
		if name != prefix && !strings.HasPrefix(name, prefix+"-") {
			if owned {
				return nil, cleanupError(result, "A cluster-labelled resource has an unexpected name; broad deletion was refused.")
			}
			continue
		}
		if kind == "network" && name != prefix {
			if owned {
				return nil, cleanupError(result, "A cluster-labelled network has an unexpected name; broad deletion was refused.")
			}
			continue
		}
		resources = append(resources, clusterResource{id: fields[0], name: name})
	}
	return resources, model.CommandResult{}
}

// PrepareCluster refuses to arm cleanup when any same-name runtime object
// existed before creation. A k3d label or familiar name alone is not ownership.
func (c *Client) PrepareCluster(ctx context.Context, cluster string) model.CommandResult {
	if !builderRunName.MatchString(cluster) {
		return cleanupError(model.CommandResult{}, "The cluster run identity is invalid.")
	}
	for _, kind := range []string{"container", "network", "volume"} {
		resources, result := c.inventory(ctx, kind, "k3d-"+cluster, false)
		if builderCleanupFailed(result) {
			return result
		}
		if len(resources) > 0 {
			return cleanupError(result, "A same-name cluster resource already exists; creation and cleanup were refused.")
		}
	}
	c.clusterPrepared = cluster
	c.clusterCreated = false
	c.clusterProven = false
	c.clusterNetworkID = ""
	c.clusterVolumeName = ""
	c.clusterResources = map[string]string{}
	c.clusterPrivateInfrastructure = false
	c.clusterInfrastructureAttempts = map[string]bool{}
	return model.CommandResult{}
}

// ClusterCreated records a successful create after the absence preflight.
func (c *Client) ClusterCreated() { c.clusterCreated = true }

// k3d v5.9.0 networks carry only app=k3d; its transient tools node does
// not inherit user runtime labels. Their ownership comes from the actual
// network/mount relations of this invocation's privately marked server nodes.
func (c *Client) inspectClusterResource(ctx context.Context, cluster, kind string, resource clusterResource) (bool, model.CommandResult) {
	if c.clusterPrepared != cluster {
		return false, cleanupError(model.CommandResult{}, "No prior absence check exists for this cluster; removal was refused.")
	}
	if c.clusterPrivateInfrastructure && (kind == "network" || kind == "volume") {
		return c.inspectProvisionedClusterResource(ctx, cluster, kind, resource)
	}
	prefix := "k3d-" + cluster
	identity, labels := ".Id", ".Labels"
	if kind == "volume" {
		identity = ".Name"
	}
	if kind == "container" {
		labels = ".Config.Labels"
	}
	format := `{{json ` + identity + `}} {{json .Name}} {{json (eq (index ` + labels + ` "app") "k3d")}}`
	if kind != "network" {
		format += ` {{json (eq (index ` + labels + ` "k3d.cluster") "` + cluster + `")}}`
	}
	switch kind {
	case "container":
		format += c.clusterContainerOwnershipFormat(prefix)
	case "volume":
		format += ` {{json .CreatedAt}}`
	}
	result := c.runner.Run(ctx, command.Request{Name: "docker", Args: []string{kind, "inspect", "--format", format, resource.id}, Timeout: 5 * time.Second, OutputLimit: 4096})
	if builderObjectMissing(result, kind, resource.id) {
		return false, model.CommandResult{}
	}
	if builderCleanupFailed(result) {
		return false, cleanupError(result, "Cluster resource ownership could not be observed reliably; removal was refused.")
	}
	var id, name, created, networkID, volume string
	var app, clusterLabel, runMarker, references, toolsRole bool
	values := []any{&id, &name, &app}
	if kind != "network" {
		values = append(values, &clusterLabel)
	}
	switch kind {
	case "container":
		values = append(values, &runMarker, &networkID, &volume, &references, &toolsRole)
	case "volume":
		values = append(values, &created)
	}
	if !decodeBuilderFields(result.Stdout, values...) || id != resource.id || strings.TrimPrefix(name, "/") != resource.name || !app || (kind != "network" && !clusterLabel) {
		return false, cleanupError(result, "Cluster resource identity did not match the observation; removal was refused.")
	}
	switch kind {
	case "container":
		if reason := c.captureClusterRelations(prefix, resource.name, runMarker, networkID, volume, references, toolsRole); reason != "" {
			return false, cleanupError(result, reason)
		}
	case "network":
		if c.clusterNetworkID == "" || id != c.clusterNetworkID || name != prefix {
			return false, cleanupError(result, "The network did not match a privately owned node's captured attachment; removal was refused.")
		}
	case "volume":
		if c.clusterVolumeName == "" || name != c.clusterVolumeName {
			return false, cleanupError(result, "The volume did not match a privately owned node's captured mount; removal was refused.")
		}
		if _, err := time.Parse(time.RFC3339Nano, created); err != nil {
			return false, cleanupError(result, "The cluster volume creation identity was unavailable; removal was refused.")
		}
	}
	key, identity := kind+":"+resource.name, id+":"+created
	if prior, captured := c.clusterResources[key]; captured && prior != identity {
		return false, cleanupError(result, "A cluster resource changed after its ownership was captured; removal was refused.")
	}
	if c.clusterResources == nil {
		c.clusterResources = map[string]string{}
	}
	c.clusterResources[key] = identity
	return true, model.CommandResult{}
}

func (c *Client) captureClusterRelations(prefix, name string, marker bool, networkID, volume string, references, toolsRole bool) string {
	if name == prefix+"-tools" {
		if !toolsRole || c.clusterNetworkID == "" || networkID != c.clusterNetworkID || c.clusterVolumeName == "" || volume != c.clusterVolumeName {
			return "The tools container did not share the captured network and volume of a privately owned node; removal was refused."
		}
		return ""
	}
	if name != prefix+"-server-0" && name != prefix+"-serverlb" {
		return "The cluster container was not part of the configured node topology; removal was refused."
	}
	if !marker || !references || !resourceID.MatchString(networkID) || (name == prefix+"-server-0" && volume != prefix+"-images") || (volume != "" && volume != prefix+"-images") {
		return "The node's invocation marker or deletion references did not match its actual attachments; removal was refused."
	}
	if c.clusterNetworkID != "" && c.clusterNetworkID != networkID {
		return "The node network changed after its ownership was established; removal was refused."
	}
	c.clusterNetworkID = networkID
	if volume != "" {
		c.clusterVolumeName = volume
	}
	c.clusterProven = true
	return ""
}

func (c *Client) verifyClusterAttachments(ctx context.Context, cluster string) model.CommandResult {
	attachments := map[string]clusterResource{}
	if c.clusterNetworkID != "" {
		attachments["network"] = clusterResource{id: c.clusterNetworkID, name: "k3d-" + cluster}
	}
	if c.clusterVolumeName != "" {
		attachments["volume"] = clusterResource{id: c.clusterVolumeName, name: c.clusterVolumeName}
	}
	for _, kind := range []string{"network", "volume"} {
		resource, needed := attachments[kind]
		if !needed {
			continue
		}
		exists, result := c.inspectClusterResource(ctx, cluster, kind, resource)
		if builderCleanupFailed(result) {
			return result
		}
		if !exists {
			return cleanupError(result, "A privately owned node's captured network or mounted volume could not be observed; node removal was refused.")
		}
	}
	return model.CommandResult{}
}

func orderClusterContainers(resources []clusterResource, cluster string) {
	prefix := "k3d-" + cluster
	priority := func(name string) int {
		if name == prefix+"-server-0" {
			return 0
		}
		if name == prefix+"-serverlb" {
			return 1
		}
		return 2
	}
	sort.SliceStable(resources, func(i, j int) bool { return priority(resources[i].name) < priority(resources[j].name) })
}

// VerifyClusterOwnership guards k3d's name-based deletion. Failed creation never
// authorizes that broad operation; individually proven remnants can still be
// handled by RemoveClusterRemnants.
func (c *Client) VerifyClusterOwnership(ctx context.Context, cluster string) model.CommandResult {
	if c.clusterPrepared != cluster || !c.clusterCreated {
		return cleanupError(model.CommandResult{}, "This invocation did not establish cluster creation ownership.")
	}
	for _, kind := range []string{"container", "network", "volume"} {
		for _, labelled := range []bool{false, true} {
			resources, result := c.inventory(ctx, kind, "k3d-"+cluster, labelled)
			if builderCleanupFailed(result) {
				return result
			}
			if kind == "container" {
				orderClusterContainers(resources, cluster)
			}
			for _, resource := range resources {
				if _, result := c.inspectClusterResource(ctx, cluster, kind, resource); builderCleanupFailed(result) {
					return result
				}
			}
		}
	}
	return c.verifyClusterAttachments(ctx, cluster)
}

// RemoveClusterRemnants checks a complete inventory before removing only
// verified resources, then independently establishes their absence.
func (c *Client) RemoveClusterRemnants(ctx context.Context, cluster, kind string) model.CommandResult {
	if c.clusterPrivateInfrastructure {
		if result := c.captureProvisionedClusterResources(ctx, cluster); builderCleanupFailed(result) {
			return result
		}
	}
	prefix := "k3d-" + cluster
	resources, result := c.inventory(ctx, kind, prefix, false)
	if builderCleanupFailed(result) {
		return result
	}
	if kind == "container" {
		orderClusterContainers(resources, cluster)
	}
	present := []clusterResource{}
	for _, resource := range resources {
		if !ownedName.MatchString(resource.name) || (kind == "network" && resource.name != prefix) {
			return cleanupError(result, "An unexpected same-name cluster resource remains; removal was refused.")
		}
		exists, observed := c.inspectClusterResource(ctx, cluster, kind, resource)
		if builderCleanupFailed(observed) {
			return observed
		}
		if exists {
			present = append(present, resource)
		}
	}
	if kind == "container" && len(present) > 0 {
		if observed := c.verifyClusterAttachments(ctx, cluster); builderCleanupFailed(observed) {
			return observed
		}
	}
	for _, resource := range present {
		remove := []string{kind, "rm"}
		if kind == "container" {
			remove = append(remove, "--force")
		}
		remove = append(remove, resource.id)
		removed := c.runner.Run(ctx, command.Request{Name: "docker", Args: remove, Timeout: 30 * time.Second, OutputLimit: 32 * 1024})
		if builderCleanupFailed(removed) && !builderObjectMissing(removed, kind, resource.id) {
			return cleanupError(removed, "A verified cluster resource could not be removed.")
		}
	}
	remaining, result := c.inventory(ctx, kind, prefix, false)
	if builderCleanupFailed(result) {
		return result
	}
	if len(remaining) != 0 {
		return cleanupError(result, fmt.Sprintf("The final %s inventory still contains cluster resources.", kind))
	}
	return model.CommandResult{}
}
