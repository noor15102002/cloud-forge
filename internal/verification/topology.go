package verification

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/noor15102002/cloud-forge/pkg/model"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

func testTopology(application model.Application, replicas int32, strategy appsv1.DeploymentStrategy, grace *int64, minReady int32, probes []*corev1.Probe, config model.RuntimeConfiguration) *model.TestTopology {
	result := &model.TestTopology{Origin: "generated", ReplicaOrigin: "generated", StrategyOrigin: "kubernetes_default", ReadinessOrigin: "generated", Replicas: replicas, Strategy: "rolling_update", TerminationGraceSeconds: 30, MinReadySeconds: minReady, Probes: []model.Probe{}, ReadinessPath: config.Endpoints.Readiness, ReadinessAcceptance: config.Readiness}
	if result.ReadinessPath != "" {
		result.ReadinessScheme = "http"
	}
	if grace != nil {
		result.TerminationGraceSeconds = *grace
	}
	if len(application.Kubernetes.Deployments) == 1 {
		source := application.Kubernetes.Deployments[0]
		result.Origin = "source"
		result.Source = &source.Source
		result.ReplicaOrigin = "kubernetes_default"
		if source.Replicas != nil {
			result.ReplicaOrigin = "source"
		}
		if source.Strategy != nil && source.Strategy.Type != "" {
			result.StrategyOrigin = "source"
		}
		for _, probe := range source.Probes {
			if probe.Purpose == "readiness" {
				result.ReadinessOrigin = "source"
			}
		}
		for _, endpoint := range source.Endpoints {
			if endpoint.Purpose == "readiness" {
				result.ReadinessOrigin = "source"
			}
		}
	}
	if strategy.Type == appsv1.RecreateDeploymentStrategyType {
		result.Strategy = "recreate"
	} else {
		surge, unavailable := "25%", "25%"
		if strategy.RollingUpdate != nil {
			if strategy.RollingUpdate.MaxSurge != nil {
				surge = strategy.RollingUpdate.MaxSurge.String()
			}
			if strategy.RollingUpdate.MaxUnavailable != nil {
				unavailable = strategy.RollingUpdate.MaxUnavailable.String()
			}
		}
		result.MaxSurge = &surge
		result.MaxUnavailable = &unavailable
	}
	for i, p := range probes {
		if p != nil {
			result.Probes = append(result.Probes, topologyProbe([]string{"liveness", "readiness", "startup"}[i], p))
		}
	}
	return result
}
func topologyProbe(purpose string, p *corev1.Probe) model.Probe {
	value := model.Probe{Purpose: purpose, InitialDelaySeconds: p.InitialDelaySeconds, TimeoutSeconds: p.TimeoutSeconds, PeriodSeconds: p.PeriodSeconds, SuccessThreshold: p.SuccessThreshold, FailureThreshold: p.FailureThreshold, TerminationGracePeriodSeconds: p.TerminationGracePeriodSeconds}
	if value.TimeoutSeconds == 0 {
		value.TimeoutSeconds = 1
	}
	if value.PeriodSeconds == 0 {
		value.PeriodSeconds = 10
	}
	if value.SuccessThreshold == 0 {
		value.SuccessThreshold = 1
	}
	if value.FailureThreshold == 0 {
		value.FailureThreshold = 3
	}
	switch {
	case p.HTTPGet != nil:
		value.Type = "http"
		value.Path = p.HTTPGet.Path
		value.Port = p.HTTPGet.Port.String()
		value.Scheme = strings.ToLower(string(p.HTTPGet.Scheme))
		if value.Scheme == "" {
			value.Scheme = "http"
		}
	case p.TCPSocket != nil:
		value.Type = "tcp"
		value.Port = p.TCPSocket.Port.String()
	case p.GRPC != nil:
		value.Type = "grpc"
		value.Port = strconv.Itoa(int(p.GRPC.Port))
		value.GRPCService = p.GRPC.Service
	}
	return value
}

func qualifyTopology(outcome *recoveryOutcome, current plan) {
	outcome.Evidence.Topology = current.topology
	if outcome.Evidence.Status != model.StatusPass && outcome.Evidence.Status != model.StatusFail {
		return
	}
	id := outcome.Evidence.ExperimentID
	if id != "graceful-shutdown" && id != "pod-recovery" && id != "rolling-deployment" {
		return
	}
	replicas := current.desiredReplicas
	operation := "replacement of a pod"
	if replicas == 1 {
		operation = "replacement of the only replica"
	}
	if id == "rolling-deployment" {
		operation = "the version B rollout"
	}
	summary := "Availability requirement maintained during " + operation + "."
	if outcome.Evidence.Status == model.StatusFail {
		summary = "Availability requirement not maintained during " + operation + "."
	}
	failedRequests, total, downtime := "", "", ""
	for _, m := range outcome.Evidence.Measurements {
		switch m.Name {
		case "failed_requests", "dropped_requests":
			failedRequests = m.Value
		case "request_count":
			total = m.Value
		case "downtime_ms":
			downtime = m.Value
		}
	}
	if total == "" {
		return
	}
	if total != "" && failedRequests != "" && downtime != "" {
		if outcome.Evidence.Status == model.StatusFail && failedRequests == "0" {
			summary = "The required final health or deployment state was not established during " + operation + "."
		}
		summary += fmt.Sprintf(" %s/%s probes failed; %s ms observed downtime.", failedRequests, total, downtime)
	}
	if current.topology != nil {
		origin := "source"
		if current.topology.Origin == "generated" {
			origin = "generated"
		}
		summary += fmt.Sprintf(" Test topology: %s, %d replica(s), %s.", origin, replicas, current.topology.Strategy)
	}
	if id == "graceful-shutdown" {
		summary += " Service probes do not establish a defective SIGTERM handler."
	}
	outcome.Evidence.Summary = summary
	if outcome.Finding != nil {
		outcome.Finding.Summary = summary
		if outcome.Finding.Status == model.StatusFail {
			outcome.Finding.Remediation = "Assess the tested replica count, readiness and rollout configuration against the availability requirement; use the targeted in-flight experiment to assess request draining."
		}
	}
	if outcome.Diagnostic != nil {
		outcome.Diagnostic.Message = summary
	}
}
