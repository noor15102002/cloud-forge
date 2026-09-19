package docker

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

type runnerFunc func(context.Context, command.Request) model.CommandResult

func (f runnerFunc) Run(ctx context.Context, request command.Request) model.CommandResult {
	return f(ctx, request)
}

func TestPublishedPortReturnsLoopbackMapping(t *testing.T) {
	client := New(runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		return model.CommandResult{Command: request.Name, Arguments: request.Args, Stdout: "127.0.0.1:49152\n"}
	}))
	port, result, err := client.PublishedPort(context.Background(), "cloudforge-test", 30080)
	if err != nil || result.ExitCode != 0 || port != 49152 {
		t.Fatalf("unexpected published port result: port=%d result=%#v err=%v", port, result, err)
	}
	if len(result.Arguments) != 3 || result.Arguments[1] != "k3d-cloudforge-test-serverlb" || result.Arguments[2] != "30080/tcp" {
		t.Fatalf("unexpected docker port command: %#v", result.Arguments)
	}
}

func TestPublishedPortRejectsNonLoopbackMapping(t *testing.T) {
	client := New(runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		return model.CommandResult{Command: request.Name, Arguments: request.Args, Stdout: "0.0.0.0:49152\n"}
	}))
	if port, _, err := client.PublishedPort(context.Background(), "cloudforge-test", 30080); err == nil || port != 0 {
		t.Fatalf("expected a rejected non-loopback mapping, got port=%d err=%v", port, err)
	}
}

func TestBuildVersionPassesOnlyPublicVersionArgument(t *testing.T) {
	client := New(runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		return model.CommandResult{Command: request.Name, Arguments: request.Args}
	}))
	result := client.BuildVersion(context.Background(), "/tmp/application", "cloudforge/api:test-b", "b")
	want := []string{"build", "--build-arg", "CLOUDFORGE_VERSION=b", "--tag", "cloudforge/api:test-b", "."}
	if len(result.Arguments) != len(want) {
		t.Fatalf("unexpected build arguments: %#v", result.Arguments)
	}
	for index := range want {
		if result.Arguments[index] != want[index] {
			t.Fatalf("unexpected build arguments: %#v", result.Arguments)
		}
	}
}

func TestRemoveImageTreatsMissingImageAsAlreadyClean(t *testing.T) {
	client := New(runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		return model.CommandResult{
			Command: request.Name, Arguments: request.Args, ExitCode: 1,
			FailureType: model.FailureExit, Stderr: "Error response from daemon: No such image: cloudforge/api:test",
		}
	}))
	result := client.RemoveImage(context.Background(), "cloudforge/api:test")
	if result.ExitCode != 0 || result.FailureType != model.FailureNone {
		t.Fatalf("missing image cleanup was not idempotent: %#v", result)
	}
}

func TestRemnantCleanupRequiresOwnershipAndExactRunName(t *testing.T) {
	var removals []string
	client := New(runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		result := model.CommandResult{}
		if len(request.Args) > 1 && request.Args[1] == "ls" {
			result.Stdout = "123456abcdef k3d-cloudforge-0123abcd\nabcdef123456 k3d-cloudforge-0123abcd-other\nfedcba123456 k3d-cloudforge-ffffaaaa\n"
		}
		if len(request.Args) > 1 && request.Args[1] == "rm" {
			removals = append(removals, request.Args[len(request.Args)-1])
		}
		return result
	}))
	client.RemoveClusterRemnants(context.Background(), "cloudforge-0123abcd", "network")
	if len(removals) != 1 || removals[0] != "123456abcdef" {
		t.Fatalf("cleanup targeted unrelated resources: %#v", removals)
	}
}

func TestSelectedBuildPreservesArgumentBoundariesAndRevalidates(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "apps/my app"), 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "apps/my app/Containerfile")
	if err := os.WriteFile(file, []byte("FROM node:24-alpine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	selection := &model.BuildSelection{App: "apps/my app", Dockerfile: "apps/my app/Containerfile", Context: "."}
	calls := 0
	client := New(runnerFunc(func(_ context.Context, r command.Request) model.CommandResult {
		calls++
		if r.Dir != root || !slices.Contains(r.Args, "./apps/my app/Containerfile") || r.Args[len(r.Args)-1] != "./." {
			t.Fatalf("unsafe args: %#v", r)
		}
		return model.CommandResult{}
	}))
	if result := client.BuildSelected(context.Background(), root, "image:a", "a", selection); result.FailureType != model.FailureNone {
		t.Fatal(result)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), file); err != nil {
		t.Fatal(err)
	}
	if result := client.BuildSelected(context.Background(), root, "image:b", "b", selection); result.FailureType != model.FailureExecution || calls != 1 {
		t.Fatal("changed path reached Docker", result)
	}
}

func TestBuildStopsBeforeRunnerOnCanceledOrExpiredContext(t *testing.T) {
	client := New(runnerFunc(func(context.Context, command.Request) model.CommandResult {
		t.Fatal("canceled build executed")
		return model.CommandResult{}
	}))
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	for _, item := range []struct {
		ctx  context.Context
		want model.FailureType
	}{{canceled, model.FailureCanceled}, {expired, model.FailureTimeout}} {
		if result := client.BuildSelected(item.ctx, ".", "image:a", "a", nil); result.FailureType != item.want {
			t.Fatal(result)
		}
	}
}
