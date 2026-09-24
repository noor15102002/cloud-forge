// Package doctor inspects the local tools required by CloudForge runtime verification.
package doctor

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/internal/executor/k3d"
	"github.com/noor15102002/cloud-forge/internal/executor/trivy"
	"github.com/noor15102002/cloud-forge/internal/runtimepolicy"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

// MemoryReader reports total system memory in bytes.
type MemoryReader func() (uint64, error)

// Doctor checks the local verification prerequisites.
type Doctor struct {
	runner command.Runner
	memory MemoryReader
}

// New creates a Doctor using the platform memory detector.
func New(runner command.Runner) *Doctor {
	return &Doctor{runner: runner, memory: linuxMemoryBytes}
}

// NewWithMemory creates a Doctor with an injected memory detector for tests.
func NewWithMemory(runner command.Runner, memory MemoryReader) *Doctor {
	return &Doctor{runner: runner, memory: memory}
}

// Run applies the same endpoint, tool and Kubernetes skew policy as verify,
// but keeps each prerequisite actionable and never creates runtime resources.
func (d *Doctor) Run(ctx context.Context) model.DoctorReport {
	report := model.DoctorReport{SchemaVersion: model.SchemaVersion, Status: model.StatusPass}
	platform := model.DoctorCheck{Name: "Runtime platform", Status: model.StatusPass, Detail: "SUPPORTED: Linux/amd64 runtime contract."}
	if !runtimepolicy.PlatformSupported(runtime.GOOS, runtime.GOARCH) {
		platform.Status, platform.Detail = model.StatusFail, "UNSUPPORTED: runtime verification requires Linux/amd64."
		platform.Guidance = "Analysis and verify --plan remain available without runtime prerequisites."
	}
	report.Checks = append(report.Checks, platform)
	selection := runtimepolicy.ResolveDocker(ctx, d.runner, os.Getenv)
	endpoint := model.DoctorCheck{Name: "Docker endpoint", Status: model.StatusPass, DurationMS: selection.Observation.DurationMS, Detail: "SUPPORTED: default local Unix socket; selection origin: " + selection.Origin + "."}
	if selection.Status != "supported" {
		endpoint.Status, endpoint.Detail = model.StatusFail, "UNSUPPORTED: selected Docker endpoint is outside the local runtime contract."
		if selection.Status == "not_validated" {
			endpoint.Status, endpoint.Detail = model.StatusError, "NOT_VALIDATED: effective Docker endpoint could not be resolved."
			if selection.Observation.FailureType == model.FailureNotFound || selection.Observation.FailureType == model.FailureExit {
				endpoint.Status = model.StatusFail
			}
		}
		endpoint.Guidance = "Select the default local Linux Docker daemon and check Docker context metadata; remote endpoints, Docker VMs, TLS selectors and nondefault/rootless sockets are not qualified."
	}
	report.Checks = append(report.Checks, endpoint)
	clientVersion := ""
	for _, spec := range runtimepolicy.Tools() {
		if ctx.Err() != nil {
			report.Checks = append(report.Checks, model.DoctorCheck{Name: spec.Label, Status: model.StatusError, Detail: "Prerequisite observation was canceled; no further commands were started."})
			report.Status = model.StatusError
			return report
		}
		check := model.DoctorCheck{Name: spec.Label, Status: model.StatusPass}
		if spec.Name == "docker" && (selection.Status != "supported" || platform.Status != model.StatusPass) {
			check.Status, check.Detail = model.StatusBlocked, "Docker daemon observation was blocked by the runtime platform or endpoint prerequisite."
			check.Guidance = "Resolve the platform and Docker endpoint findings first."
			report.Checks = append(report.Checks, check)
			continue
		}
		req := command.Request{Name: spec.Command, Args: spec.Args, Timeout: 10 * time.Second, OutputLimit: 16 * 1024}
		if selection.Status == "supported" {
			req = runtimepolicy.PinDocker(req)
		}
		var result model.CommandResult
		switch spec.Name {
		case "buildx":
			result = runtimepolicy.BuildxVersion(ctx, doctorPinnedRunner{runner: d.runner, pinned: selection.Status == "supported"})
		case "trivy":
			result = trivy.Version(ctx, doctorPinnedRunner{runner: d.runner, pinned: selection.Status == "supported"})
		default:
			result = d.runner.Run(ctx, req)
		}
		check.DurationMS = result.DurationMS
		if result.FailureType != model.FailureNone || result.ExitCode != 0 {
			failure := result.FailureType
			if failure == model.FailureNone {
				failure = model.FailureExit
			}
			check.Status, check.Detail, check.Guidance = failureStatus(failure), failureDetail(failure), spec.Guidance
			if spec.Name == "docker" {
				check.Detail = dockerDaemonDetail(result)
			}
		} else if result.Truncated {
			check.Status, check.Detail, check.Guidance = model.StatusError, "Version output exceeded its bound; no version was established.", spec.Guidance
		} else {
			if spec.Name == "kubectl" {
				check.Version, _ = runtimepolicy.KubernetesVersions(result.Stdout)
				clientVersion = check.Version
			} else {
				check.Version = ParsedVersion(result.Stdout)
			}
			if check.Version == "" {
				check.Status, check.Detail, check.Guidance = model.StatusFail, "Version output was empty or unrecognized.", spec.Guidance
			} else {
				status, reason := runtimepolicy.VersionDisposition(spec, check.Version)
				check.Detail = strings.ToUpper(status) + ": " + reason
				if status == "not_validated" {
					check.Status = model.StatusWarn
				}
			}
		}
		report.Checks = append(report.Checks, check)
	}
	status, reason := runtimepolicy.KubectlCompatibility(clientVersion, k3d.KubernetesVersion)
	skew := model.DoctorCheck{Name: "kubectl/Kubernetes compatibility", Status: model.StatusPass, Detail: strings.ToUpper(status) + ": " + reason}
	if status != "supported" {
		skew.Status, skew.Guidance = model.StatusFail, "Install kubectl v1.35.5 for the pinned Kubernetes server; runtime execution rejects unsupported client/server skew."
	}
	report.Checks = append(report.Checks, skew)

	memoryCheck := model.DoctorCheck{Name: "Memory", Status: model.StatusPass}
	if total, err := d.memory(); err != nil {
		memoryCheck.Status = model.StatusWarn
		memoryCheck.Detail = "Available system memory could not be detected."
		memoryCheck.Guidance = "CloudForge is designed for machines with approximately 16 GB of memory."
	} else {
		gib := float64(total) / (1024 * 1024 * 1024)
		memoryCheck.Detail = fmt.Sprintf("%.1f GiB detected", gib)
		if gib < 8 {
			memoryCheck.Status = model.StatusWarn
			memoryCheck.Guidance = "Use at least 8 GiB; approximately 16 GiB is recommended for runtime experiments."
		}
	}
	report.Checks = append(report.Checks, memoryCheck)
	report.Status = overallStatus(report.Checks)
	return report
}

func dockerDaemonDetail(result model.CommandResult) string {
	detail := strings.ToLower(result.Stderr + " " + result.Stdout)
	switch {
	case strings.Contains(detail, "permission denied"):
		return "Docker daemon access was denied for the current user."
	case strings.Contains(detail, "cannot connect"), strings.Contains(detail, "connection refused"):
		return "Docker daemon is not running or cannot be reached."
	default:
		return "Docker daemon is unavailable."
	}
}

func overallStatus(checks []model.DoctorCheck) model.Status {
	status := model.StatusPass
	for _, check := range checks {
		if check.Status == model.StatusError {
			return model.StatusError
		}
		if check.Status == model.StatusFail || check.Status == model.StatusBlocked {
			status = model.StatusFail
		} else if check.Status == model.StatusWarn && status == model.StatusPass {
			status = model.StatusWarn
		}
	}
	return status
}

func failureStatus(value model.FailureType) model.Status {
	if value == model.FailureNotFound || value == model.FailureExit {
		return model.StatusFail
	}
	return model.StatusError
}

func failureDetail(value model.FailureType) string {
	switch value {
	case model.FailureNotFound:
		return "Executable was not found on PATH."
	case model.FailureTimeout:
		return "Version check timed out."
	case model.FailureCanceled:
		return "Version check was canceled."
	case model.FailureExit:
		return "Version check returned a non-zero exit status."
	default:
		return "Version check could not be executed."
	}
}

func linuxMemoryBytes() (uint64, error) {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	defer func() { _ = file.Close() }()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			kib, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				return 0, err
			}
			return kib * 1024, nil
		}
	}
	return 0, fmt.Errorf("MemTotal was not present in /proc/meminfo")
}

// doctorPinnedRunner applies the resolved endpoint even to helpers that create
// their own isolated request environment.
type doctorPinnedRunner struct {
	runner command.Runner
	pinned bool
}

func (r doctorPinnedRunner) Run(ctx context.Context, req command.Request) model.CommandResult {
	if r.pinned {
		req = runtimepolicy.PinDocker(req)
	}
	return r.runner.Run(ctx, req)
}
