package k3d

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

type runnerFunc func(context.Context, command.Request) model.CommandResult

func (f runnerFunc) Run(ctx context.Context, request command.Request) model.CommandResult {
	return f(ctx, request)
}

func TestCreateEnforcesSupportedClusterNameLengthBeforeExecution(t *testing.T) {
	for _, name := range []string{"cloudforge-0123abcd", "cloudforge-" + strings.Repeat("a", 20), strings.Repeat("a", 32), strings.Repeat("a", 33), "cloudforge-" + strings.Repeat("a", 32)} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			client := New(runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
				calls++
				if request.Args[2] != name {
					t.Fatal("cluster name was silently changed")
				}
				return model.CommandResult{}
			}))
			result := client.Create(context.Background(), name, 30080)
			if len(name) > 32 {
				if calls != 0 || result.ExitCode != -1 || result.FailureType != model.FailureExecution || !strings.Contains(result.Stderr, "32-character limit") {
					t.Fatalf("overlong name reached k3d or lost its diagnostic: %+v calls=%d", result, calls)
				}
			} else if calls != 1 || result.ExitCode != 0 || result.FailureType != model.FailureNone {
				t.Fatalf("supported name was rejected: %+v calls=%d", result, calls)
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
