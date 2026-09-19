package render

import (
	"fmt"
	"io"
	"strings"

	"github.com/noor15102002/cloud-forge/pkg/model"
)

// PlanText prints intent before expensive execution; supported does not mean passed.
func PlanText(w io.Writer, plan model.VerificationPlan) error {
	if _, err := fmt.Fprintf(w, "CloudForge runtime plan: %s (port %d)\n", strings.ToUpper(string(plan.Status)), plan.Port); err != nil {
		return err
	}
	for _, capability := range plan.Capabilities {
		if _, err := fmt.Fprintf(w, "  %-10s %-25s %s\n", strings.ToUpper(capability.Disposition), terminalText(capability.Name), terminalText(capability.Reason)); err != nil {
			return err
		}
	}
	return nil
}

func dependenciesText(w io.Writer, run model.VerificationRun) error {
	for _, dep := range run.Dependencies {
		if _, err := fmt.Fprintf(w, "Dependency %s: %s · %s · startup %d ms · %s · auth %s\n  %s\n", terminalText(dep.Name), strings.ToUpper(string(dep.Status)), terminalText(dep.Image), dep.StartupMS, terminalText(dep.NetworkExposure), terminalText(dep.Authentication), terminalText(dep.Reason)); err != nil {
			return err
		}
	}
	return nil
}
func dependenciesMarkdown(w io.Writer, run model.VerificationRun) error {
	if len(run.Dependencies) == 0 {
		return nil
	}
	if _, err := fmt.Fprintln(w, "\n### Dependencies\n\n| Dependency | Status | Image | Startup | Exposure | Authentication |\n|---|---|---|---:|---|---|"); err != nil {
		return err
	}
	for _, dep := range run.Dependencies {
		if _, err := fmt.Fprintf(w, "| %s | %s | %s | %d ms | %s | %s |\n", markdownText(dep.Name), strings.ToUpper(string(dep.Status)), markdownText(dep.Image), dep.StartupMS, markdownText(dep.NetworkExposure), markdownText(dep.Authentication)); err != nil {
			return err
		}
	}
	return nil
}

// PlanMarkdown renders a standalone inspection without implying execution.
func PlanMarkdown(w io.Writer, plan model.VerificationPlan) error {
	if _, err := fmt.Fprintf(w, "## CloudForge runtime plan\n\n**Status:** %s\n\n| Capability | Disposition | Reason |\n|---|---|---|\n", strings.ToUpper(string(plan.Status))); err != nil {
		return err
	}
	for _, c := range plan.Capabilities {
		if _, err := fmt.Fprintf(w, "| %s | %s | %s |\n", markdownText(c.Name), strings.ToUpper(c.Disposition), markdownText(c.Reason)); err != nil {
			return err
		}
	}
	return nil
}
