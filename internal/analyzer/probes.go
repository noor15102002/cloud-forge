package analyzer

import (
	"sort"
	"strconv"

	"github.com/noor15102002/cloud-forge/pkg/model"
	corev1 "k8s.io/api/core/v1"
)

func safeProbe(probe *corev1.Probe, purpose string) model.Probe {
	result := model.Probe{Purpose: purpose, InitialDelaySeconds: probe.InitialDelaySeconds,
		TimeoutSeconds: probe.TimeoutSeconds, PeriodSeconds: probe.PeriodSeconds,
		SuccessThreshold: probe.SuccessThreshold, FailureThreshold: probe.FailureThreshold,
		TerminationGracePeriodSeconds: probe.TerminationGracePeriodSeconds}
	switch {
	case probe.HTTPGet != nil:
		result.Type = "http"
		result.Path, result.Port, result.Scheme = probe.HTTPGet.Path, probe.HTTPGet.Port.String(), string(probe.HTTPGet.Scheme)
		if probe.HTTPGet.Host != "" {
			result.Unsupported = append(result.Unsupported, "httpGet.host")
		}
		if len(probe.HTTPGet.HTTPHeaders) > 0 {
			result.Unsupported = append(result.Unsupported, "httpGet.httpHeaders")
		}
	case probe.TCPSocket != nil:
		result.Type, result.Port = "tcp", probe.TCPSocket.Port.String()
		if probe.TCPSocket.Host != "" {
			result.Unsupported = append(result.Unsupported, "tcpSocket.host")
		}
	case probe.GRPC != nil:
		result.Type, result.Port, result.GRPCService = "grpc", strconv.Itoa(int(probe.GRPC.Port)), probe.GRPC.Service
	case probe.Exec != nil:
		result.Type = "exec"
		result.Unsupported = []string{"exec.command"}
	default:
		result.Type = "unknown"
		result.Unsupported = []string{"probe.handler"}
	}
	return result
}

func unsupportedPodFields(pod corev1.PodSpec) []string {
	var fields []string
	checks := map[string]bool{
		"initContainers": len(pod.InitContainers) > 0, "volumes": len(pod.Volumes) > 0,
		"securityContext": pod.SecurityContext != nil, "serviceAccountName": pod.ServiceAccountName != "",
		"hostNetwork": pod.HostNetwork, "hostPID": pod.HostPID, "hostIPC": pod.HostIPC,
		"nodeSelector": len(pod.NodeSelector) > 0, "affinity": pod.Affinity != nil,
		"tolerations": len(pod.Tolerations) > 0, "topologySpreadConstraints": len(pod.TopologySpreadConstraints) > 0,
		"readinessGates": len(pod.ReadinessGates) > 0, "imagePullSecrets": len(pod.ImagePullSecrets) > 0,
		"dnsConfig": pod.DNSConfig != nil, "hostAliases": len(pod.HostAliases) > 0,
	}
	for name, present := range checks {
		if present {
			fields = append(fields, "spec.template.spec."+name)
		}
	}
	for _, container := range pod.Containers {
		checks = map[string]bool{
			"command": len(container.Command) > 0, "args": len(container.Args) > 0,
			"env": len(container.Env) > 0, "envFrom": len(container.EnvFrom) > 0,
			"volumeMounts": len(container.VolumeMounts) > 0, "volumeDevices": len(container.VolumeDevices) > 0,
			"lifecycle": container.Lifecycle != nil, "securityContext": container.SecurityContext != nil,
			"workingDir": container.WorkingDir != "", "stdin": container.Stdin, "tty": container.TTY,
		}
		for name, present := range checks {
			if present {
				fields = append(fields, "spec.template.spec.containers[]."+name)
			}
		}
		for name := range container.Resources.Requests {
			if name != corev1.ResourceCPU && name != corev1.ResourceMemory {
				fields = append(fields, "resources.requests.non_cpu_memory")
			}
		}
		for name := range container.Resources.Limits {
			if name != corev1.ResourceCPU && name != corev1.ResourceMemory {
				fields = append(fields, "resources.limits.non_cpu_memory")
			}
		}
	}
	sort.Strings(fields)
	return fields
}
