package render

import (
	"fmt"
	"io"
	"strings"

	"github.com/noor15102002/cloud-forge/pkg/model"
)

// PlanText prints intent before expensive execution; supported does not mean passed.
func PlanText(w io.Writer, plan model.VerificationPlan) error {
	plan = canonicalPlan(plan)
	if _, err := fmt.Fprintf(w, "CloudForge planned capabilities: %s (port %d)\n", strings.ToUpper(string(plan.Status)), plan.Port); err != nil {
		return err
	}

	if plan.Topology != nil {
		if _, err := fmt.Fprintf(w, "  Planned topology: %s\n", terminalText(topologyDescription(plan.Topology))); err != nil {
			return err
		}
	}
	if len(plan.Detected) > 0 {
		if _, err := fmt.Fprintf(w, "  Detected: %s\n", terminalText(strings.Join(plan.Detected, ", "))); err != nil {
			return err
		}
	}
	for _, capability := range plan.Capabilities {
		if _, err := fmt.Fprintf(w, "  %-10s %-25s %s\n", strings.ToUpper(capability.Disposition), terminalText(capability.Name), terminalText(capability.Reason)); err != nil {
			return err
		}

		if len(capability.Prerequisites) > 0 {
			if _, err := fmt.Fprintf(w, "    Prerequisites: %s\n    Expected mutation: %s\n    Recovery: %s\n", terminalText(strings.Join(capability.Prerequisites, ", ")), terminalText(capability.Mutation), terminalText(capability.RecoveryStrategy)); err != nil {
				return err
			}
		}
		for _, limit := range capability.Limitations {
			if _, err := fmt.Fprintf(w, "    Limitation: %s\n", terminalText(limit)); err != nil {
				return err
			}
		}
	}
	for _, limit := range plan.Limitations {
		if _, err := fmt.Fprintf(w, "  Limitation: %s\n", terminalText(limit)); err != nil {
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
	plan = canonicalPlan(plan)
	if _, err := fmt.Fprintf(w, "### Planned capabilities\n\n**Status:** %s\n\n| Capability | Planned disposition | Reason | Prerequisites | Expected mutation | Recovery |\n|---|---|---|---|---|---|\n", strings.ToUpper(string(plan.Status))); err != nil {
		return err
	}
	for _, c := range plan.Capabilities {
		if _, err := fmt.Fprintf(w, "| %s | %s | %s | %s | %s | %s |\n", markdownText(c.Name), strings.ToUpper(c.Disposition), markdownText(c.Reason), markdownText(strings.Join(c.Prerequisites, ", ")), markdownText(c.Mutation), markdownText(c.RecoveryStrategy)); err != nil {
			return err
		}
	}

	if plan.Topology != nil {
		if _, err := fmt.Fprintf(w, "\n**Planned topology:** %s\n", markdownText(topologyDescription(plan.Topology))); err != nil {
			return err
		}
	}
	for _, limit := range plan.Limitations {
		if _, err := fmt.Fprintf(w, "\n- Limitation: %s\n", markdownText(limit)); err != nil {
			return err
		}
	}
	for _, c := range plan.Capabilities {
		for _, limit := range c.Limitations {
			if _, err := fmt.Fprintf(w, "\n- %s: %s\n", markdownText(c.Name), markdownText(limit)); err != nil {
				return err
			}
		}
	}
	return nil
}

func topologyDescription(t *model.TestTopology) string {
	if t == nil {
		return "Unavailable: workload could not be planned."
	}
	surge, unavailable := "not applicable", "not applicable"
	if t.MaxSurge != nil {
		surge = *t.MaxSurge
	}
	if t.MaxUnavailable != nil {
		unavailable = *t.MaxUnavailable
	}
	return fmt.Sprintf("%s topology; %d replicas (%s); %s strategy (%s); maxUnavailable=%s; maxSurge=%s; readiness=%s %s (%s).", t.Origin, t.Replicas, t.ReplicaOrigin, t.Strategy, t.StrategyOrigin, unavailable, surge, t.ReadinessScheme, t.ReadinessPath, t.ReadinessOrigin)
}

func reliabilityText(w io.Writer, run model.VerificationRun) error {
	if run.Producer != nil {
		if _, err := fmt.Fprintf(w, "Verifier: %s (commit %s)\n", terminalText(run.Producer.Version), terminalText(run.Producer.Commit)); err != nil {
			return err
		}
	}
	if run.Compatibility != nil {
		if _, err := fmt.Fprintf(w, "Runtime compatibility: %s\n", strings.ToUpper(run.Compatibility.Status)); err != nil {
			return err
		}
		for _, check := range run.Compatibility.Checks {
			if _, err := fmt.Fprintf(w, "  %s %s: %s\n", strings.ToUpper(check.Status), terminalText(check.Name), terminalText(check.Reason)); err != nil {
				return err
			}
		}
	}
	return nil
}

func recoveryText(w io.Writer, e model.Evidence) error {
	if e.Execution != nil {
		if _, err := fmt.Fprintf(w, "  Executed: %t; mutation attempted: %t\n", e.Execution.Executed, e.Execution.MutationAttempted); err != nil {
			return err
		}
	}
	if e.Recovery != nil {
		if _, err := fmt.Fprintf(w, "  Baseline: %s — %s\n", strings.ToUpper(string(e.Recovery.Status)), terminalText(e.Recovery.Summary)); err != nil {
			return err
		}
	}
	return nil
}

func reliabilityMarkdown(w io.Writer, run model.VerificationRun) error {
	if run.Producer != nil {
		if _, err := fmt.Fprintf(w, "\n**Verifier:** %s (commit %s)\n", markdownText(run.Producer.Version), markdownText(run.Producer.Commit)); err != nil {
			return err
		}
	}
	if run.Compatibility != nil {
		if _, err := fmt.Fprintf(w, "\n**Runtime compatibility:** %s\n", strings.ToUpper(run.Compatibility.Status)); err != nil {
			return err
		}
		for _, check := range run.Compatibility.Checks {
			if _, err := fmt.Fprintf(w, "\n- %s %s: %s\n", strings.ToUpper(check.Status), markdownText(check.Name), markdownText(check.Reason)); err != nil {
				return err
			}
		}
	}
	return nil
}
