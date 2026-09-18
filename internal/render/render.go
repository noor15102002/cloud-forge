// Package render produces CloudForge's human and machine-readable output.
package render

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"sort"
	"strings"
	"unicode"

	"github.com/noor15102002/cloud-forge/pkg/model"
)

const (
	terminalFindingLimit = 10
	markdownFindingLimit = 25
	displayRuneLimit     = 300
)

// JSON writes an indented versioned contract value.
func JSON(w io.Writer, value any) error {
	if run, ok := value.(model.VerificationRun); ok {
		value = canonicalVerification(run)
	} else if run, ok := value.(*model.VerificationRun); ok && run != nil {
		value = canonicalVerification(*run)
	}
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

// DoctorText writes the compact human-readable environment report.
func DoctorText(w io.Writer, report model.DoctorReport) error {
	if _, err := fmt.Fprintln(w, "CloudForge Environment"); err != nil {
		return err
	}
	for _, check := range report.Checks {
		value := check.Version
		if value == "" {
			value = check.Detail
		}
		if _, err := fmt.Fprintf(w, "%-16s %-7s %s\n", check.Name, strings.ToUpper(string(check.Status)), value); err != nil {
			return err
		}
		if check.Guidance != "" {
			if _, err := fmt.Fprintf(w, "  %s\n", check.Guidance); err != nil {
				return err
			}
		}
	}
	return nil
}

// AnalysisText writes the compact human-readable repository report.
func AnalysisText(w io.Writer, result model.AnalysisResult) error {
	if _, err := fmt.Fprintln(w, "CloudForge Repository Analysis"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Status: %s\nApplication: %s\n", strings.ToUpper(string(result.Status)), displayValue(result.Application.Name)); err != nil {
		return err
	}
	for _, runtime := range result.Application.Runtimes {
		framework := runtime.Framework
		if framework == "" && len(runtime.FrameworkCandidates) > 0 {
			framework = strings.Join(runtime.FrameworkCandidates, ", ")
		}
		if _, err := fmt.Fprintf(w, "Runtime: %s %s", runtime.Language, runtime.Version); err != nil {
			return err
		}
		if framework != "" {
			if _, err := fmt.Fprintf(w, " (%s)", framework); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "Containers: %d\nDeployments: %d\nServices: %d\nHPAs: %d\n", len(result.Application.Containers), len(result.Application.Kubernetes.Deployments), len(result.Application.Kubernetes.Services), len(result.Application.Kubernetes.HorizontalPodScalers)); err != nil {
		return err
	}
	if err := findingsText(w, result.Findings); err != nil {
		return err
	}
	for _, item := range result.Diagnostics {
		if _, err := fmt.Fprintf(w, "%s: %s\n", strings.ToUpper(string(item.Status)), item.Message); err != nil {
			return err
		}
	}
	return nil
}

// VerificationText writes the compact human-readable runtime evidence report.
func VerificationText(w io.Writer, run model.VerificationRun) error {
	run = canonicalVerification(run)
	if _, err := fmt.Fprintln(w, "CloudForge Verification"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Status: %s  Application: %s  Duration: %d ms\nRun: %s\n", strings.ToUpper(string(run.Status)), terminalText(displayValue(run.Application)), run.DurationMS, terminalText(run.RunID)); err != nil {
		return err
	}
	counts := countStatuses(run.Evidence)
	if _, err := fmt.Fprintf(w, "Evidence: %d passed, %d warned, %d failed, %d skipped, %d errors\n", counts[model.StatusPass], counts[model.StatusWarn], counts[model.StatusFail], counts[model.StatusSkipped], counts[model.StatusError]); err != nil {
		return err
	}
	for _, evidence := range run.Evidence {
		if _, err := fmt.Fprintf(w, "%-7s %-28s %s (%d ms)\n", strings.ToUpper(string(evidence.Status)), terminalText(evidence.Title), terminalText(evidence.Summary), evidence.DurationMS); err != nil {
			return err
		}
	}
	actionable := actionableFindings(run.Findings)
	if len(actionable) > 0 {
		if _, err := fmt.Fprintf(w, "Actionable findings: %d\n", len(actionable)); err != nil {
			return err
		}
		visible := actionable
		if len(visible) > terminalFindingLimit {
			visible = visible[:terminalFindingLimit]
		}
		if err := findingsText(w, visible); err != nil {
			return err
		}
		if omitted := len(actionable) - len(visible); omitted > 0 {
			if _, err := fmt.Fprintf(w, "... %d more actionable findings; use --format json or markdown.\n", omitted); err != nil {
				return err
			}
		}
	}
	if len(run.Diagnostics) > 0 {
		if _, err := fmt.Fprintln(w, "Diagnostics:"); err != nil {
			return err
		}
	}
	for _, item := range run.Diagnostics {
		if _, err := fmt.Fprintf(w, "%s %s: %s\n", strings.ToUpper(string(item.Status)), terminalText(item.Code), terminalText(item.Message)); err != nil {
			return err
		}
		if item.Guidance != "" {
			if _, err := fmt.Fprintf(w, "  %s\n", terminalText(item.Guidance)); err != nil {
				return err
			}
		}
	}
	if run.Environment.Kept {
		_, err := fmt.Fprintf(w, "Environment: kept k3d cluster %s\n", run.Environment.ClusterName)
		return err
	}
	return nil
}

// VerificationMarkdown writes a deterministic report suitable for a pull-request comment.
func VerificationMarkdown(w io.Writer, run model.VerificationRun) error {
	run = canonicalVerification(run)
	if _, err := fmt.Fprintf(w, "<!-- cloudforge-verification-report:%s -->\n", markdownText(run.SchemaVersion)); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "## CloudForge verification"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "\n**Status:** %s · **Application:** %s · **Duration:** %d ms\n", strings.ToUpper(string(run.Status)), markdownText(displayValue(run.Application)), run.DurationMS); err != nil {
		return err
	}
	environment := strings.Trim(strings.Join([]string{run.Environment.Backend, run.Environment.ClusterName, run.Environment.Namespace}, " / "), " /")
	if environment != "" {
		if _, err := fmt.Fprintf(w, "\n**Environment:** %s", markdownText(environment)); err != nil {
			return err
		}
		if run.Environment.Endpoint != "" {
			if _, err := fmt.Fprintf(w, " · **Endpoint:** %s", markdownText(run.Environment.Endpoint)); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(w, "\n| Experiment | Status | Duration | Result |"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "|---|---:|---:|---|"); err != nil {
		return err
	}
	measurementCount := 0
	for _, evidence := range run.Evidence {
		measurementCount += len(evidence.Measurements)
		if _, err := fmt.Fprintf(w, "| %s | **%s** | %d ms | %s |\n", markdownText(evidence.Title), strings.ToUpper(string(evidence.Status)), evidence.DurationMS, markdownText(evidence.Summary)); err != nil {
			return err
		}
	}
	if measurementCount > 0 {
		if _, err := fmt.Fprintf(w, "\n<details>\n<summary>Measurements (%d)</summary>\n\n| Experiment | Measurement | Value |\n|---|---|---:|\n", measurementCount); err != nil {
			return err
		}
		for _, evidence := range run.Evidence {
			for _, measurement := range evidence.Measurements {
				value := strings.TrimSpace(measurement.Value + " " + measurement.Unit)
				if _, err := fmt.Fprintf(w, "| %s | %s | %s |\n", markdownText(evidence.ExperimentID), markdownText(measurement.Name), markdownText(value)); err != nil {
					return err
				}
			}
		}
		if _, err := fmt.Fprintln(w, "\n</details>"); err != nil {
			return err
		}
	}
	if len(run.Findings) > 0 {
		visible := run.Findings
		if len(visible) > markdownFindingLimit {
			visible = visible[:markdownFindingLimit]
		}
		summary := fmt.Sprintf("Findings (%d)", len(run.Findings))
		if len(visible) < len(run.Findings) {
			summary = fmt.Sprintf("Findings (showing %d of %d; JSON contains all)", len(visible), len(run.Findings))
		}
		if _, err := fmt.Fprintf(w, "\n<details>\n<summary>%s</summary>\n", summary); err != nil {
			return err
		}
		for _, finding := range visible {
			if _, err := fmt.Fprintf(w, "\n### %s · %s/%s\n\n%s\n\n", markdownText(finding.ID), strings.ToUpper(string(finding.Status)), strings.ToUpper(string(finding.Severity)), markdownText(finding.Summary)); err != nil {
				return err
			}
			for _, field := range []struct{ label, value string }{
				{"Category", finding.Category}, {"Observed", finding.Observed}, {"Expected", finding.Expected},
				{"Duration", fmt.Sprintf("%d ms", finding.DurationMS)}, {"Source", sourceText(finding.Source)}, {"Remediation", finding.Remediation},
			} {
				if field.value != "" {
					if _, err := fmt.Fprintf(w, "- **%s:** %s\n", field.label, markdownText(field.value)); err != nil {
						return err
					}
				}
			}
		}
		if _, err := fmt.Fprintln(w, "\n</details>"); err != nil {
			return err
		}
	}
	if len(run.Diagnostics) > 0 {
		if _, err := fmt.Fprintln(w, "\n### Diagnostics"); err != nil {
			return err
		}
		for _, diagnostic := range run.Diagnostics {
			if _, err := fmt.Fprintf(w, "\n- **%s · %s:** %s", strings.ToUpper(string(diagnostic.Status)), markdownText(diagnostic.Code), markdownText(diagnostic.Message)); err != nil {
				return err
			}
			if diagnostic.Guidance != "" {
				if _, err := fmt.Fprintf(w, " %s", markdownText(diagnostic.Guidance)); err != nil {
					return err
				}
			}
			if source := sourceText(diagnostic.Source); source != "" {
				if _, err := fmt.Fprintf(w, " Source: %s.", markdownText(source)); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintln(w); err != nil {
				return err
			}
		}
	}
	_, err := fmt.Fprintf(w, "\n<sub>Schema %s · Run %s</sub>\n", markdownText(run.SchemaVersion), markdownText(run.RunID))
	return err
}

func canonicalVerification(run model.VerificationRun) model.VerificationRun {
	result := run
	result.Evidence = append([]model.Evidence{}, run.Evidence...)
	result.Findings = append([]model.Finding(nil), run.Findings...)
	result.Diagnostics = append([]model.Diagnostic(nil), run.Diagnostics...)
	for index := range result.Evidence {
		result.Evidence[index].Measurements = append([]model.Measurement(nil), result.Evidence[index].Measurements...)
		sort.SliceStable(result.Evidence[index].Measurements, func(i, j int) bool {
			left, right := result.Evidence[index].Measurements[i], result.Evidence[index].Measurements[j]
			return left.Name+"\x00"+left.Unit+"\x00"+left.Value < right.Name+"\x00"+right.Unit+"\x00"+right.Value
		})
	}
	sort.SliceStable(result.Evidence, func(i, j int) bool {
		return canonicalSortKey(result.Evidence[i]) < canonicalSortKey(result.Evidence[j])
	})
	sort.SliceStable(result.Findings, func(i, j int) bool {
		return canonicalSortKey(result.Findings[i]) < canonicalSortKey(result.Findings[j])
	})
	sort.SliceStable(result.Diagnostics, func(i, j int) bool {
		return canonicalSortKey(result.Diagnostics[i]) < canonicalSortKey(result.Diagnostics[j])
	})
	return result
}

func actionableFindings(findings []model.Finding) []model.Finding {
	result := make([]model.Finding, 0)
	for _, finding := range findings {
		if finding.Status == model.StatusFail || finding.Status == model.StatusWarn || finding.Status == model.StatusError {
			result = append(result, finding)
		}
	}
	return result
}

func countStatuses(evidence []model.Evidence) map[model.Status]int {
	result := map[model.Status]int{}
	for _, item := range evidence {
		result[item.Status]++
	}
	return result
}

func sourceText(source *model.SourceReference) string {
	if source == nil {
		return ""
	}
	value := source.Path
	if source.Document > 0 {
		value += fmt.Sprintf(" document %d", source.Document)
	}
	if source.Field != "" {
		value += " field " + source.Field
	}
	return value
}

func markdownText(value string) string {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	value = truncateRunes(value, displayRuneLimit)
	value = html.EscapeString(value)
	value = strings.NewReplacer(
		"\\", "&#92;", "|", "&#124;", "`", "&#96;", "@", "&#64;", "[", "&#91;", "]", "&#93;",
		"(", "&#40;", ")", "&#41;", "*", "&#42;", "_", "&#95;", "#", "&#35;", ":", "&#58;", ".", "&#46;",
	).Replace(value)
	return value
}

func findingsText(w io.Writer, findings []model.Finding) error {
	for _, finding := range findings {
		if _, err := fmt.Fprintf(w, "%s/%s %s: %s\n", strings.ToUpper(string(finding.Status)), strings.ToUpper(string(finding.Severity)), terminalText(finding.ID), terminalText(finding.Summary)); err != nil {
			return err
		}
		if finding.Remediation != "" {
			if _, err := fmt.Fprintf(w, "  %s\n", terminalText(finding.Remediation)); err != nil {
				return err
			}
		}
	}
	return nil
}

func displayValue(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

func terminalText(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	return truncateRunes(value, displayRuneLimit)
}

func canonicalSortKey(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit-1]) + "…"
}
