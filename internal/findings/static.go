// Package findings derives deterministic, normalized findings from application metadata.
package findings

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/noor15102002/cloud-forge/pkg/model"
)

// Static evaluates the container and Kubernetes metadata supported by this release.
func Static(application model.Application) []model.Finding {
	var values []model.Finding
	values = append(values, containerFindings(application.Containers)...)
	values = append(values, deploymentFindings(application.Kubernetes.Deployments)...)
	values = append(values, serviceFindings(application.Kubernetes.Services)...)
	values = append(values, hpaFindings(application.Kubernetes.HorizontalPodScalers)...)
	sort.Slice(values, func(i, j int) bool {
		if values[i].ID == values[j].ID {
			return sourceKey(values[i].Source) < sourceKey(values[j].Source)
		}
		return values[i].ID < values[j].ID
	})
	return values
}

func containerFindings(containers []model.Container) []model.Finding {
	if len(containers) == 0 {
		return []model.Finding{
			finding("container.dockerfile", "container", model.StatusFail, model.SeverityHigh, "No root Dockerfile was detected.", "absent", "one root Dockerfile", "Add a Dockerfile at the selected application root.", nil),
		}
	}
	var values []model.Finding
	for _, container := range containers {
		source := container.Source
		identifier := "container." + identifier(source.Path)
		user := strings.TrimSpace(strings.ToLower(container.User))
		if strings.ContainsAny(user, "${}") {
			values = append(values, finding(identifier+".non-root", "container", model.StatusWarn, model.SeverityMedium, "Container runtime user cannot be resolved statically.", container.User, "a known non-root USER in the final stage", "Resolve the final USER to a concrete non-root name or numeric identifier.", &source))
		} else if user == "" || user == "root" || user == "0" || user == "0:0" {
			values = append(values, finding(identifier+".non-root", "container", model.StatusFail, model.SeverityHigh, "Container does not declare a non-root runtime user.", display(container.User, "root or unspecified"), "a non-root USER in the final stage", "Create an unprivileged user and select it with USER in the final Dockerfile stage.", &source))
		} else {
			values = append(values, finding(identifier+".non-root", "container", model.StatusPass, model.SeverityInfo, "Container declares a non-root runtime user.", container.User, "a non-root USER in the final stage", "", &source))
		}
		switch len(container.Ports) {
		case 0:
			values = append(values, finding(identifier+".port", "container", model.StatusFail, model.SeverityHigh, "Container does not declare a runtime port.", "none", "one TCP port", "Declare the application port with EXPOSE in the final Dockerfile stage.", &source))
		case 1:
			values = append(values, finding(identifier+".port", "container", model.StatusPass, model.SeverityInfo, "Container declares one runtime port.", formatPort(container.Ports[0]), "one TCP port", "", &source))
		default:
			values = append(values, finding(identifier+".port", "container", model.StatusWarn, model.SeverityMedium, "Container declares multiple runtime ports.", strconv.Itoa(len(container.Ports))+" ports", "one unambiguous TCP port", "Use the Deployment readiness probe to identify the application port, or analyze a narrower application root.", &source))
		}
	}
	return values
}

func deploymentFindings(deployments []model.Deployment) []model.Finding {
	if len(deployments) == 0 {
		return []model.Finding{finding("kubernetes.deployment", "kubernetes", model.StatusSkipped, model.SeverityInfo, "No Deployment manifest was detected.", "absent", "optional; CloudForge can generate a minimal Deployment", "", nil)}
	}
	var values []model.Finding
	for _, deployment := range deployments {
		source := deployment.Source
		prefix := "kubernetes.deployment." + identifier(namespacedName(deployment.Namespace, deployment.Name))
		readiness, health := false, false
		for _, endpoint := range deployment.Endpoints {
			readiness = readiness || endpoint.Purpose == "readiness"
			health = health || endpoint.Purpose == "liveness" || endpoint.Purpose == "startup"
		}
		if readiness {
			values = append(values, finding(prefix+".readiness-probe", "kubernetes", model.StatusPass, model.SeverityInfo, "Deployment declares a readiness probe.", "present", "readiness probe", "", &source))
		} else {
			values = append(values, finding(prefix+".readiness-probe", "kubernetes", model.StatusFail, model.SeverityHigh, "Deployment does not declare a readiness probe.", "absent", "readiness probe", "Add a readinessProbe that reflects when the application can accept traffic.", &source))
		}
		if health {
			values = append(values, finding(prefix+".health-probe", "kubernetes", model.StatusPass, model.SeverityInfo, "Deployment declares a liveness or startup probe.", "present", "liveness or startup probe", "", &source))
		} else {
			values = append(values, finding(prefix+".health-probe", "kubernetes", model.StatusFail, model.SeverityMedium, "Deployment does not declare a liveness or startup probe.", "absent", "liveness or startup probe", "Add a livenessProbe or startupProbe suited to the application's startup behavior.", &source))
		}
		replicas := int32(1)
		if deployment.Replicas != nil {
			replicas = *deployment.Replicas
		}
		if replicas >= 2 {
			values = append(values, finding(prefix+".replicas", "kubernetes", model.StatusPass, model.SeverityInfo, "Deployment declares multiple replicas.", strconv.FormatInt(int64(replicas), 10), "at least 2 replicas", "", &source))
		} else {
			values = append(values, finding(prefix+".replicas", "kubernetes", model.StatusWarn, model.SeverityMedium, "Deployment has a single replica.", strconv.FormatInt(int64(replicas), 10), "at least 2 replicas", "Use multiple replicas when the workload must remain available during disruption.", &source))
		}
		values = append(values, resourceFinding(prefix, deployment, source))
	}
	return values
}

func resourceFinding(prefix string, deployment model.Deployment, source model.SourceReference) model.Finding {
	if len(deployment.Containers) == 0 {
		return finding(prefix+".resources", "kubernetes", model.StatusFail, model.SeverityHigh, "Deployment has no analyzable containers.", "none", "CPU and memory requests and limits", "Declare at least one application container with resource requirements.", &source)
	}
	complete := 0
	declared := 0
	for _, container := range deployment.Containers {
		resource := container.Resources
		fields := []string{resource.CPURequest, resource.MemoryRequest, resource.CPULimit, resource.MemoryLimit}
		containerComplete := true
		for _, value := range fields {
			if value == "" {
				containerComplete = false
			} else {
				declared++
			}
		}
		if containerComplete {
			complete++
		}
	}
	if complete == len(deployment.Containers) {
		return finding(prefix+".resources", "kubernetes", model.StatusPass, model.SeverityInfo, "Every Deployment container declares CPU and memory requests and limits.", fmt.Sprintf("%d/%d containers complete", complete, len(deployment.Containers)), "all containers complete", "", &source)
	}
	if declared > 0 {
		return finding(prefix+".resources", "kubernetes", model.StatusWarn, model.SeverityMedium, "Deployment resource requirements are incomplete.", fmt.Sprintf("%d of %d resource fields declared", declared, len(deployment.Containers)*4), "CPU and memory requests and limits for every container", "Complete the resources.requests and resources.limits fields for every container.", &source)
	}
	return finding(prefix+".resources", "kubernetes", model.StatusFail, model.SeverityHigh, "Deployment does not declare resource requirements.", "none", "CPU and memory requests and limits for every container", "Add resources.requests and resources.limits for CPU and memory.", &source)
}

func serviceFindings(services []model.Service) []model.Finding {
	if len(services) == 0 {
		return []model.Finding{finding("kubernetes.service", "kubernetes", model.StatusSkipped, model.SeverityInfo, "No Service manifest was detected.", "absent", "optional; CloudForge can generate a minimal Service", "", nil)}
	}
	values := make([]model.Finding, 0, len(services))
	for _, service := range services {
		source := service.Source
		id := "kubernetes.service." + identifier(namespacedName(service.Namespace, service.Name)) + ".ports"
		if len(service.Ports) == 0 {
			values = append(values, finding(id, "kubernetes", model.StatusFail, model.SeverityHigh, "Service does not declare any ports.", "none", "at least one service port", "Declare spec.ports for the application Service.", &source))
		} else {
			values = append(values, finding(id, "kubernetes", model.StatusPass, model.SeverityInfo, "Service declares a port mapping.", strconv.Itoa(len(service.Ports))+" port(s)", "at least one service port", "", &source))
		}
	}
	return values
}

func hpaFindings(values []model.HorizontalPodAutoscaler) []model.Finding {
	if len(values) == 0 {
		return []model.Finding{finding("kubernetes.hpa", "kubernetes", model.StatusSkipped, model.SeverityInfo, "No HorizontalPodAutoscaler was detected.", "absent", "optional", "", nil)}
	}
	findings := make([]model.Finding, 0, len(values))
	for _, hpa := range values {
		source := hpa.Source
		id := "kubernetes.hpa." + identifier(namespacedName(hpa.Namespace, hpa.Name)) + ".range"
		minimum := int32(1)
		if hpa.MinReplicas != nil {
			minimum = *hpa.MinReplicas
		}
		observed := fmt.Sprintf("min=%d max=%d target=%s/%s", minimum, hpa.MaxReplicas, hpa.TargetKind, hpa.TargetName)
		if hpa.MaxReplicas < minimum || hpa.MaxReplicas < 1 || hpa.TargetKind == "" || hpa.TargetName == "" {
			findings = append(findings, finding(id, "kubernetes", model.StatusFail, model.SeverityHigh, "HorizontalPodAutoscaler configuration is incomplete or invalid.", observed, "a valid target and min/max replica range", "Correct the scale target and ensure maxReplicas is not lower than minReplicas.", &source))
		} else {
			findings = append(findings, finding(id, "kubernetes", model.StatusPass, model.SeverityInfo, "HorizontalPodAutoscaler declares a valid target and replica range.", observed, "a valid target and min/max replica range", "", &source))
		}
	}
	return findings
}

func finding(id, category string, status model.Status, severity model.Severity, summary, observed, expected, remediation string, source *model.SourceReference) model.Finding {
	return model.Finding{ID: id, Category: category, Status: status, Severity: severity, Summary: summary, Observed: observed, Expected: expected, Remediation: remediation, Source: source}
}

func identifier(value string) string {
	value = strings.ToLower(value)
	var builder strings.Builder
	lastSeparator := false
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			builder.WriteRune(character)
			lastSeparator = false
		} else if !lastSeparator && builder.Len() > 0 {
			builder.WriteByte('-')
			lastSeparator = true
		}
	}
	return strings.Trim(builder.String(), "-")
}

func namespacedName(namespace, name string) string {
	if namespace == "" {
		namespace = "default"
	}
	return namespace + "/" + name
}

func sourceKey(source *model.SourceReference) string {
	if source == nil {
		return ""
	}
	return fmt.Sprintf("%s/%09d/%s", source.Path, source.Document, source.Field)
}

func display(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func formatPort(port model.ContainerPort) string {
	protocol := port.Protocol
	if protocol == "" {
		protocol = "TCP"
	}
	return fmt.Sprintf("%d/%s", port.Port, strings.ToUpper(protocol))
}
