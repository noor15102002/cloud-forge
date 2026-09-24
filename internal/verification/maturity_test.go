package verification

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

func TestReadOnlyPlanRecordsMaturityWithoutRuntimeTools(t *testing.T) {
	s := New(runnerFunc(func(context.Context, command.Request) model.CommandResult {
		t.Fatal("read-only planning invoked a runtime tool")
		return model.CommandResult{}
	}))
	result := s.Run(context.Background(), filepath.Join("..", "..", "testdata", "healthy-node"), Options{PlanOnly: true})
	if result.ExitCode != 0 || result.Run.Plan == nil {
		t.Fatalf("plan failed: %+v", result)
	}
	want := map[string]string{"workload": "core", "deployment-readiness": "core", "horizontal-autoscaling": "experimental", "readiness-gating": "experimental", "inflight-shutdown": "experimental", "dependency-loss": "unsupported"}
	for _, c := range result.Run.Plan.Capabilities {
		if expected, ok := want[c.Name]; ok {
			if actual := model.RecordedMaturity(c); actual != expected {
				t.Errorf("%s maturity = %s; want %s", c.Name, actual, expected)
			}
			delete(want, c.Name)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing planned capabilities: %v", want)
	}
}

func TestExperimentalDependencyDoesNotClaimCoreWorkload(t *testing.T) {
	plan := &model.VerificationPlan{Capabilities: []model.Capability{{Name: "workload"}, {Name: "dependency.postgresql"}, {Name: "deployment-readiness"}}}
	annotateMaturity(plan, model.RuntimeConfiguration{Dependencies: map[string]model.DependencySpec{"postgresql": {Enabled: true}}})
	for _, c := range plan.Capabilities {
		if model.RecordedMaturity(c) != "experimental" {
			t.Errorf("experimental workload misclassified: %+v", c)
		}
	}
}
