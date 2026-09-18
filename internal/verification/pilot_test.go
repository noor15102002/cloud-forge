package verification

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/noor15102002/cloud-forge/internal/analyzer"
	"github.com/noor15102002/cloud-forge/internal/command"
	k6executor "github.com/noor15102002/cloud-forge/internal/executor/k6"
	"github.com/noor15102002/cloud-forge/internal/executor/kubernetes"
	"github.com/noor15102002/cloud-forge/pkg/model"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
)

func TestPilotConfigurationRejectsUnsafeOrUnknownInputs(t *testing.T) {
	for _, document := range []string{
		"schema_version: future", "schema_version: v1alpha1\nunknown: true",
		"schema_version: v1alpha1\nload: {vus: 1000, duration: 20s}",
		"schema_version: v1alpha1\nload: {vus: 2, duration: 2h}",
		"schema_version: v1alpha1\nendpoints: {load: https://example.com}",
		"schema_version: v1alpha1\nendpoints: {load: '//example.com'}",
		"schema_version: v1alpha1\nruntime: {port: 8000, port: 9000}",
	} {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "cloudforge.yaml"), []byte(document), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadConfiguration(root, ""); err == nil {
			t.Fatalf("accepted invalid configuration: %s", document)
		}
	}
}

func TestPilotPreservesProbeAndDeploymentSemantics(t *testing.T) {
	analysis, err := analyzer.New().Analyze(fixturePath(t))
	if err != nil {
		t.Fatal(err)
	}
	deployment := &analysis.Application.Kubernetes.Deployments[0]
	grace := int64(90)
	deployment.TerminationGracePeriodSeconds = &grace
	deployment.Strategy = &appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}
	deployment.Probes[0].InitialDelaySeconds = 25
	deployment.Probes[0].FailureThreshold = 10
	deployment.Probes[0].TimeoutSeconds = 4
	deployment.Probes = append(deployment.Probes, model.Probe{Purpose: "startup", Type: "tcp", Port: "8080", FailureThreshold: 30, PeriodSeconds: 2})
	current, err := buildPlan(analysis, "0123abcd")
	if err != nil {
		t.Fatal(err)
	}
	decoder := yaml.NewYAMLOrJSONDecoder(strings.NewReader(string(current.manifest)), 4096)
	var generated appsv1.Deployment
	for i := 0; i < 2; i++ {
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			if err := json.Unmarshal(raw, &generated); err != nil {
				t.Fatal(err)
			}
		}
	}
	container := generated.Spec.Template.Spec.Containers[0]
	if generated.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType || *generated.Spec.Template.Spec.TerminationGracePeriodSeconds != 90 || container.StartupProbe == nil || container.StartupProbe.FailureThreshold != 30 || container.ReadinessProbe.InitialDelaySeconds != 25 || container.ReadinessProbe.FailureThreshold != 10 || container.ReadinessProbe.TimeoutSeconds != 4 {
		t.Fatalf("source settings lost: %#v", generated.Spec)
	}
}

func TestPilotRejectsExcessiveReplicasAndUnsupportedSettingsBeforeCommands(t *testing.T) {
	for _, mutate := range []func(*model.AnalysisResult){
		func(a *model.AnalysisResult) { n := int32(99); a.Application.Kubernetes.Deployments[0].Replicas = &n },
		func(a *model.AnalysisResult) { a.Application.Kubernetes.HorizontalPodScalers[0].MaxReplicas = 99 },
		func(a *model.AnalysisResult) {
			a.Application.Kubernetes.Deployments[0].Containers[0].Resources.MemoryLimit = "64Gi"
		},
		func(a *model.AnalysisResult) {
			a.Application.Kubernetes.Deployments[0].Unsupported = []string{"spec.template.spec.containers[].env"}
		},
	} {
		a, err := analyzer.New().Analyze(fixturePath(t))
		if err != nil {
			t.Fatal(err)
		}
		mutate(&a)
		if _, err := buildPlan(a, "0123abcd"); err == nil {
			t.Fatal("unsafe/unsupported plan accepted")
		}
	}
}

func TestPilotLowCPUIsNotScalingFailure(t *testing.T) {
	hpa := `{"status":{"currentReplicas":2,"desiredReplicas":2,"currentMetrics":[{"type":"Resource","resource":{"name":"cpu","current":{"averageUtilization":1}}}]}}`
	runner := loadRunner(`{"metrics":{"http_reqs":{"values":{"count":20,"rate":10}},"http_req_failed":{"values":{"rate":0}},"http_req_duration":{"values":{"p(50)":1,"p(95)":2,"p(99)":3}}}}`, hpa)
	service := New(runner)
	service.poll = time.Millisecond
	service.hpaScaleTimeout = 5 * time.Millisecond
	current := plan{loadURL: "http://127.0.0.1:8000/work", desiredReplicas: 2, hpaTargetCPU: 70}
	_, outcome := service.runLoadAndAutoscaling(context.Background(), k6executor.New(runner), kubernetes.New(runner), current, t.TempDir(), "hpa.yaml")
	if outcome.ExitCode != 0 || outcome.Evidence.Status != model.StatusSkipped || measurementValue(outcome.Evidence.Measurements, "peak_cpu_percent") != "1" {
		t.Fatalf("idle HPA falsely failed: %#v", outcome)
	}
}

func TestPilotPrivateConfigurationAndBuildBounds(t *testing.T) {
	var calls []command.Request
	runner := runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		calls = append(calls, request)
		return successfulCommand(request)
	})
	service := fixedService(runner)
	outcome := service.Run(context.Background(), fixturePath(t), Options{})
	if outcome.ExitCode != 0 {
		t.Fatalf("unexpected result: %#v", outcome)
	}
	for _, call := range calls {
		if call.Name == "k3d" || call.Name == "kubectl" {
			found := false
			for _, entry := range call.Env {
				if strings.HasPrefix(entry, "KUBECONFIG=") {
					found = true
				}
			}
			if !found {
				t.Fatal("cluster command uses user's kubeconfig")
			}
		}
		if call.Name == "k3d" && containsArgument(call.Args, "create") {
			for _, flag := range []string{"--kubeconfig-update-default=false", "--kubeconfig-switch-context=false", "--servers-memory", "--runtime-label"} {
				if !containsArgument(call.Args, flag) {
					t.Fatalf("missing cluster isolation flag %s", flag)
				}
			}
		}
		if call.Name == "docker" && containsArgument(call.Args, "buildx") && containsArgument(call.Args, "create") && !containsArgument(call.Args, "memory=2g,cpu-period=100000,cpu-quota=200000") {
			t.Fatal("builder missing resource limits")
		}
	}
}

func TestWorkloadFingerprintIgnoresRunIDYAMLQuoting(t *testing.T) {
	analysis, err := analyzer.New().Analyze(fixturePath(t))
	if err != nil {
		t.Fatal(err)
	}
	left, err := buildPlan(analysis, "12345678")
	if err != nil {
		t.Fatal(err)
	}
	right, err := buildPlan(analysis, "abcdefab")
	if err != nil {
		t.Fatal(err)
	}
	if workloadFingerprint(left) != workloadFingerprint(right) {
		t.Fatal("generated YAML quoting changed experiment compatibility")
	}
}

func TestCompatibilityRequiresKnownCleanCloudForgeAndImageIdentity(t *testing.T) {
	runner := runnerFunc(func(_ context.Context, req command.Request) model.CommandResult {
		result := model.CommandResult{Stdout: "version 1.2.3"}
		if req.Name == "kubectl" {
			result.Stdout = `{"serverVersion":{"gitVersion":"v1.34.0"}}`
		}
		return result
	})
	for _, commit := range []string{"unknown", strings.Repeat("a", 40) + "+dirty", strings.Repeat("a", 40)} {
		fingerprint := &model.RunFingerprint{CloudForgeCommit: commit, ImageID: "sha256:" + strings.Repeat("b", 64), WorkloadHash: "test"}
		fingerprintTools(context.Background(), runner, plan{}, fingerprint)
		if (fingerprint.CompatibilityKey != "") != (commit == strings.Repeat("a", 40)) {
			t.Fatalf("wrong compatibility for build %q", commit)
		}
	}
	fingerprint := &model.RunFingerprint{CloudForgeCommit: strings.Repeat("a", 40), WorkloadHash: "test"}
	fingerprintTools(context.Background(), runner, plan{}, fingerprint)
	if fingerprint.CompatibilityKey != "" {
		t.Fatal("missing image identity established compatibility")
	}
}
