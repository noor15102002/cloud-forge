package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

const cleanupBuilderName = "cloudforge-0123abcd"

type builderCleanupRunner struct {
	t                 *testing.T
	calls             []command.Request
	deadlines         []time.Time
	containerExists   bool
	volumeExists      bool
	containerID       string
	containerName     string
	marker            bool
	volumeName        string
	createdAt         string
	driver            string
	scope             string
	inspectOverride   *model.CommandResult
	removeFailure     model.CommandResult
	partialRemoval    bool
	containerFailure  bool
	volumeFailure     bool
	containerRetained bool
	volumeRetained    bool
	containerVanished bool
	volumeVanished    bool
}

func newBuilderCleanupRunner(t *testing.T) *builderCleanupRunner {
	return &builderCleanupRunner{t: t, containerExists: true, volumeExists: true, containerID: strings.Repeat("a", 64),
		containerName: "/buildx_buildkit_" + cleanupBuilderName + "0", marker: true,
		volumeName: "buildx_buildkit_" + cleanupBuilderName + "0_state", createdAt: "2026-09-21T12:00:00Z", driver: "local", scope: "local"}
}

func jsonFields(values ...any) string {
	var buffer bytes.Buffer
	for _, value := range values {
		_ = json.NewEncoder(&buffer).Encode(value)
	}
	return buffer.String()
}

func missingBuilderObject(kind, name string) model.CommandResult {
	message := "Error: No such object: " + name
	if kind == "volume" {
		message = "Error response from daemon: get " + name + ": no such volume"
	}
	return model.CommandResult{ExitCode: 1, FailureType: model.FailureExit, Stderr: message}
}

func (r *builderCleanupRunner) Run(ctx context.Context, request command.Request) model.CommandResult {
	r.calls = append(r.calls, request)
	deadline, ok := ctx.Deadline()
	if !ok {
		r.t.Fatal("cleanup request has no aggregate deadline")
	}
	r.deadlines = append(r.deadlines, deadline)
	args := request.Args
	if request.Name != "docker" || len(args) < 2 {
		r.t.Fatalf("unexpected request: %+v", request)
	}
	if args[1] == "inspect" {
		if request.Timeout != 5*time.Second || request.OutputLimit != 4096 {
			r.t.Fatal("inspection exceeded its bound")
		}
		if r.inspectOverride != nil {
			return *r.inspectOverride
		}
		if args[0] == "container" {
			if !r.containerExists {
				return missingBuilderObject("container", args[len(args)-1])
			}
			return model.CommandResult{Stdout: jsonFields(r.containerID, r.containerName, r.marker, "volume", r.volumeName)}
		}
		if !r.volumeExists {
			return missingBuilderObject("volume", args[len(args)-1])
		}
		return model.CommandResult{Stdout: jsonFields(r.volumeName, r.driver, r.scope, r.createdAt)}
	}
	if args[0] == "buildx" && args[1] == "rm" {
		if request.Timeout != time.Minute || !slices.Equal(args, []string{"buildx", "rm", "--force", cleanupBuilderName}) {
			r.t.Fatal("ordinary builder removal contract changed")
		}
		if r.partialRemoval || !builderCleanupFailed(r.removeFailure) {
			r.containerExists = false
		}
		if !builderCleanupFailed(r.removeFailure) {
			r.volumeExists = false
		}
		return r.removeFailure
	}
	if args[0] == "container" && args[1] == "rm" {
		if !slices.Equal(args, []string{"container", "rm", "--force", strings.Repeat("a", 64)}) {
			r.t.Fatalf("fallback did not target captured immutable ID: %v", args)
		}
		if r.containerFailure {
			return model.CommandResult{ExitCode: 1, FailureType: model.FailureExit, Stderr: "PRIVATE daemon error"}
		}
		if r.containerVanished {
			r.containerExists = false
			return missingBuilderObject("container", r.containerID)
		}
		if !r.containerRetained {
			r.containerExists = false
		}
		return model.CommandResult{}
	}
	if args[0] == "volume" && args[1] == "rm" {
		if r.containerExists || !slices.Equal(args, []string{"volume", "rm", r.volumeName}) {
			r.t.Fatal("volume removed before container absence, without exact name, or with force")
		}
		if r.volumeFailure {
			return model.CommandResult{ExitCode: 1, FailureType: model.FailureExit, Stderr: "PRIVATE volume in use"}
		}
		if r.volumeVanished {
			r.volumeExists = false
			return missingBuilderObject("volume", r.volumeName)
		}
		if !r.volumeRetained {
			r.volumeExists = false
		}
		return model.CommandResult{}
	}
	r.t.Fatalf("unexpected cleanup request: %+v", request)
	return model.CommandResult{}
}

func cleanupClient(r *builderCleanupRunner) *Client {
	c := New(r)
	c.IsolateBuild(cleanupBuilderName)
	return c
}

func TestBuilderRunMarkerUsesSupportedDriverOptionWithoutChangingLimits(t *testing.T) {
	calls := 0
	c := New(runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		calls++
		want := "memory=2g,cpu-period=100000,cpu-quota=200000,env.CLOUDFORGE_RUN_ID=" + cleanupBuilderName + ",env.CLOUDFORGE_OWNER_ID=" + strings.Repeat("d", 32)
		if !slices.Equal(request.Args, []string{"buildx", "create", "--name", cleanupBuilderName, "--driver", "docker-container", "--driver-opt", want}) || request.Timeout != time.Minute {
			t.Fatalf("unexpected create request: %+v", request)
		}
		return model.CommandResult{}
	}))
	c.IsolateBuild(cleanupBuilderName)
	c.ownershipToken = strings.Repeat("d", 32)
	if result := c.CreateBuilder(context.Background()); builderCleanupFailed(result) {
		t.Fatal(result)
	}
	c.IsolateBuild("unrelated; --all")
	if result := c.CreateBuilder(context.Background()); !builderCleanupFailed(result) || calls != 1 {
		t.Fatal("unsafe builder name reached Docker")
	}
}

func TestBuilderOwnershipFormatOmitsEnvironmentAndUnrelatedMounts(t *testing.T) {
	c := New(runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		format := request.Args[3]
		parsed, err := template.New("inspect").Funcs(template.FuncMap{"json": func(value any) string { body, _ := json.Marshal(value); return string(body) }}).Parse(format)
		if err != nil {
			t.Fatal(err)
		}
		data := map[string]any{
			"Id": strings.Repeat("a", 64), "Name": "/buildx_buildkit_" + cleanupBuilderName + "0",
			"Config": map[string]any{"Env": []string{"PRIVATE_TOKEN=secret-canary", "CLOUDFORGE_RUN_ID=" + cleanupBuilderName, "CLOUDFORGE_OWNER_ID=" + strings.Repeat("d", 32)}},
			"Mounts": []map[string]string{{"Type": "volume", "Name": "buildx_buildkit_" + cleanupBuilderName + "0_state", "Destination": "/var/lib/buildkit"}, {"Type": "bind", "Name": "unrelated-private-path", "Destination": "/unrelated"}},
		}
		var output bytes.Buffer
		if err := parsed.Execute(&output, data); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(output.String(), "PRIVATE") || strings.Contains(output.String(), "secret-canary") || strings.Contains(output.String(), "unrelated") || strings.Contains(output.String(), "CLOUDFORGE_RUN_ID") {
			t.Fatal("inspect format exposed environment or unrelated mount")
		}
		return model.CommandResult{Stdout: output.String()}
	}))
	c.IsolateBuild(cleanupBuilderName)
	c.ownershipToken = strings.Repeat("d", 32)
	proof, exists, result := c.inspectBuilderContainer(context.Background(), c.builderContainerName())
	if !exists || builderCleanupFailed(result) || proof.containerID != strings.Repeat("a", 64) {
		t.Fatalf("allowlisted inspect did not establish ownership: %+v %v %+v", proof, exists, result)
	}
}

func TestBuilderCleanupPreservesFailureAndRemovesOnlyCapturedResources(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprint("partial=", partial), func(t *testing.T) {
			r := newBuilderCleanupRunner(t)
			r.partialRemoval = partial
			r.removeFailure = model.CommandResult{ExitCode: 1, FailureType: model.FailureExit, Stderr: "original buildx removal failure"}
			c := cleanupClient(r)
			original := c.RemoveBuilder(context.Background())
			if original.ExitCode != 1 || original.FailureType != model.FailureExit || original.Stderr != r.removeFailure.Stderr || c.builderOwnership == nil {
				t.Fatalf("original failure/proof lost: %+v", original)
			}
			cut := len(r.calls)
			if result := c.RemoveBuilderRemnants(context.Background()); builderCleanupFailed(result) || r.containerExists || r.volumeExists {
				t.Fatalf("owned fallback failed: %+v", result)
			}
			if original.FailureType != model.FailureExit || original.Stderr != r.removeFailure.Stderr {
				t.Fatal("fallback rewrote original failure")
			}
			for index, deadline := range r.deadlines {
				limit := time.Minute
				first := r.deadlines[0]
				if index >= cut {
					limit = 30 * time.Second
					first = r.deadlines[cut]
				}
				if !deadline.Equal(first) || time.Until(deadline) > limit {
					t.Fatal("cleanup aggregate deadline was renewed between steps")
				}
			}
			if result := c.RemoveBuilderRemnants(context.Background()); builderCleanupFailed(result) {
				t.Fatal("proven absence was not idempotent", result)
			}
		})
	}
}

func TestBuilderOwnershipMismatchAndInvalidInspectionRefuseRemoval(t *testing.T) {
	cases := map[string]func(*builderCleanupRunner){
		"wrong marker":          func(r *builderCleanupRunner) { r.marker = false },
		"wrong name":            func(r *builderCleanupRunner) { r.containerName = "/unrelated" },
		"wrong volume":          func(r *builderCleanupRunner) { r.volumeName = "unrelated-cache" },
		"short id":              func(r *builderCleanupRunner) { r.containerID = "aaaaaaaaaaaa" },
		"argument-like id":      func(r *builderCleanupRunner) { r.containerID = "--force unrelated" },
		"empty id":              func(r *builderCleanupRunner) { r.containerID = "" },
		"wrong driver":          func(r *builderCleanupRunner) { r.driver = "remote" },
		"wrong scope":           func(r *builderCleanupRunner) { r.scope = "global" },
		"missing creation time": func(r *builderCleanupRunner) { r.createdAt = "" },
		"mounted volume absent": func(r *builderCleanupRunner) { r.volumeExists = false },
		"unproven orphan":       func(r *builderCleanupRunner) { r.containerExists = false },
		"empty":                 func(r *builderCleanupRunner) { r.inspectOverride = &model.CommandResult{} },
		"malformed": func(r *builderCleanupRunner) {
			r.inspectOverride = &model.CommandResult{Stdout: "PRIVATE invalid output"}
		},
		"truncated": func(r *builderCleanupRunner) {
			r.inspectOverride = &model.CommandResult{Truncated: true, Stdout: "PRIVATE"}
		},
		"daemon error": func(r *builderCleanupRunner) {
			r.inspectOverride = &model.CommandResult{ExitCode: 1, FailureType: model.FailureExit, Stderr: "PRIVATE daemon unavailable"}
		},
		"wrong missing identity": func(r *builderCleanupRunner) {
			result := missingBuilderObject("container", "unrelated")
			r.inspectOverride = &result
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			r := newBuilderCleanupRunner(t)
			change(r)
			c := cleanupClient(r)
			result := c.RemoveBuilder(context.Background())
			if !builderCleanupFailed(result) || c.builderOwnership != nil || strings.Contains(result.Stdout+result.Stderr, "PRIVATE") {
				t.Fatalf("invalid inspection accepted or exposed: %+v", result)
			}
			for _, call := range r.calls {
				if call.Args[1] != "inspect" {
					t.Fatalf("unproven ownership reached removal: %v", call.Args)
				}
			}
		})
	}
}

func TestBuilderFallbackRefusesUnprovenOrChangedResources(t *testing.T) {
	for _, mode := range []string{"no cache container", "no cache orphan", "changed container", "changed volume", "container remove failure", "volume remove failure", "container retained", "volume retained"} {
		t.Run(mode, func(t *testing.T) {
			r := newBuilderCleanupRunner(t)
			c := cleanupClient(r)
			if !strings.HasPrefix(mode, "no cache") {
				if result := c.captureBuilderOwnership(withTestBuilderDeadline(t)); builderCleanupFailed(result) {
					t.Fatal(result)
				}
			}
			switch mode {
			case "no cache orphan":
				r.containerExists = false
			case "changed container":
				r.containerID = strings.Repeat("b", 64)
			case "changed volume":
				r.containerExists = false
				r.createdAt = "2026-09-21T12:00:01Z"
			case "container remove failure":
				r.containerFailure = true
			case "volume remove failure":
				r.volumeFailure = true
			case "container retained":
				r.containerRetained = true
			case "volume retained":
				r.volumeRetained = true
			}
			start := len(r.calls)
			result := c.RemoveBuilderRemnants(context.Background())
			if !builderCleanupFailed(result) || strings.Contains(result.Stderr+result.Stdout, "PRIVATE") {
				t.Fatalf("unsafe or failed cleanup became success: %+v", result)
			}
			if mode == "no cache container" || mode == "no cache orphan" || mode == "changed container" || mode == "changed volume" {
				for _, call := range r.calls[start:] {
					if call.Args[1] != "inspect" {
						t.Fatal("unproven resource removed", call.Args)
					}
				}
			}
		})
	}
}

func withTestBuilderDeadline(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	return ctx
}

func TestBuilderCleanupAbsenceAndCancellationRemainDistinct(t *testing.T) {
	r := newBuilderCleanupRunner(t)
	r.containerExists, r.volumeExists = false, false
	c := cleanupClient(r)
	if result := c.RemoveBuilderRemnants(context.Background()); builderCleanupFailed(result) {
		t.Fatal("confirmed absent builder was not clean", result)
	}
	for _, timeout := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		want := model.FailureCanceled
		if timeout {
			cancel()
			ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			want = model.FailureTimeout
		}
		cancel()
		before := len(r.calls)
		result := c.RemoveBuilderRemnants(ctx)
		if result.FailureType != want || len(r.calls) != before {
			t.Fatalf("ended cleanup context executed or became absence: %+v", result)
		}
	}
	if decodeBuilderFields(`"value" null`, new(string)) || decodeBuilderFields(``, new(string)) {
		t.Fatal("trailing or absent identity fields accepted")
	}
}

func TestBuilderNewRunDropsPriorOwnershipProof(t *testing.T) {
	r := newBuilderCleanupRunner(t)
	c := cleanupClient(r)
	if result := c.captureBuilderOwnership(withTestBuilderDeadline(t)); builderCleanupFailed(result) {
		t.Fatal(result)
	}
	c.IsolateBuild("cloudforge-ffffaaaa")
	if c.builderOwnership != nil {
		t.Fatal("another run inherited cleanup authority")
	}
}

func TestBuilderFallbackConfirmsAbsenceWhenRemovalAlreadyCompleted(t *testing.T) {
	r := newBuilderCleanupRunner(t)
	c := cleanupClient(r)
	if result := c.captureBuilderOwnership(withTestBuilderDeadline(t)); builderCleanupFailed(result) {
		t.Fatal(result)
	}
	r.containerVanished, r.volumeVanished = true, true
	if result := c.RemoveBuilderRemnants(context.Background()); builderCleanupFailed(result) || r.containerExists || r.volumeExists {
		t.Fatalf("confirmed absence after concurrent completion was rejected: %+v", result)
	}
	last := r.calls[len(r.calls)-1]
	if last.Args[0] != "volume" || last.Args[1] != "inspect" {
		t.Fatal("removal not-found was accepted without confirming absence")
	}
}
