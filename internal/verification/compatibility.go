package verification

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/internal/executor/k3d"
	"github.com/noor15102002/cloud-forge/internal/executor/trivy"
	"github.com/noor15102002/cloud-forge/internal/runtimepolicy"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

var buildVersionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+_-]{0,63}$`)

func validIdentity(identity model.BuildIdentity) bool {
	return identity.Version != "unknown" && buildVersionPattern.MatchString(identity.Version) && commitPattern.MatchString(strings.TrimSuffix(identity.Commit, "+dirty"))
}

// Runtime preflight is deliberately after the pure planner and before builds.
// NOT_VALIDATED permits measurement with an explicit limitation; only known
// incompatible combinations are BLOCKED. Unobservable versions are ERROR.
func checkRuntimeTools(ctx context.Context, runner command.Runner, out *Outcome) bool {
	compatibility := &model.RuntimeCompatibility{Status: "supported", ExpectedKubernetes: k3d.KubernetesVersion, Checks: []model.CompatibilityCheck{}}
	out.Run.Compatibility = compatibility
	specs := runtimepolicy.Tools()
	for _, spec := range specs {
		if spec.Name == "k6" && out.Run.Plan != nil && plannedCapability(out.Run.Plan, "load-profile").Disposition != "supported" {
			continue
		}
		var result model.CommandResult
		switch spec.Name {
		case "buildx":
			result = runtimepolicy.BuildxVersion(ctx, runner)
		case "trivy":
			result = trivy.Version(ctx, runner)
		default:
			result = runner.Run(ctx, command.Request{Name: spec.Command, Args: spec.Args, Timeout: 10 * time.Second, OutputLimit: 16 * 1024})
		}
		version := ""
		if !failed(result) && !result.Truncated {
			if spec.Name == "kubectl" {
				version, _ = kubernetesVersions(result.Stdout)
			} else {
				version = runtimepolicy.ParsedVersion(result.Stdout)
			}
		}
		if version == "" {
			out.Run.Fingerprint.Tools = append(out.Run.Fingerprint.Tools, model.ToolVersion{Name: spec.Name, Version: "unknown"})
			addCompatibility(compatibility, spec.Name, "not_validated", "Tool version could not be observed reliably.")
			out.addError("runtime_version_unavailable", "CloudForge could not establish the runtime tool versions.", "Check "+spec.Name+" installation and access; no application build was started. "+versionObservationGuidance(result))
			return false
		}
		out.Run.Fingerprint.Tools = append(out.Run.Fingerprint.Tools, model.ToolVersion{Name: spec.Name, Version: version})
		disposition, reason := runtimepolicy.VersionDisposition(spec, version)
		addCompatibility(compatibility, spec.Name, disposition, reason)
	}
	client := toolVersion(out.Run.Fingerprint, "kubectl")
	disposition, reason := kubectlCompatibility(client, k3d.KubernetesVersion)
	addCompatibility(compatibility, "kubectl-planned-server", disposition, reason)
	return compatibilityAllowsRun(out)
}

func kubernetesVersions(data string) (string, string) {
	return runtimepolicy.KubernetesVersions(data)
}

func kubectlCompatibility(client, server string) (string, string) {
	return runtimepolicy.KubectlCompatibility(client, server)
}

func checkRuntimeServer(ctx context.Context, runner command.Runner, current plan, out *Outcome) bool {
	result := runner.Run(ctx, command.Request{Name: "kubectl", Args: []string{"--context", "k3d-" + current.clusterName, "version", "--output=json"}, Timeout: 10 * time.Second, OutputLimit: 16 * 1024})
	client, server := kubernetesVersions(result.Stdout)
	if failed(result) || result.Truncated || client == "" || server == "" {
		addCompatibility(out.Run.Compatibility, "kubectl-observed-server", "not_validated", "The actual client/server versions could not be observed reliably.")
		out.Run.Fingerprint.Tools = append(out.Run.Fingerprint.Tools, model.ToolVersion{Name: "kubernetes", Version: "unknown"})
		out.addError("runtime_version_unavailable", "CloudForge could not observe the isolated Kubernetes client/server versions.", "No application deployment was attempted; inspect the isolated API and retry. "+versionObservationGuidance(result))
		return false
	}
	out.Run.Compatibility.ObservedKubernetes = server
	for i := range out.Run.Fingerprint.Tools {
		if out.Run.Fingerprint.Tools[i].Name == "kubectl" {
			out.Run.Fingerprint.Tools[i].Version = client
		}
	}
	out.Run.Fingerprint.Tools = append(out.Run.Fingerprint.Tools, model.ToolVersion{Name: "kubernetes", Version: server})
	disposition, reason := kubectlCompatibility(client, server)
	addCompatibility(out.Run.Compatibility, "kubectl-observed-server", disposition, reason)
	disposition, reason = "supported", "Server matches the explicitly selected k3d node image."
	if server != k3d.KubernetesVersion {
		disposition, reason = "not_validated", "Observed server differs from the tested Kubernetes version."
	}
	addCompatibility(out.Run.Compatibility, "kubernetes", disposition, reason)
	completeFingerprint(out.Run.Fingerprint)
	return compatibilityAllowsRun(out)
}

func versionObservationGuidance(result model.CommandResult) string {
	if failed(result) {
		// Only normalized failure classes, timing, and fixed guidance enter the
		// report. Tool output and invocation arguments can contain private data.
		switch result.FailureType {
		case model.FailureNotFound, model.FailureExit, model.FailureTimeout, model.FailureCanceled, model.FailureExecution:
		case model.FailureNone:
			result.FailureType = model.FailureExit
		default:
			result.FailureType = model.FailureExecution
		}
		return fmt.Sprintf("Version observation failed (%s; %d ms). %s", result.FailureType, result.DurationMS, commandGuidance(result, nil))
	}
	if result.Truncated {
		return fmt.Sprintf("Version command completed in %d ms, but its output was truncated; the version is unverified.", result.DurationMS)
	}
	return fmt.Sprintf("Version command completed in %d ms, but its output did not contain the required version information.", result.DurationMS)
}

func addCompatibility(report *model.RuntimeCompatibility, name, status, reason string) {
	report.Checks = append(report.Checks, model.CompatibilityCheck{Name: name, Status: status, Reason: reason})
	if status == "unsupported" || status == "not_validated" && report.Status == "supported" {
		report.Status = status
	}
	sort.Slice(report.Checks, func(i, j int) bool { return report.Checks[i].Name < report.Checks[j].Name })
}
func compatibilityAllowsRun(out *Outcome) bool {
	if out.Run.Compatibility.Status == "unsupported" {
		out.Run.Status = model.StatusBlocked
		out.ExitCode = 1
		out.Run.Diagnostics = append(out.Run.Diagnostics, model.Diagnostic{Code: "runtime_incompatible", Status: model.StatusBlocked, Message: "Unsupported tool/runtime combination; application execution was not started.", Guidance: "Use the pinned CloudForge tool bundle and supported kubectl/server skew."})
		return false
	}
	return true
}
func toolVersion(fp *model.RunFingerprint, name string) string {
	for _, tool := range fp.Tools {
		if tool.Name == name {
			return tool.Version
		}
	}
	return ""
}
func completeFingerprint(fp *model.RunFingerprint) {
	fp.CompatibilityKey = ""
	for _, provider := range fp.Dependencies {
		if provider.Kind == "clamav" && (provider.DataVersion == "" || provider.DataTimestamp == "") {
			return
		}
	}
	sort.Slice(fp.Tools, func(i, j int) bool { return fp.Tools[i].Name < fp.Tools[j].Name })
	if !commitPattern.MatchString(fp.CloudForgeCommit) || !imagePattern.MatchString(fp.ImageID) || fp.WorkloadHash == "" {
		return
	}
	for _, name := range []string{"docker", "buildx", "k3d", "kubectl", "kubernetes", "k6", "trivy"} {
		if name == "k6" && fp.Configuration.Endpoints.Load == "" {
			continue
		}
		if version := toolVersion(fp, name); version == "" || version == "unknown" {
			return
		}
	}
	comparison := *fp
	comparison.SourceCommit, comparison.ImageID, comparison.ImageDigest, comparison.CompatibilityKey = "", "", "", ""
	comparison.SourceDirty = false
	data, _ := json.Marshal(comparison)
	fp.CompatibilityKey = hashBytes(data)
}
