package verification

import (
	"strings"

	"github.com/noor15102002/cloud-forge/pkg/model"
)

// verificationFindings scopes only HTTP requirements that the explicit worker
// contract replaces. Standalone analysis and unrelated findings remain intact.
func verificationFindings(values []model.Finding, config model.RuntimeConfiguration) []model.Finding {
	if !isWorker(config) {
		return values
	}
	result := append([]model.Finding(nil), values...)
	for i := range result {
		finding := &result[i]
		switch {
		case finding.Category == "container" && strings.HasPrefix(finding.ID, "container.") && strings.HasSuffix(finding.ID, ".port"):
			finding.Summary = "Container HTTP port requirements do not apply to the explicit worker runtime."
			finding.Expected = "process liveness through the configured Redis heartbeat; no HTTP listener required"
		case finding.Category == "kubernetes" && strings.HasPrefix(finding.ID, "kubernetes.deployment.") &&
			(strings.HasSuffix(finding.ID, ".readiness-probe") || strings.HasSuffix(finding.ID, ".health-probe")):
			finding.Summary = "Deployment HTTP probe requirements do not apply to the explicit worker runtime."
			finding.Expected = "process identity and fresh advancing Redis heartbeats; HTTP probes are not part of the worker contract"
		case finding.Category == "kubernetes" && strings.HasPrefix(finding.ID, "kubernetes.deployment.") && strings.HasSuffix(finding.ID, ".replicas"):
			finding.Summary = "HTTP availability replica requirements do not apply to the explicit single-worker runtime."
			finding.Expected = "one worker with Recreate and no overlapping heartbeat writers"
		default:
			continue
		}
		finding.Status = model.StatusSkipped
		finding.Severity = model.SeverityInfo
		finding.Remediation = ""
	}
	return result
}
