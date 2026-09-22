package docker

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

func TestCleanupInventoryRequiresCompleteObservation(t *testing.T) {
	for _, kind := range []string{"container", "network", "volume"} {
		for _, test := range []struct {
			name   string
			result model.CommandResult
		}{
			{"truncated", model.CommandResult{Truncated: true}},
			{"malformed", model.CommandResult{Stdout: "not a valid inventory record\n"}},
			{"incomplete_record", model.CommandResult{Stdout: "k3d-cloudforge-0123abcd-images"}},
			{"command_error", model.CommandResult{ExitCode: 1, FailureType: model.FailureExit}},
		} {
			t.Run(kind+"/"+test.name, func(t *testing.T) {
				calls := 0
				client := New(runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
					calls++
					if request.Args[1] != "ls" {
						t.Fatal("untrustworthy inventory reached removal")
					}
					return test.result
				}))
				result := client.RemoveClusterRemnants(context.Background(), cleanupBuilderName, kind)
				if !builderCleanupFailed(result) || result.ExitCode == 0 || result.FailureType == model.FailureNone || calls != 1 {
					t.Fatalf("incomplete inventory passed: result=%+v calls=%d", result, calls)
				}
			})
		}
	}
}

func TestClusterCleanupProvesAbsenceAndPreservesSentinels(t *testing.T) {
	for _, kind := range []string{"container", "network", "volume"} {
		for _, retained := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/retained=%t", kind, retained), func(t *testing.T) {
				id, name := strings.Repeat("a", 64), "k3d-"+cleanupBuilderName
				if kind == "container" {
					name += "-server-0"
				}
				if kind == "volume" {
					name += "-images"
					id = name
				}
				exists, removals := true, 0
				client := New(runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
					switch request.Args[1] {
					case "ls":
						output := strings.Repeat("b", 64) + " unrelated-sentinel\n"
						if kind == "volume" {
							output = "unrelated-sentinel\n"
						}
						if exists {
							if kind == "volume" {
								output += name + "\n"
							} else {
								output += id + " " + name + "\n"
							}
						}
						return model.CommandResult{Stdout: output}
					case "inspect":
						prefix := "k3d-" + cleanupBuilderName
						if request.Args[0] == "volume" {
							return model.CommandResult{Stdout: jsonFields(prefix+"-images", prefix+"-images", true, true, "2026-09-22T12:00:00Z")}
						}
						if request.Args[0] == "network" {
							return model.CommandResult{Stdout: jsonFields(strings.Repeat("a", 64), prefix, true)}
						}
						return model.CommandResult{Stdout: jsonFields(id, name, true, true, true, strings.Repeat("a", 64), prefix+"-images", true, false)}
					case "rm":
						if request.Args[len(request.Args)-1] != id {
							t.Fatal("unrelated resource targeted")
						}
						removals++
						exists = retained
						return model.CommandResult{}
					default:
						t.Fatalf("unexpected request: %+v", request)
						return model.CommandResult{}
					}
				}))
				client.clusterPrepared, client.clusterCreated, client.clusterProven = cleanupBuilderName, true, true
				client.clusterNetworkID, client.clusterVolumeName = strings.Repeat("a", 64), "k3d-"+cleanupBuilderName+"-images"
				result := client.RemoveClusterRemnants(context.Background(), cleanupBuilderName, kind)
				if builderCleanupFailed(result) != retained || removals != 1 {
					t.Fatalf("absence not established: result=%+v removals=%d", result, removals)
				}
				if !retained {
					result = client.RemoveClusterRemnants(context.Background(), cleanupBuilderName, kind)
					if builderCleanupFailed(result) || removals != 1 {
						t.Fatal("cleanup was not idempotent")
					}
				}
			})
		}
	}
}

func TestExistingResourcePreventsCreationOwnership(t *testing.T) {
	for _, kind := range []string{"container", "network", "volume", "image", "builder-container", "builder-volume"} {
		t.Run(kind, func(t *testing.T) {
			client := New(runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
				if request.Args[1] != "ls" {
					t.Fatal("collision preflight mutated resources")
				}
				actual := kind
				if strings.HasPrefix(kind, "builder-") {
					actual = strings.TrimPrefix(kind, "builder-")
				}
				if request.Args[0] != actual {
					return model.CommandResult{}
				}
				name := "k3d-" + cleanupBuilderName
				if kind == "container" {
					name += "-server-0"
				}
				if kind == "volume" {
					name += "-images"
				}
				if kind == "builder-container" {
					name = "buildx_buildkit_" + cleanupBuilderName + "0"
				}
				if kind == "builder-volume" {
					name = "buildx_buildkit_" + cleanupBuilderName + "0_state"
				}
				if actual == "image" {
					return model.CommandResult{Stdout: "sha256:" + strings.Repeat("a", 64) + " cloudforge/api:0123abcd-a\n"}
				}
				if actual == "volume" {
					return model.CommandResult{Stdout: name + "\n"}
				}
				return model.CommandResult{Stdout: strings.Repeat("a", 64) + " " + name + "\n"}
			}))
			client.IsolateBuild(cleanupBuilderName)
			var result model.CommandResult
			if kind == "image" || strings.HasPrefix(kind, "builder-") {
				result = client.PrepareBuild(context.Background(), "cloudforge/api:0123abcd-a")
			} else {
				result = client.PrepareCluster(context.Background(), cleanupBuilderName)
			}
			if !builderCleanupFailed(result) {
				t.Fatal("same-name resource was adopted")
			}
		})
	}
}

func TestClusterForeignOwnershipAndReplacementRefuseRemoval(t *testing.T) {
	for _, kind := range []string{"container", "network", "volume"} {
		t.Run(kind, func(t *testing.T) {
			name := "k3d-" + cleanupBuilderName
			if kind == "container" {
				name += "-server-0"
			}
			if kind == "volume" {
				name += "-images"
			}
			id := strings.Repeat("a", 64)
			if kind == "volume" {
				id = name
			}
			client := New(runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
				if request.Args[1] == "rm" {
					t.Fatal("foreign ownership reached removal")
				}
				if request.Args[1] == "ls" {
					if kind == "volume" {
						return model.CommandResult{Stdout: name + "\n"}
					}
					return model.CommandResult{Stdout: id + " " + name + "\n"}
				}
				values := []any{id, name, true}
				if kind != "network" {
					values = append(values, true)
				}
				if kind == "container" {
					values = append(values, false, strings.Repeat("a", 64), "k3d-"+cleanupBuilderName+"-images", true, false)
				}
				if kind == "volume" {
					values = append(values, "2026-09-22T12:00:00Z")
				}
				return model.CommandResult{Stdout: jsonFields(values...)}
			}))
			client.clusterPrepared = cleanupBuilderName
			// A network/volume without a proven current container is unowned even
			// when its labels and familiar name match a failed create attempt.
			if result := client.RemoveClusterRemnants(context.Background(), cleanupBuilderName, kind); !builderCleanupFailed(result) {
				t.Fatal("foreign resource adopted")
			}
		})
	}
}

func TestImageCleanupOwnershipAndFinalAbsence(t *testing.T) {
	for _, mode := range []string{"absent", "removed", "retained", "foreign", "truncated", "malformed", "nonzero", "false_missing_text"} {
		t.Run(mode, func(t *testing.T) {
			name := "cloudforge/api:0123abcd-a"
			removed := false
			removals := 0
			client := New(runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
				if request.Args[1] == "rm" {
					removals++
					removed = mode != "retained"
					if mode == "nonzero" {
						return model.CommandResult{ExitCode: 1, FailureType: model.FailureExit}
					}
					return model.CommandResult{}
				}
				if mode == "absent" || removed {
					return missingBuilderObject("image", request.Args[len(request.Args)-1])
				}
				if mode == "false_missing_text" {
					return model.CommandResult{ExitCode: 2, FailureType: model.FailureExit, Stderr: "daemon unavailable; no such image text is not absence"}
				}
				if mode == "truncated" {
					return model.CommandResult{Truncated: true}
				}
				if mode == "malformed" {
					return model.CommandResult{Stdout: "{}"}
				}
				return model.CommandResult{Stdout: jsonFields("sha256:"+strings.Repeat("a", 64), mode != "foreign")}
			}))
			client.IsolateBuild(cleanupBuilderName)
			client.attemptedImages = map[string]bool{name: true}
			result := client.RemoveImage(context.Background(), name)
			wantError := mode != "absent" && mode != "removed"
			if builderCleanupFailed(result) != wantError {
				t.Fatalf("mode=%s result=%+v", mode, result)
			}
			if (mode == "foreign" || mode == "truncated" || mode == "malformed" || mode == "false_missing_text" || mode == "absent") && removals != 0 {
				t.Fatal("image removed without reliable ownership")
			}
			if mode == "removed" {
				if result := client.RemoveImage(context.Background(), name); builderCleanupFailed(result) || removals != 1 {
					t.Fatal("image cleanup not idempotent")
				}
			}
		})
	}
}

func TestPrivateOwnershipIdentityIsIndependentOfPublicRunName(t *testing.T) {
	client := New(nil)
	client.IsolateBuild(cleanupBuilderName)
	first := client.OwnershipToken()
	client.IsolateBuild(cleanupBuilderName)
	second := client.OwnershipToken()
	if len(first) != 32 || len(second) != 32 || first == second || first == cleanupBuilderName {
		t.Fatal("a reused public name reused private ownership proof")
	}
}

func TestClusterLabelCannotAuthorizeDeletionOutsideItsName(t *testing.T) {
	client := New(runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		if request.Args[1] != "ls" {
			t.Fatal("unexpected cluster-labelled resource reached inspect/delete")
		}
		if strings.Contains(strings.Join(request.Args, " "), "label=k3d.cluster=") {
			return model.CommandResult{Stdout: strings.Repeat("a", 64) + " unrelated-sentinel\n"}
		}
		return model.CommandResult{}
	}))
	client.clusterPrepared, client.clusterCreated = cleanupBuilderName, true
	if result := client.VerifyClusterOwnership(context.Background(), cleanupBuilderName); !builderCleanupFailed(result) {
		t.Fatal("foreign name was accepted for broad k3d deletion")
	}
}

func TestOwnedImageSweepRequiresFinalCompleteAbsence(t *testing.T) {
	for _, mode := range []string{"removed", "retained", "truncated", "malformed", "foreign"} {
		t.Run(mode, func(t *testing.T) {
			id := "sha256:" + strings.Repeat("a", 64)
			exists, removals := true, 0
			client := New(runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
				switch request.Args[1] {
				case "ls":
					if mode == "truncated" {
						return model.CommandResult{Truncated: true, Stdout: id + "\n"}
					}
					if mode == "malformed" {
						return model.CommandResult{Stdout: "{}\n"}
					}
					if exists {
						return model.CommandResult{Stdout: id + "\n" + id + "\n"}
					}
					return model.CommandResult{}
				case "inspect":
					return model.CommandResult{Stdout: jsonFields(id, mode != "foreign")}
				case "rm":
					if len(request.Args) != 3 || request.Args[2] != id {
						t.Fatal("image sweep used names, force, or prune")
					}
					removals++
					exists = mode == "retained"
					return model.CommandResult{}
				default:
					t.Fatal("unexpected image sweep operation")
					return model.CommandResult{}
				}
			}))
			client.IsolateBuild(cleanupBuilderName)
			result := client.RemoveOwnedImages(context.Background())
			if builderCleanupFailed(result) != (mode != "removed") {
				t.Fatalf("mode=%s result=%+v", mode, result)
			}
			if mode == "removed" && removals != 1 {
				t.Fatal("untagged ID was not removed exactly once")
			}
			if (mode == "truncated" || mode == "malformed" || mode == "foreign") && removals != 0 {
				t.Fatal("untrusted image observation authorized removal")
			}
		})
	}
}

func TestImageObjectMustDisappearAsWellAsItsTag(t *testing.T) {
	name := "cloudforge/api:0123abcd-a"
	id := "sha256:" + strings.Repeat("a", 64)
	tagRemoved := false
	client := New(runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		if request.Args[1] == "rm" {
			tagRemoved = true
			return model.CommandResult{}
		}
		if tagRemoved && request.Args[len(request.Args)-1] == name {
			return missingBuilderObject("image", name)
		}
		return model.CommandResult{Stdout: jsonFields(id, true)}
	}))
	client.IsolateBuild(cleanupBuilderName)
	client.attemptedImages = map[string]bool{name: true}
	if result := client.RemoveImage(context.Background(), name); !builderCleanupFailed(result) {
		t.Fatal("tag absence hid retained image object")
	}
}

func TestClusterCapturedIdentityReplacementRefusesRemoval(t *testing.T) {
	for _, kind := range []string{"network", "volume"} {
		t.Run(kind, func(t *testing.T) {
			name := "k3d-" + cleanupBuilderName
			id := strings.Repeat("b", 64)
			prior := strings.Repeat("a", 64) + ":"
			if kind == "volume" {
				name += "-images"
				id = name
				prior = id + ":2026-09-21T12:00:00Z"
			}
			client := New(runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
				if request.Args[1] == "rm" {
					t.Fatal("replacement identity reached removal")
				}
				if request.Args[1] == "ls" {
					if kind == "volume" {
						return model.CommandResult{Stdout: name + "\n"}
					}
					return model.CommandResult{Stdout: id + " " + name + "\n"}
				}
				values := []any{id, name, true}
				if kind != "network" {
					values = append(values, true)
				}
				if kind == "volume" {
					values = append(values, "2026-09-22T12:00:00Z")
				}
				return model.CommandResult{Stdout: jsonFields(values...)}
			}))
			client.clusterPrepared, client.clusterProven = cleanupBuilderName, true
			client.clusterNetworkID, client.clusterVolumeName = strings.Repeat("a", 64), "k3d-"+cleanupBuilderName+"-images"
			client.clusterResources = map[string]string{kind + ":" + name: prior}
			if result := client.RemoveClusterRemnants(context.Background(), cleanupBuilderName, kind); !builderCleanupFailed(result) {
				t.Fatal("changed creation identity was adopted")
			}
		})
	}
}
