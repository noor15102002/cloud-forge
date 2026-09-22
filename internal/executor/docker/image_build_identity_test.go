package docker

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

// Docker image identity includes image configuration labels, even when the
// Dockerfile ignores build arguments. This fake models that contract and the
// daemon's refusal to delete a multiply-tagged image ID without force.
func TestSameSourceBuildsRemainIndependentlyRemovableWhenArgumentsAreIgnored(t *testing.T) {
	tags := map[string]string{}
	built := map[string]string{}
	removals := 0
	client := New(runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		args := request.Args
		if args[0] == "buildx" && args[1] == "build" {
			if request.Dir != "/selected/frozen/source" || args[len(args)-1] != "." {
				t.Fatal("build identity changed the selected source or context")
			}
			labels, image, version := []string{}, "", ""
			for index, arg := range args {
				switch arg {
				case "--label":
					labels = append(labels, args[index+1])
				case "--tag":
					image = args[index+1]
				case "--build-arg":
					version = strings.TrimPrefix(args[index+1], "CLOUDFORGE_VERSION=")
				}
			}
			if !strings.Contains(strings.Join(labels, "\n"), "cloudforge.dev/build-version="+version) {
				t.Fatal("image configuration lacks an explicit build version")
			}
			digest := sha256.Sum256([]byte("unchanged frozen source\n" + strings.Join(labels, "\n")))
			id := fmt.Sprintf("sha256:%x", digest)
			tags[image], built[version] = id, id
			return model.CommandResult{}
		}
		if args[0] != "image" {
			t.Fatalf("unexpected request: %+v", request)
		}
		switch args[1] {
		case "ls":
			return model.CommandResult{}
		case "inspect":
			reference := args[len(args)-1]
			if id, exists := tags[reference]; exists {
				return model.CommandResult{Stdout: jsonFields(id, true)}
			}
			for _, id := range tags {
				if id == reference {
					return model.CommandResult{Stdout: jsonFields(id, true)}
				}
			}
			return missingBuilderObject("image", reference)
		case "rm":
			if len(args) != 3 || !strings.HasPrefix(args[2], "sha256:") {
				t.Fatal("cleanup used a mutable tag or force")
			}
			aliases := []string{}
			for tag, id := range tags {
				if id == args[2] {
					aliases = append(aliases, tag)
				}
			}
			if len(aliases) > 1 {
				return model.CommandResult{ExitCode: 1, FailureType: model.FailureExit, Stderr: "conflict: image is referenced in multiple repositories"}
			}
			for _, tag := range aliases {
				delete(tags, tag)
			}
			removals++
			return model.CommandResult{}
		}
		t.Fatal("unexpected image operation")
		return model.CommandResult{}
	}))
	client.IsolateBuild(cleanupBuilderName)
	for _, version := range []string{"a", "b"} {
		if result := client.BuildVersion(context.Background(), "/selected/frozen/source", "cloudforge/api:0123abcd-"+version, version); builderCleanupFailed(result) {
			t.Fatal(result)
		}
	}
	if built["a"] == "" || built["a"] == built["b"] {
		t.Fatal("ignored Dockerfile argument caused A/B image identity to collapse")
	}
	for _, version := range []string{"a", "b"} {
		if result := client.RemoveImage(context.Background(), "cloudforge/api:0123abcd-"+version); builderCleanupFailed(result) {
			t.Fatalf("same-source image cleanup failed: %+v", result)
		}
	}
	if len(tags) != 0 || removals != 2 {
		t.Fatal("same-source image objects remained")
	}
}

func TestSharedImageAliasesStillRefuseForcedOrNameBasedRemoval(t *testing.T) {
	id := "sha256:" + strings.Repeat("a", 64)
	removals := 0
	client := New(runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		if request.Args[1] == "inspect" {
			return model.CommandResult{Stdout: jsonFields(id, true)}
		}
		if request.Args[1] == "rm" {
			if len(request.Args) != 3 || request.Args[2] != id {
				t.Fatal("alias conflict weakened immutable nonforced removal")
			}
			removals++
			return model.CommandResult{ExitCode: 1, FailureType: model.FailureExit, Stderr: "conflict: image is referenced in multiple repositories"}
		}
		t.Fatal("unexpected operation")
		return model.CommandResult{}
	}))
	client.IsolateBuild(cleanupBuilderName)
	client.attemptedImages = map[string]bool{"cloudforge/api:0123abcd-a": true}
	if result := client.RemoveImage(context.Background(), "cloudforge/api:0123abcd-a"); !builderCleanupFailed(result) || removals != 1 {
		t.Fatal("shared alias conflict did not remain an explicit cleanup error")
	}
}
