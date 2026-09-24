package render

import (
	"bytes"
	"encoding/json"
	"html"
	"strings"
	"testing"

	"github.com/noor15102002/cloud-forge/pkg/model"
)

func TestMaturitySurvivesSerializationAndAppearsBesideDispositionAndEvidence(t *testing.T) {
	run := model.VerificationRun{SchemaVersion: model.VerificationSchemaVersion, Status: model.StatusFail,
		Plan: &model.VerificationPlan{Capabilities: []model.Capability{
			{Name: "horizontal-autoscaling", Disposition: "supported", Reason: "Demand required", Limitations: []string{model.ExperimentalMaturity}},
			{Name: "deployment-readiness", Disposition: "supported", Limitations: []string{model.CoreMaturity}},
		}}, Evidence: []model.Evidence{{ExperimentID: "horizontal-autoscaling", Title: "Autoscaling", Status: model.StatusFail, Summary: "Requirement was not met."}}}
	var encoded bytes.Buffer
	if err := JSON(&encoded, run); err != nil {
		t.Fatal(err)
	}
	var restored model.VerificationRun
	if err := json.Unmarshal(encoded.Bytes(), &restored); err != nil {
		t.Fatal(err)
	}
	for _, writer := range []func(*bytes.Buffer) error{
		func(b *bytes.Buffer) error { return VerificationText(b, restored) },
		func(b *bytes.Buffer) error { return VerificationMarkdown(b, restored) },
	} {
		var output bytes.Buffer
		if err := writer(&output); err != nil {
			t.Fatal(err)
		}
		for _, expected := range []string{"SUPPORTED", "[CORE]", "[EXPERIMENTAL]", "Autoscaling", "FAIL", "Requirement was not met."} {
			if !strings.Contains(html.UnescapeString(output.String()), expected) {
				t.Errorf("missing %q from %s", expected, output.String())
			}
		}
	}
	if restored.Evidence[0].Status != model.StatusFail || restored.Plan.Capabilities[1].Disposition != "supported" {
		t.Fatal("maturity presentation changed observed status or planned disposition")
	}
}

func TestHistoricalMaturityIsNotInvented(t *testing.T) {
	c := model.Capability{Name: "horizontal-autoscaling", Disposition: "supported"}
	if model.RecordedMaturity(c) != "not_recorded" || maturityLabel(c) != "" {
		t.Fatal("assigned today's maturity to historical evidence")
	}
	c.Limitations = []string{model.CoreMaturity, model.ExperimentalMaturity}
	if model.RecordedMaturity(c) != "not_recorded" {
		t.Fatal("conflicting claims must not establish maturity")
	}
}
