// Package runtimepolicy shares the supported runtime contract between doctor
// and verification. Analysis and planning do not invoke this package's checks.
package runtimepolicy

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// Tool describes one bounded, read-only prerequisite observation.
type Tool struct {
	Name, Label, Command, Tested, Guidance string
	Args                                   []string
}

// Tools returns a fresh tool list so callers cannot mutate the shared policy.
func Tools() []Tool {
	return []Tool{
		{"docker", "Docker daemon", "docker", "28.0.4", "Start Docker and allow the current user to access the supported local daemon.", []string{"info", "--format", "{{.ServerVersion}}"}},
		{"buildx", "Docker Buildx", "docker", "", "Install the Docker Buildx CLI plugin; CloudForge uses a private docker-container builder.", []string{"buildx", "version"}},
		{"k3d", "k3d", "k3d", "5.9.0", "Install k3d from https://k3d.io/.", []string{"version"}},
		{"kubectl", "kubectl", "kubectl", "1.35.5", "Install kubectl v1.35.5; its major version must match and its minor version must be within one of the Kubernetes server.", []string{"version", "--client=true", "--output=json"}},
		{"k6", "k6", "k6", "2.2.0", "Install k6 from https://grafana.com/docs/k6/.", []string{"version"}},
		{"trivy", "Trivy", "trivy", "0.74.0", "Install Trivy from https://trivy.dev/.", []string{"--version"}},
	}
}

var versionPattern = regexp.MustCompile(`\b[vV]?([0-9]+\.[0-9]+\.[0-9]+(?:[-+][A-Za-z0-9.-]+)?)\b`)

// ParsedVersion extracts only a semantic version, never arbitrary tool output.
func ParsedVersion(value string) string {
	match := versionPattern.FindStringSubmatch(value)
	if len(match) != 2 {
		return ""
	}
	return match[1]
}

// KubernetesVersions decodes the official client/server JSON version shape.
func KubernetesVersions(data string) (string, string) {
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
	return ParsedVersion(value.Client.GitVersion), ParsedVersion(value.Server.GitVersion)
}

// VersionDisposition distinguishes validated tuples from permitted untested
// versions. It never labels an unobserved version as supported.
func VersionDisposition(tool Tool, version string) (string, string) {
	if version == "" {
		return "not_validated", "Tool version could not be observed reliably."
	}
	if tool.Tested == "" {
		return "not_validated", "Tool availability and version were observed; this version has no pinned qualification reference."
	}
	if version != tool.Tested {
		return "not_validated", "Version differs from the tested bundle (" + tool.Tested + "); measurements are permitted with this limitation."
	}
	return "supported", "Version matches the tested tool bundle."
}

// KubectlCompatibility enforces Kubernetes' supported one-minor version skew.
func KubectlCompatibility(client, server string) (string, string) {
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

// PlatformSupported bounds runtime execution to the qualified platform.
func PlatformSupported(goos, goarch string) bool { return goos == "linux" && goarch == "amd64" }
