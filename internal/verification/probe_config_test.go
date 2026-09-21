package verification

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/internal/regression"
	"github.com/noor15102002/cloud-forge/pkg/model"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func writeProbeConfiguration(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cloudforge.yaml")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProbeConfigurationRejectsInvalidContractsBeforeTools(t *testing.T) {
	cases := map[string]string{}
	for name, section := range map[string]string{
		"null": "null", "empty": "{}", "null-interval": "{interval: null}",
		"empty-interval": "{interval: ''}", "number": "{interval: 2000}",
		"boolean": "{interval: true}", "list": "{interval: [2s]}",
		"unknown":   "{interval: 2s, private_marker: hidden}",
		"duplicate": "{interval: 2s, interval: 3s}", "unitless": "{interval: '20'}",
		"zero": "{interval: 0s}", "negative": "{interval: -2s}",
		"below-bound": "{interval: 19ms}", "above-bound": "{interval: 5.001s}",
		"fractional-ms": "{interval: 20.5ms}", "nanosecond": "{interval: 20.000001ms}",
		"overflow":      "{interval: 999999999999999999999999999999999s}",
		"private-value": "{interval: private-marker-never-disclose}",
	} {
		cases[name] = "schema_version: v1alpha7\nprobes: " + section + "\n"
	}
	cases["duplicate-section"] = "schema_version: v1alpha7\nprobes: {interval: 2s}\nprobes: {interval: 3s}\n"
	for version := 1; version <= 6; version++ {
		cases[fmt.Sprintf("historical-v%d", version)] = fmt.Sprintf("schema_version: v1alpha%d\nprobes: {interval: 2s}\n", version)
	}
	for _, section := range []string{"{}", "{interval: 2s}"} {
		cases["worker-"+section] = strings.Replace(workerTestConfiguration, "v1alpha6", "v1alpha7", 1) + "probes: " + section + "\n"
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeProbeConfiguration(t, body)
			s := New(runnerFunc(func(context.Context, command.Request) model.CommandResult {
				t.Fatal("invalid probe configuration invoked a tool")
				return model.CommandResult{}
			}))
			options := testOptions()
			options.ConfigPath = path
			out := s.Run(context.Background(), filepath.Dir(path), options)
			if out.ExitCode != 2 {
				t.Fatalf("invalid probe configuration accepted: %+v", out.Run.Diagnostics)
			}
			encoded, _ := json.Marshal(out.Run)
			if strings.Contains(string(encoded), "private-marker-never-disclose") {
				t.Fatal("invalid duration was disclosed")
			}
		})
	}
}

func TestProbeConfigurationNormalizesAndPreservesNil(t *testing.T) {
	for input, expected := range map[string]string{"20ms": "20ms", "2000ms": "2s", "5000ms": "5s", "0.020s": "20ms", "1s20ms": "1.02s", "20000us": "20ms"} {
		path := writeProbeConfiguration(t, fmt.Sprintf("schema_version: v1alpha7\nprobes: {interval: %q}\n", input))
		config, err := LoadConfiguration(filepath.Dir(path), path)
		if err != nil || config.Probes == nil || config.Probes.Interval != expected {
			t.Fatalf("%s not normalized to %s: %+v %v", input, expected, config.Probes, err)
		}
	}
	for version := 1; version <= 7; version++ {
		path := writeProbeConfiguration(t, fmt.Sprintf("schema_version: v1alpha%d\n", version))
		config, err := LoadConfiguration(filepath.Dir(path), path)
		if err != nil || config.Probes != nil {
			t.Fatalf("omitted probes changed old behavior: %+v %v", config, err)
		}
	}
	path := writeProbeConfiguration(t, strings.Replace(workerTestConfiguration, "v1alpha6", "v1alpha7", 1))
	if _, err := LoadConfiguration(filepath.Dir(path), path); err != nil {
		t.Fatalf("v7 lost the existing worker contract: %v", err)
	}
}

func TestProbePlanIsDeterministicExplicitAndReadOnly(t *testing.T) {
	path := writeProbeConfiguration(t, "schema_version: v1alpha7\nprobes: {interval: 2000ms}\n")
	s := New(runnerFunc(func(context.Context, command.Request) model.CommandResult {
		t.Fatal("read-only probe plan invoked a tool")
		return model.CommandResult{}
	}))
	options := testOptions()
	options.ConfigPath, options.PlanOnly = path, true
	first, second := s.Run(context.Background(), fixturePath(t), options), s.Run(context.Background(), fixturePath(t), options)
	if first.ExitCode != 0 || second.ExitCode != 0 || first.Run.Plan.Probes == nil || first.Run.Plan.Probes.Interval != "2s" {
		t.Fatalf("explicit probe plan unavailable: %+v", first.Run.Diagnostics)
	}
	a, _ := json.Marshal(first.Run.Plan)
	b, _ := json.Marshal(second.Run.Plan)
	if string(a) != string(b) {
		t.Fatal("probe plan is not deterministic")
	}
	for _, phrase := range []string{"minimum time", "Kubernetes probes", "explicit control", "k6", "absence of outages shorter"} {
		if !strings.Contains(strings.Join(first.Run.Plan.Limitations, "\n"), phrase) {
			t.Fatalf("missing pacing limitation %q", phrase)
		}
	}
	compiler := jsonschema.NewCompiler()
	schema, err := compiler.Compile(filepath.Join("..", "..", "schemas", "plan.v1alpha8.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	_ = json.Unmarshal(a, &doc)
	if err := schema.Validate(doc); err != nil {
		t.Fatal(err)
	}
}

func TestProbeFingerprintCanonicalizationAndBaselineIsolation(t *testing.T) {
	makeFingerprint := func(interval string) *model.RunFingerprint {
		config := defaultConfiguration()
		config.SchemaVersion = "v1alpha7"
		if interval != "" {
			config.Probes = &model.ProbeSettings{Interval: interval}
		}
		fp := &model.RunFingerprint{CloudForgeCommit: strings.Repeat("a", 40), ImageID: "sha256:" + strings.Repeat("b", 64), WorkloadHash: "same-workload", Configuration: safeConfiguration(config)}
		for _, name := range []string{"docker", "k3d", "kubectl", "kubernetes", "trivy"} {
			fp.Tools = append(fp.Tools, model.ToolVersion{Name: name, Version: "test"})
		}
		completeFingerprint(fp)
		if fp.CompatibilityKey == "" {
			t.Fatal("missing complete fingerprint")
		}
		return fp
	}
	first := makeFingerprint("2000ms")
	if first.Configuration.Probes.Interval != "2s" || first.CompatibilityKey != makeFingerprint("2s").CompatibilityKey {
		t.Fatal("equivalent probe settings have different compatibility")
	}
	for _, other := range []string{"", "20ms", "3s"} {
		second := makeFingerprint(other)
		current := model.VerificationRun{SchemaVersion: model.VerificationSchemaVersion, Status: model.StatusPass, Fingerprint: first}
		baseline := model.VerificationRun{SchemaVersion: model.VerificationSchemaVersion, Status: model.StatusPass, Fingerprint: second}
		if comparison := regression.Compare(current, baseline); comparison.Status != model.StatusWarn || len(comparison.Unavailable) == 0 || len(comparison.Regressions) != 0 {
			t.Fatalf("different sampling policy compared: %+v", comparison)
		}
	}
}
