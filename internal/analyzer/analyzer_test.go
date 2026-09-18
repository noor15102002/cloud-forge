package analyzer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/noor15102002/cloud-forge/pkg/model"
)

func TestAnalyzeNodeFixture(t *testing.T) {
	result, err := New().Analyze(filepath.Join("..", "..", "testdata", "metadata-node"))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Supported || result.Status != "warn" {
		t.Fatalf("unexpected support/status: %#v", result)
	}
	if result.Application.Name != "healthy-node-api" || len(result.Application.Runtimes) != 1 {
		t.Fatalf("unexpected application: %#v", result.Application)
	}
	runtime := result.Application.Runtimes[0]
	if runtime.Language != "typescript" || runtime.Framework != "express" || runtime.PackageManager != "npm" {
		t.Fatalf("unexpected runtime: %#v", runtime)
	}
	if len(result.Application.Containers) != 1 || result.Application.Containers[0].User != "node" || result.Application.Containers[0].Ports[0].Port != 8080 {
		t.Fatalf("unexpected Docker analysis: %#v", result.Application.Containers)
	}
	kubernetes := result.Application.Kubernetes
	if len(kubernetes.Deployments) != 1 || len(kubernetes.Services) != 1 || len(kubernetes.HorizontalPodScalers) != 1 || len(kubernetes.OtherResources) != 2 {
		t.Fatalf("unexpected Kubernetes analysis: %#v", kubernetes)
	}
	if kubernetes.Deployments[0].Containers[0].Resources.CPURequest != "100m" || kubernetes.Deployments[0].Endpoints[0].Path != "/ready" {
		t.Fatalf("missing Kubernetes evidence: %#v", kubernetes.Deployments[0])
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "never-include-this-value") {
		t.Fatal("secret value leaked into analysis")
	}
}

func TestAnalyzePythonFixtureIsDeterministic(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "metadata-python")
	first, err := New().Analyze(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := New().Analyze(path)
	if err != nil {
		t.Fatal(err)
	}
	left, _ := json.Marshal(first)
	right, _ := json.Marshal(second)
	if string(left) != string(right) {
		t.Fatalf("analysis is not deterministic:\n%s\n%s", left, right)
	}
	if first.Application.Runtimes[0].Framework != "fastapi" || len(first.Application.Dependencies) != 2 {
		t.Fatalf("unexpected Python analysis: %#v", first.Application)
	}
}

func TestConflictingCandidatesArePreserved(t *testing.T) {
	result, err := New().Analyze(filepath.Join("..", "..", "testdata", "conflicting"))
	if err != nil {
		t.Fatal(err)
	}
	runtime := result.Application.Runtimes[0]
	if runtime.Framework != "" || len(runtime.FrameworkCandidates) != 2 || !hasDiagnostic(result, "multiple_frameworks") {
		t.Fatalf("framework ambiguity was not preserved: %#v", runtime)
	}
	deployments := result.Application.Kubernetes.Deployments
	if len(deployments) != 2 || deployments[0].Name != "api-a" || deployments[1].Name != "api-b" {
		t.Fatalf("multiple workloads were not preserved: %#v", deployments)
	}
	if deployments[0].Containers[0].Resources.CPURequest != "" || deployments[0].Containers[0].Ports[0].Port != 3000 || deployments[1].Containers[0].Ports[0].Port != 8080 {
		t.Fatalf("missing resources or conflicting ports were misrepresented: %#v", deployments)
	}
}

func TestStaticFindingsCoverHealthyAndBrokenConfiguration(t *testing.T) {
	healthy, err := New().Analyze(filepath.Join("..", "..", "testdata", "metadata-node"))
	if err != nil {
		t.Fatal(err)
	}
	if healthy.Status != model.StatusWarn {
		t.Fatalf("healthy fixture has unexpected findings: %#v", healthy.Findings)
	}
	for _, id := range []string{
		"container.dockerfile.non-root",
		"container.dockerfile.port",
		"kubernetes.deployment.default-node-api.health-probe",
		"kubernetes.deployment.default-node-api.readiness-probe",
		"kubernetes.deployment.default-node-api.replicas",
		"kubernetes.deployment.default-node-api.resources",
		"kubernetes.hpa.default-node-api.range",
		"kubernetes.service.default-node-api.ports",
	} {
		finding := findFinding(healthy.Findings, id)
		if finding == nil || finding.Status != model.StatusPass {
			t.Fatalf("missing healthy finding %q: %#v", id, finding)
		}
	}

	broken, err := New().Analyze(filepath.Join("..", "..", "testdata", "broken-config"))
	if err != nil {
		t.Fatal(err)
	}
	if broken.Status != model.StatusFail {
		t.Fatalf("broken fixture should fail static checks: %#v", broken.Findings)
	}
	for _, id := range []string{
		"container.dockerfile.non-root",
		"kubernetes.deployment.default-broken-api.health-probe",
		"kubernetes.deployment.default-broken-api.readiness-probe",
		"kubernetes.deployment.default-broken-api.resources",
		"kubernetes.hpa.default-broken-api.range",
		"kubernetes.service.default-broken-api.ports",
	} {
		finding := findFinding(broken.Findings, id)
		if finding == nil || finding.Status != model.StatusFail || finding.Source == nil {
			t.Fatalf("missing broken finding %q: %#v", id, finding)
		}
	}
	if finding := findFinding(broken.Findings, "container.dockerfile.port"); finding == nil || finding.Status != model.StatusWarn {
		t.Fatalf("multiple ports should be preserved as a warning: %#v", finding)
	}
}

func TestUnsupportedAndMalformedInput(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "README.md"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := New().Analyze(directory)
	if err != nil {
		t.Fatal(err)
	}
	if result.Supported || result.Status != "fail" {
		t.Fatalf("unexpected unsupported result: %#v", result)
	}
	if err := os.WriteFile(filepath.Join(directory, "package.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err = New().Analyze(directory)
	if err != nil {
		t.Fatal(err)
	}
	if !hasDiagnostic(result, "manifest_invalid") {
		t.Fatalf("expected invalid manifest diagnostic: %#v", result.Diagnostics)
	}
}

func TestSymlinkIsNotRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions differ on Windows")
	}
	directory := t.TempDir()
	outside := filepath.Join(t.TempDir(), "package.json")
	if err := os.WriteFile(outside, []byte(`{"name":"outside","dependencies":{"express":"1"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(directory, "package.json")); err != nil {
		t.Fatal(err)
	}
	requirements := filepath.Join(t.TempDir(), "requirements.txt")
	if err := os.WriteFile(requirements, []byte("flask\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(requirements, filepath.Join(directory, "requirements.txt")); err != nil {
		t.Fatal(err)
	}
	result, err := New().Analyze(directory)
	if err != nil {
		t.Fatal(err)
	}
	if result.Supported || !hasDiagnostic(result, "symlink_skipped") {
		t.Fatalf("symlink should not be analyzed: %#v", result)
	}
}

func TestMalformedKubernetesAndOversizedManifestAreDiagnostics(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "package.json"), []byte(`{"name":"api","dependencies":{"express":"1"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "requirements.txt"), []byte("flask\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	badYAML := "apiVersion: apps/v1\nkind: Deployment\nspec:\n  bad: [\n"
	if err := os.WriteFile(filepath.Join(directory, "broken.yaml"), []byte(badYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "large.yml"), append([]byte("apiVersion: v1\nkind: Service\n#"), make([]byte, maxFileSize)...), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := New().Analyze(directory)
	if err != nil {
		t.Fatal(err)
	}
	if !hasDiagnostic(result, "kubernetes_invalid") || !hasDiagnostic(result, "manifest_unreadable") {
		t.Fatalf("expected bounded malformed input diagnostics: %#v", result.Diagnostics)
	}
}

func hasDiagnostic(result model.AnalysisResult, code string) bool {
	for _, item := range result.Diagnostics {
		if item.Code == code {
			return true
		}
	}
	return false
}

func findFinding(values []model.Finding, id string) *model.Finding {
	for index := range values {
		if values[index].ID == id {
			return &values[index]
		}
	}
	return nil
}
