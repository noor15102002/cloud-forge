package verification

import (
	"sort"
	"strings"

	"github.com/noor15102002/cloud-forge/pkg/model"
)

func capabilityPlan(analysis model.AnalysisResult, current plan, config model.RuntimeConfiguration, planErr error) *model.VerificationPlan {
	result := &model.VerificationPlan{SchemaVersion: model.VerificationSchemaVersion, Status: model.StatusPass, Port: current.config.Runtime.Port, Budget: safetyBudget(), Capabilities: []model.Capability{}}
	result.Build = analysis.Build
	if result.Build == nil && planErr == nil {
		result.Build = &model.BuildSelection{App: ".", Dockerfile: "Dockerfile", Context: "."}
	}
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
	if current.hpaName == "" {
		reason := current.hpaSkipReason
		if reason == "" {
			reason = "No supported HPA could be selected."
		}
		add("horizontal-autoscaling", "skipped", reason)
	} else if config.Endpoints.Load == "" {
		add("horizontal-autoscaling", "skipped", "A representative load endpoint is required to test the selected HPA.")
	} else {
		add("horizontal-autoscaling", "supported", "Observe autoscaling only when sufficient demand is established.")
	}
	add("dependency-loss", "skipped", "Dependency disruption is outside this release's supported experiments.")

	result.Topology = current.topology
	detected := map[string]bool{}
	for _, runtime := range analysis.Application.Runtimes {
		detected[runtime.Language] = true
		if runtime.Framework != "" {
			detected[runtime.Framework] = true
		}
	}
	for _, dep := range analysis.Application.Dependencies {
		detected[dep.Name] = true
	}
	if current.readinessPath != "" {
		detected["http-service"] = true
	}
	for name := range detected {
		if name != "" {
			result.Detected = append(result.Detected, name)
		}
	}
	sort.Strings(result.Detected)
	result.Limitations = []string{
		"Planned support does not establish execution or a passing observation.",
		"Runtime compatibility and verifier identity are checked during execution, not by this read-only plan.",
		"Baseline restoration validates deployment state and health, not business-data equivalence.",
		"No PodDisruptionBudget or production topology is inferred.",
		"Service availability probes open new connections; persistent-client session continuity is not inferred.",
		"Minimum ready pod counts are sampled observations, excluding terminating pods; transitions between samples may be missed.",
	}
	if config.Topology != nil {
		result.Limitations = append(result.Limitations, "Replica count and rollout policy come from explicit test configuration; they are not source or production topology. Source probes and per-pod resources remain in effect.")
	}
	for i := range result.Capabilities {
		c := &result.Capabilities[i]
		switch c.Name {
		case "readiness-gating", "inflight-shutdown", "graceful-shutdown", "pod-recovery", "rolling-deployment", "load-profile", "horizontal-autoscaling":
			c.Prerequisites = []string{"dependency-readiness", "intended-image-and-revision", "planned-ready-replicas", "service-readiness"}
			if config.Readiness != nil {
				c.Prerequisites = append(c.Prerequisites, "semantic-readiness")
			}
			c.RecoveryStrategy = baselineRecoveryStrategy
			switch c.Name {
			case "readiness-gating":
				c.Prerequisites = append(c.Prerequisites, "explicit-control-protocol", "at-least-two-replicas")
				c.Mutation = "Temporarily mark a selected pod unready; restore readiness through the explicit control protocol."
			case "inflight-shutdown":
				c.Prerequisites = append(c.Prerequisites, "explicit-control-protocol")
				c.Mutation = "Terminate the pod handling an acknowledged request."
			case "graceful-shutdown", "pod-recovery":
				c.Mutation = "Delete one application pod and observe its replacement."
			case "rolling-deployment":
				c.Mutation = "Build image B and change the Deployment image; restore image A afterwards."
			case "load-profile":
				c.Prerequisites = append(c.Prerequisites, "representative-get-endpoint")
				c.Mutation = "Send bounded GET traffic; application data side effects cannot be restored generically."
			case "horizontal-autoscaling":
				c.Prerequisites = append(c.Prerequisites, "supported-hpa", "load-profile", "cpu-metrics")
				c.Mutation = "Apply the test HPA, observe demand and replicas, then remove it and restore fixed replicas."
			}
			if c.Name == "graceful-shutdown" {
				c.Limitations = []string{"Service availability probes do not prove a specific request's SIGTERM handling."}
			}
			if c.Name == "horizontal-autoscaling" {
				c.Limitations = []string{"Scaling is only required when measured demand establishes that expectation."}
			}
			sort.Strings(c.Prerequisites)
		}
	}
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
	canceled := false
	for _, d := range out.Run.Diagnostics {
		if strings.Contains(d.Code, "canceled") {
			canceled = true
		}
	}
	for _, c := range out.Run.Plan.Capabilities {
		if seen[c.Name] || c.Name == "workload" || strings.HasPrefix(c.Name, "dependency.") {
			continue
		}
		status, reason := model.StatusSkipped, c.Reason
		if c.Disposition == "supported" || c.Disposition == "blocked" {
			status, reason = model.StatusBlocked, "Execution prerequisites were not established before this experiment."
		}
		if canceled {
			status, reason = model.StatusSkipped, "Not scheduled after cancellation; previously collected evidence is preserved."
		}
		if out.Run.Status == model.StatusSkipped {
			status, reason = model.StatusSkipped, "Plan-only inspection; no experiment was executed."
		}
		out.Run.Evidence = append(out.Run.Evidence, model.Evidence{ExperimentID: c.Name, Title: c.Name, Status: status, Summary: reason, Topology: out.Run.Plan.Topology, Execution: &model.ExperimentExecution{Executed: false}})
	}
	for i := range out.Run.Evidence {
		e := &out.Run.Evidence[i]
		if e.Execution == nil {
			e.Execution = &model.ExperimentExecution{Executed: e.Status != model.StatusSkipped && e.Status != model.StatusBlocked}
		}
		switch e.ExperimentID {
		case "deployment-readiness", "semantic-readiness":
			e.Topology = out.Run.Plan.Topology
		}
	}
}
