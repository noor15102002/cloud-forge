package kubernetes

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

func TestObservePodsReturnsImageAndSortedIdentity(t *testing.T) {
	client := New(runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		return model.CommandResult{Command: request.Name, Arguments: request.Args, Stdout: `{"items":[{"metadata":{"name":"z"},"spec":{"containers":[{"image":"api:b"}]},"status":{"conditions":[{"type":"Ready","status":"True"}]}},{"metadata":{"name":"a"},"spec":{"containers":[{"image":"api:a"}]},"status":{"conditions":[{"type":"Ready","status":"False"}]}}]}`}
	}))
	pods, _, err := client.ObservePods(context.Background(), "test", "cloudforge", "app=api")
	if err != nil || len(pods) != 2 || pods[0].Name != "a" || pods[0].Image != "api:a" || pods[0].Ready || pods[1].Image != "api:b" || !pods[1].Ready {
		t.Fatalf("unexpected pod state: pods=%#v err=%v", pods, err)
	}
}

func TestSetImageUsesExplicitContextAndContainer(t *testing.T) {
	client := New(runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		return model.CommandResult{Command: request.Name, Arguments: request.Args}
	}))
	result := client.SetImage(context.Background(), "test", "cloudforge", "api", "application", "api:b")
	want := []string{"--context", "k3d-test", "--namespace", "cloudforge", "set", "image", "deployment/api", "application=api:b"}
	if len(result.Arguments) != len(want) {
		t.Fatalf("unexpected set image arguments: %#v", result.Arguments)
	}
	for index := range want {
		if result.Arguments[index] != want[index] {
			t.Fatalf("unexpected set image arguments: %#v", result.Arguments)
		}
	}
}
