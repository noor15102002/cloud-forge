package render

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/noor15102002/cloud-forge/pkg/model"
)

const terminalFailureReasonLimit = 8

var safeProbeFailureClasses = map[string]bool{
	"timeout": true, "connection_refused": true, "connection_reset": true,
	"connection_closed": true, "transport_error": true, "http_status": true,
	"invalid_http_status": true, "body_unreadable": true, "body_limit": true,
	"invalid_json": true, "semantic_mismatch": true,
}

func nonnegativeCount(value string) (uint64, bool) {
	number, err := strconv.ParseUint(value, 10, 64)
	return number, err == nil
}

func measurementValues(evidence model.Evidence) map[string]string {
	result := make(map[string]string, len(evidence.Measurements))
	for _, item := range evidence.Measurements {
		result[item.Name] = item.Value
	}
	return result
}

// failureDetailsText accepts only known category names and numeric counts.
// Arbitrary report measurements, transport text and response contents cannot
// become terminal diagnostics through this path.
func failureDetailsText(w io.Writer, evidence model.Evidence) error {
	if evidence.Status == model.StatusPass || evidence.Status == model.StatusSkipped {
		return nil
	}
	values := measurementValues(evidence)
	for _, prefix := range []string{"", "prerequisite_"} {
		failed, ok := nonnegativeCount(values[prefix+"failed_requests"])
		if !ok && prefix == "" {
			failed, ok = nonnegativeCount(values["dropped_requests"])
		}
		if !ok {
			continue
		}
		label := "Failed requests"
		if prefix != "" {
			label = "Prerequisite failed requests"
		}
		if total, ok := nonnegativeCount(values[prefix+"request_count"]); ok {
			if _, err := fmt.Fprintf(w, "  %s: %d / %d\n", label, failed, total); err != nil {
				return err
			}
		} else if _, err := fmt.Fprintf(w, "  %s: %d\n", label, failed); err != nil {
			return err
		}
	}
	type reason struct {
		label string
		count uint64
	}
	var reasons []reason
	for _, prefix := range []struct{ name, label string }{{"probe_", ""}, {"startup_probe_", "Startup "}, {"final_probe_", "Final "}} {
		for name, value := range values {
			count, ok := nonnegativeCount(value)
			if !ok || count == 0 {
				continue
			}
			if class, found := strings.CutPrefix(name, prefix.name+"failure_"); found && safeProbeFailureClasses[class] {
				reasons = append(reasons, reason{prefix.label + class, count})
			}
			if code, found := strings.CutPrefix(name, prefix.name+"http_status_"); found {
				status, err := strconv.Atoi(code)
				if err == nil && len(code) == 3 && status >= 300 && status <= 599 {
					reasons = append(reasons, reason{prefix.label + "HTTP " + code, count})
				}
			}
		}
	}
	sort.Slice(reasons, func(i, j int) bool {
		if reasons[i].count != reasons[j].count {
			return reasons[i].count > reasons[j].count
		}
		return reasons[i].label < reasons[j].label
	})
	if len(reasons) > 0 {
		if _, err := fmt.Fprintln(w, "  Failure reasons:"); err != nil {
			return err
		}
	}
	for index, item := range reasons {
		if index == terminalFailureReasonLimit {
			_, err := fmt.Fprintf(w, "    ... %d more categories; see JSON or Markdown.\n", len(reasons)-index)
			return err
		}
		if _, err := fmt.Fprintf(w, "    %s: %d\n", item.label, item.count); err != nil {
			return err
		}
	}
	return nil
}

func priorityFindings(findings []model.Finding) []model.Finding {
	result := append([]model.Finding(nil), findings...)
	status := map[model.Status]int{model.StatusError: 0, model.StatusFail: 1, model.StatusBlocked: 2, model.StatusWarn: 3, model.StatusPass: 4, model.StatusSkipped: 5}
	severity := map[model.Severity]int{model.SeverityCritical: 0, model.SeverityHigh: 1, model.SeverityMedium: 2, model.SeverityLow: 3, model.SeverityInfo: 4}
	sort.SliceStable(result, func(i, j int) bool {
		left, right := result[i], result[j]
		if status[left.Status] != status[right.Status] {
			return status[left.Status] < status[right.Status]
		}
		if severity[left.Severity] != severity[right.Severity] {
			return severity[left.Severity] < severity[right.Severity]
		}
		return canonicalSortKey(left) < canonicalSortKey(right)
	})
	return result
}

type scanDisplayField struct{ label, value string }

func scanSummary(run model.VerificationRun) (model.Status, []scanDisplayField, bool) {
	for _, evidence := range run.Evidence {
		if evidence.ExperimentID != "container-scan" {
			continue
		}
		values := measurementValues(evidence)
		fields := []scanDisplayField{}
		for _, field := range []struct{ name, label string }{
			{"vulnerabilities", "Findings"}, {"severity_critical", "Critical"}, {"severity_high", "High"},
			{"severity_medium", "Medium"}, {"severity_low", "Low"}, {"severity_info", "Unknown/Info"},
			{"known_fix_available", "Known fix available"},
		} {
			if count, ok := nonnegativeCount(values[field.name]); ok {
				fields = append(fields, scanDisplayField{field.label, strconv.FormatUint(count, 10)})
			}
		}
		for _, field := range []struct{ name, label string }{
			{"scanned_image_reference", "Scanned image"}, {"scanned_image_id", "Image ID"},
			{"scan_schema_version", "Trivy schema"}, {"scan_scope", "Scan scope"},
		} {
			if value := values[field.name]; value != "" {
				fields = append(fields, scanDisplayField{field.label, value})
			}
		}
		return evidence.Status, fields, true
	}
	return "", nil, false
}

func scanSummaryText(w io.Writer, run model.VerificationRun) error {
	status, fields, found := scanSummary(run)
	if !found {
		return nil
	}
	if _, err := fmt.Fprintf(w, "Container scan: %s\n", strings.ToUpper(string(status))); err != nil {
		return err
	}
	for _, field := range fields {
		if _, err := fmt.Fprintf(w, "  %s: %s\n", field.label, terminalText(field.value)); err != nil {
			return err
		}
	}
	if len(fields) > 0 && fields[0].label == "Findings" {
		_, err := fmt.Fprintf(w, "  Trivy reported %s normalized vulnerability findings; full detail is retained in JSON.\n", fields[0].value)
		return err
	}
	return nil
}

func scanSummaryMarkdown(w io.Writer, run model.VerificationRun) error {
	status, fields, found := scanSummary(run)
	if !found {
		return nil
	}
	if _, err := fmt.Fprintf(w, "\n### Container scan\n\n**Status:** %s\n", strings.ToUpper(string(status))); err != nil {
		return err
	}
	for _, field := range fields {
		if _, err := fmt.Fprintf(w, "\n- **%s:** %s", field.label, markdownText(field.value)); err != nil {
			return err
		}
	}
	if len(fields) > 0 && fields[0].label == "Findings" {
		if _, err := fmt.Fprintf(w, "\n\nTrivy reported %s normalized vulnerability findings; full detail is retained in JSON.", fields[0].value); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(w)
	return err
}

func comparisonCounts(comparison model.BaselineComparison) (regressions, improvements, numerical int) {
	for _, change := range comparison.Regressions {
		if change.Kind == model.ComparisonStatus {
			regressions++
		} else {
			numerical++
		}
	}
	for _, change := range comparison.Improvements {
		if change.Kind == model.ComparisonStatus {
			improvements++
		} else {
			numerical++
		}
	}
	return
}
