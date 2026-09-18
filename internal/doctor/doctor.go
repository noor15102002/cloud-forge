// Package doctor inspects the local tools required by CloudForge runtime verification.
package doctor

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/noor15102002/cloud-forge/internal/command"
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

type toolSpec struct {
	name, command string
	args          []string
	guidance      string
}

var tools = []toolSpec{
	{"Docker", "docker", []string{"--version"}, "Install Docker and ensure it is available on PATH."},
	{"k3d", "k3d", []string{"version"}, "Install k3d from https://k3d.io/."},
	{"kubectl", "kubectl", []string{"version", "--client=true"}, "Install kubectl from the Kubernetes documentation."},
	{"k6", "k6", []string{"version"}, "Install k6 from https://grafana.com/docs/k6/."},
	{"Trivy", "trivy", []string{"--version"}, "Install Trivy from https://trivy.dev/."},
}

// Run executes every environment check and returns a structured report.
func (d *Doctor) Run(ctx context.Context) model.DoctorReport {
	report := model.DoctorReport{SchemaVersion: model.SchemaVersion, Status: model.StatusPass}
	for _, spec := range tools {
		result := d.runner.Run(ctx, command.Request{Name: spec.command, Args: spec.args, Timeout: 10 * time.Second})
		check := model.DoctorCheck{Name: spec.name, Status: model.StatusPass, DurationMS: result.DurationMS}
		if result.FailureType != model.FailureNone {
			check.Status = failureStatus(result.FailureType)
			check.Detail = failureDetail(result.FailureType)
			check.Guidance = spec.guidance
		} else {
			check.Version = ParsedVersion(result.Stdout)
			if check.Version == "" {
				check.Version = ParsedVersion(result.Stderr)
			}
			if check.Version == "" {
				check.Status = model.StatusFail
				check.Detail = "Version output was empty or unrecognized."
				check.Guidance = spec.guidance
			}
		}
		report.Checks = append(report.Checks, check)
	}

	daemon := d.runner.Run(ctx, command.Request{Name: "docker", Args: []string{"info", "--format", "{{.ServerVersion}}"}, Timeout: 10 * time.Second})
	daemonCheck := model.DoctorCheck{Name: "Docker daemon", Status: model.StatusPass, DurationMS: daemon.DurationMS}
	if daemon.FailureType != model.FailureNone {
		daemonCheck.Status = failureStatus(daemon.FailureType)
		daemonCheck.Detail = dockerDaemonDetail(daemon)
		daemonCheck.Guidance = "Start Docker and ensure the current user can access its daemon, then run cloudforge doctor again."
	} else {
		daemonCheck.Version = safeFirstLine(daemon.Stdout)
	}
	report.Checks = append(report.Checks, daemonCheck)

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
		if check.Status == model.StatusFail {
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

func safeFirstLine(value string) string {
	value = strings.TrimSpace(value)
	if index := strings.IndexByte(value, '\n'); index >= 0 {
		value = value[:index]
	}
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value)
	if len(value) > 160 {
		value = value[:160]
	}
	return value
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
