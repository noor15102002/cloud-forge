package verification

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/noor15102002/cloud-forge/pkg/model"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func effectiveTopology(value *model.TopologySettings) *model.TopologySettings {
	if value == nil {
		return nil
	}
	if value.Rollout != nil && value.Rollout.Strategy == "recreate" {
		cloned := *value
		rollout := *value.Rollout
		cloned.Rollout = &rollout
		return &cloned
	}
	unavailable, surge := int32(0), int32(1)
	strategy := "rolling_update"
	if value.Rollout != nil {
		if value.Rollout.Strategy != "" {
			strategy = value.Rollout.Strategy
		}
		if value.Rollout.MaxUnavailable != nil {
			unavailable = *value.Rollout.MaxUnavailable
		}
		if value.Rollout.MaxSurge != nil {
			surge = *value.Rollout.MaxSurge
		}
	}
	return &model.TopologySettings{Replicas: value.Replicas, Rollout: &model.RolloutSettings{Strategy: strategy, MaxUnavailable: &unavailable, MaxSurge: &surge}}
}

func validateTopology(config model.RuntimeConfiguration) error {
	value := effectiveTopology(config.Topology)
	if value == nil {
		return nil
	}
	if config.SchemaVersion != "v1alpha4" && config.SchemaVersion != "v1alpha5" && config.SchemaVersion != "v1alpha6" && config.SchemaVersion != "v1alpha7" {
		return errors.New("explicit test topology requires configuration schema_version v1alpha4")
	}
	if value.Replicas < 1 || value.Replicas > safetyBudget().MaxReplicas {
		return errors.New("topology.replicas must be between 1 and 5")
	}
	r := value.Rollout
	if isWorker(config) {
		return validateWorker(config)
	}
	if r.Strategy != "rolling_update" {
		return errors.New("test topology supports only the rolling_update strategy")
	}
	if *r.MaxUnavailable < 0 || *r.MaxUnavailable > value.Replicas || *r.MaxSurge < 0 || *r.MaxSurge > safetyBudget().MaxReplicas || (*r.MaxUnavailable == 0 && *r.MaxSurge == 0) {
		return errors.New("test rollout requires integer max_unavailable between 0 and replicas and max_surge between 0 and 5; both cannot be zero")
	}
	return nil
}

func topologyStrategy(value *model.TopologySettings) appsv1.DeploymentStrategy {
	if value.Rollout.Strategy == "recreate" {
		return appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}
	}
	unavailable, surge := intstr.FromInt32(*value.Rollout.MaxUnavailable), intstr.FromInt32(*value.Rollout.MaxSurge)
	return appsv1.DeploymentStrategy{Type: appsv1.RollingUpdateDeploymentStrategyType, RollingUpdate: &appsv1.RollingUpdateDeployment{MaxUnavailable: &unavailable, MaxSurge: &surge}}
}

func testTopology(application model.Application, replicas int32, strategy appsv1.DeploymentStrategy, grace *int64, minReady int32, probes []*corev1.Probe, config model.RuntimeConfiguration) *model.TestTopology {
	result := &model.TestTopology{ConnectionPolicy: "new_connection_per_probe", Origin: "generated", ReplicaOrigin: "generated", StrategyOrigin: "kubernetes_default", ReadinessOrigin: "generated", Replicas: replicas, Strategy: "rolling_update", TerminationGraceSeconds: 30, MinReadySeconds: minReady, Probes: []model.Probe{}, ReadinessPath: config.Endpoints.Readiness, ReadinessAcceptance: config.Readiness}
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
	if config.Topology != nil {
		result.Origin = "explicit_test_configuration"
		result.ReplicaOrigin = "explicit_test_configuration"
		result.StrategyOrigin = "explicit_test_configuration"
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
		operation = "the same-source image B rollout"
		outcome.Evidence.Measurements = append(outcome.Evidence.Measurements, model.Measurement{Name: "rollout_source", Value: "same_source"})
	}
	values := map[string]string{}
	for _, m := range outcome.Evidence.Measurements {
		values[m.Name] = m.Value
	}
	failedRequests := values["failed_requests"]
	if failedRequests == "" {
		failedRequests = values["dropped_requests"]
	}
	total, downtime, interval, final := values["request_count"], values["downtime_ms"], values["probe_poll_interval_ms"], values["final_http_status"]
	if total == "" {
		return
	}
	summary := fmt.Sprintf("%s/%s sampled requests failed during %s.", failedRequests, total, operation)
	if outcome.Evidence.Status == model.StatusPass {
		summary = fmt.Sprintf("No failed requests were observed across %s probes during %s.", total, operation)
	} else if failedRequests == "0" {
		summary += " The required final health or deployment state was not established."
	}
	if interval != "" {
		summary += " Sampling interval: " + interval + " ms."
	}
	if downtime == "0" {
		summary += " No sampled failure window was observed."
	} else if downtime != "" {
		summary += " Maximum sampled failure window: " + downtime + " ms."
	}
	if final != "" {
		if final == "0" {
			summary += " Final HTTP response unavailable."
		} else {
			summary += " Final HTTP status: " + final + "."
		}
	}
	summary += " Shorter interruptions between samples cannot be excluded."
	if current.topology != nil {
		origin := strings.ReplaceAll(current.topology.Origin, "_", " ")
		summary += fmt.Sprintf(" Test topology: %s, %d replica(s), %s.", origin, replicas, current.topology.Strategy)
	}
	if id == "rolling-deployment" {
		summary += " This tests rollout mechanics from the same source; image B is not separately scanned."
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
