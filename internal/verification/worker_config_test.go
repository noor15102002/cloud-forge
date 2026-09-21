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
	"github.com/noor15102002/cloud-forge/pkg/model"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/yaml"
)

func TestWorkerConfigurationRejectsUnsafeOrMixedContractsBeforeTools(t *testing.T) {
	for name, body := range map[string]string{
		"old-schema":       strings.Replace(workerTestConfiguration, "v1alpha6", "v1alpha5", 1),
		"unknown-kind":     strings.Replace(workerTestConfiguration, "kind: worker", "kind: unknown", 1),
		"replicas":         strings.Replace(workerTestConfiguration, "replicas: 1", "replicas: 2", 1),
		"rolling":          strings.Replace(workerTestConfiguration, "strategy: recreate", "strategy: rolling_update", 1),
		"surge":            strings.Replace(workerTestConfiguration, "strategy: recreate", "strategy: recreate, max_surge: 1", 1),
		"no-redis":         strings.Replace(workerTestConfiguration, "enabled: true", "enabled: false", 1),
		"no-restriction":   strings.Replace(workerTestConfiguration, "network: {outbound: declared_dependencies_only}\n", "", 1),
		"null-worker":      strings.Replace(workerTestConfiguration, "worker:\n  command: [node, worker-contract-marker.js]\n  heartbeat: {key: contract-key-marker, timestamp_field: at, max_age: 30s}", "worker: null", 1),
		"port":             strings.Replace(workerTestConfiguration, "kind: worker", "kind: worker, port: 8080", 1),
		"shell":            strings.Replace(workerTestConfiguration, "[node, worker-contract-marker.js]", "[sh, -c, command]", 1),
		"eval":             strings.Replace(workerTestConfiguration, "[node, worker-contract-marker.js]", "[node, -e, command]", 1),
		"duration":         strings.Replace(workerTestConfiguration, "max_age: 30s", "max_age: 30000ms", 1),
		"nested-timestamp": strings.Replace(workerTestConfiguration, "timestamp_field: at", "timestamp_field: nested.at", 1),
		"unknown":          workerTestConfiguration + "private_setting: hidden\n",
		"load":             workerTestConfiguration + "load: {vus: 1, duration: 1s}\n",
		"endpoints":        workerTestConfiguration + "endpoints: {}\n",
		"http-readiness":   workerTestConfiguration + "readiness: {status: 200}\n",
		"control":          workerTestConfiguration + "experiments: {}\n",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if os.WriteFile(filepath.Join(root, "cloudforge.yaml"), []byte(body), 0600) != nil {
				t.Fatal("fixture write")
			}
			s := New(runnerFunc(func(context.Context, command.Request) model.CommandResult {
				t.Fatal("invalid worker config invoked a tool")
				return model.CommandResult{}
			}))
			if out := s.Run(context.Background(), root, testOptions()); out.ExitCode != 2 {
				t.Fatalf("invalid contract accepted: %+v", out.Run.Diagnostics)
			}
		})
	}
}

func TestWorkerPlanIsDeterministicExplicitAndHasNoHTTPPrerequisites(t *testing.T) {
	root, current := workerFixture(t)
	s := New(runnerFunc(func(context.Context, command.Request) model.CommandResult {
		t.Fatal("plan invoked a runtime tool")
		return model.CommandResult{}
	}))
	options := testOptions()
	options.PlanOnly = true
	first := s.Run(context.Background(), root, options)
	second := s.Run(context.Background(), root, options)
	a, _ := json.Marshal(first.Run.Plan)
	b, _ := json.Marshal(second.Run.Plan)
	if first.ExitCode != 0 || string(a) != string(b) {
		t.Fatalf("invalid/nondeterministic worker plan: %+v", first.Run.Diagnostics)
	}
	p := first.Run.Plan
	if p.SchemaVersion != "v1alpha7" || p.RuntimeKind != "worker" || p.Port != 0 || p.Worker == nil || p.Worker.Claim != "process_liveness_only" || p.Topology.Origin != "explicit_test_configuration" || p.Topology.Strategy != "recreate" || p.Topology.Replicas != 1 || p.Topology.ReadinessOrigin != "worker_heartbeat" {
		t.Fatalf("wrong worker semantics: %+v", p)
	}
	for _, c := range p.Capabilities {
		if strings.HasPrefix(c.Name, "worker-") && c.Disposition != "supported" {
			t.Fatalf("worker capability blocked: %+v", c)
		}
	}
	for _, forbidden := range []string{"kind: Service", "containerPort:", "readinessProbe:", "livenessProbe:", "startupProbe:"} {
		if strings.Contains(string(current.manifest), forbidden) {
			t.Fatalf("HTTP configuration fabricated: %s", forbidden)
		}
	}
	compiler := jsonschema.NewCompiler()
	schema, err := compiler.Compile(filepath.Join("..", "..", "schemas", "plan.v1alpha7.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if json.Unmarshal(a, &doc) != nil {
		t.Fatal("invalid JSON")
	}
	if err := schema.Validate(doc); err != nil {
		t.Fatal(err)
	}
	safe, _ := json.Marshal(safeConfiguration(current.config))
	for _, secret := range []string{"worker-contract-marker.js", "contract-key-marker"} {
		if strings.Contains(string(a), secret) || strings.Contains(string(safe), secret) {
			t.Fatal("worker contract disclosed")
		}
	}
	hash := workerContract(current.config).ContractHash
	changed := current.config
	worker := *changed.Worker
	worker.Command = []string{"node", "different.js"}
	changed.Worker = &worker
	if hash == workerContract(changed).ContractHash {
		t.Fatal("command change not bound to fingerprint")
	}
	worker.Command = current.config.Worker.Command
	worker.Heartbeat.Key = "different-key"
	if hash == workerContract(changed).ContractHash {
		t.Fatal("key change not bound to fingerprint")
	}
}

func TestWorkerSourceTimingIsPreservedAndUnsupportedSettingsAreBlocked(t *testing.T) {
	root, current := workerFixture(t)
	analysis, err := analyzer.New().Analyze(root)
	if err != nil {
		t.Fatal(err)
	}
	grace, deadline := int64(45), int32(90)
	source := model.Deployment{Name: "worker", Containers: []model.Container{{}}, TerminationGracePeriodSeconds: &grace, MinReadySeconds: 7, ProgressDeadlineSeconds: &deadline}
	analysis.Application.Kubernetes.Deployments = []model.Deployment{source}
	got, err := buildConfiguredPlan(analysis, "0123abcd", current.config)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, document := range strings.Split(string(got.manifest), "---") {
		var deployment appsv1.Deployment
		if yaml.Unmarshal([]byte(document), &deployment) != nil {
			t.Fatal("invalid generated manifest")
		}
		if deployment.Kind == "Deployment" {
			found = true
			if deployment.Spec.MinReadySeconds != 7 || *deployment.Spec.ProgressDeadlineSeconds != 90 || *deployment.Spec.Template.Spec.TerminationGracePeriodSeconds != 45 || got.topology.MinReadySeconds != 7 {
				t.Fatal("source timing silently replaced")
			}
		}
	}
	if !found {
		t.Fatal("missing Deployment")
	}
	for _, mutation := range []func(*model.Application){
		func(a *model.Application) { a.Kubernetes.Deployments[0].Unsupported = []string{"unsupported-setting"} },
		func(a *model.Application) { a.Kubernetes.Deployments[0].Probes = []model.Probe{{}} },
		func(a *model.Application) { a.Kubernetes.Services = []model.Service{{Name: "source-service"}} },
	} {
		clone := analysis
		clone.Application.Kubernetes.Deployments = []model.Deployment{source}
		mutation(&clone.Application)
		if _, err := buildConfiguredPlan(clone, "0123abcd", current.config); err == nil {
			t.Fatal("source semantics silently discarded")
		}
	}
}
