package verification

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noor15102002/cloud-forge/internal/analyzer"
	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/internal/regression"
	"github.com/noor15102002/cloud-forge/pkg/model"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	"sigs.k8s.io/yaml"
)

func selectedFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.CopyFS(filepath.Join(root, "apps", "api"), os.DirFS(fixturePath(t))); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "apps/api/Dockerfile"), filepath.Join(root, "apps/api/Production")); err != nil {
		t.Fatal(err)
	}
	config := "schema_version: v1alpha3\nbuild: {app: apps/api, dockerfile: apps/api/Production, context: .}\n"
	if err := os.WriteFile(filepath.Join(root, "cloudforge.yaml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestSelectionPlansAreDeterministicWithoutRuntimeTools(t *testing.T) {
	root := selectedFixture(t)
	service := New(runnerFunc(func(context.Context, command.Request) model.CommandResult {
		t.Fatal("plan executed a subprocess")
		return model.CommandResult{}
	}))
	options := Options{PlanOnly: true}
	first := service.Run(context.Background(), root, options)
	second := service.Run(context.Background(), root, options)
	if first.ExitCode != 0 || first.Run.Plan.Build == nil {
		t.Fatalf("blocked plan: %#v", first)
	}
	a, _ := json.Marshal(first.Run.Plan)
	b, _ := json.Marshal(second.Run.Plan)
	if string(a) != string(b) || strings.Contains(string(a), root) {
		t.Fatal("plan exposed absolute paths or generated identities")
	}
	if first.Run.Plan.Build.Dockerfile != "apps/api/Production" || first.Run.Plan.Build.App != "apps/api" || first.Run.Plan.Topology.Source.Path != "apps/api/k8s/app.yaml" {
		t.Fatalf("lost selection provenance: %s", a)
	}
}

func TestSelectedBuildsAndSourceIdentityUseRepositoryBoundary(t *testing.T) {
	root := selectedFixture(t)
	var builds []command.Request
	runner := runnerFunc(func(_ context.Context, r command.Request) model.CommandResult {
		if r.Name == "docker" && containsArgument(r.Args, "build") {
			builds = append(builds, r)
		}
		if r.Name == "git" && r.Dir != root {
			t.Fatal("git fingerprint ignored root-context inputs")
		}
		return successfulCommand(r)
	})
	out := fixedService(runner).Run(context.Background(), root, testOptions())
	if len(builds) != 2 {
		t.Fatalf("expected A and B builds: %#v, %#v", builds, out.Run.Diagnostics)
	}
	for i, r := range builds {
		if r.Dir != root || !containsArgument(r.Args, "./apps/api/Production") || r.Args[len(r.Args)-1] != "./." || !containsArgument(r.Args, "CLOUDFORGE_VERSION="+[]string{"a", "b"}[i]) {
			t.Fatalf("inconsistent build: %#v", r)
		}
	}
	if out.Run.Fingerprint.Configuration.Build == nil || out.Run.Fingerprint.Configuration.Build.Context != "." {
		t.Fatal("fingerprint omitted effective build")
	}
	for _, f := range out.Run.Findings {
		if f.ID == "container.build" && (f.Source == nil || f.Source.Path != "apps/api/Production") {
			t.Fatal("wrong build finding source")
		}
	}
}

func TestSelectedBuildCancellationPreservesCleanup(t *testing.T) {
	root := selectedFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls []command.Request
	runner := runnerFunc(func(ctx context.Context, r command.Request) model.CommandResult {
		calls = append(calls, r)
		if r.Name == "docker" && containsArgument(r.Args, "build") {
			cancel()
			return model.CommandResult{ExitCode: -1, FailureType: model.FailureCanceled}
		}
		if r.Name == "docker" && containsArgument(r.Args, "rm") && ctx.Err() != nil {
			t.Fatal("cleanup inherited cancellation")
		}
		return successfulCommand(r)
	})
	out := fixedService(runner).Run(ctx, root, testOptions())
	if out.ExitCode != 2 || !hasCommand(calls, "docker", "rm") || hasCommand(calls, "k3d", "create") || !hasDiagnosticCode(out.Run.Diagnostics, "verification_canceled") {
		t.Fatalf("cancellation/cleanup lost: %#v", out)
	}
}

func TestInvalidBuildSelectionNeverRunsTools(t *testing.T) {
	for _, config := range []string{
		"schema_version: v1alpha2\nbuild: {app: ., dockerfile: Dockerfile, context: .}",
		"schema_version: v1alpha3\nbuild: null",
		"schema_version: v1alpha3\nbuild: {}",
		"schema_version: v1alpha3\nbuild: {app: ., dockerfile: Dockerfile, context: ../outside}",
		"schema_version: v1alpha3\nbuild: {app: missing, dockerfile: missing, context: .}",
	} {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "cloudforge.yaml"), []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
		service := New(runnerFunc(func(context.Context, command.Request) model.CommandResult {
			t.Fatal("invalid selection invoked runtime")
			return model.CommandResult{}
		}))
		out := service.Run(context.Background(), root, Options{PlanOnly: true})
		if out.ExitCode != 2 {
			t.Fatalf("invalid config accepted: %s", config)
		}
	}
}

func TestBuildSelectionChangesCompatibility(t *testing.T) {
	root := selectedFixture(t)
	config, err := loadConfiguration(root, "")
	if err != nil {
		t.Fatal(err)
	}
	a, err := analyzer.New().AnalyzeSelected(root, config.Build)
	if err != nil {
		t.Fatal(err)
	}
	p, err := buildConfiguredPlan(a, "0123abcd", config)
	if err != nil {
		t.Fatal(err)
	}
	fp := newFingerprint(context.Background(), runnerFunc(func(_ context.Context, r command.Request) model.CommandResult { return successfulCommand(r) }), root, p, testOptions())
	fp.ImageID = "sha256:" + strings.Repeat("b", 64)
	for _, name := range []string{"docker", "k3d", "kubectl", "kubernetes", "k6", "trivy"} {
		fp.Tools = append(fp.Tools, model.ToolVersion{Name: name, Version: "test"})
	}
	completeFingerprint(fp)
	original := fp.CompatibilityKey
	changed := *fp.Configuration.Build
	changed.Context = "apps/api"
	fp.Configuration.Build = &changed
	completeFingerprint(fp)
	if original == "" || original == fp.CompatibilityKey {
		t.Fatal("different build contexts compared as compatible")
	}
}

func TestSelectedAndHistoricalReportsPreserveEvidence(t *testing.T) {
	for _, version := range []string{"v1alpha5", "v1alpha4", "v1alpha3"} {
		root := selectedFixture(t)
		if version == "v1alpha3" {
			root = fixturePath(t)
		}
		result := fixedService(runnerFunc(func(_ context.Context, r command.Request) model.CommandResult { return successfulCommand(r) })).Run(context.Background(), root, testOptions()).Run
		result.SchemaVersion = version
		result.Plan.SchemaVersion = version
		result.Plan.Resources = nil // This field was introduced after these historical schemas.
		if version == "v1alpha3" {
			result.Plan.Build = nil
		}
		// Preserve an observed failure on load; restoration must not reinterpret it.
		result.Status = model.StatusFail
		result.Evidence[0].Status = model.StatusFail
		data, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(t.TempDir(), "report.json")
		if err := os.WriteFile(file, data, 0o600); err != nil {
			t.Fatal(err)
		}
		loaded, err := regression.Load(file)
		if err != nil {
			t.Fatal(version, err)
		}
		if loaded.Status != model.StatusFail || loaded.Evidence[0].Status != model.StatusFail || len(loaded.Evidence) != len(result.Evidence) {
			t.Fatal("historical observations changed")
		}
	}
}

func TestPublishedSelectionConfigurationAndPlanSchemas(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "monorepo")
	config, err := os.ReadFile("../../testdata/monorepo/cloudforge.yaml")
	if err != nil {
		t.Fatal(err)
	}
	config, err = yaml.YAMLToJSON(config)
	if err != nil {
		t.Fatal(err)
	}
	out := New(runnerFunc(func(context.Context, command.Request) model.CommandResult {
		t.Fatal("plan ran tools")
		return model.CommandResult{}
	})).Run(context.Background(), root, Options{PlanOnly: true})
	planned, err := json.Marshal(out.Run.Plan)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"runtime.v1alpha3": config, "plan." + model.VerificationSchemaVersion: planned} {
		var document any
		if err := json.Unmarshal(data, &document); err != nil {
			t.Fatal(err)
		}
		compiler := jsonschema.NewCompiler()
		schema, err := compiler.Compile(filepath.Join("..", "..", "schemas", name+".schema.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(document); err != nil {
			t.Fatal(err)
		}
	}
}
