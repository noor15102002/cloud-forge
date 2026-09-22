package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

func TestReportRejectsDuplicateKeysWithExitTwoBeforeRendering(t *testing.T) {
	path := filepath.Join(t.TempDir(), "duplicate.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":"v1alpha1","status":"pass","status":"fail"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"report", path, "--format", "json"}, &stdout, &stderr)
	if code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "duplicate JSON object key") {
		t.Fatalf("ambiguous report not rejected safely: exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestNumericalBaselineAdvisoryKeepsSuccessfulVerificationExitZero(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "package.json"), []byte(`{"name":"cli-reporting-test"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "Dockerfile"), []byte("FROM node:22-alpine\nUSER node\nEXPOSE 8080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := successfulCLIRunner()
	runner := cliRunnerFunc(func(ctx context.Context, request command.Request) model.CommandResult {
		result := base(ctx, request)
		// Both baseline and current observations are deterministic; only the
		// selected retained timing below differs across the two comparisons.
		result.DurationMS = 100
		return result
	})
	var initialOutput, initialError bytes.Buffer
	initial := newRootCommand(&initialOutput, &initialError, runner)
	initial.SetArgs([]string{"verify", directory, "--format", "json"})
	if err := initial.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("initial verification failed: %v stderr=%q", err, initialError.String())
	}
	var baseline model.VerificationRun
	if err := json.Unmarshal(initialOutput.Bytes(), &baseline); err != nil {
		t.Fatal(err)
	}
	changed := false
	for index := range baseline.Evidence {
		for metric := range baseline.Evidence[index].Measurements {
			measurement := &baseline.Evidence[index].Measurements[metric]
			if measurement.Name == "readiness_duration_ms" {
				measurement.Value = "50"
				changed = true
			}
		}
	}
	if !changed {
		t.Fatal("fixture did not retain readiness duration")
	}
	baselinePath := filepath.Join(t.TempDir(), "baseline.json")
	encoded, err := json.Marshal(baseline)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(baselinePath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	currentCommand := newRootCommand(&stdout, &stderr, runner)
	currentCommand.SetArgs([]string{"verify", directory, "--format", "json", "--baseline", baselinePath})
	if err := currentCommand.ExecuteContext(context.Background()); err != nil {
		var coded *exitError
		if errors.As(err, &coded) {
			t.Fatalf("advisory numerical change produced exit %d: %v", coded.code, err)
		}
		t.Fatal(err)
	}
	var current model.VerificationRun
	if err := json.Unmarshal(stdout.Bytes(), &current); err != nil {
		t.Fatal(err)
	}
	if current.Status != model.StatusPass || current.Comparison == nil || current.Comparison.Status != model.StatusWarn {
		t.Fatalf("absolute and advisory statuses incorrect: %#v", current)
	}
	var advisory, clean bool
	for _, change := range current.Comparison.Regressions {
		if change.Measurement == "readiness_duration_ms" && strings.Contains(change.Summary, "Advisory numerical change") {
			advisory = true
		}
	}
	for _, evidence := range current.Evidence {
		if evidence.ExperimentID == "environment-cleanup" && evidence.Status == model.StatusPass {
			clean = true
		}
		if evidence.Status != model.StatusPass && evidence.Status != model.StatusSkipped {
			t.Fatalf("advisory comparison changed original evidence: %#v", evidence)
		}
	}
	if !advisory || !clean {
		t.Fatalf("missing numerical diagnostic or cleanup evidence: advisory=%t cleanup=%t", advisory, clean)
	}
}
