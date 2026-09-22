package kubernetes

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

func TestObservePodsRetainsSeparateSafeTerminationSlots(t *testing.T) {
	calls := 0
	raw := `{"items":[{"metadata":{"name":"z"},"status":{"containerStatuses":[{"name":"application","state":{"terminated":{"reason":"Error","exitCode":1,"signal":0}},"lastState":{"terminated":{"reason":"OOMKilled","exitCode":137,"signal":9,"message":"private-message"}}}]}},{"metadata":{"name":"a"},"status":{"containerStatuses":[{"name":"application","state":{"waiting":{"reason":"CrashLoopBackOff"}},"lastState":{"terminated":{"reason":"OOMKilled","exitCode":137,"signal":9}}}]}}]}`
	client := New(runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		calls++
		want := []string{"--context", "k3d-test", "--namespace", "cloudforge", "get", "pods", "--selector", "app=api", "--output", "json"}
		if request.Name != "kubectl" || !reflect.DeepEqual(request.Args, want) || request.OutputLimit != 256*1024 || request.Timeout != 30*time.Second {
			t.Fatalf("observation command changed: %+v", request)
		}
		return model.CommandResult{Stdout: raw}
	}))
	pods, result, err := client.ObservePods(context.Background(), "test", "cloudforge", "app=api")
	if err != nil || len(pods) != 2 || calls != 1 || result.Stdout != raw {
		t.Fatalf("snapshot acquisition changed: pods=%+v err=%v calls=%d", pods, err, calls)
	}
	if pods[0].Name != "a" || pods[0].Reason != "crash_loop" || pods[0].CurrentTermination != nil || !pods[0].ApplicationStatusObserved {
		t.Fatalf("waiting state lost previous slot: %+v", pods[0])
	}
	assertTermination(t, pods[0].PreviousTermination, "oom_killed", 137, 9)
	if pods[1].Name != "z" || !pods[1].ApplicationStatusObserved {
		t.Fatalf("sorted identity lost: %+v", pods[1])
	}
	assertTermination(t, pods[1].CurrentTermination, "error", 1, 0)
	assertTermination(t, pods[1].PreviousTermination, "oom_killed", 137, 9)
	encoded, err := json.Marshal(pods)
	if err != nil || strings.Contains(string(encoded), "private-message") {
		t.Fatalf("message entered safe state: %s %v", encoded, err)
	}
}

func assertTermination(t *testing.T, state *ContainerTermination, reason string, exitCode, signal int32) {
	t.Helper()
	if state == nil || state.Reason != reason || state.ExitCode == nil || *state.ExitCode != exitCode || state.Signal == nil || *state.Signal != signal {
		t.Fatalf("unexpected termination: %+v", state)
	}
}

func TestObservePodsTerminationBoundsAndUnknowns(t *testing.T) {
	for _, tc := range []struct {
		name, fields string
		exit, signal *int32
	}{
		{name: "absent"},
		{name: "null", fields: `,"exitCode":null,"signal":null`},
		{name: "negative", fields: `,"exitCode":-1,"signal":-1`},
		{name: "out-of-range", fields: `,"exitCode":256,"signal":65`},
		{name: "zero", fields: `,"exitCode":0,"signal":0`, exit: new(int32), signal: new(int32)},
		{name: "upper-bound", fields: `,"exitCode":255,"signal":64`, exit: int32Pointer(255), signal: int32Pointer(64)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := fmt.Sprintf(`{"items":[{"metadata":{"name":"observed"},"status":{"containerStatuses":[{"name":"application","state":{"terminated":{"reason":"custom-private-reason","message":"private-message"%s}}}]}}]}`, tc.fields)
			client := New(runnerFunc(func(context.Context, command.Request) model.CommandResult { return model.CommandResult{Stdout: raw} }))
			pods, _, err := client.ObservePods(context.Background(), "test", "cloudforge", "app=api")
			if err != nil || len(pods) != 1 || !pods[0].ApplicationStatusObserved {
				t.Fatalf("observation failed: %+v %v", pods, err)
			}
			state := pods[0].CurrentTermination
			if state == nil || state.Reason != "unknown" || !reflect.DeepEqual(state.ExitCode, tc.exit) || !reflect.DeepEqual(state.Signal, tc.signal) {
				t.Fatalf("absence/bounds confused with known values: %+v", state)
			}
			body, _ := json.Marshal(pods)
			for _, private := range []string{"custom-private-reason", "private-message"} {
				if strings.Contains(string(body), private) {
					t.Fatalf("unsafe content retained: %s", body)
				}
			}
		})
	}
}

func int32Pointer(value int32) *int32 { return &value }

func TestObservePodsTerminationIgnoresForeignAndAmbiguousContainers(t *testing.T) {
	for _, statuses := range []string{
		`[{"name":"foreign","state":{"terminated":{"reason":"OOMKilled","exitCode":137,"signal":9}}}]`,
		`[{"name":"application"},{"name":"application","lastState":{"terminated":{"reason":"OOMKilled","exitCode":137,"signal":9}}}]`,
		`[]`,
	} {
		raw := `{"items":[{"metadata":{"name":"observed"},"status":{"initContainerStatuses":[{"name":"application","state":{"terminated":{"reason":"OOMKilled","exitCode":137}}}],"containerStatuses":` + statuses + `}}]}`
		client := New(runnerFunc(func(context.Context, command.Request) model.CommandResult { return model.CommandResult{Stdout: raw} }))
		pods, _, err := client.ObservePods(context.Background(), "test", "cloudforge", "app=api")
		if err != nil || len(pods) != 1 || pods[0].ApplicationStatusObserved || pods[0].CurrentTermination != nil || pods[0].PreviousTermination != nil {
			t.Fatalf("foreign or ambiguous status attributed to application: %+v %v", pods, err)
		}
	}
	raw := `{"items":[{"metadata":{"name":"observed"},"status":{"containerStatuses":[{"name":"application","state":{"running":{}}},{"name":"foreign","state":{"terminated":{"reason":"OOMKilled","exitCode":137}}}]}}]}`
	client := New(runnerFunc(func(context.Context, command.Request) model.CommandResult { return model.CommandResult{Stdout: raw} }))
	pods, _, err := client.ObservePods(context.Background(), "test", "cloudforge", "app=api")
	if err != nil || len(pods) != 1 || !pods[0].ApplicationStatusObserved || pods[0].CurrentTermination != nil || pods[0].PreviousTermination != nil {
		t.Fatalf("foreign termination contaminated known application status: %+v %v", pods, err)
	}
}

func TestObservePodsRejectsUnavailableTerminationSnapshot(t *testing.T) {
	for _, result := range []model.CommandResult{
		{Stdout: `{"items":[]}`, Truncated: true},
		{Stdout: `{"items":[]}`, FailureType: model.FailureTimeout, ExitCode: -1},
		{Stdout: `{"items":[`, ExitCode: 0},
	} {
		client := New(runnerFunc(func(context.Context, command.Request) model.CommandResult { return result }))
		pods, _, _ := client.ObservePods(context.Background(), "test", "cloudforge", "app=api")
		if pods != nil {
			t.Fatalf("unavailable snapshot reported as observed: %+v", pods)
		}
	}
}
