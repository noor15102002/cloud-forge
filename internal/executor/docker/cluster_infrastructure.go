package docker

import (
	"context"
	"strings"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

// ProvisionClusterResources gives the network and image volume an invocation
// marker before k3d can create its tokenless tools node. PrepareCluster must
// have completed its absence checks, and callers must already arm cleanup.
func (c *Client) ProvisionClusterResources(ctx context.Context, cluster string) model.CommandResult {
	if !builderRunName.MatchString(cluster) || c.clusterPrepared != cluster || c.clusterPrivateInfrastructure || len(c.ownershipToken) != 32 {
		return cleanupError(model.CommandResult{}, "Private cluster provisioning requires an unused, preflighted invocation identity.")
	}
	c.clusterPrivateInfrastructure = true
	for _, kind := range []string{"network", "volume"} {
		name := "k3d-" + cluster
		args := []string{kind, "create", "--driver", "bridge", "--opt", "com.docker.network.bridge.enable_ip_masquerade=true"}
		if kind == "volume" {
			name += "-images"
			args = []string{kind, "create", "--driver", "local"}
		}
		args = append(args, "--label", "app=k3d", "--label", "k3d.cluster="+cluster, "--label", "cloudforge.dev/ownership="+c.ownershipToken, name)
		c.clusterInfrastructureAttempts[kind] = true
		result := c.runner.Run(ctx, command.Request{Name: "docker", Args: args, Timeout: 30 * time.Second, OutputLimit: 4096})
		if builderCleanupFailed(result) {
			return cleanupError(result, "Private cluster infrastructure creation did not complete reliably.")
		}
		identity := strings.TrimSuffix(result.Stdout, "\n")
		if result.Stdout != identity+"\n" || kind == "network" && !resourceID.MatchString(identity) || kind == "volume" && identity != name {
			return cleanupError(result, "Private cluster infrastructure creation returned an incomplete identity.")
		}
		exists, observed := c.captureProvisionedClusterKind(ctx, cluster, kind)
		if builderCleanupFailed(observed) {
			return observed
		}
		if !exists || kind == "network" && c.clusterNetworkID != identity {
			return cleanupError(observed, "The created private cluster resource did not match its ownership observation.")
		}
	}
	return model.CommandResult{}
}

// A canceled or truncated create may have created its resource. Recapture only
// attempted kinds with this private marker; a same-name object is insufficient.
func (c *Client) captureProvisionedClusterResources(ctx context.Context, cluster string) model.CommandResult {
	for _, kind := range []string{"network", "volume"} {
		if !c.clusterInfrastructureAttempts[kind] {
			continue
		}
		if _, result := c.captureProvisionedClusterKind(ctx, cluster, kind); builderCleanupFailed(result) {
			return result
		}
	}
	return model.CommandResult{}
}

func (c *Client) captureProvisionedClusterKind(ctx context.Context, cluster, kind string) (bool, model.CommandResult) {
	name := "k3d-" + cluster
	if kind == "volume" {
		name += "-images"
	}
	resources, result := c.inventory(ctx, kind, name, false)
	if builderCleanupFailed(result) || len(resources) == 0 {
		return false, result
	}
	if len(resources) != 1 || resources[0].name != name {
		return false, cleanupError(result, "Private cluster infrastructure has an ambiguous name; removal was refused.")
	}
	return c.inspectProvisionedClusterResource(ctx, cluster, kind, resources[0])
}

func (c *Client) inspectProvisionedClusterResource(ctx context.Context, cluster, kind string, resource clusterResource) (bool, model.CommandResult) {
	if c.clusterPrepared != cluster || !c.clusterInfrastructureAttempts[kind] {
		return false, cleanupError(model.CommandResult{}, "This invocation did not attempt the private cluster resource; removal was refused.")
	}
	name, identity, createdField := "k3d-"+cluster, ".Id", `""`
	if kind == "volume" {
		name, identity, createdField = name+"-images", ".Name", ".CreatedAt"
	}
	format := `{{json ` + identity + `}} {{json .Name}} {{json (eq (index .Labels "cloudforge.dev/ownership") "` + c.ownershipToken + `")}} {{json (eq (index .Labels "app") "k3d")}} {{json (eq (index .Labels "k3d.cluster") "` + cluster + `")}} {{json ` + createdField + `}}`
	result := c.runner.Run(ctx, command.Request{Name: "docker", Args: []string{kind, "inspect", "--format", format, resource.id}, Timeout: 5 * time.Second, OutputLimit: 4096})
	if builderObjectMissing(result, kind, resource.id) {
		return false, model.CommandResult{}
	}
	if builderCleanupFailed(result) {
		return false, cleanupError(result, "Private cluster resource ownership could not be observed reliably; removal was refused.")
	}
	var id, observedName, created string
	var marker, app, clusterLabel bool
	if !decodeBuilderFields(result.Stdout, &id, &observedName, &marker, &app, &clusterLabel, &created) || id != resource.id || observedName != name || resource.name != name || !marker || !app || !clusterLabel {
		return false, cleanupError(result, "The cluster resource did not match its private invocation ownership; removal was refused.")
	}
	if kind == "volume" {
		if id != name {
			return false, cleanupError(result, "The private cluster volume name changed; removal was refused.")
		}
		if _, err := time.Parse(time.RFC3339Nano, created); err != nil {
			return false, cleanupError(result, "The private cluster volume creation identity was unavailable; removal was refused.")
		}
	} else if !resourceID.MatchString(id) || created != "" {
		return false, cleanupError(result, "The private cluster network identity was malformed; removal was refused.")
	}
	key, capturedIdentity := kind+":"+name, id+":"+created
	if prior, exists := c.clusterResources[key]; exists && prior != capturedIdentity {
		return false, cleanupError(result, "A private cluster resource changed after its ownership was captured; removal was refused.")
	}
	c.clusterResources[key] = capturedIdentity
	if kind == "network" {
		c.clusterNetworkID = id
	} else {
		c.clusterVolumeName = name
	}
	return true, model.CommandResult{}
}

func (c *Client) clusterContainerOwnershipFormat(prefix string) string {
	external := "false"
	format := ` {{json (eq (index .Config.Labels "cloudforge.dev/ownership") "` + c.ownershipToken + `")}} `
	if c.clusterPrivateInfrastructure {
		external = "true"
		format += `{{$created := eq .State.Status "created"}} `
	}
	format += `{{$networkID := ""}}{{with index .NetworkSettings.Networks "` + prefix + `"}}{{$networkID = .NetworkID}}`
	if c.clusterPrivateInfrastructure && resourceID.MatchString(c.clusterNetworkID) {
		// Docker records a configured endpoint before start but fills its ID at
		// start. Resolve only that explicit pending endpoint against the private
		// network whose immutable identity was captured and is revalidated.
		format += `{{if and $created (eq $networkID "")}}{{$networkID = "` + c.clusterNetworkID + `"}}{{end}}`
	}
	format += `{{end}}{{json $networkID}} {{$volume := ""}}{{range .Mounts}}{{if and (eq .Type "volume") (eq .Destination "/k3d/images")}}{{$volume = .Name}}{{end}}{{end}}{{json $volume}} {{json (and (eq (index .Config.Labels "k3d.cluster.network") "` + prefix + `") (eq (index .Config.Labels "k3d.cluster.network.id") $networkID) (eq (index .Config.Labels "k3d.cluster.network.external") "` + external + `") (eq (index .Config.Labels "k3d.cluster.imageVolume") "` + prefix + `-images"))}} {{json (eq (index .Config.Labels "k3d.role") "noRole")}}`
	return format
}
