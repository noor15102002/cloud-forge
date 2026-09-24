package regression

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/noor15102002/cloud-forge/pkg/model"
)

func TestLoadRejectsDuplicateKeysBeforeSchemaSelection(t *testing.T) {
	for _, input := range []string{
		`{"schema_version":"unsupported","schema_version":"v1alpha1"}`,
		`{"status":"pass","status":"fail"}`,
		`{"evidence":[{"status":"pass","status":"fail"}]}`,
		`{"status":"pass","st\u0061tus":"fail"}`,
	} {
		if _, err := Load(writeBaseline(t, input)); err == nil || !strings.Contains(err.Error(), "duplicate JSON object key") {
			t.Fatalf("duplicate key was not rejected before schema decoding: %v", err)
		}
	}
}

func TestSupportedHistoricalReportsPreserveMissingMetadataAndComparison(t *testing.T) {
	for version := 1; version <= 8; version++ {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			input := fmt.Sprintf(`{"schema_version":"v1alpha%d","run_id":"historical","status":"pass","started_at":"2026-09-18T12:00:00Z","duration_ms":1,"environment":{"backend":"k3d","kept":false},"evidence":[],"comparison":{"baseline_run_id":"older","status":"fail","regressions":[{"experiment_id":"startup","kind":"measurement","measurement":"startup_duration_ms","baseline":"10","current":"20","unit":"ms","summary":"historical grading"}]}}`, version)
			if version >= 3 {
				input = strings.Replace(input, `"run_id":`, `"producer":{"version":"dev","commit":"unknown"},"run_id":`, 1)
			}
			run, err := Load(writeBaseline(t, input))
			if err != nil {
				t.Fatal(err)
			}
			if run.Application != "" || run.Fingerprint != nil || len(run.Findings) != 0 || len(run.Evidence) != 0 || run.Comparison.Status != model.StatusFail || run.Comparison.Regressions[0].Summary != "historical grading" {
				t.Fatalf("historical evidence synthesized or regraded: %#v", run)
			}
			if version < 3 && run.Producer != nil || version >= 3 && (run.Producer == nil || run.Producer.Version != "dev" || run.Producer.Commit != "unknown") {
				t.Fatalf("historical producer was synthesized or rewritten: %#v", run.Producer)
			}
			encoded, err := json.Marshal(run)
			if err != nil || strings.Contains(string(encoded), "scanned_image") || strings.Contains(string(encoded), "known_fix") {
				t.Fatalf("historical scan metadata synthesized: %s %v", encoded, err)
			}
		})
	}
}

func TestNumericalComparisonsAreAdvisoryAndPreserveAbsoluteEvidence(t *testing.T) {
	for _, metric := range []string{"latency_p95_ms", "startup_duration_ms", "failed_requests", "vulnerabilities"} {
		t.Run(metric, func(t *testing.T) {
			baseline := verificationRun("baseline", []model.Evidence{{ExperimentID: "observed", Status: model.StatusPass, Measurements: []model.Measurement{{Name: metric, Value: "100", Unit: "count"}}}})
			current := verificationRun("current", []model.Evidence{{ExperimentID: "observed", Status: model.StatusPass, Measurements: []model.Measurement{{Name: metric, Value: "125", Unit: "count"}}}})
			current.Status = model.StatusPass
			comparison := Compare(current, baseline)
			if comparison.Status != model.StatusWarn || len(comparison.Regressions) != 1 || !strings.Contains(comparison.Regressions[0].Summary, "Advisory numerical change") || !strings.Contains(comparison.Regressions[0].Summary, "not statistical evidence") {
				t.Fatalf("numerical comparison became an unsupported strong verdict: %#v", comparison)
			}
			if current.Status != model.StatusPass || current.Evidence[0].Status != model.StatusPass || current.Evidence[0].Measurements[0].Value != "125" {
				t.Fatal("comparison changed original evidence")
			}
			current.Evidence[0].Status = model.StatusFail
			if comparison := Compare(current, baseline); comparison.Status != model.StatusFail {
				t.Fatalf("observed status regression was weakened: %#v", comparison)
			}
		})
	}
}
