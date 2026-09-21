package verification

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/noor15102002/cloud-forge/internal/analyzer"
	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/internal/executor/kubernetes"
	"github.com/noor15102002/cloud-forge/internal/regression"
	"github.com/noor15102002/cloud-forge/pkg/model"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	"sigs.k8s.io/yaml"
)

func TestTopologyRejectsInvalidConfigurationBeforeTools(t *testing.T) {
	for _, body := range []string{
		"topology: null", "topology: {}", "topology: {replicas: 0}", "topology: {replicas: 99}",
		"topology: {replicas: 2, rollout: null}", "topology: {replicas: 2, extra: secret}",
		"topology: {replicas: 2, rollout: {strategy: recreate}}",
		"topology: {replicas: 2, rollout: {max_surge: null}}",
		"topology: {replicas: 2, rollout: {max_surge: '25%'}}",
		"topology: {replicas: 2, rollout: {max_surge: -1}}",
		"topology: {replicas: 2, rollout: {max_unavailable: 3}}",
		"topology: {replicas: 2, rollout: {max_unavailable: 0, max_surge: 0}}",
		"topology: {replicas: 2, rollout: {max_surge: 6}}",
	} {
		t.Run(body, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "cloudforge.yaml"), []byte("schema_version: v1alpha4\n"+body), 0o600); err != nil {
				t.Fatal(err)
			}
			s := New(runnerFunc(func(context.Context, command.Request) model.CommandResult {
				t.Fatal("invalid topology invoked a tool")
				return model.CommandResult{}
			}))
			out := s.Run(context.Background(), root, testOptions())
			if out.ExitCode != 2 {
				t.Fatal("invalid topology accepted")
			}
		})
	}
	for _, version := range []string{"v1alpha1", "v1alpha2", "v1alpha3"} {
		if validateTopology(model.RuntimeConfiguration{SchemaVersion: version, Topology: &model.TopologySettings{Replicas: 2}}) == nil {
			t.Fatal("old schema accepted new configuration")
		}
	}
}

func TestTopologyOverridesOnlyReplicasAndStrategy(t *testing.T) {
	a, err := analyzer.New().Analyze(fixturePath(t))
	if err != nil {
		t.Fatal(err)
	}
	a.Application.Kubernetes.HorizontalPodScalers = nil
	baseline, err := buildPlan(a, "0123abcd")
	if err != nil {
		t.Fatal(err)
	}
	config := defaultConfiguration()
	config.SchemaVersion = "v1alpha4"
	config.Topology = &model.TopologySettings{Replicas: 1}
	current, err := buildConfiguredPlan(a, "0123abcd", config)
	if err != nil {
		t.Fatal(err)
	}
	if current.topology.Origin != "explicit_test_configuration" || current.topology.ReplicaOrigin != "explicit_test_configuration" || current.topology.StrategyOrigin != "explicit_test_configuration" || current.topology.ReadinessOrigin != "source" || current.topology.Source == nil || *current.topology.MaxUnavailable != "0" || *current.topology.MaxSurge != "1" {
		t.Fatalf("wrong provenance: %#v", current.topology)
	}
	if current.effectiveResources != baseline.effectiveResources || current.desiredReplicas != 1 {
		t.Fatal("wrong effective deployment")
	}
	// Compare actual manifests after removing only the two authorized overrides.
	documents := func(data []byte) []map[string]any {
		t.Helper()
		var out []map[string]any
		for _, doc := range strings.Split(string(data), "---") {
			var value map[string]any
			if err := yaml.Unmarshal([]byte(doc), &value); err != nil {
				t.Fatal(err)
			}
			if value["kind"] == "Deployment" {
				spec := value["spec"].(map[string]any)
				delete(spec, "replicas")
				delete(spec, "strategy")
			}
			out = append(out, value)
		}
		return out
	}
	x, _ := json.Marshal(documents(baseline.manifest))
	y, _ := json.Marshal(documents(current.manifest))
	if string(x) != string(y) {
		t.Fatal("topology changed probes, resources or other runtime settings")
	}
	if config.Topology.Rollout != nil {
		t.Fatal("planning mutated caller configuration")
	}
	// Explicit reduction is permitted, but implicit capping remains prohibited.
	excessive := int32(99)
	a.Application.Kubernetes.Deployments[0].Replicas = &excessive
	if _, err := buildConfiguredPlan(a, "0123abcd", config); err != nil {
		t.Fatal(err)
	}
	if _, err := buildPlan(a, "0123abcd"); err == nil {
		t.Fatal("source replicas silently capped")
	}
	a.Application.Kubernetes.HorizontalPodScalers = []model.HorizontalPodAutoscaler{{MaxReplicas: 3}}
	if _, err := buildConfiguredPlan(a, "0123abcd", config); err == nil {
		t.Fatal("HPA could change fixed test topology")
	}
}

func TestTopologyDeterminismBudgetAndCompatibility(t *testing.T) {
	a, err := analyzer.New().Analyze(fixturePath(t))
	if err != nil {
		t.Fatal(err)
	}
	a.Application.Kubernetes.HorizontalPodScalers = nil
	config := defaultConfiguration()
	config.SchemaVersion = "v1alpha4"
	config.Topology = &model.TopologySettings{Replicas: 2}
	p, err := buildConfiguredPlan(a, "0123abcd", config)
	if err != nil {
		t.Fatal(err)
	}
	repeat, err := buildConfiguredPlan(a, "0123abcd", config)
	if err != nil {
		t.Fatal(err)
	}
	if string(p.manifest) != string(repeat.manifest) {
		t.Fatal("nondeterministic manifest")
	}
	config.Topology.Replicas = 1
	other, err := buildConfiguredPlan(a, "0123abcd", config)
	if err != nil {
		t.Fatal(err)
	}
	if workloadFingerprint(p) == workloadFingerprint(other) {
		t.Fatal("topology omitted from fingerprint")
	}
	makeFingerprint := func(p plan) *model.RunFingerprint {
		fp := newFingerprint(context.Background(), runnerFunc(func(_ context.Context, r command.Request) model.CommandResult { return successfulCommand(r) }), fixturePath(t), p, testOptions())
		fp.ImageID = "sha256:" + strings.Repeat("b", 64)
		for _, name := range []string{"docker", "k3d", "kubectl", "kubernetes", "k6", "trivy"} {
			fp.Tools = append(fp.Tools, model.ToolVersion{Name: name, Version: "test"})
		}
		completeFingerprint(fp)
		return fp
	}
	if makeFingerprint(p).CompatibilityKey == makeFingerprint(other).CompatibilityKey {
		t.Fatal("cross-topology baseline accepted")
	}
	a.Application.Kubernetes.Deployments[0].Containers[0].Resources.CPULimit = "2"
	config.Topology.Replicas = 2
	if _, err := buildConfiguredPlan(a, "0123abcd", config); err == nil {
		t.Fatal("surge excluded from resource budget")
	}
}

func TestTopologyPlansAndReportsUseVersionedSchemas(t *testing.T) {
	root := fixtureNamedPath(t, "monorepo")
	configPath := filepath.Join(t.TempDir(), "runtime.json")
	config, err := loadConfiguration(root, "")
	if err != nil {
		t.Fatal(err)
	}
	config.SchemaVersion = "v1alpha4"
	config.Topology = &model.TopologySettings{Replicas: 2}
	data, _ := json.Marshal(config)
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	s := New(runnerFunc(func(context.Context, command.Request) model.CommandResult {
		t.Fatal("plan invoked tools")
		return model.CommandResult{}
	}))
	opts := Options{PlanOnly: true, ConfigPath: configPath}
	out := s.Run(context.Background(), root, opts)
	if out.ExitCode != 0 {
		t.Fatalf("plan failed: %#v", out.Run.Diagnostics)
	}
	repeat := s.Run(context.Background(), root, opts)
	planJSON, _ := json.Marshal(out.Run.Plan)
	again, _ := json.Marshal(repeat.Run.Plan)
	if string(planJSON) != string(again) {
		t.Fatal("plan is not deterministic")
	}
	for name, value := range map[string][]byte{"runtime.v1alpha4": data, "plan." + model.VerificationSchemaVersion: planJSON} {
		compiler := jsonschema.NewCompiler()
		schema, err := compiler.Compile("../../schemas/" + name + ".schema.json")
		if err != nil {
			t.Fatal(err)
		}
		var doc any
		if err := json.Unmarshal(value, &doc); err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(doc); err != nil {
			t.Fatal(err)
		}
	}
	// A current report must survive strict loading without converting FAIL.
	out.Run.Status = model.StatusFail
	report, _ := json.Marshal(out.Run)
	path := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(path, report, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := regression.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != model.StatusFail || loaded.Plan.Topology.Origin != "explicit_test_configuration" {
		t.Fatal("report changed evidence")
	}
}

func TestDeletionObservesReadyFloorAndJoinsCancellation(t *testing.T) {
	for _, cancelObservation := range []bool{false, true} {
		started := make(chan struct{})
		release := make(chan struct{})
		var finished atomic.Bool
		runner := runnerFunc(func(ctx context.Context, r command.Request) model.CommandResult {
			if containsArgument(r.Args, "delete") {
				close(started)
				select {
				case <-release:
				case <-ctx.Done():
				}
				finished.Store(true)
				return model.CommandResult{}
			}
			<-started
			if cancelObservation {
				return model.CommandResult{Stdout: "malformed"}
			}
			select {
			case <-release:
			default:
				close(release)
			}
			return model.CommandResult{Stdout: `{"items":[{"metadata":{"name":"remaining"},"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}`}
		})
		s := New(runner)
		s.poll = time.Millisecond
		_, minimum, err := s.deletePodObserved(context.Background(), kubernetes.New(runner), plan{}, "app=test", "target", 2)
		if !finished.Load() || (cancelObservation && err == nil) || (!cancelObservation && (err != nil || minimum != 1)) {
			t.Fatalf("bad joined observation: %d %v", minimum, err)
		}
	}
}

func TestReadyFloorExcludesTerminatingPods(t *testing.T) {
	ready, total, _ := summarizePods([]kubernetes.PodState{{Ready: true}, {Ready: true, Terminating: true}, {Ready: false}})
	if ready != 1 || total != 3 {
		t.Fatal("terminating pod inflated Ready floor")
	}
}

func TestExplicitTopologyCancellationCleansUp(t *testing.T) {
	root := fixtureNamedPath(t, "monorepo")
	config, err := loadConfiguration(root, "")
	if err != nil {
		t.Fatal(err)
	}
	config.SchemaVersion = "v1alpha4"
	config.Topology = &model.TopologySettings{Replicas: 2}
	data, _ := json.Marshal(config)
	path := filepath.Join(t.TempDir(), "runtime.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
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
	opts := testOptions()
	opts.ConfigPath = path
	out := fixedService(runner).Run(ctx, root, opts)
	if out.ExitCode != 2 || !hasCommand(calls, "docker", "rm") || hasCommand(calls, "k3d", "create") || out.Run.Plan.Topology.Origin != "explicit_test_configuration" {
		t.Fatal("cancellation lost cleanup or topology evidence")
	}
}
