package render

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/noor15102002/cloud-forge/pkg/model"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func TestVerificationReportsMatchGoldenContracts(t *testing.T) {
	run := reportFixture()
	var textOutput, markdownOutput, jsonOutput bytes.Buffer
	if err := VerificationText(&textOutput, run); err != nil {
		t.Fatal(err)
	}
	if err := VerificationMarkdown(&markdownOutput, run); err != nil {
		t.Fatal(err)
	}
	if err := JSON(&jsonOutput, run); err != nil {
		t.Fatal(err)
	}
	assertGoldenBytes(t, "verification.txt", textOutput.Bytes())
	assertGoldenBytes(t, "verification.md", markdownOutput.Bytes())
	assertGoldenJSON(t, "verification.json", jsonOutput.Bytes())
	if strings.Contains(markdownOutput.String(), "<script>") || !strings.Contains(markdownOutput.String(), "&lt;script&gt;") || !strings.Contains(markdownOutput.String(), `checkout&#124;api`) {
		t.Fatalf("Markdown did not escape untrusted report text:\n%s", markdownOutput.String())
	}
	if strings.Contains(textOutput.String(), "threshold.\nInvestigate") {
		t.Fatalf("terminal output retained an embedded control character:\n%s", textOutput.String())
	}
}

func TestMarkdownTextNeutralizesCommentMarkup(t *testing.T) {
	got := markdownText(`@team [link](https://example.test) # heading \| split`)
	for _, unsafe := range []string{"@team", "[link]", "https://", "# heading", `\|`} {
		if strings.Contains(got, unsafe) {
			t.Fatalf("Markdown text retained unsafe construct %q: %s", unsafe, got)
		}
	}
}

func TestVerificationJSONSortsCollectionsWithoutMutatingInput(t *testing.T) {
	run := reportFixture()
	run.Comparison.Regressions = append(run.Comparison.Regressions, model.ComparisonChange{ExperimentID: "container-scan", Kind: model.ComparisonStatus, Baseline: "pass", Current: "warn", Summary: "status changed from pass to warn"})
	run.Comparison.Improvements = append(run.Comparison.Improvements, model.ComparisonChange{ExperimentID: "pod-recovery", Kind: model.ComparisonStatus, Baseline: "fail", Current: "pass", Summary: "status changed from fail to pass"})
	run.Comparison.Unavailable = append(run.Comparison.Unavailable, model.ComparisonUnavailable{ExperimentID: "new-experiment", Kind: model.ComparisonStatus, Reason: "baseline does not contain this current experiment"})
	originalFirstEvidence := run.Evidence[0].ExperimentID
	var first, second bytes.Buffer
	if err := JSON(&first, run); err != nil {
		t.Fatal(err)
	}
	if run.Evidence[0].ExperimentID != originalFirstEvidence {
		t.Fatal("rendering mutated the caller's evidence order")
	}
	reverseEvidence(run.Evidence)
	reverseFindings(run.Findings)
	reverseDiagnostics(run.Diagnostics)
	reverseComparisonChanges(run.Comparison.Regressions)
	reverseComparisonChanges(run.Comparison.Improvements)
	reverseUnavailable(run.Comparison.Unavailable)
	for index := range run.Evidence {
		reverseMeasurements(run.Evidence[index].Measurements)
	}
	if err := JSON(&second, run); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatalf("canonical JSON changed when collection input order changed:\n%s\n%s", first.Bytes(), second.Bytes())
	}
}

func TestVerificationSchemaIsVersionedJSON(t *testing.T) {
	path := filepath.Join("..", "..", "schemas", "verification.v1alpha1.schema.json")
	// #nosec G304 -- the schema path is a fixed repository test fixture.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	if schema["$schema"] != "https://json-schema.org/draft/2020-12/schema" || schema["title"] != "CloudForge Verification Report v1alpha1" {
		t.Fatalf("unexpected schema identity: %#v", schema)
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err := compiler.AddResource("verification.v1alpha1.schema.json", schema); err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile("verification.v1alpha1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := JSON(&output, reportFixture()); err != nil {
		t.Fatal(err)
	}
	var report any
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if err := compiled.Validate(report); err != nil {
		t.Fatalf("verification JSON does not satisfy its schema: %v", err)
	}
}

func reportFixture() model.VerificationRun {
	return model.VerificationRun{
		SchemaVersion: model.SchemaVersion, RunID: "run-123", Status: model.StatusWarn, Application: "checkout|api <script>",
		StartedAt: "2026-09-18T12:00:00Z", DurationMS: 1250,
		Environment: model.VerificationEnvironment{Backend: "k3d", ClusterName: "cloudforge-run-123", Namespace: "cloudforge", Endpoint: "http://127.0.0.1:18080/ready"},
		Evidence: []model.Evidence{
			{ExperimentID: "load-profile", Title: "Bounded load", Status: model.StatusWarn, Summary: "Latency crossed | target", DurationMS: 800, Measurements: []model.Measurement{{Name: "latency_p95_ms", Value: "120.000", Unit: "ms"}, {Name: "request_count", Value: "200", Unit: "requests"}}},
			{ExperimentID: "container-build", Title: "Container build", Status: model.StatusPass, Summary: "Image built.", DurationMS: 400},
			{ExperimentID: "horizontal-autoscaling", Title: "Horizontal autoscaling", Status: model.StatusSkipped, Summary: "No HPA configured."},
		},
		Findings: []model.Finding{
			{ID: "runtime.load-profile", Category: "reliability", Status: model.StatusWarn, Severity: model.SeverityMedium, Summary: "Latency exceeded the expected threshold.\nInvestigate.", Observed: "120 ms", Expected: "below 100 ms", Remediation: "Tune `worker` count.", DurationMS: 800, Source: &model.SourceReference{Path: "deploy|app.yaml", Document: 2, Field: "spec.template"}},
			{ID: "container.startup", Category: "container", Status: model.StatusPass, Severity: model.SeverityInfo, Summary: "Application started.", Observed: "2/2 ready", Expected: "all replicas ready", DurationMS: 400},
		},
		Diagnostics: []model.Diagnostic{{Code: "hpa_metrics_unavailable", Status: model.StatusWarn, Message: "CPU metrics unavailable.", Guidance: "Check metrics-server.", Source: &model.SourceReference{Path: "deploy|app.yaml", Document: 2}}},
		Comparison: &model.BaselineComparison{
			BaselineRunID: "baseline-100", Status: model.StatusFail,
			Regressions:  []model.ComparisonChange{{ExperimentID: "load-profile", Kind: model.ComparisonMeasurement, Measurement: "latency_p95_ms", Baseline: "90.000", Current: "120.000", Unit: "ms", Summary: "latency_p95_ms changed from 90.000 ms to 120.000 ms"}},
			Improvements: []model.ComparisonChange{{ExperimentID: "load-profile", Kind: model.ComparisonMeasurement, Measurement: "throughput_rps", Baseline: "40.000", Current: "50.000", Unit: "requests/second", Summary: "throughput_rps changed from 40.000 requests/second to 50.000 requests/second"}},
			Unavailable:  []model.ComparisonUnavailable{{ExperimentID: "horizontal-autoscaling", Kind: model.ComparisonStatus, Reason: "status comparison is unavailable for baseline pass and current skipped"}},
		},
	}
}

func assertGoldenBytes(t *testing.T, name string, actual []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, actual, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// #nosec G304 -- name is supplied only by tests in this package.
	expected, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, expected) {
		t.Fatalf("%s mismatch (-want +got):\nwant:\n%s\ngot:\n%s", name, expected, actual)
	}
}

func assertGoldenJSON(t *testing.T, name string, actual []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, actual, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// #nosec G304 -- name is supplied only by tests in this package.
	expected, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var actualValue, expectedValue any
	if err := json.Unmarshal(actual, &actualValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(expected, &expectedValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actualValue, expectedValue) {
		t.Fatalf("%s semantic mismatch:\nwant:\n%s\ngot:\n%s", name, expected, actual)
	}
}

func reverseEvidence(values []model.Evidence) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}
func reverseFindings(values []model.Finding) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}
func reverseDiagnostics(values []model.Diagnostic) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}
func reverseMeasurements(values []model.Measurement) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}
func reverseComparisonChanges(values []model.ComparisonChange) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}
func reverseUnavailable(values []model.ComparisonUnavailable) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}

func TestReliabilityCollectionsCanonicalWithoutMutatingInput(t *testing.T) {
	run := model.VerificationRun{Plan: &model.VerificationPlan{Detected: []string{"redis", "python"}, Capabilities: []model.Capability{{Name: "rollout", Prerequisites: []string{"ready", "image"}}, {Name: "build"}}}, Evidence: []model.Evidence{{ExperimentID: "rollout", Recovery: &model.RecoveryEvidence{Checks: []model.BaselineCheck{{Name: "replicas"}, {Name: "image"}}}}}}
	before, _ := json.Marshal(run)
	var first, second bytes.Buffer
	if err := JSON(&first, run); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(run)
	if !bytes.Equal(before, after) {
		t.Fatal("rendering mutated input")
	}
	run.Plan.Detected = []string{"python", "redis"}
	run.Plan.Capabilities[0].Prerequisites = []string{"image", "ready"}
	run.Plan.Capabilities[0], run.Plan.Capabilities[1] = run.Plan.Capabilities[1], run.Plan.Capabilities[0]
	run.Evidence[0].Recovery.Checks[0], run.Evidence[0].Recovery.Checks[1] = run.Evidence[0].Recovery.Checks[1], run.Evidence[0].Recovery.Checks[0]
	if err := JSON(&second, run); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("collection order changed canonical serialization")
	}
}
