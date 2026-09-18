package regression

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noor15102002/cloud-forge/pkg/model"
)

func TestCompareSeparatesRegressionsImprovementsAndUnavailableEvidence(t *testing.T) {
	baseline := verificationRun("baseline-1", []model.Evidence{
		{ExperimentID: "load-profile", Status: model.StatusPass, Measurements: []model.Measurement{
			{Name: "latency_p95_ms", Value: "100.000", Unit: "ms"},
			{Name: "latency_p50_ms", Value: "50.000", Unit: "ms"},
			{Name: "throughput_rps", Value: "50.000", Unit: "requests/second"},
			{Name: "error_rate", Value: "NaN", Unit: "ratio"},
			{Name: "virtual_users", Value: "16", Unit: "users"},
		}},
		{ExperimentID: "pod-recovery", Status: model.StatusPass, Measurements: []model.Measurement{{Name: "downtime_ms", Value: "0", Unit: "ms"}}},
		{ExperimentID: "removed-experiment", Status: model.StatusPass},
	})
	current := verificationRun("current-1", []model.Evidence{
		{ExperimentID: "load-profile", Status: model.StatusWarn, Measurements: []model.Measurement{
			{Name: "latency_p95_ms", Value: "125.000", Unit: "ms"},
			{Name: "latency_p50_ms", Value: "0.050", Unit: "seconds"},
			{Name: "throughput_rps", Value: "60.000", Unit: "requests/second"},
			{Name: "error_rate", Value: "NaN", Unit: "ratio"},
			{Name: "virtual_users", Value: "32", Unit: "users"},
		}},
		{ExperimentID: "pod-recovery", Status: model.StatusPass},
		{ExperimentID: "new-experiment", Status: model.StatusPass},
	})

	comparison := Compare(current, baseline)
	if comparison.Status != model.StatusFail || comparison.BaselineRunID != "baseline-1" {
		t.Fatalf("unexpected comparison status: %#v", comparison)
	}
	if len(comparison.Regressions) != 2 || comparison.Regressions[0].Kind != model.ComparisonStatus || comparison.Regressions[1].Measurement != "latency_p95_ms" {
		t.Fatalf("unexpected regressions: %#v", comparison.Regressions)
	}
	if len(comparison.Improvements) != 1 || comparison.Improvements[0].Measurement != "throughput_rps" {
		t.Fatalf("unexpected improvements: %#v", comparison.Improvements)
	}
	if len(comparison.Unavailable) != 5 {
		t.Fatalf("unexpected unavailable comparisons: %#v", comparison.Unavailable)
	}
	for _, item := range comparison.Regressions {
		if item.Measurement == "virtual_users" {
			t.Fatal("configuration measurements must not be assigned a regression direction")
		}
	}
}

func TestSkippedStatusIsUnavailable(t *testing.T) {
	baseline := verificationRun("baseline", []model.Evidence{{ExperimentID: "hpa", Status: model.StatusPass}})
	current := verificationRun("current", []model.Evidence{{ExperimentID: "hpa", Status: model.StatusSkipped}})
	comparison := Compare(current, baseline)
	if comparison.Status != model.StatusWarn || len(comparison.Regressions) != 0 || len(comparison.Unavailable) != 1 {
		t.Fatalf("unexpected skipped comparison: %#v", comparison)
	}
}

func TestEqualSkippedStatusesAreUnavailable(t *testing.T) {
	baseline := verificationRun("baseline", []model.Evidence{{ExperimentID: "hpa", Title: "HPA", Status: model.StatusSkipped, Summary: "No HPA"}})
	current := verificationRun("current", []model.Evidence{{ExperimentID: "hpa", Title: "HPA", Status: model.StatusSkipped, Summary: "No HPA"}})
	comparison := Compare(current, baseline)
	if comparison.Status != model.StatusWarn || len(comparison.Unavailable) != 1 || comparison.Unavailable[0].Reason != "both baseline and current experiments were skipped" {
		t.Fatalf("equal skipped evidence was treated as comparable: %#v", comparison)
	}
}

func TestContinuousMeasurementChangesWithinTenPercentAreIgnored(t *testing.T) {
	baseline := verificationRun("baseline", []model.Evidence{{ExperimentID: "load", Status: model.StatusPass, Measurements: []model.Measurement{{Name: "latency_p95_ms", Value: "100", Unit: "ms"}, {Name: "throughput_rps", Value: "100", Unit: "requests/second"}}}})
	current := verificationRun("current", []model.Evidence{{ExperimentID: "load", Status: model.StatusPass, Measurements: []model.Measurement{{Name: "latency_p95_ms", Value: "109", Unit: "ms"}, {Name: "throughput_rps", Value: "91", Unit: "requests/second"}}}})
	comparison := Compare(current, baseline)
	if comparison.Status != model.StatusPass || len(comparison.Regressions) != 0 || len(comparison.Improvements) != 0 {
		t.Fatalf("measurement noise was classified as a change: %#v", comparison)
	}
}

func TestDifferentApplicationsAreNotCompared(t *testing.T) {
	baseline := verificationRun("baseline", []model.Evidence{{ExperimentID: "load", Status: model.StatusPass}})
	baseline.Application = "api"
	current := verificationRun("current", []model.Evidence{{ExperimentID: "load", Status: model.StatusFail}})
	current.Application = "worker"
	comparison := Compare(current, baseline)
	if comparison.Status != model.StatusWarn || len(comparison.Regressions) != 0 || len(comparison.Unavailable) != 1 || !strings.Contains(comparison.Unavailable[0].Reason, "does not match") {
		t.Fatalf("unrelated applications were compared: %#v", comparison)
	}
}

func TestLoadAcceptsStrictBoundedBaseline(t *testing.T) {
	path := writeBaseline(t, `{
  "schema_version":"v1alpha1",
  "run_id":"baseline",
  "status":"pass",
  "started_at":"2026-09-18T12:00:00Z",
  "duration_ms":1,
  "environment":{"backend":"k3d","kept":false},
  "evidence":[]
}`)
	baseline, err := Load(path)
	if err != nil || baseline.RunID != "baseline" || baseline.Evidence == nil {
		t.Fatalf("unexpected load result: baseline=%#v err=%v", baseline, err)
	}
}

func TestLoadRejectsSchemaMismatchUnknownFieldsAndDuplicateMetrics(t *testing.T) {
	tests := map[string]string{
		"schema":    `{"schema_version":"v2","run_id":"baseline","evidence":[]}`,
		"unknown":   `{"schema_version":"v1alpha1","run_id":"baseline","evidence":[],"secret":"value"}`,
		"duplicate": `{"schema_version":"v1alpha1","run_id":"baseline","status":"pass","started_at":"2026-09-18T12:00:00Z","duration_ms":1,"environment":{"backend":"k3d","kept":false},"evidence":[{"experiment_id":"load","title":"Load","status":"pass","summary":"ok","duration_ms":1,"measurements":[{"name":"error_rate","value":"0"},{"name":"error_rate","value":"1"}]}]}`,
		"required":  `{"schema_version":"v1alpha1","run_id":"baseline","status":"pass","evidence":[]}`,
	}
	for name, document := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Load(writeBaseline(t, document))
			if err == nil {
				t.Fatal("expected baseline error")
			}
			if name == "schema" && !strings.Contains(err.Error(), `expected "v1alpha1"`) {
				t.Fatalf("schema mismatch was not actionable: %v", err)
			}
		})
	}
}

func TestEmbeddedSchemaMatchesPublicContract(t *testing.T) {
	publicPath := filepath.Join("..", "..", "schemas", "verification.v1alpha1.schema.json")
	// #nosec G304 -- the path is a fixed repository contract checked by this test.
	publicSchema, err := os.ReadFile(publicPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(verificationSchema, publicSchema) {
		t.Fatal("embedded baseline schema differs from the public verification schema")
	}
}

func TestLoadRejectsOversizedBaseline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baseline.json")
	if err := os.WriteFile(path, []byte(strings.Repeat(" ", maxReportBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("unexpected oversized baseline error: %v", err)
	}
}

func verificationRun(id string, evidence []model.Evidence) model.VerificationRun {
	return model.VerificationRun{SchemaVersion: model.SchemaVersion, RunID: id, Evidence: evidence}
}

func writeBaseline(t *testing.T, document string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "baseline.json")
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
