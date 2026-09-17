package doctor

import (
	"context"
	"testing"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

type fakeRunner struct {
	results []model.CommandResult
	index   int
}

func (f *fakeRunner) Run(_ context.Context, _ command.Request) model.CommandResult {
	result := f.results[f.index]
	f.index++
	return result
}

func TestDoctorReportsMissingToolsAndDaemon(t *testing.T) {
	runner := &fakeRunner{results: []model.CommandResult{
		{Stdout: "Docker version 29\n"},
		{FailureType: model.FailureNotFound, ExitCode: -1},
		{Stdout: "Client Version: v1.34\n"},
		{Stdout: "k6 v1.2\n"},
		{Stdout: "Version: 0.67\n"},
		{FailureType: model.FailureExit, ExitCode: 1},
	}}
	report := NewWithMemory(runner, func() (uint64, error) { return 16 * 1024 * 1024 * 1024, nil }).Run(context.Background())
	if report.Status != model.StatusFail {
		t.Fatalf("expected fail, got %#v", report)
	}
	if report.Checks[1].Status != model.StatusFail || report.Checks[5].Name != "Docker daemon" || report.Checks[5].Status != model.StatusFail {
		t.Fatalf("unexpected checks: %#v", report.Checks)
	}
	if report.Checks[6].Status != model.StatusPass {
		t.Fatalf("unexpected memory check: %#v", report.Checks[6])
	}
}

func TestDoctorDistinguishesExecutionError(t *testing.T) {
	results := make([]model.CommandResult, 6)
	for index := range results {
		results[index] = model.CommandResult{Stdout: "ok"}
	}
	results[2] = model.CommandResult{FailureType: model.FailureTimeout, ExitCode: -1}
	report := NewWithMemory(&fakeRunner{results: results}, func() (uint64, error) { return 4 * 1024 * 1024 * 1024, nil }).Run(context.Background())
	if report.Status != model.StatusError || report.Checks[2].Status != model.StatusError || report.Checks[6].Status != model.StatusWarn {
		t.Fatalf("unexpected report: %#v", report)
	}
}
