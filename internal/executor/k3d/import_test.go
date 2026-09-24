package k3d

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

const testImportCluster = "cloudforge-0123abcd"

var (
	testImageID = "sha256:" + strings.Repeat("a", 64)
	testNodeID  = strings.Repeat("d", 64)
)

type importHarness struct {
	t        *testing.T
	client   *Client
	requests []command.Request
	stages   []string
	contexts []context.Context
	mutate   func(string, command.Request, *model.CommandResult)
	after    func(string)
}

func newImportHarness(t *testing.T) *importHarness {
	t.Helper()
	h := &importHarness{t: t}
	h.client = New(runnerFunc(h.run))
	h.client.SetOwnership(strings.Repeat("f", 32))
	h.client.SetWorkspace(t.TempDir())
	// #nosec G302 -- private directory requires owner traversal permission.
	if err := os.Chmod(h.client.workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *importHarness) run(ctx context.Context, request command.Request) model.CommandResult {
	h.t.Helper()
	stage := importStage(h.t, request)
	h.requests = append(h.requests, request)
	h.stages = append(h.stages, stage)
	h.contexts = append(h.contexts, ctx)
	result := model.CommandResult{Command: request.Name, Arguments: slices.Clone(request.Args), DurationMS: 3}
	switch stage {
	case "source":
		result.Stdout = `"` + testImageID + `"`
	case "node":
		result.Stdout = `{"id":"` + testNodeID + `","name":true,"owner":true,"run":true,"cluster":true,"role":true,"running":true}`
	case "save":
		if request.StdoutFile == nil || request.StdoutFile.MaxBytes != imageArchiveLimit {
			h.t.Fatal("archive did not use the hard-capped file sink")
		}
		if err := os.WriteFile(request.StdoutFile.Path, []byte("bounded test archive"), 0o600); err != nil {
			h.t.Fatal(err)
		}
	case "CRI":
		result.Stdout = `{"id":"` + testImageID + `"}`
	}
	if h.mutate != nil {
		h.mutate(stage, request, &result)
	}
	if h.after != nil {
		h.after(stage)
	}
	return result
}

func importStage(t *testing.T, request command.Request) string {
	t.Helper()
	if request.Name != "docker" {
		t.Fatalf("import unexpectedly invoked %s", request.Name)
	}
	args := request.Args
	if len(args) >= 2 && args[0] == "image" {
		switch args[1] {
		case "inspect":
			return "source"
		case "save":
			return "save"
		}
	}
	if len(args) >= 2 && args[0] == "container" && args[1] == "inspect" {
		return "node"
	}
	if len(args) > 2 && args[0] == "exec" {
		switch args[2] {
		case "mkdir", "ctr", "rm", "rmdir":
			return args[2]
		case "crictl":
			return "CRI"
		}
	}
	if len(args) > 0 && args[0] == "cp" {
		return "cp"
	}
	t.Fatalf("unexpected image import arguments: %v", args)
	return ""
}

func (h *importHarness) execute(ctx context.Context) model.CommandResult {
	h.t.Helper()
	result := h.client.ImportImage(ctx, testImportCluster, "cloudforge/api:test")
	if files, err := os.ReadDir(h.client.workspace); err != nil || len(files) != 0 {
		h.t.Fatalf("private archive retained after return: %v %v", files, err)
	}
	return result
}

func TestLocalImportStagesExactArchiveIntoOwnedImmutableNode(t *testing.T) {
	h := newImportHarness(t)
	result := h.execute(context.Background())
	if importFailed(result) || result.DurationMS != 27 || !reflect.DeepEqual(h.stages, []string{"source", "node", "save", "mkdir", "cp", "ctr", "CRI", "rm", "rmdir"}) {
		t.Fatalf("incomplete import or retry: %+v stages=%v", result, h.stages)
	}
	archive := h.requests[2].StdoutFile.Path
	directory := "/tmp/" + filepath.Base(filepath.Dir(archive))
	nodeFile := directory + "/image.tar"
	expected := [][]string{
		{"image", "inspect", "--format", "{{json .Id}}", "cloudforge/api:test"},
		{"container", "inspect", "--format", h.client.importNodeTemplate(testImportCluster), "k3d-" + testImportCluster + "-server-0"},
		{"image", "save", "cloudforge/api:test"},
		{"exec", testNodeID, "mkdir", "-m", "700", directory},
		{"cp", archive, testNodeID + ":" + nodeFile},
		{"exec", testNodeID, "ctr", "--address", "/run/k3s/containerd/containerd.sock", "--namespace", "k8s.io", "images", "import", "--local", "--all-platforms", nodeFile},
		{"exec", testNodeID, "crictl", "inspecti", "--quiet", "--output", "go-template", "--template", `{"id":{{printf "%q" .status.id}}}`, "cloudforge/api:test"},
		{"exec", testNodeID, "rm", "-f", nodeFile},
		{"exec", testNodeID, "rmdir", directory},
	}
	for i, request := range h.requests {
		if !reflect.DeepEqual(request.Args, expected[i]) || request.Timeout <= 0 || request.Timeout > imageImportTimeout || request.OutputLimit <= 0 || request.OutputLimit > 128*1024 {
			t.Fatalf("unbounded or wrong invocation %d: %+v", i, request)
		}
		if i != 2 && request.StdoutFile != nil {
			t.Fatal("only the archive export may write binary stdout")
		}
	}
	if filepath.Dir(filepath.Dir(archive)) != h.client.workspace || filepath.Base(archive) != "image.tar" || strings.Contains(h.requests[1].Args[3], ".Config.Env") {
		t.Fatal("staging escaped ownership or node inspection collects environment")
	}
	firstDeadline, _ := h.contexts[0].Deadline()
	for _, ctx := range h.contexts[:7] {
		deadline, ok := ctx.Deadline()
		if !ok || !deadline.Equal(firstDeadline) {
			t.Fatal("import phases do not share one overall deadline")
		}
	}
	if result.Stdout != `{"id":"`+testImageID+`"}` || result.ExitCode != 0 {
		t.Fatal("final observation or actual exit changed")
	}
}

func TestLocalImportRejectsMissingOrUnsafeBoundaryBeforeExecution(t *testing.T) {
	for _, name := range []string{"invalid cluster", "overlong cluster", "missing owner", "unsafe owner", "missing workspace", "relative workspace", "shared workspace", "symlink workspace"} {
		t.Run(name, func(t *testing.T) {
			h := newImportHarness(t)
			cluster := testImportCluster
			switch name {
			case "invalid cluster":
				cluster = "../cloudforge-test"
			case "overlong cluster":
				cluster = "cloudforge-" + strings.Repeat("a", 22)
			case "missing owner":
				h.client.SetOwnership("")
			case "unsafe owner":
				h.client.SetOwnership(`"}}private`)
			case "missing workspace":
				h.client.SetWorkspace("")
			case "relative workspace":
				h.client.SetWorkspace("relative")
			case "shared workspace":
				// #nosec G302 -- deliberately unsafe directory mode exercises rejection.
				if err := os.Chmod(h.client.workspace, 0o755); err != nil {
					t.Fatal(err)
				}
			case "symlink workspace":
				link := filepath.Join(t.TempDir(), "link")
				if err := os.Symlink(h.client.workspace, link); err != nil {
					t.Fatal(err)
				}
				h.client.SetWorkspace(link)
			}
			result := h.client.ImportImage(context.Background(), cluster, "cloudforge/api:test")
			if len(h.requests) != 0 || result.FailureType != model.FailureExecution || result.ExitCode != -1 {
				t.Fatalf("unsafe import began: %+v %v", result, h.stages)
			}
		})
	}
}

func TestLocalImportRejectsIncompleteSourceAndCRIIdentity(t *testing.T) {
	for _, stage := range []string{"source", "CRI"} {
		for _, invalid := range []string{"", `null`, `{broken`, `"not-an-id"`, `"` + testImageID + `" {}`, `{"id":"` + testImageID + `","id":"` + testImageID + `"}`, `{"id":"sha256:` + strings.Repeat("b", 64) + `"}`, strings.Repeat(" ", imageIdentityLimit+1), "truncated"} {
			t.Run(stage+"/"+invalid[:min(len(invalid), 32)], func(t *testing.T) {
				h := newImportHarness(t)
				h.mutate = func(current string, _ command.Request, result *model.CommandResult) {
					if current == stage {
						if invalid == "truncated" {
							result.Truncated = true
						} else {
							result.Stdout = invalid
						}
					}
				}
				result := h.execute(context.Background())
				if result.FailureType != model.FailureExecution || result.ExitCode != 0 || result.Stdout != "" {
					t.Fatalf("unproven identity accepted or exit fabricated: %+v", result)
				}
				if stage == "source" && len(h.stages) != 1 {
					t.Fatal("source failure did not stop new operations")
				}
				if stage == "CRI" && !reflect.DeepEqual(h.stages[len(h.stages)-2:], []string{"rm", "rmdir"}) {
					t.Fatal("identity failure skipped staged-file cleanup")
				}
			})
		}
	}
}

func TestLocalImportRequiresCompleteExactNodeOwnership(t *testing.T) {
	for _, field := range []string{"name", "owner", "run", "cluster", "role", "running", "id", "duplicate", "extra", "missing", "truncated"} {
		t.Run(field, func(t *testing.T) {
			h := newImportHarness(t)
			h.mutate = func(stage string, _ command.Request, result *model.CommandResult) {
				if stage != "node" {
					return
				}
				switch field {
				case "duplicate":
					result.Stdout = strings.Replace(result.Stdout, `"running":true`, `"running":true,"running":true`, 1)
				case "extra":
					result.Stdout = strings.Replace(result.Stdout, `"running":true`, `"running":true,"private":"do-not-collect"`, 1)
				case "missing":
					result.Stdout = strings.Replace(result.Stdout, `,"running":true`, "", 1)
				case "truncated":
					result.Truncated = true
				case "id":
					result.Stdout = strings.Replace(result.Stdout, testNodeID, "short-id", 1)
				default:
					result.Stdout = strings.Replace(result.Stdout, `"`+field+`":true`, `"`+field+`":false`, 1)
				}
			}
			result := h.execute(context.Background())
			if result.FailureType != model.FailureExecution || result.ExitCode != 0 || len(h.stages) != 2 || strings.Contains(result.Stderr, "do-not-collect") {
				t.Fatalf("unowned node reached export: %+v %v", result, h.stages)
			}
		})
	}
}

func TestLocalImportRejectsIncompleteOrUnsafeArchiveBeforeNodeMutation(t *testing.T) {
	for _, kind := range []string{"missing", "empty", "oversized", "symlink", "directory", "shared mode", "truncated"} {
		t.Run(kind, func(t *testing.T) {
			h := newImportHarness(t)
			h.mutate = func(stage string, request command.Request, result *model.CommandResult) {
				if stage != "save" {
					return
				}
				path := request.StdoutFile.Path
				switch kind {
				case "missing":
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				case "empty":
					if err := os.Truncate(path, 0); err != nil {
						t.Fatal(err)
					}
				case "oversized":
					if err := os.Truncate(path, imageArchiveLimit+1); err != nil {
						t.Fatal(err)
					}
				case "symlink", "directory":
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
					if kind == "symlink" {
						if err := os.Symlink(t.TempDir(), path); err != nil {
							t.Fatal(err)
						}
					} else if err := os.Mkdir(path, 0o700); err != nil {
						t.Fatal(err)
					}
				case "shared mode":
					// #nosec G302 -- deliberately unsafe archive mode exercises rejection.
					if err := os.Chmod(path, 0o644); err != nil {
						t.Fatal(err)
					}
				case "truncated":
					result.Truncated = true
				}
			}
			result := h.execute(context.Background())
			if result.FailureType != model.FailureExecution || result.ExitCode != 0 || len(h.stages) != 3 {
				t.Fatalf("unsafe archive copied: %+v %v", result, h.stages)
			}
		})
	}
}

func TestLocalImportPreservesEveryFailedStepWithoutRetry(t *testing.T) {
	for _, stage := range []string{"source", "node", "save", "mkdir", "cp", "ctr", "CRI"} {
		for _, failure := range []model.FailureType{model.FailureExit, model.FailureExecution, model.FailureTimeout, model.FailureCanceled} {
			t.Run(stage+"/"+string(failure), func(t *testing.T) {
				h := newImportHarness(t)
				h.mutate = func(current string, _ command.Request, result *model.CommandResult) {
					if current == stage {
						result.ExitCode, result.FailureType, result.Stdout, result.Stderr = 7, failure, "original stdout", "original stderr"
					}
				}
				result := h.execute(context.Background())
				if result.ExitCode != 7 || result.FailureType != failure || result.Stdout != "original stdout" || result.Stderr != "original stderr" {
					t.Fatalf("failure changed: %+v", result)
				}
				if slices.Index(h.stages, stage) != len(h.stages)-1 && !slices.Equal(h.stages[len(h.stages)-2:], []string{"rm", "rmdir"}) {
					t.Fatalf("failure scheduled more than cleanup: %v", h.stages)
				}
				if slices.Contains([]string{"source", "node", "save", "mkdir"}, stage) && slices.Contains(h.stages, "rm") {
					t.Fatal("cleanup removed a node path whose creation was not confirmed")
				}
			})
		}
	}
}

func TestLocalImportCancellationStopsSchedulingAndCleansWithFreshContext(t *testing.T) {
	for _, stage := range []string{"before", "source", "node", "save", "mkdir", "cp", "ctr", "CRI"} {
		t.Run(stage, func(t *testing.T) {
			h := newImportHarness(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if stage == "before" {
				cancel()
			}
			h.after = func(current string) {
				if current == stage {
					cancel()
				}
			}
			h.mutate = func(current string, _ command.Request, _ *model.CommandResult) {
				if current == "rm" || current == "rmdir" {
					if err := h.contexts[len(h.contexts)-1].Err(); err != nil {
						t.Fatalf("cleanup inherited cancellation: %v", err)
					}
				}
			}
			result := h.execute(ctx)
			if result.FailureType != model.FailureCanceled || result.ExitCode != -1 {
				t.Fatalf("cancellation became success: %+v", result)
			}
			if stage == "before" && len(h.stages) != 0 {
				t.Fatal("pre-canceled import executed")
			}
			if stage != "before" {
				tail := h.stages[slices.Index(h.stages, stage)+1:]
				if len(tail) > 0 && !slices.Equal(tail, []string{"rm", "rmdir"}) {
					t.Fatalf("cancellation scheduled work: %v", h.stages)
				}
			}
		})
	}
}

func TestLocalImportCleanupFailureCannotBecomeSuccessOrEraseOriginalFailure(t *testing.T) {
	for _, prior := range []bool{false, true} {
		for _, cleanup := range []string{"rm", "rmdir"} {
			t.Run(cleanup+"/prior="+map[bool]string{true: "yes", false: "no"}[prior], func(t *testing.T) {
				h := newImportHarness(t)
				h.mutate = func(stage string, _ command.Request, result *model.CommandResult) {
					if prior && stage == "ctr" {
						result.ExitCode, result.FailureType, result.Stderr = 17, model.FailureExit, "original import failure"
					}
					if stage == cleanup {
						result.ExitCode, result.FailureType, result.Stderr = 9, model.FailureExit, "cleanup failure"
					}
				}
				result := h.execute(context.Background())
				if len(h.stages) < 2 || !slices.Equal(h.stages[len(h.stages)-2:], []string{"rm", "rmdir"}) {
					t.Fatal("cleanup did not attempt both bounded removals")
				}
				if prior && (result.ExitCode != 17 || !strings.HasPrefix(result.Stderr, "original import failure\n")) {
					t.Fatalf("cleanup erased initial failure: %+v", result)
				}
				if !prior && (result.ExitCode != 9 || !strings.HasPrefix(result.Stderr, "cleanup failure\n")) {
					t.Fatalf("cleanup failure became success: %+v", result)
				}
				if strings.Count(result.Stderr, ImportCleanupWarning) != 1 {
					t.Fatal("incomplete staging cleanup was not recorded once")
				}
			})
		}
	}
}

func TestLocalImportCancellationDuringCleanupRemainsCanceled(t *testing.T) {
	h := newImportHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.after = func(stage string) {
		if stage == "rm" {
			cancel()
		}
	}
	result := h.execute(ctx)
	if result.FailureType != model.FailureCanceled || result.ExitCode != -1 || len(h.stages) != 9 || h.stages[8] != "rmdir" {
		t.Fatalf("cleanup interruption became successful import or stopped bounded cleanup: %+v %v", result, h.stages)
	}
}

func TestImportIdentityDecodersRejectAdditionalObjectsAndFields(t *testing.T) {
	valid := `{"id":"` + testNodeID + `","name":true,"owner":true,"run":true,"cluster":true,"role":true,"running":true}`
	for _, payload := range []string{valid + `{}`, valid + ` null`, `[]`, `null`, strings.Repeat(" ", imageIdentityLimit) + valid} {
		if _, ok := decodeImportNode(payload); ok {
			t.Fatal("ambiguous node observation accepted")
		}
	}
	if _, ok := decodeImportNode(valid); !ok {
		t.Fatal("complete node observation rejected")
	}
	var value map[string]any
	if err := json.Unmarshal([]byte(valid), &value); err != nil {
		t.Fatal(err)
	}
	delete(value, "running")
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := decodeImportNode(string(payload)); ok {
		t.Fatal("incomplete ownership accepted")
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	result, stopped := importCanceled(ctx)
	if !stopped || result.FailureType != model.FailureTimeout || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatal("deadline did not remain timeout")
	}
}
