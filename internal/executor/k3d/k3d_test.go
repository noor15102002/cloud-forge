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
			if len(calls) != test.calls || (result.ExitCode != 0) != (test.importFails || test.missing) {
				t.Fatalf("unexpected import result: %#v calls=%#v", result, calls)
			}
			if len(calls) == 2 && (calls[1].Name != "docker" || !reflect.DeepEqual(calls[1].Args, []string{"exec", "k3d-cloudforge-0123abcd-server-0", "crictl", "inspecti", "cloudforge/api:test"})) {
				t.Fatalf("image check escaped owned node: %#v", calls[1])
			}
		})
	}
}
