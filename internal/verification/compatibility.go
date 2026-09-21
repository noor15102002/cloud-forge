package verification

import (
	"context"
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/internal/doctor"
	"github.com/noor15102002/cloud-forge/internal/executor/k3d"
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
	specs := []struct {
		name   string
		args   []string
		tested string
	}{
		{"docker", []string{"info", "--format", "{{.ServerVersion}}"}, "28.0.4"},
		{"k3d", []string{"version"}, "5.9.0"},
		{"kubectl", []string{"version", "--client=true", "--output=json"}, "1.35.5"},
		{"k6", []string{"version"}, "2.2.0"},
		{"trivy", []string{"--version"}, "0.74.0"},
	}
	for _, spec := range specs {
		result := runner.Run(ctx, command.Request{Name: spec.name, Args: spec.args, Timeout: 10 * time.Second, OutputLimit: 16 * 1024})
		version := ""
		if !failed(result) && !result.Truncated {
			if spec.name == "kubectl" {
				version, _ = kubernetesVersions(result.Stdout)
			} else {
				version = doctor.ParsedVersion(result.Stdout)
			}
		}
		if version == "" {
			out.Run.Fingerprint.Tools = append(out.Run.Fingerprint.Tools, model.ToolVersion{Name: spec.name, Version: "unknown"})
			addCompatibility(compatibility, spec.name, "not_validated", "Tool version could not be observed reliably.")
			out.addError("runtime_version_unavailable", "CloudForge could not establish the runtime tool versions.", "Check "+spec.name+" installation and access; no application build was started.")
			return false
		}
		out.Run.Fingerprint.Tools = append(out.Run.Fingerprint.Tools, model.ToolVersion{Name: spec.name, Version: version})
		disposition, reason := "supported", "Version matches the tested tool bundle."
		if version != spec.tested {
			disposition, reason = "not_validated", "Version differs from the tested bundle ("+spec.tested+"); measurements are permitted with this limitation."
		}
		addCompatibility(compatibility, spec.name, disposition, reason)
	}
	client := toolVersion(out.Run.Fingerprint, "kubectl")
	disposition, reason := kubectlCompatibility(client, k3d.KubernetesVersion)
	addCompatibility(compatibility, "kubectl-planned-server", disposition, reason)
	return compatibilityAllowsRun(out)
}

func kubernetesVersions(data string) (string, string) {
	var value struct {
		Client struct {
			GitVersion string `json:"gitVersion"`
		} `json:"clientVersion"`
		Server struct {
			GitVersion string `json:"gitVersion"`
		} `json:"serverVersion"`
	}
	if json.Unmarshal([]byte(data), &value) != nil {
		return "", ""
	}
	return doctor.ParsedVersion(value.Client.GitVersion), doctor.ParsedVersion(value.Server.GitVersion)
}

func kubectlCompatibility(client, server string) (string, string) {
	parse := func(version string) (int, int, bool) {
		parts := strings.Split(version, ".")
		if len(parts) < 3 {
			return 0, 0, false
		}
		major, e1 := strconv.Atoi(parts[0])
		minor, e2 := strconv.Atoi(parts[1])
		return major, minor, e1 == nil && e2 == nil
	}
	cm, cn, cok := parse(client)
	sm, sn, sok := parse(server)
	if !cok || !sok {
		return "not_validated", "Client/server versions could not be compared."
	}
	if cm != sm || cn-sn > 1 || sn-cn > 1 {
		return "unsupported", "kubectl must use the same major and be within one minor of the Kubernetes API server."
	}
	return "supported", "kubectl is within Kubernetes' supported one-minor client/server skew."
}

func checkRuntimeServer(ctx context.Context, runner command.Runner, current plan, out *Outcome) bool {
	result := runner.Run(ctx, command.Request{Name: "kubectl", Args: []string{"--context", "k3d-" + current.clusterName, "version", "--output=json"}, Timeout: 10 * time.Second, OutputLimit: 16 * 1024})
	client, server := kubernetesVersions(result.Stdout)
	if failed(result) || result.Truncated || client == "" || server == "" {
		addCompatibility(out.Run.Compatibility, "kubectl-observed-server", "not_validated", "The actual client/server versions could not be observed reliably.")
		out.Run.Fingerprint.Tools = append(out.Run.Fingerprint.Tools, model.ToolVersion{Name: "kubernetes", Version: "unknown"})
		out.addError("runtime_version_unavailable", "CloudForge could not observe the isolated Kubernetes client/server versions.", "No application deployment was attempted; inspect the isolated API and retry.")
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
	for _, name := range []string{"docker", "k3d", "kubectl", "kubernetes", "k6", "trivy"} {
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
