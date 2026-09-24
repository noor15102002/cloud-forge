package doctor

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/internal/runtimepolicy"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

type doctorRunner func(context.Context, command.Request) model.CommandResult

func (f doctorRunner) Run(ctx context.Context, req command.Request) model.CommandResult {
	return f(ctx, req)
}

func doctorSuccess(req command.Request) model.CommandResult {
	if slices.Contains(req.Args, "context") {
		return model.CommandResult{Stdout: `"unix:///var/run/docker.sock"`}
	}
	if slices.Contains(req.Args, "buildx") {
		return model.CommandResult{Stdout: "github.com/docker/buildx v0.21.2"}
	}
	if req.Name == "kubectl" {
		return model.CommandResult{Stdout: `{"clientVersion":{"gitVersion":"v1.35.5"}}`}
	}
	return model.CommandResult{Stdout: map[string]string{"docker": "28.0.4", "k3d": "5.9.0", "k6": "2.2.0", "trivy": "0.74.0"}[req.Name]}
}
func doctorCheck(t *testing.T, report model.DoctorReport, name string) model.DoctorCheck {
	t.Helper()
	for _, check := range report.Checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("missing check %q: %+v", name, report)
	return model.DoctorCheck{}
}
func TestDoctorReportsMissingToolsAndDaemon(t *testing.T) {
	runner := doctorRunner(func(_ context.Context, req command.Request) model.CommandResult {
		if req.Name == "k3d" {
			return model.CommandResult{FailureType: model.FailureNotFound, ExitCode: -1}
		}
		if slices.Contains(req.Args, "info") {
			return model.CommandResult{FailureType: model.FailureExit, ExitCode: 1, Stderr: "permission denied /private/socket"}
		}
		return doctorSuccess(req)
	})
	report := NewWithMemory(runner, func() (uint64, error) { return 16 << 30, nil }).Run(context.Background())
	if report.Status != model.StatusFail || doctorCheck(t, report, "k3d").Status != model.StatusFail || doctorCheck(t, report, "Docker daemon").Status != model.StatusFail || doctorCheck(t, report, "Memory").Status != model.StatusPass {
		t.Fatalf("unexpected checks: %+v", report)
	}
	if !strings.Contains(doctorCheck(t, report, "Docker daemon").Detail, "denied") {
		t.Fatal("daemon permission guidance lost")
	}
}
func TestDoctorDistinguishesExecutionError(t *testing.T) {
	runner := doctorRunner(func(_ context.Context, req command.Request) model.CommandResult {
		if req.Name == "kubectl" {
			return model.CommandResult{FailureType: model.FailureTimeout, ExitCode: -1}
		}
		return doctorSuccess(req)
	})
	report := NewWithMemory(runner, func() (uint64, error) { return 4 << 30, nil }).Run(context.Background())
	if report.Status != model.StatusError || doctorCheck(t, report, "kubectl").Status != model.StatusError || doctorCheck(t, report, "Memory").Status != model.StatusWarn {
		t.Fatalf("unexpected report: %+v", report)
	}
}
func TestMalformedVersionIsNotAPass(t *testing.T) {
	runner := doctorRunner(func(_ context.Context, req command.Request) model.CommandResult {
		if slices.Contains(req.Args, "info") {
			return model.CommandResult{Stdout: "unexpected arbitrary output"}
		}
		return doctorSuccess(req)
	})
	report := NewWithMemory(runner, func() (uint64, error) { return 16 << 30, nil }).Run(context.Background())
	if report.Status != model.StatusFail || doctorCheck(t, report, "Docker daemon").Version != "" || doctorCheck(t, report, "Docker daemon").Status != model.StatusFail {
		t.Fatalf("malformed passed: %+v", report)
	}
}
func TestDoctorSharesToolPolicyAndRequiresBuildx(t *testing.T) {
	for _, scenario := range []string{"available", "missing-buildx", "wrong-kubectl", "truncated", "nonzero"} {
		t.Run(scenario, func(t *testing.T) {
			runner := doctorRunner(func(_ context.Context, req command.Request) model.CommandResult {
				result := doctorSuccess(req)
				if slices.Contains(req.Args, "buildx") {
					switch scenario {
					case "missing-buildx":
						result = model.CommandResult{ExitCode: 1, FailureType: model.FailureExit}
					case "truncated":
						result.Truncated = true
					case "nonzero":
						result.ExitCode = 1
					}
				}
				if scenario == "wrong-kubectl" && req.Name == "kubectl" {
					result.Stdout = `{"clientVersion":{"gitVersion":"v1.31.1"}}`
				}
				if slices.Contains(req.Args, "info") && !slices.Contains(req.Env, "DOCKER_HOST="+runtimepolicy.DockerEndpoint) {
					t.Fatal("doctor did not pin endpoint")
				}
				if req.Name == "trivy" && !req.ClearEnv {
					t.Fatal("doctor inherited scanner settings")
				}
				return result
			})
			report := NewWithMemory(runner, func() (uint64, error) { return 16 << 30, nil }).Run(context.Background())
			for _, tool := range runtimepolicy.Tools() {
				doctorCheck(t, report, tool.Label)
			}
			if scenario == "available" {
				if report.Status != model.StatusWarn || !strings.HasPrefix(doctorCheck(t, report, "Docker Buildx").Detail, "NOT_VALIDATED") {
					t.Fatalf("unqualified buildx mislabeled: %+v", report)
				}
			} else if report.Status != model.StatusFail && report.Status != model.StatusError {
				t.Fatalf("bad prerequisite passed: %+v", report)
			}
			if scenario == "wrong-kubectl" && !strings.HasPrefix(doctorCheck(t, report, "kubectl/Kubernetes compatibility").Detail, "UNSUPPORTED") {
				t.Fatal("skew was not enforced")
			}
		})
	}
}
func TestDoctorDoesNotContactUnsupportedEndpointOrLeakSelection(t *testing.T) {
	t.Setenv("DOCKER_CONTEXT", "PRIVATE_CONTEXT")
	runner := doctorRunner(func(_ context.Context, req command.Request) model.CommandResult {
		if slices.Contains(req.Args, "context") {
			return model.CommandResult{Stdout: `"ssh://PRIVATE_USER@PRIVATE_HOST"`}
		}
		if slices.Contains(req.Args, "info") {
			t.Fatal("doctor contacted unapproved daemon")
		}
		return doctorSuccess(req)
	})
	report := NewWithMemory(runner, func() (uint64, error) { return 16 << 30, nil }).Run(context.Background())
	if report.Status != model.StatusFail || doctorCheck(t, report, "Docker daemon").Status != model.StatusBlocked {
		t.Fatalf("endpoint did not block daemon: %+v", report)
	}
	data, _ := json.Marshal(report)
	if strings.Contains(string(data), "PRIVATE_") {
		t.Fatal("private endpoint leaked")
	}
}
func TestDoctorCancellationStartsNoFurtherCommands(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	runner := doctorRunner(func(_ context.Context, req command.Request) model.CommandResult {
		calls++
		cancel()
		return doctorSuccess(req)
	})
	report := NewWithMemory(runner, func() (uint64, error) { t.Fatal("memory observed after cancellation"); return 0, nil }).Run(ctx)
	if calls != 1 || report.Status != model.StatusError {
		t.Fatalf("cancellation ignored: calls=%d report=%+v", calls, report)
	}
}

func TestMissingDockerIsAnActionablePrerequisiteFailure(t *testing.T) {
	runner := doctorRunner(func(_ context.Context, req command.Request) model.CommandResult {
		if req.Name == "docker" {
			return model.CommandResult{ExitCode: -1, FailureType: model.FailureNotFound}
		}
		return doctorSuccess(req)
	})
	report := NewWithMemory(runner, func() (uint64, error) { return 16 << 30, nil }).Run(context.Background())
	if report.Status != model.StatusFail || doctorCheck(t, report, "Docker endpoint").Status != model.StatusFail || doctorCheck(t, report, "Docker Buildx").Status != model.StatusFail {
		t.Fatalf("missing prerequisites mislabeled: %+v", report)
	}
}
