package kubernetes

import (
	"context"
	"testing"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

func TestReleasePodListRequiresUsableObservation(t *testing.T) {
	for _, data := range []string{`null`, `{}`, `{"items":null}`, `{"items":[null]}`, `{"items":[{}]}`, `{"items":[],"items":[]}`, `{"items":[]} {}`} {
		client := New(runnerFunc(func(context.Context, command.Request) model.CommandResult { return model.CommandResult{Stdout: data} }))
		pods, _, err := client.ObservePods(context.Background(), "test", "cloudforge", "app=api")
		if err == nil || pods != nil {
			t.Fatalf("unusable list became observed pods: %s %+v", data, pods)
		}
	}
	client := New(runnerFunc(func(context.Context, command.Request) model.CommandResult {
		return model.CommandResult{Stdout: `{"items":[]}`}
	}))
	if pods, _, err := client.ObservePods(context.Background(), "test", "cloudforge", "app=api"); err != nil || pods == nil || len(pods) != 0 {
		t.Fatalf("explicit empty observation rejected: %v", err)
	}
}

func TestReleaseNodeListRequiresReadyObservation(t *testing.T) {
	for _, data := range []string{`null`, `{}`, `{"items":null}`, `{"items":[]}`, `{"items":[{}]}`, `{"items":[{"status":{"conditions":[]}}]}`, `{"items":[],"items":[]}`} {
		client := New(runnerFunc(func(context.Context, command.Request) model.CommandResult { return model.CommandResult{Stdout: data} }))
		if _, _, err := client.NodeProblems(context.Background(), "test"); err == nil {
			t.Fatalf("unknown node health accepted: %s", data)
		}
	}
}
