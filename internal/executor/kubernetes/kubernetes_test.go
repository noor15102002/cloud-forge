package kubernetes

import (
	"context"
	"strings"
	"testing"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

type runnerFunc func(context.Context, command.Request) model.CommandResult

func (f runnerFunc) Run(ctx context.Context, request command.Request) model.CommandResult {
	return f(ctx, request)
}

func TestStartupDiagnosticsOmitMessagesAndCustomReasons(t *testing.T) {
	client := New(runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		if strings.Contains(strings.Join(request.Args, " "), "get nodes") {
			return model.CommandResult{Stdout: `{"items":[{"status":{"conditions":[{"type":"DiskPressure","status":"True","message":"secret"},{"type":"MemoryPressure","status":"False"},{"type":"Ready","status":"True"},{"type":"secret","status":"True"}]}}]}`}
		}
		return model.CommandResult{Stdout: `{"items":[{"metadata":{"name":"api"},"status":{"phase":"Pending","containerStatuses":[{"state":{"waiting":{"reason":"ErrImageNeverPull","message":"secret"}}}]}},{"metadata":{"name":"custom"},"status":{"containerStatuses":[{"state":{"waiting":{"reason":"secret","message":"secret"}}}]}}]}`}
	}))
	pods, _, err := client.ObservePods(context.Background(), "test", "cloudforge", "app=api")
	if err != nil || len(pods) != 2 || pods[0].Reason != "image_unavailable" || pods[1].Reason != "unknown" {
		t.Fatalf("unexpected safe pod reasons: %#v, %v", pods, err)
	}
	problems, _, err := client.NodeProblems(context.Background(), "test")
	if err != nil || len(problems) != 1 || problems[0] != "node_disk_pressure" {
		t.Fatalf("unexpected safe node reasons: %#v, %v", problems, err)
	}
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

func TestObserveHPAReportsCPUAndReplicaState(t *testing.T) {
	client := New(runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		return model.CommandResult{Command: request.Name, Arguments: request.Args, Stdout: `{"status":{"currentReplicas":2,"desiredReplicas":4,"currentMetrics":[{"type":"Resource","resource":{"name":"cpu","current":{"averageUtilization":91,"averageValue":"91m"}}}],"conditions":[{"type":"ScalingActive","status":"True"}]}}`}
	}))
	state, _, err := client.ObserveHPA(context.Background(), "test", "cloudforge", "api")
	if err != nil || !state.MetricsReady || state.CurrentCPU == nil || *state.CurrentCPU != 91 || state.CurrentReplicas != 2 || state.DesiredReplicas != 4 {
		t.Fatalf("unexpected HPA state: state=%#v err=%v", state, err)
	}
}
