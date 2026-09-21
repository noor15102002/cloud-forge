package regression

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noor15102002/cloud-forge/pkg/model"
)

func workerSchemaReport() model.VerificationRun {
	report := backendSchemaReport()
	report.SchemaVersion = "v1alpha7"
	report.Fingerprint.WorkerHash = strings.Repeat("b", 64)
	report.Fingerprint.Configuration.SchemaVersion = "v1alpha6"
	report.Fingerprint.Configuration.Runtime.Kind = "worker"
	report.Fingerprint.Configuration.Worker = &model.WorkerSettings{Command: []string{"<omitted>"}, Heartbeat: model.RedisHeartbeatSettings{Key: "<omitted>", TimestampField: "at", MaxAge: "30s"}}
	observation := &model.WorkerObservation{PodUID: "current", PreviousPodUID: "prior", Image: "expected", RunningPods: 1, MaximumRunningPods: 1, HeartbeatState: "advancing", KeyAbsentBeforeStart: true, PredecessorTerminated: true, PreviousHeartbeatGone: true, Samples: []model.HeartbeatSample{{RedisTime: "2050-01-01T00:00:01Z", Timestamp: "2050-01-01T00:00:00Z", AgeMS: 1000, TTLMS: 3000}, {RedisTime: "2050-01-01T00:00:02Z", Timestamp: "2050-01-01T00:00:01Z", AgeMS: 1000, TTLMS: 3000}}}
	report.Evidence = []model.Evidence{{ExperimentID: "worker-recovery", Title: "Worker recovery", Status: model.StatusFail, Summary: "original observation retained", Measurements: []model.Measurement{}, Execution: &model.ExperimentExecution{Executed: true, MutationAttempted: true}, Worker: observation, Recovery: &model.RecoveryEvidence{Status: model.StatusPass, Strategy: "stop_wait_for_expiry_restore_worker_and_validate", Summary: "intended baseline restored", Checks: []model.BaselineCheck{}, Worker: observation}}}
	report.Status = model.StatusFail
	return report
}
func TestWorkerPublishedSchemaMatchesStrictLoader(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "schemas", "verification.v1alpha7.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, workerVerificationSchema) {
		t.Fatal("worker schema/embed mismatch")
	}
	var document map[string]any
	_ = json.Unmarshal(body, &document)
	if !strings.Contains(document["$id"].(string), "v1alpha7") || !strings.Contains(document["title"].(string), "v1alpha7") {
		t.Fatal("copied old contract identity")
	}
}
func TestWorkerStrictReportRoundtripRetainsOriginalFailureAndRecoveryProof(t *testing.T) {
	original := workerSchemaReport()
	data, _ := json.Marshal(original)
	for range 2 {
		loaded, err := Load(writeBaseline(t, string(data)))
		if err != nil {
			t.Fatal(err)
		}
		if loaded.Status != model.StatusFail || loaded.Evidence[0].Status != model.StatusFail || loaded.Evidence[0].Recovery.Status != model.StatusPass || loaded.Evidence[0].Recovery.Worker == nil || len(loaded.Evidence[0].Recovery.Worker.Samples) != 2 || loaded.Fingerprint.WorkerHash == "" {
			t.Fatal("worker recovery erased original failed evidence")
		}
		data, err = json.Marshal(loaded)
		if err != nil {
			t.Fatal(err)
		}
	}
}
func TestWorkerStrictReportRejectsUnknownStateAndRawContract(t *testing.T) {
	for name, mutate := range map[string]func(*model.VerificationRun){
		"unknown-heartbeat-state": func(r *model.VerificationRun) { r.Evidence[0].Worker.HeartbeatState = "future_unknown" },
		"raw-key":                 func(r *model.VerificationRun) { r.Fingerprint.Configuration.Worker.Heartbeat.Key = "raw-key" },
		"raw-command": func(r *model.VerificationRun) {
			r.Fingerprint.Configuration.Worker.Command = []string{"node", "raw.js"}
		},
		"invalid-max-age": func(r *model.VerificationRun) { r.Fingerprint.Configuration.Worker.Heartbeat.MaxAge = "30000ms" },
		"missing-hash":    func(r *model.VerificationRun) { r.Fingerprint.WorkerHash = "invalid" },
		"too-many-samples": func(r *model.VerificationRun) {
			r.Evidence[0].Worker.Samples = append(r.Evidence[0].Worker.Samples, r.Evidence[0].Worker.Samples[0])
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := workerSchemaReport()
			mutate(&r)
			data, _ := json.Marshal(r)
			if _, err := Load(writeBaseline(t, string(data))); err == nil {
				t.Fatal("invalid worker report accepted")
			}
		})
	}
}
func TestWorkerRuntimeSchemaMatchesBoundedModes(t *testing.T) {
	schema := loadPublishedSchema(t, "runtime.v1alpha6.schema.json")
	base := `{"schema_version":"v1alpha6","runtime":{"kind":"worker"},"worker":{"command":["node","worker.js"],"heartbeat":{"key":"worker:heartbeat","timestamp_field":"at","max_age":"30s"}},"topology":{"replicas":1,"rollout":{"strategy":"recreate"}},"dependencies":{"redis":{"enabled":true}},"network":{"outbound":"declared_dependencies_only"}}`
	for name, mutate := range map[string]func(map[string]any){
		"valid":          func(map[string]any) {},
		"missing-worker": func(d map[string]any) { delete(d, "worker") },
		"wrong-mode":     func(d map[string]any) { d["runtime"].(map[string]any)["kind"] = "http" },
		"replicas":       func(d map[string]any) { d["topology"].(map[string]any)["replicas"] = 2 },
		"port":           func(d map[string]any) { d["runtime"].(map[string]any)["port"] = 8080 },
		"http":           func(d map[string]any) { d["endpoints"] = map[string]any{} },
		"redis-disabled": func(d map[string]any) {
			d["dependencies"].(map[string]any)["redis"].(map[string]any)["enabled"] = false
		},
		"network":               func(d map[string]any) { delete(d, "network") },
		"noncanonical-duration": func(d map[string]any) { d["worker"].(map[string]any)["heartbeat"].(map[string]any)["max_age"] = "1m0s" },
	} {
		t.Run(name, func(t *testing.T) {
			var doc map[string]any
			_ = json.Unmarshal([]byte(base), &doc)
			mutate(doc)
			if err := schema.Validate(doc); (err == nil) != (name == "valid") {
				t.Fatalf("unexpected contract result %v", err)
			}
		})
	}
}
func TestHistoricalHTTPConfigurationRoundtripPreservesEmptyValues(t *testing.T) {
	report := backendSchemaReport()
	report.Fingerprint.Configuration.Runtime.Port = 0
	report.Fingerprint.Configuration.Endpoints = model.EndpointSettings{Health: "/health", Readiness: "/ready", Load: ""}
	data, _ := json.Marshal(report)
	for range 2 {
		loaded, err := Load(writeBaseline(t, string(data)))
		if err != nil {
			t.Fatal(err)
		}
		if loaded.SchemaVersion != "v1alpha6" || loaded.Fingerprint.Configuration.Runtime.Port != 0 || loaded.Fingerprint.Configuration.Endpoints.Load != "" {
			t.Fatal("historical field changed")
		}
		data, err = json.Marshal(loaded)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), `"port":0`) || !strings.Contains(string(data), `"load":""`) {
			t.Fatal("empty historical fields disappeared")
		}
	}
}
