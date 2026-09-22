package docker

import (
	"context"
	"strings"
	"testing"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

type infrastructureRunner struct {
	base             *k3dContractRunner
	client           *Client
	failureKind      string
	failureMode      string
	failureTriggered bool
}

func provisionedFixture(t *testing.T) (*Client, *infrastructureRunner) {
	t.Helper()
	client, base := k3dContractFixture(t)
	for _, object := range base.objects {
		object.present = false
		if object.kind == "container" {
			object.data["State"] = map[string]string{"Status": "running"}
			if object.labels()["k3d.role"] != "noRole" {
				object.labels()["k3d.cluster.network.external"] = "true"
			}
		}
	}
	runner := &infrastructureRunner{base: base, client: client}
	client.runner = runner
	return client, runner
}

func (r *infrastructureRunner) Run(ctx context.Context, request command.Request) model.CommandResult {
	args := request.Args
	if len(args) > 1 && args[1] == "create" && (args[0] == "network" || args[0] == "volume") {
		suffix := ""
		if args[0] == "volume" {
			suffix = "-images"
		}
		object := r.base.object(suffix)
		if args[len(args)-1] != object.name || !strings.Contains(strings.Join(args, " "), "--label cloudforge.dev/ownership="+r.client.OwnershipToken()) {
			r.base.t.Fatal("provisioning omitted the private marker or changed the exact name")
		}
		result := model.CommandResult{Command: request.Name, Arguments: args, Stdout: object.id + "\n"}
		if r.failureKind == args[0] && r.failureMode == "before-create" {
			r.failureTriggered = true
			return model.CommandResult{ExitCode: 17, FailureType: model.FailureExit}
		}
		object.present = true
		object.labels()["app"] = "k3d"
		object.labels()["k3d.cluster"] = cleanupBuilderName
		if r.failureMode != "late-foreign-collision" {
			object.labels()["cloudforge.dev/ownership"] = r.client.OwnershipToken()
		}
		if r.failureKind == args[0] {
			switch r.failureMode {
			case "canceled":
				r.failureTriggered = true
				result.ExitCode, result.FailureType = -1, model.FailureCanceled
			case "truncated":
				r.failureTriggered = true
				result.Truncated = true
			case "unterminated":
				r.failureTriggered = true
				result.Stdout = object.id
			}
		}
		return result
	}
	result := r.base.Run(ctx, request)
	if len(args) > 1 && args[0] == r.failureKind && args[1] == "inspect" && r.failureMode == "first-inspect-malformed" && !r.failureTriggered {
		r.failureTriggered = true
		result.Stdout = "incomplete"
	}
	return result
}

func TestProvisionedClusterCleansEveryPartialCreationWindow(t *testing.T) {
	for _, phase := range []string{"infrastructure-only", "tools-only", "created-tools", "created-server", "running-nodes", "normal-k3d-deletion"} {
		t.Run(phase, func(t *testing.T) {
			client, runner := provisionedFixture(t)
			if result := client.ProvisionClusterResources(context.Background(), cleanupBuilderName); builderCleanupFailed(result) {
				t.Fatal("private infrastructure provisioning failed", result)
			}
			switch phase {
			case "tools-only", "created-tools", "created-server":
				suffix := "-tools"
				if phase == "created-server" {
					suffix = "-server-0"
				}
				object := runner.base.object(suffix)
				object.present = true
				if strings.HasPrefix(phase, "created-") {
					object.data["State"] = map[string]string{"Status": "created"}
					object.data["NetworkSettings"] = map[string]any{"Networks": map[string]any{"k3d-" + cleanupBuilderName: map[string]string{"NetworkID": ""}}}
				}
			case "running-nodes", "normal-k3d-deletion":
				for _, object := range runner.base.objects {
					object.present = true
				}
			}
			if phase == "normal-k3d-deletion" {
				client.ClusterCreated()
				if result := client.VerifyClusterOwnership(context.Background(), cleanupBuilderName); builderCleanupFailed(result) {
					t.Fatal("privately provisioned network was rejected as an external network", result)
				}
				// k3d removes nodes and its image volume, but preserves its
				// externally managed network for CloudForge to remove by ID.
				for _, object := range runner.base.objects {
					if object.kind != "network" {
						object.present = false
					}
				}
			}
			for attempt := range 2 {
				for _, kind := range []string{"container", "network", "volume"} {
					if result := client.RemoveClusterRemnants(context.Background(), cleanupBuilderName, kind); builderCleanupFailed(result) {
						t.Fatalf("cleanup %d %s failed: %+v", attempt, kind, result)
					}
				}
			}
			for _, object := range runner.base.objects {
				if object.present {
					t.Fatalf("owned partial-create resource remained: %s", object.name)
				}
			}
		})
	}
}

func TestProvisioningFailurePreservesErrorAndRecoversOnlyPrivateResources(t *testing.T) {
	for _, kind := range []string{"network", "volume"} {
		for _, mode := range []string{"before-create", "canceled", "truncated", "unterminated", "first-inspect-malformed"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				client, runner := provisionedFixture(t)
				runner.failureKind, runner.failureMode = kind, mode
				result := client.ProvisionClusterResources(context.Background(), cleanupBuilderName)
				if !runner.failureTriggered || !builderCleanupFailed(result) {
					t.Fatalf("failed provisioning was accepted: %+v", result)
				}
				for _, resourceKind := range []string{"container", "network", "volume"} {
					if cleanup := client.RemoveClusterRemnants(context.Background(), cleanupBuilderName, resourceKind); builderCleanupFailed(cleanup) {
						t.Fatalf("private cleanup after %s failed: %+v", mode, cleanup)
					}
				}
				if !builderCleanupFailed(result) {
					t.Fatal("successful fallback erased the original provisioning failure")
				}
				for _, object := range runner.base.objects {
					if object.present {
						t.Fatal("private resource remained after failed provisioning")
					}
				}
			})
		}
	}
}

func TestProvisionedPartialCleanupRefusesMissingOrChangedRelationships(t *testing.T) {
	cases := map[string]func(*infrastructureRunner){
		"missing endpoint": func(r *infrastructureRunner) {
			r.base.object("-tools").data["NetworkSettings"] = map[string]any{"Networks": map[string]any{}}
		},
		"running endpoint without identity": func(r *infrastructureRunner) {
			r.base.object("-tools").data["State"] = map[string]string{"Status": "running"}
		},
		"foreign mount": func(r *infrastructureRunner) {
			r.base.object("-tools").data["Mounts"] = []map[string]string{{"Type": "volume", "Name": "unrelated-volume", "Destination": "/k3d/images"}}
		},
		"foreign private network": func(r *infrastructureRunner) { r.base.object("").labels()["cloudforge.dev/ownership"] = "foreign" },
		"foreign private volume": func(r *infrastructureRunner) {
			r.base.object("-images").labels()["cloudforge.dev/ownership"] = "foreign"
		},
		"replaced network": func(r *infrastructureRunner) {
			network := r.base.object("")
			network.id = strings.Repeat("e", 64)
			network.data["Id"], network.data["ID"] = network.id, network.id
		},
		"replaced volume": func(r *infrastructureRunner) { r.base.object("-images").data["CreatedAt"] = "2026-09-22T12:00:01Z" },
		"missing network": func(r *infrastructureRunner) { r.base.object("").present = false },
		"missing volume":  func(r *infrastructureRunner) { r.base.object("-images").present = false },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			client, runner := provisionedFixture(t)
			if result := client.ProvisionClusterResources(context.Background(), cleanupBuilderName); builderCleanupFailed(result) {
				t.Fatal(result)
			}
			tools := runner.base.object("-tools")
			tools.present = true
			tools.data["State"] = map[string]string{"Status": "created"}
			tools.data["NetworkSettings"] = map[string]any{"Networks": map[string]any{"k3d-" + cleanupBuilderName: map[string]string{"NetworkID": ""}}}
			mutate(runner)
			if result := client.RemoveClusterRemnants(context.Background(), cleanupBuilderName, "container"); !builderCleanupFailed(result) || runner.base.removals != 0 || !tools.present {
				t.Fatalf("unproven relationship was deleted: %+v removals=%d", result, runner.base.removals)
			}
		})
	}
}

func TestProvisioningDoesNotAdoptLateSameNameResource(t *testing.T) {
	client, runner := provisionedFixture(t)
	runner.failureKind, runner.failureMode = "network", "late-foreign-collision"
	if result := client.ProvisionClusterResources(context.Background(), cleanupBuilderName); !builderCleanupFailed(result) {
		t.Fatal("same-name foreign network was adopted")
	}
	for _, kind := range []string{"container", "network", "volume"} {
		if result := client.RemoveClusterRemnants(context.Background(), cleanupBuilderName, kind); !builderCleanupFailed(result) {
			t.Fatal("foreign same-name collision was reported clean")
		}
	}
	if !runner.base.object("").present || runner.base.removals != 0 {
		t.Fatal("same-name foreign network was removed")
	}
}
