package docker

import (
	"context"
	"testing"

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
