package k3d

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

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

func TestImportRequiresExactSourceAndCRIImageIdentity(t *testing.T) {
	id := "sha256:" + strings.Repeat("a", 64)
	other := "sha256:" + strings.Repeat("b", 64)
	for _, test := range []struct {
		name      string
		stage     int
		output    string
		truncated bool
		calls     int
	}{
		{name: "exact identity", stage: -1, calls: 3},
		{name: "source missing", calls: 1},
		{name: "source malformed", output: `{"id":"` + id + `"}`, calls: 1},
		{name: "source non digest", output: `"cloudforge/api:test"`, calls: 1},
		{name: "source extra JSON", output: `"` + id + `" "` + id + `"`, calls: 1},
		{name: "source truncated", output: `"` + id + `"`, truncated: true, calls: 1},
		{name: "source oversized", output: `"` + id + `"` + strings.Repeat(" ", imageIdentityLimit), calls: 1},
		{name: "CRI missing despite zero import exit", stage: 2, calls: 3},
		{name: "CRI wrong image despite zero import exit", stage: 2, output: `{"id":"` + other + `"}`, calls: 3},
		{name: "CRI malformed", stage: 2, output: `{broken`, calls: 3},
		{name: "CRI null", stage: 2, output: `{"id":null}`, calls: 3},
		{name: "CRI extra object", stage: 2, output: `{"id":"` + id + `"}{}`, calls: 3},
		{name: "CRI extra field", stage: 2, output: `{"id":"` + id + `","other":true}`, calls: 3},
		{name: "CRI duplicate field", stage: 2, output: `{"id":"` + other + `","id":"` + id + `"}`, calls: 3},
		{name: "CRI truncated", stage: 2, output: `{"id":"` + id + `"}`, truncated: true, calls: 3},
		{name: "CRI oversized", stage: 2, output: `{"id":"` + id + `"}` + strings.Repeat(" ", imageIdentityLimit), calls: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls []command.Request
			client := New(runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
				stage := len(calls)
				calls = append(calls, request)
				output := []string{`"` + id + `"` + "\n", "time=now level=info msg=imported\n", `{"id":"` + id + `"}`}[stage]
				result := model.CommandResult{Command: request.Name, Arguments: request.Args, Stdout: output, DurationMS: 3}
				if stage == test.stage {
					result.Stdout, result.Truncated = test.output, test.truncated
				}
				return result
			}))
			result := client.ImportImage(context.Background(), "cloudforge-0123abcd", "cloudforge/api:test")
			if len(calls) != test.calls || (result.FailureType != model.FailureNone || result.ExitCode != 0) != (test.stage >= 0) {
				t.Fatalf("unproven image was accepted or import retried: %+v calls=%+v", result, calls)
			}
			if result.ExitCode != 0 {
				t.Fatal("identity rejection changed the tool's observed exit code")
			}
			if calls[0].Name != "docker" || !reflect.DeepEqual(calls[0].Args, []string{"image", "inspect", "--format", "{{json .Id}}", "cloudforge/api:test"}) || calls[0].OutputLimit != imageIdentityLimit || calls[0].Timeout != 10*time.Second {
				t.Fatalf("source observation is not bounded and allowlisted: %+v", calls[0])
			}
			if len(calls) > 1 {
				if calls[1].Name != "k3d" || !reflect.DeepEqual(calls[1].Args, []string{"image", "import", "cloudforge/api:test", "--cluster", "cloudforge-0123abcd", "--mode", "tools-node"}) || calls[1].Timeout != 3*time.Minute || calls[1].OutputLimit != 128*1024 {
					t.Fatalf("unexpected import or unbounded tool invocation: %+v", calls[1])
				}
				if !reflect.DeepEqual(calls[1].Env, []string{"LOG_LEVEL=info", "LOG_COLORS=false", "LOG_TIMESTAMPS="}) {
					t.Fatalf("ambient log settings can suppress import errors: %+v", calls[1])
				}
			}
			if len(calls) > 2 {
				expected := []string{"exec", "k3d-cloudforge-0123abcd-server-0", "crictl", "inspecti", "--quiet", "--output", "go-template", "--template", `{"id":{{printf "%q" .status.id}}}`, "cloudforge/api:test"}
				if calls[2].Name != "docker" || !reflect.DeepEqual(calls[2].Args, expected) || calls[2].OutputLimit != imageIdentityLimit || calls[2].Timeout != 15*time.Second {
					t.Fatalf("CRI observation collects more than the owned image identity: %+v", calls[2])
				}
			}
			if test.stage == -1 && result.DurationMS != 9 {
				t.Fatal("image import duration excludes a required observation")
			}
		})
	}
}

func TestImportPreservesFailuresAndRejectsLoggedFalseSuccess(t *testing.T) {
	id := "sha256:" + strings.Repeat("a", 64)
	for _, test := range []struct {
		name    string
		result  model.CommandResult
		logged  bool
		altered bool
	}{
		{name: "nonzero", result: model.CommandResult{ExitCode: 1, FailureType: model.FailureExit, Stderr: "original import error"}},
		{name: "timeout", result: model.CommandResult{ExitCode: -1, FailureType: model.FailureTimeout, Stdout: "partial output"}},
		{name: "canceled", result: model.CommandResult{ExitCode: -1, FailureType: model.FailureCanceled, Stderr: "partial error"}},
		{name: "truncated stdout", result: model.CommandResult{Truncated: true, Stdout: "original partial output"}, altered: true},
		{name: "node import error with zero exit", result: model.CommandResult{Stderr: "time=now level=error msg=\"failed to import images in node\"\n"}, logged: true, altered: true},
		{name: "tar removal error with zero exit", result: model.CommandResult{Stderr: "time=now level=error msg=\"failed to delete one or more tarballs\"\n"}, logged: true, altered: true},
		{name: "tools cleanup error with zero exit", result: model.CommandResult{Stderr: "time=now level=error msg=\"failed to delete tools node\"\n"}, logged: true, altered: true},
		{name: "fatal with zero exit", result: model.CommandResult{Stderr: "time=now level=fatal msg=failed\n"}, logged: true, altered: true},
		{name: "panic with zero exit", result: model.CommandResult{Stderr: "time=now level=panic msg=failed\n"}, logged: true, altered: true},
		{name: "error in stdout", result: model.CommandResult{Stdout: "time=now level=error msg=failed\n"}, logged: true, altered: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			client := New(runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
				calls++
				if calls == 1 {
					return model.CommandResult{Stdout: `"` + id + `"`}
				}
				if calls != 2 || request.Name != "k3d" {
					t.Fatal("failed import retried or followed by a success-overriding inspection")
				}
				return test.result
			}))
			result := client.ImportImage(context.Background(), "cloudforge-0123abcd", "cloudforge/api:test")
			expected := test.result
			if test.altered {
				expected.FailureType = model.FailureExecution
			}
			if calls != 2 || !reflect.DeepEqual(result, expected) {
				t.Fatalf("original failure/output was lost: result=%+v expected=%+v calls=%d", result, expected, calls)
			}
			if test.logged && (result.ExitCode != 0 || result.FailureType != model.FailureExecution) {
				t.Fatal("logged tools-node error was treated as successful import")
			}
		})
	}
}

func TestImportIdentityCommandFailuresStopWithoutRetry(t *testing.T) {
	id := "sha256:" + strings.Repeat("a", 64)
	for _, stage := range []int{0, 2} {
		for _, failure := range []model.FailureType{model.FailureExit, model.FailureTimeout, model.FailureCanceled, model.FailureExecution} {
			t.Run(strconv.Itoa(stage)+"/"+string(failure), func(t *testing.T) {
				calls := 0
				observed := model.CommandResult{ExitCode: 7, FailureType: failure, Stdout: "original output", Stderr: "original diagnostic", DurationMS: 11}
				client := New(runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
					current := calls
					calls++
					if current == stage {
						observed.Command, observed.Arguments = request.Name, request.Args
						return observed
					}
					return model.CommandResult{Stdout: []string{`"` + id + `"`, "", `{"id":"` + id + `"}`}[current], DurationMS: 3}
				}))
				result := client.ImportImage(context.Background(), "cloudforge-0123abcd", "cloudforge/api:test")
				if stage == 2 {
					observed.DurationMS += 6
				}
				if calls != stage+1 || !reflect.DeepEqual(result, observed) {
					t.Fatalf("failed identity observation was changed or retried: %+v expected=%+v calls=%d", result, observed, calls)
				}
			})
		}
	}
}

func TestImportCancellationStopsNewOperations(t *testing.T) {
	id := "sha256:" + strings.Repeat("a", 64)
	for _, cancelAfter := range []int{0, 1, 2, 3} {
		t.Run("after "+strconv.Itoa(cancelAfter), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelAfter == 0 {
				cancel()
			}
			calls := 0
			client := New(runnerFunc(func(_ context.Context, _ command.Request) model.CommandResult {
				calls++
				result := model.CommandResult{Stdout: []string{`"` + id + `"`, "", `{"id":"` + id + `"}`}[calls-1]}
				if calls == cancelAfter {
					cancel()
				}
				return result
			}))
			result := client.ImportImage(ctx, "cloudforge-0123abcd", "cloudforge/api:test")
			if calls != cancelAfter || result.FailureType != model.FailureCanceled || result.ExitCode != -1 {
				t.Fatalf("canceled import scheduled new work or became success: %+v calls=%d", result, calls)
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
