package verification

import (
	"sort"

	"github.com/noor15102002/cloud-forge/pkg/model"
)

func capabilityPlan(analysis model.AnalysisResult, current plan, config model.RuntimeConfiguration, planErr error) *model.VerificationPlan {
	result := &model.VerificationPlan{SchemaVersion: model.VerificationSchemaVersion, Status: model.StatusPass, Port: current.config.Runtime.Port, Budget: safetyBudget(), Capabilities: []model.Capability{}}
	add := func(name, disposition, reason string) {
		result.Capabilities = append(result.Capabilities, model.Capability{Name: name, Disposition: disposition, Reason: reason})
		if disposition == "blocked" {
			result.Status = model.StatusBlocked
		}
	}
	if planErr != nil {
		add("workload", "blocked", planErr.Error())
	} else {
		add("workload", "supported", "Build and deploy one bounded HTTP application in an owned cluster.")
	}
	seen := map[string]bool{}
	for name, spec := range config.Dependencies {
		seen[name] = true
		switch {
		case !spec.Enabled:
			add("dependency."+name, "skipped", "Explicitly declared unnecessary for this test configuration.")
		case name == "redis":
			add("dependency.redis", "supported", "Provision pinned internal Redis and wait for readiness before deploying the application.")
		default:
			add("dependency."+name, "blocked", "This required dependency has no supported runtime provider.")
		}
	}
	for _, dep := range analysis.Application.Dependencies {
		if !seen[dep.Name] {
			seen[dep.Name] = true
			add("dependency."+dep.Name, "blocked", "Repository metadata suggests this dependency; explicitly declare whether the isolated test requires it. No external service will be inferred.")
		}
	}
	readiness := "supported"
	reason := "Observe Kubernetes readiness and bounded HTTP availability."
	if current.readinessPath == "" {
		reason = "Observe Kubernetes readiness; no HTTP endpoint is configured."
	}
	add("deployment-readiness", readiness, reason)
	if config.Readiness != nil {
		if current.readinessPath == "" {
			add("semantic-readiness", "blocked", "Readiness acceptance requires a supported HTTP readiness endpoint.")
		} else {
			add("semantic-readiness", "supported", "Check configured HTTP status and flat JSON assertions; response bodies are omitted.")
		}
	} else {
		add("semantic-readiness", "skipped", "No explicit response acceptance contract.")
	}
	for _, name := range []string{"readiness-gating", "inflight-shutdown"} {
		disposition, why := "supported", "Use the explicitly configured application control protocol."
		if config.Experiments.ControlPath == "" || current.readinessPath == "" {
			disposition, why = "skipped", "No supported explicit control protocol and HTTP endpoint."
		}
		if name == "readiness-gating" && current.desiredReplicas < 2 {
			disposition, why = "skipped", "Service gating requires at least two application replicas."
		}
		add(name, disposition, why)
	}
	for _, name := range []string{"graceful-shutdown", "pod-recovery", "rolling-deployment"} {
		disposition, why := "supported", "Measure application traffic and recovery in the isolated cluster."
		if current.readinessPath == "" {
			disposition, why = "skipped", "No supported HTTP readiness endpoint."
		}
		add(name, disposition, why)
	}
	if config.Endpoints.Load == "" {
		add("load-profile", "skipped", "No representative GET endpoint configured.")
	} else {
		add("load-profile", "supported", "Run the bounded configured GET profile.")
	}
	if current.hpaName == "" || config.Endpoints.Load == "" {
		add("horizontal-autoscaling", "skipped", "No supported HPA and representative load profile.")
	} else {
		add("horizontal-autoscaling", "supported", "Observe autoscaling only when sufficient demand is established.")
	}
	add("dependency-loss", "skipped", "Dependency disruption is outside this release's supported experiments.")
	sort.Slice(result.Capabilities, func(i, j int) bool { return result.Capabilities[i].Name < result.Capabilities[j].Name })
	return result
}

func appendUnexecuted(out *Outcome) {
	if out.Run.Plan == nil {
		return
	}
	seen := map[string]bool{}
	for _, e := range out.Run.Evidence {
		seen[e.ExperimentID] = true
	}
	for _, capability := range out.Run.Plan.Capabilities {
		if seen[capability.Name] || capability.Name == "workload" || len(capability.Name) >= 11 && capability.Name[:11] == "dependency." {
			continue
		}
		reason := capability.Reason
		if capability.Disposition == "supported" {
			reason = "Not executed because verification stopped before this experiment."
		}
		out.Run.Evidence = append(out.Run.Evidence, model.Evidence{ExperimentID: capability.Name, Title: capability.Name, Status: model.StatusSkipped, Summary: reason})
	}
}
