package regression

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/noor15102002/cloud-forge/pkg/model"
)

func probeSchemaReport() model.VerificationRun {
	report := backendSchemaReport()
	report.SchemaVersion = model.VerificationSchemaVersion
	report.Status = model.StatusFail
	report.Fingerprint.Configuration.SchemaVersion = "v1alpha7"
	report.Fingerprint.Configuration.Probes = &model.ProbeSettings{Interval: "2s"}
	report.Plan = &model.VerificationPlan{SchemaVersion: model.VerificationSchemaVersion, Status: model.StatusPass, Capabilities: []model.Capability{}, Probes: &model.ProbeSettings{Interval: "2s"}}
	report.Evidence = []model.Evidence{{ExperimentID: "pod-recovery", Title: "Recovery", Status: model.StatusFail, Summary: "Original availability failure", Measurements: []model.Measurement{{Name: "failed_requests", Value: "7"}}, Execution: &model.ExperimentExecution{Executed: true, MutationAttempted: true}}}
	return report
}

func TestProbePublishedSchemaMatchesStrictLoader(t *testing.T) {
	published, err := os.ReadFile(filepath.Join("..", "..", "schemas", "verification.v1alpha8.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(published, probeVerificationSchema) {
		t.Fatal("published probe schema differs from embedded loader")
	}
}

func TestProbeReportRoundtripPreservesConfigurationAndFailure(t *testing.T) {
	report := probeSchemaReport()
	data, _ := json.Marshal(report)
	for range 2 {
		loaded, err := Load(writeBaseline(t, string(data)))
		if err != nil {
			t.Fatal(err)
		}
		if loaded.SchemaVersion != "v1alpha8" || loaded.Status != model.StatusFail || loaded.Evidence[0].Status != model.StatusFail || loaded.Evidence[0].Measurements[0].Value != "7" || loaded.Plan.Probes.Interval != "2s" || loaded.Fingerprint.Configuration.Probes.Interval != "2s" {
			t.Fatal("roundtrip changed pacing or original failure")
		}
		data, _ = json.Marshal(loaded)
	}
}

func TestProbeReportRejectsNoncanonicalOrIncompatibleSettings(t *testing.T) {
	for name, mutate := range map[string]func(*model.VerificationRun){
		"below-bound": func(r *model.VerificationRun) { r.Plan.Probes.Interval = "19ms" },
		"above-bound": func(r *model.VerificationRun) { r.Plan.Probes.Interval = "5.001s" },
		"fractional-ms": func(r *model.VerificationRun) {
			r.Fingerprint.Configuration.Probes.Interval = "20.5ms"
		},
		"noncanonical": func(r *model.VerificationRun) { r.Plan.Probes.Interval = "2000ms" },
		"empty":        func(r *model.VerificationRun) { r.Plan.Probes.Interval = "" },
		"old-input-version": func(r *model.VerificationRun) {
			r.Fingerprint.Configuration.SchemaVersion = "v1alpha6"
		},
		"worker-config": func(r *model.VerificationRun) {
			r.Fingerprint.Configuration.Runtime.Kind = "worker"
		},
		"worker-plan": func(r *model.VerificationRun) { r.Plan.RuntimeKind = "worker" },
		"historical-report": func(r *model.VerificationRun) {
			r.SchemaVersion, r.Plan.SchemaVersion = "v1alpha7", "v1alpha7"
		},
	} {
		t.Run(name, func(t *testing.T) {
			report := probeSchemaReport()
			mutate(&report)
			data, _ := json.Marshal(report)
			if _, err := Load(writeBaseline(t, string(data))); err == nil {
				t.Fatal("invalid pacing report accepted")
			}
		})
	}
	for _, interval := range []string{"20ms", "999ms", "1s", "1.005s", "4.999s", "5s"} {
		report := probeSchemaReport()
		report.Plan.Probes.Interval, report.Fingerprint.Configuration.Probes.Interval = interval, interval
		data, _ := json.Marshal(report)
		if _, err := Load(writeBaseline(t, string(data))); err != nil {
			t.Fatalf("canonical interval %q rejected: %v", interval, err)
		}
	}
}

func TestProbeInputSchemaRejectsStructuralAndVersionErrors(t *testing.T) {
	schema := loadPublishedSchema(t, "runtime.v1alpha7.schema.json")
	for name, probes := range map[string]any{
		"valid": map[string]any{"interval": "2s"},
		"null":  nil, "empty": map[string]any{}, "null-interval": map[string]any{"interval": nil},
		"number": map[string]any{"interval": 2}, "boolean": map[string]any{"interval": true},
		"unknown": map[string]any{"interval": "2s", "other": true},
	} {
		document := map[string]any{"schema_version": "v1alpha7", "probes": probes}
		if err := schema.Validate(document); (err == nil) != (name == "valid") {
			t.Fatalf("unexpected result for %s: %v", name, err)
		}
	}
	for version := 1; version <= 6; version++ {
		historical := loadPublishedSchema(t, fmt.Sprintf("runtime.v1alpha%d.schema.json", version))
		document := map[string]any{"schema_version": fmt.Sprintf("v1alpha%d", version), "probes": map[string]any{"interval": "2s"}}
		if err := historical.Validate(document); err == nil {
			t.Fatalf("historical input v%d accepted new probes", version)
		}
	}
}

func TestProbeVersionRetainsHistoricalWorkerLoading(t *testing.T) {
	original := workerSchemaReport()
	data, _ := json.Marshal(original)
	loaded, err := Load(writeBaseline(t, string(data)))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SchemaVersion != "v1alpha7" || loaded.Status != model.StatusFail || loaded.Evidence[0].Status != model.StatusFail || loaded.Evidence[0].Recovery.Status != model.StatusPass || loaded.Fingerprint.Configuration.Probes != nil {
		t.Fatal("historical worker evidence reinterpreted")
	}
}
