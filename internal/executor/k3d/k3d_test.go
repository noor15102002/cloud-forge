package k3d

import (
	"context"
	"reflect"
	"testing"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

type runnerFunc func(context.Context, command.Request) model.CommandResult

func (f runnerFunc) Run(ctx context.Context, request command.Request) model.CommandResult {
	return f(ctx, request)
}

func TestImportConfirmsCRIImageAndPropagatesFailure(t *testing.T) {
	for _, test := range []struct {
		name                 string
		importFails, missing bool
		calls                int
	}{
		{name: "present", calls: 2}, {name: "missing despite successful import", missing: true, calls: 2}, {name: "import failure", importFails: true, calls: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls []command.Request
			client := New(runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
				calls = append(calls, request)
				result := model.CommandResult{Command: request.Name, Arguments: request.Args}
				if test.importFails || (request.Name == "docker" && test.missing) {
					result.ExitCode = 1
					result.FailureType = model.FailureExit
				}
				return result
			}))
			result := client.ImportImage(context.Background(), "cloudforge-0123abcd", "cloudforge/api:test")
			if !reflect.DeepEqual(calls[0].Args, []string{"image", "import", "cloudforge/api:test", "--cluster", "cloudforge-0123abcd", "--mode", "direct"}) {
				t.Fatalf("expected direct import with error propagation: %#v", calls[0])
			}
			if len(calls) != test.calls || (result.ExitCode != 0) != (test.importFails || test.missing) {
				t.Fatalf("unexpected import result: %#v calls=%#v", result, calls)
			}
			if len(calls) == 2 && (calls[1].Name != "docker" || !reflect.DeepEqual(calls[1].Args, []string{"exec", "k3d-cloudforge-0123abcd-server-0", "crictl", "inspecti", "cloudforge/api:test"})) {
				t.Fatalf("image check escaped owned node: %#v", calls[1])
			}
		})
	}
}

func TestCreateAcceptsOnlyFixedMemoryBudgets(t *testing.T) {
	for _, memory := range []string{"4g", "6g", "", "64g", "6g --privileged", "6144m"} {
		t.Run(memory, func(t *testing.T) {
			var calls []command.Request
			client := New(runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
				calls = append(calls, request)
				return model.CommandResult{}
			}))
			result := client.CreateWithMemory(context.Background(), "cloudforge-test", 30080, memory)
			valid := memory == "4g" || memory == "6g"
			if !valid {
				if len(calls) != 0 || result.FailureType != model.FailureExecution || result.ExitCode != -1 {
					t.Fatalf("unbounded profile was executed: %+v", result)
				}
				return
			}
			if len(calls) != 1 || result.ExitCode != 0 {
				t.Fatalf("fixed profile was not executed: %+v", calls)
			}
			args := calls[0].Args
			selected := ""
			for i, arg := range args {
				if arg == "--servers-memory" && i+1 < len(args) {
					selected = args[i+1]
				}
			}
			if selected != memory {
				t.Fatal("selected memory changed or was omitted")
			}
			client.Create(context.Background(), "cloudforge-test", 30080)
			legacy := append([]string(nil), args...)
			for i, arg := range legacy {
				if arg == "--servers-memory" {
					legacy[i+1] = "4g"
				}
			}
			if !reflect.DeepEqual(calls[1].Args, legacy) || calls[0].Timeout != calls[1].Timeout || calls[0].OutputLimit != calls[1].OutputLimit {
				t.Fatal("legacy creation or isolation flags changed")
			}
		})
	}
}

func TestCreateCarriesIndependentInvocationOwnership(t *testing.T) {
	const token = "abcdef0123456789abcdef0123456789"
	client := New(runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		found := false
		for index, argument := range request.Args {
			if argument == "cloudforge.dev/ownership="+token+"@all" && index > 0 && request.Args[index-1] == "--runtime-label" {
				found = true
			}
		}
		if !found {
			t.Fatal("cluster creation lacks invocation ownership marker")
		}
		return model.CommandResult{}
	}))
	client.SetOwnership(token)
	if result := client.Create(context.Background(), "cloudforge-0123abcd", 30080); result.ExitCode != 0 || result.FailureType != model.FailureNone {
		t.Fatal(result)
	}
}
