// Package render produces CloudForge's human and machine-readable output.
package render

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/noor15102002/cloud-forge/pkg/model"
)

// JSON writes an indented versioned contract value.
func JSON(w io.Writer, value any) error {
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
	for _, item := range result.Diagnostics {
		if _, err := fmt.Fprintf(w, "%s: %s\n", strings.ToUpper(string(item.Status)), item.Message); err != nil {
			return err
		}
	}
	return nil
}

// VerificationText writes the compact human-readable runtime evidence report.
func VerificationText(w io.Writer, run model.VerificationRun) error {
	if _, err := fmt.Fprintln(w, "CloudForge Verification"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Status: %s\nRun: %s\nApplication: %s\n", strings.ToUpper(string(run.Status)), run.RunID, displayValue(run.Application)); err != nil {
		return err
	}
	for _, evidence := range run.Evidence {
		if _, err := fmt.Fprintf(w, "%-24s %-7s %s (%d ms)\n", evidence.Title, strings.ToUpper(string(evidence.Status)), evidence.Summary, evidence.DurationMS); err != nil {
			return err
		}
		for _, measurement := range evidence.Measurements {
			if _, err := fmt.Fprintf(w, "  %s: %s %s\n", measurement.Name, measurement.Value, measurement.Unit); err != nil {
				return err
			}
		}
	}
	for _, item := range run.Diagnostics {
		if _, err := fmt.Fprintf(w, "%s: %s\n", strings.ToUpper(string(item.Status)), item.Message); err != nil {
			return err
		}
		if item.Guidance != "" {
			if _, err := fmt.Fprintf(w, "  %s\n", item.Guidance); err != nil {
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

func displayValue(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}
