package regression

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noor15102002/cloud-forge/pkg/model"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func TestBackendSchemaMatchesPublishedContract(t *testing.T) {
	published, err := os.ReadFile(filepath.Join("..", "..", "schemas", "verification.v1alpha6.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(published, backendVerificationSchema) {
		t.Fatal("published backend schema differs from embedded loader contract")
	}
}

func TestHistoricalVerificationSchemasRemainLoadableWithoutReinterpretation(t *testing.T) {
	for version := 1; version <= 5; version++ {
		t.Run(fmt.Sprintf("v1alpha%d", version), func(t *testing.T) {
			producer, execution := "", ""
			if version >= 3 {
				producer = `,"producer":{"version":"historical","commit":"original"}`
				execution = `,"execution":{"executed":true,"mutation_attempted":true}`
			}
			document := fmt.Sprintf(`{"schema_version":"v1alpha%d","run_id":"historical","status":"fail","started_at":"2026-09-18T12:00:00Z","duration_ms":1,"environment":{"backend":"k3d","kept":false},"evidence":[{"experiment_id":"pod-recovery","title":"Historical evidence","status":"fail","summary":"original observation","duration_ms":1,"measurements":[{"name":"failed_requests","value":"7"}]%s}]%s}`, version, execution, producer)
			report, err := Load(writeBaseline(t, document))
			if err != nil {
				t.Fatal(err)
			}
			if report.SchemaVersion != fmt.Sprintf("v1alpha%d", version) || report.Status != model.StatusFail || report.Evidence[0].Status != model.StatusFail || report.Evidence[0].Measurements[0].Value != "7" {
				t.Fatal("historical observation was reinterpreted")
			}
		})
	}
}

func backendSchemaReport() model.VerificationRun {
	return model.VerificationRun{
		SchemaVersion: "v1alpha6", RunID: "backend-contract", Status: model.StatusPass,
		StartedAt: "2026-09-21T00:00:00Z", Producer: &model.BuildIdentity{Version: "test", Commit: strings.Repeat("a", 40)},
		Environment: model.VerificationEnvironment{Backend: "k3d"}, Evidence: []model.Evidence{},
		Fingerprint: &model.RunFingerprint{
			CloudForgeVersion: "test", CloudForgeCommit: strings.Repeat("a", 40), Platform: "linux/amd64", CPUs: 2,
			Tools: []model.ToolVersion{}, PreparationHash: strings.Repeat("a", 64),
			Configuration: model.RuntimeConfiguration{
				SchemaVersion: "v1alpha5", Load: model.LoadSettings{VUs: 1, Duration: "1s"},
				Safety: &model.SafetySettings{Profile: "bounded_backend"}, Network: &model.NetworkSettings{Outbound: "declared_dependencies_only"},
				Preparation:  &model.PreparationSettings{Command: []string{"<omitted>"}, Timeout: "2m"},
				Dependencies: map[string]model.DependencySpec{"postgresql": {Enabled: true}, "clamav": {Enabled: true, StartupTimeout: "10m"}},
				Environment: map[string]model.EnvironmentBinding{
					"DATABASE_URL": {From: "dependency.postgresql.url"},
					"CLAMAV_HOST":  {From: "dependency.clamav.host"},
					"JWT_SECRET":   {Generate: &model.GeneratedValueSpec{Bytes: 32, Encoding: "hex"}},
					"MAIL_HOST":    {From: "disabled.host"},
				},
			},
			Dependencies: []model.DependencyFingerprint{{Kind: "clamav", Image: "pinned", Digest: "sha256:test", Version: "test", DataVersion: "12345", DataTimestamp: "2026-09-21T00:00:00Z"}},
		},
		Dependencies: []model.DependencyEvidence{{Name: "clamav", Kind: "clamav", Image: "pinned", Version: "test", Status: model.StatusPass, DataVersion: "12345", DataTimestamp: "2026-09-21T00:00:00Z"}},
	}
}

func TestBackendReportLoadsBoundedConfigurationAndDataFingerprint(t *testing.T) {
	report := backendSchemaReport()
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(writeBaseline(t, string(data)))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Fingerprint.PreparationHash != report.Fingerprint.PreparationHash || loaded.Fingerprint.Configuration.Preparation.Command[0] != "<omitted>" || loaded.Dependencies[0].DataVersion != "12345" || loaded.Fingerprint.Dependencies[0].DataTimestamp == "" {
		t.Fatal("backend contract data was lost")
	}
}

func TestBackendReportRejectsRawPreparationAndInvalidBounds(t *testing.T) {
	for name, mutate := range map[string]func(*model.VerificationRun){
		"raw-command": func(r *model.VerificationRun) {
			r.Fingerprint.Configuration.Preparation.Command = []string{"node", "private-command"}
		},
		"unsafe-profile": func(r *model.VerificationRun) { r.Fingerprint.Configuration.Safety.Profile = "unlimited" },
		"unsafe-network": func(r *model.VerificationRun) { r.Fingerprint.Configuration.Network.Outbound = "internet" },
		"small-generator": func(r *model.VerificationRun) {
			r.Fingerprint.Configuration.Environment["JWT_SECRET"].Generate.Bytes = 15
		},
		"large-generator": func(r *model.VerificationRun) {
			r.Fingerprint.Configuration.Environment["JWT_SECRET"].Generate.Bytes = 65
		},
		"unknown-encoding": func(r *model.VerificationRun) {
			r.Fingerprint.Configuration.Environment["JWT_SECRET"].Generate.Encoding = "shell"
		},
		"multiple-bindings": func(r *model.VerificationRun) {
			r.Fingerprint.Configuration.Environment["JWT_SECRET"] = model.EnvironmentBinding{From: "dependency.redis.url", Generate: &model.GeneratedValueSpec{Bytes: 32, Encoding: "hex"}}
		},
		"unknown-binding": func(r *model.VerificationRun) {
			r.Fingerprint.Configuration.Environment["DATABASE_URL"] = model.EnvironmentBinding{From: "process.DATABASE_URL"}
		},
		"invalid-preparation-hash": func(r *model.VerificationRun) { r.Fingerprint.PreparationHash = "not-a-hash" },
		"unbounded-data-version":   func(r *model.VerificationRun) { r.Dependencies[0].DataVersion = strings.Repeat("a", 129) },
	} {
		t.Run(name, func(t *testing.T) {
			report := backendSchemaReport()
			mutate(&report)
			data, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Load(writeBaseline(t, string(data))); err == nil {
				t.Fatal("invalid backend report accepted")
			}
		})
	}
}

func loadPublishedSchema(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	// #nosec G304 -- callers provide fixed published schema names, never input paths.
	data, err := os.ReadFile(filepath.Join("..", "..", "schemas", name))
	if err != nil {
		t.Fatal(err)
	}
	var document any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	resource := "https://schemas.cloudforge.test/" + name
	if err := compiler.AddResource(resource, document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile(resource)
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func TestRuntimeV5SchemaBoundsInputsSeparatelyFromRedactedReports(t *testing.T) {
	schema := loadPublishedSchema(t, "runtime.v1alpha5.schema.json")
	base := `{"schema_version":"v1alpha5","safety":{"profile":"bounded_backend"},"network":{"outbound":"declared_dependencies_only"},"preparation":{"command":["node","prepare.js"],"timeout":"2m"},"dependencies":{"postgresql":{"enabled":true},"clamav":{"enabled":true,"startup_timeout":"10m"}},"environment":{"JWT_SECRET":{"generate":{"bytes":32,"encoding":"hex"}}}}`
	for name, alter := range map[string]func(map[string]any){
		"valid":         func(map[string]any) {},
		"empty-command": func(d map[string]any) { d["preparation"].(map[string]any)["command"] = []any{} },
		"too-many-arguments": func(d map[string]any) {
			args := make([]any, 17)
			for i := range args {
				args[i] = "arg"
			}
			d["preparation"].(map[string]any)["command"] = args
		},
		"oversized-argument": func(d map[string]any) { d["preparation"].(map[string]any)["command"] = []any{strings.Repeat("x", 257)} },
		"invalid-schema":     func(d map[string]any) { d["schema_version"] = "v1alpha4" },
	} {
		t.Run(name, func(t *testing.T) {
			var doc map[string]any
			if err := json.Unmarshal([]byte(base), &doc); err != nil {
				t.Fatal(err)
			}
			alter(doc)
			err := schema.Validate(doc)
			if (err == nil) != (name == "valid") {
				t.Fatalf("unexpected schema result: %v", err)
			}
		})
	}
	for _, from := range []string{"dependency.redis.url", "dependency.postgresql.url", "dependency.clamav.host", "dependency.clamav.port", "disabled.http_url", "disabled.https_url", "disabled.host", "disabled.email"} {
		var doc any
		_ = json.Unmarshal([]byte(fmt.Sprintf(`{"schema_version":"v1alpha5","network":{"outbound":"declared_dependencies_only"},"environment":{"TARGET":{"from":%q}}}`, from)), &doc)
		if err := schema.Validate(doc); err != nil {
			t.Fatalf("documented binding %s was rejected: %v", from, err)
		}
	}
}

func TestBackendPlanSchemaIsVersionedIndependently(t *testing.T) {
	schema := loadPublishedSchema(t, "plan.v1alpha6.schema.json")
	plan := model.VerificationPlan{SchemaVersion: "v1alpha6", Status: model.StatusBlocked, Capabilities: []model.Capability{}}
	data, _ := json.Marshal(plan)
	var document any
	_ = json.Unmarshal(data, &document)
	if err := schema.Validate(document); err != nil {
		t.Fatal(err)
	}
}
