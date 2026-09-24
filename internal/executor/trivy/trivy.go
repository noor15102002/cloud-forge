// Package trivy adapts Trivy image scanning into normalized CloudForge findings.
package trivy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/internal/jsoninput"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

// Scan contains only a validated image observation; unusable responses have no findings/counts.
type Scan struct {
	Started      bool
	Command      model.CommandResult
	Findings     []model.Finding
	Measurements []model.Measurement
}

// Client adapts bounded image vulnerability observations.
type Client struct{ runner command.Runner }

// New creates a scanner using the shared command runner.
func New(runner command.Runner) *Client { return &Client{runner: runner} }

var imageIDPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
var metadataToken = regexp.MustCompile(`^[a-zA-Z0-9_.+-]{1,64}$`)

// ScanImage binds a reliable Trivy v2 observation to the independently inspected image ID.
// The tag is run-owned; matching Metadata.ImageID prevents a report for another image being accepted.
func (c *Client) ScanImage(ctx context.Context, image, expectedImageID string) (scan Scan, err error) {
	if !imageIDPattern.MatchString(expectedImageID) {
		return Scan{}, fmt.Errorf("the expected scan image identity was not established")
	}
	policy, err := newExecutionPolicy()
	if err != nil {
		return Scan{}, err
	}
	defer func() {
		if cleanupErr := policy.remove(); cleanupErr != nil {
			// A cleanup failure cannot be represented as a successful observation.
			scan.Findings = nil
			scan.Measurements = policyMeasurements()
			err = cleanupErr
		}
	}()
	result := c.runner.Run(ctx, command.Request{
		Name: "trivy", Args: []string{
			"image", "--format", "json", "--quiet", "--scanners", "vuln", "--timeout", "5m",
			"--config", policy.config, "--ignorefile", policy.ignore, "--cache-dir", policy.cache,
			"--severity", scanSeverities, "--ignore-unfixed=false", "--image-src", "docker", image,
		},
		Dir: policy.directory, Env: policy.env, ClearEnv: true,
		Timeout: 6 * time.Minute, OutputLimit: 8 * 1024 * 1024,
	})
	scan = Scan{Command: result, Started: true, Measurements: policyMeasurements()}
	if result.FailureType != model.FailureNone || result.ExitCode != 0 {
		return scan, nil
	}
	if result.Truncated {
		return scan, fmt.Errorf("trivy observation exceeded its output limit")
	}
	value, err := parseReport([]byte(result.Stdout))
	if err != nil {
		return scan, err
	}
	if value.Metadata.ImageID != expectedImageID {
		return scan, fmt.Errorf("trivy scan subject did not match the expected image identity")
	}
	// ArtifactName is the supplied local image reference in supported image reports.
	if value.ArtifactName != image {
		return scan, fmt.Errorf("trivy scan reference did not match the requested image")
	}
	findings, measurements := normalize(value)
	scan.Findings = findings
	scan.Measurements = append(scan.Measurements, measurements...)
	scan.Measurements = append(scan.Measurements,
		model.Measurement{Name: "scan_schema_version", Value: strconv.Itoa(value.SchemaVersion)},
		model.Measurement{Name: "scanned_image_id", Value: expectedImageID},
		model.Measurement{Name: "scanned_image_reference", Value: image},
		model.Measurement{Name: "scan_scope", Value: "image_a"},
	)
	return scan, nil
}

// These fields are the bounded subset of Trivy v0.74.0 pkg/types/report.go.
// Results and Vulnerabilities use omitempty upstream: absent/null/empty is valid
// only with the complete supported image envelope. Empty objects are not scans.
type report struct {
	SchemaVersion int    `json:"SchemaVersion"`
	ArtifactName  string `json:"ArtifactName"`
	ArtifactType  string `json:"ArtifactType"`
	Metadata      struct {
		ImageID string `json:"ImageID"`
	} `json:"Metadata"`
	Results []result `json:"Results"`
}
type result struct {
	Target          string          `json:"Target"`
	Class           string          `json:"Class"`
	Type            string          `json:"Type"`
	Vulnerabilities []vulnerability `json:"Vulnerabilities"`
}
type vulnerability struct {
	VulnerabilityID  string `json:"VulnerabilityID"`
	PkgName          string `json:"PkgName"`
	PkgID            string `json:"PkgID"`
	PkgPath          string `json:"PkgPath"`
	InstalledVersion string `json:"InstalledVersion"`
	FixedVersion     string `json:"FixedVersion"`
	Severity         string `json:"Severity"`
}

func parseReport(data []byte) (report, error) {
	var value report
	if err := jsoninput.Validate(data); err != nil {
		return value, fmt.Errorf("trivy JSON is incomplete or invalid")
	}
	if err := exactReportShape(data); err != nil {
		return value, err
	}
	if json.Unmarshal(data, &value) != nil {
		return value, fmt.Errorf("trivy JSON does not match the supported report shape")
	}
	if value.SchemaVersion != 2 || value.ArtifactType != "container_image" || strings.TrimSpace(value.ArtifactName) == "" || !imageIDPattern.MatchString(value.Metadata.ImageID) {
		return value, fmt.Errorf("trivy did not provide a supported v2 container-image observation")
	}
	for _, r := range value.Results {
		if strings.TrimSpace(r.Target) == "" || (r.Class != "os-pkgs" && r.Class != "lang-pkgs") || !metadataToken.MatchString(r.Type) {
			return value, fmt.Errorf("trivy result has an unsupported or incomplete package observation")
		}
		for _, v := range r.Vulnerabilities {
			if strings.TrimSpace(v.VulnerabilityID) == "" || strings.TrimSpace(v.PkgName) == "" || strings.TrimSpace(v.InstalledVersion) == "" {
				return value, fmt.Errorf("trivy vulnerability record is incomplete")
			}
			switch v.Severity {
			case "UNKNOWN", "LOW", "MEDIUM", "HIGH", "CRITICAL":
			default:
				return value, fmt.Errorf("trivy vulnerability severity is unsupported")
			}
		}
	}
	return value, nil
}

// Parse validates the report envelope before normalization; ScanImage additionally
// checks it against the independently observed expected image subject.
func Parse(data []byte) ([]model.Finding, error) {
	value, err := parseReport(data)
	if err != nil {
		return nil, err
	}
	findings, _ := normalize(value)
	return findings, nil
}

type findingRecord struct {
	finding model.Finding
	fixed   bool
	key     string
}

func normalize(value report) ([]model.Finding, []model.Measurement) {
	byKey := map[string]findingRecord{}
	for _, r := range value.Results {
		for _, v := range r.Vulnerabilities {
			// JSON tuples avoid separator and punctuation collisions. Raw paths are used
			// only in the digest key, never published. Versions and targets remain distinct.
			keyBytes, _ := json.Marshal([]string{v.VulnerabilityID, v.PkgName, v.InstalledVersion, v.PkgID, v.PkgPath, r.Class, r.Type, r.Target})
			key := string(keyBytes)
			id := "security.trivy." + stablePart(v.VulnerabilityID) + "." + stablePart(v.PkgName)
			remediation := "Review the vulnerability and update or replace the affected package."
			if v.FixedVersion != "" {
				remediation = safeText("Update " + v.PkgName + " to " + v.FixedVersion + " or a later compatible fixed version.")
			}
			item := model.Finding{ID: id, Category: "security", Status: model.StatusWarn, Severity: severityOf(v.Severity),
				Summary:  "Trivy reported a vulnerability finding in the scanned image A.",
				Observed: safeText(strings.TrimSpace(v.VulnerabilityID + " in " + v.PkgName + " " + v.InstalledVersion)),
				Expected: "no known vulnerabilities reported", Remediation: remediation,
				Source: &model.SourceReference{Path: "container-image", Field: safeText(targetDetail(r.Target) + "; class=" + r.Class + "; type=" + r.Type)},
			}
			previous, exists := byKey[key]
			// Stable selection for duplicate identical package records, independent of input order.
			if !exists || severityRank(item.Severity) > severityRank(previous.finding.Severity) || (item.Severity == previous.finding.Severity && item.Remediation < previous.finding.Remediation) {
				byKey[key] = findingRecord{finding: item, fixed: v.FixedVersion != "", key: key}
			}
		}
	}
	groups := map[string]int{}
	for _, record := range byKey {
		groups[record.finding.ID]++
	}
	counts := map[model.Severity]int{}
	findings := make([]model.Finding, 0, len(byKey))
	fixed := 0
	for _, record := range byKey {
		item := record.finding
		if groups[item.ID] > 1 {
			sum := sha256.Sum256([]byte(record.key))
			item.ID += "." + hex.EncodeToString(sum[:])
		}
		counts[item.Severity]++
		if record.fixed {
			fixed++
		}
		findings = append(findings, item)
	}
	measurements := []model.Measurement{{Name: "vulnerabilities", Value: strconv.Itoa(len(findings)), Unit: "findings"}, {Name: "known_fix_available", Value: strconv.Itoa(fixed), Unit: "findings"}}
	for _, severity := range []model.Severity{model.SeverityCritical, model.SeverityHigh, model.SeverityMedium, model.SeverityLow, model.SeverityInfo} {
		measurements = append(measurements, model.Measurement{Name: "severity_" + string(severity), Value: strconv.Itoa(counts[severity]), Unit: "findings"})
	}
	if len(findings) == 0 {
		findings = append(findings, model.Finding{
			ID: "security.trivy.vulnerabilities", Category: "security", Status: model.StatusPass, Severity: model.SeverityInfo,
			Summary: "Trivy reported zero vulnerability findings in the scanned image A.", Observed: "0 normalized findings", Expected: "no known vulnerabilities reported",
			Source: &model.SourceReference{Path: "container-image"},
		})
	}
	sort.Slice(findings, func(i, j int) bool { return findings[i].ID < findings[j].ID })
	return findings, measurements
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
