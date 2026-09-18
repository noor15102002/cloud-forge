// Package trivy adapts Trivy image scanning into normalized CloudForge findings.
package trivy

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

// Scan contains the command result and parsed vulnerability findings.
type Scan struct {
	Command  model.CommandResult
	Findings []model.Finding
}

// Client scans built images through the Trivy CLI.
type Client struct{ runner command.Runner }

// New creates a Trivy CLI adapter.
func New(runner command.Runner) *Client { return &Client{runner: runner} }

// ScanImage scans one local image and parses Trivy's JSON contract.
func (c *Client) ScanImage(ctx context.Context, image string) (Scan, error) {
	result := c.runner.Run(ctx, command.Request{
		Name: "trivy", Args: []string{"image", "--format", "json", "--quiet", "--scanners", "vuln", "--timeout", "5m", image},
		Timeout: 6 * time.Minute, OutputLimit: 8 * 1024 * 1024,
	})
	scan := Scan{Command: result}
	if result.FailureType != model.FailureNone || result.ExitCode != 0 {
		return scan, nil
	}
	if result.Truncated {
		return scan, fmt.Errorf("trivy JSON exceeded the %d MiB output limit", 8)
	}
	findings, err := Parse([]byte(result.Stdout))
	if err != nil {
		return scan, err
	}
	scan.Findings = findings
	return scan, nil
}

type report struct {
	Results []result `json:"Results"`
}

type result struct {
	Target          string          `json:"Target"`
	Vulnerabilities []vulnerability `json:"Vulnerabilities"`
}

type vulnerability struct {
	VulnerabilityID  string `json:"VulnerabilityID"`
	PkgName          string `json:"PkgName"`
	InstalledVersion string `json:"InstalledVersion"`
	FixedVersion     string `json:"FixedVersion"`
	Severity         string `json:"Severity"`
}

// Parse converts bounded Trivy JSON into stable findings without retaining raw output.
func Parse(data []byte) ([]model.Finding, error) {
	var value report
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, fmt.Errorf("decode Trivy JSON: %w", err)
	}
	byID := map[string]model.Finding{}
	for _, scanResult := range value.Results {
		for _, vulnerability := range scanResult.Vulnerabilities {
			id := "security.trivy." + stablePart(vulnerability.VulnerabilityID) + "." + stablePart(vulnerability.PkgName)
			severity := severityOf(vulnerability.Severity)
			observed := safeText(strings.TrimSpace(vulnerability.VulnerabilityID + " in " + vulnerability.PkgName + " " + vulnerability.InstalledVersion))
			remediation := "Review the vulnerability and update or replace the affected package."
			if vulnerability.FixedVersion != "" {
				remediation = safeText("Update " + vulnerability.PkgName + " to " + vulnerability.FixedVersion + " or a later compatible fixed version.")
			}
			item := model.Finding{
				ID: id, Category: "security", Status: model.StatusWarn, Severity: severity,
				Summary:  "Trivy detected a known vulnerability in the container image.",
				Observed: observed, Expected: "no known vulnerabilities", Remediation: remediation,
				Source: &model.SourceReference{Path: "container-image", Field: safeText(targetDetail(scanResult.Target))},
			}
			if previous, exists := byID[id]; !exists || severityRank(item.Severity) > severityRank(previous.Severity) {
				byID[id] = item
			}
		}
	}
	if len(byID) == 0 {
		return []model.Finding{{
			ID: "security.trivy.vulnerabilities", Category: "security", Status: model.StatusPass, Severity: model.SeverityInfo,
			Summary: "Trivy detected no known vulnerabilities in the container image.", Observed: "0 vulnerabilities", Expected: "no known vulnerabilities",
			Source: &model.SourceReference{Path: "container-image"},
		}}, nil
	}
	findings := make([]model.Finding, 0, len(byID))
	for _, item := range byID {
		findings = append(findings, item)
	}
	sort.Slice(findings, func(i, j int) bool { return findings[i].ID < findings[j].ID })
	return findings, nil
}

func severityOf(value string) model.Severity {
	switch strings.ToLower(value) {
	case "critical":
		return model.SeverityCritical
	case "high":
		return model.SeverityHigh
	case "medium":
		return model.SeverityMedium
	case "low":
		return model.SeverityLow
	default:
		return model.SeverityInfo
	}
}

func severityRank(value model.Severity) int {
	switch value {
	case model.SeverityCritical:
		return 4
	case model.SeverityHigh:
		return 3
	case model.SeverityMedium:
		return 2
	case model.SeverityLow:
		return 1
	default:
		return 0
	}
}

func stablePart(value string) string {
	value = strings.ToLower(value)
	var builder strings.Builder
	separator := false
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			builder.WriteRune(character)
			separator = false
		} else if !separator && builder.Len() > 0 {
			builder.WriteByte('-')
			separator = true
		}
	}
	part := strings.Trim(builder.String(), "-")
	if part == "" {
		return "unknown"
	}
	return part
}

func targetDetail(value string) string {
	start := strings.IndexByte(value, '(')
	end := strings.LastIndexByte(value, ')')
	if start >= 0 && end > start {
		return strings.TrimSpace(value[start+1 : end])
	}
	return "image"
}

func safeText(value string) string {
	value = strings.Map(func(character rune) rune {
		if character < 0x20 || character == 0x7f {
			return -1
		}
		return character
	}, value)
	if len(value) > 240 {
		return value[:240]
	}
	return value
}
