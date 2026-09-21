package verification

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/noor15102002/cloud-forge/pkg/model"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func (p plan) buildSource() *model.SourceReference {
	name := "Dockerfile"
	if p.config.Build != nil {
		name = p.config.Build.Dockerfile
	}
	return &model.SourceReference{Path: name}
}

func preservedProbe(value model.Probe) (*corev1.Probe, error) {
	if len(value.Unsupported) > 0 {
		return nil, fmt.Errorf("unsupported %s probe settings: %s", value.Purpose, strings.Join(value.Unsupported, ", "))
	}
	probe := &corev1.Probe{InitialDelaySeconds: value.InitialDelaySeconds, TimeoutSeconds: value.TimeoutSeconds,
		PeriodSeconds: value.PeriodSeconds, SuccessThreshold: value.SuccessThreshold, FailureThreshold: value.FailureThreshold,
		TerminationGracePeriodSeconds: value.TerminationGracePeriodSeconds}
	port := intstr.Parse(value.Port)
	switch value.Type {
	case "http":
		scheme := corev1.URISchemeHTTP
		if strings.EqualFold(value.Scheme, "https") {
			scheme = corev1.URISchemeHTTPS
		}
		probe.HTTPGet = &corev1.HTTPGetAction{Path: value.Path, Port: port, Scheme: scheme}
	case "tcp":
		probe.TCPSocket = &corev1.TCPSocketAction{Port: port}
	case "grpc":
		parsed, err := strconv.ParseInt(value.Port, 10, 32)
		if err != nil || parsed < 1 || parsed > 65535 {
			return nil, errors.New("invalid gRPC probe port")
		}
		probe.GRPC = &corev1.GRPCAction{Port: int32(parsed), Service: value.GRPCService}
	default:
		return nil, fmt.Errorf("unsupported %s probe type: %s", value.Purpose, value.Type)
	}
	return probe, nil
}

func boundedResourcesFor(budget model.SafetyBudget, declared corev1.ResourceRequirements, replicas int32, strategy appsv1.DeploymentStrategy, dependencies ...model.ResourceRequirements) (corev1.ResourceRequirements, error) {
	if replicas < 1 || replicas > budget.MaxReplicas {
		return declared, errors.New("replica count exceeds the local safety bound of 1–5; select an explicitly reduced test deployment")
	}
	if declared.Requests == nil {
		declared.Requests = corev1.ResourceList{}
	}
	if declared.Limits == nil {
		declared.Limits = corev1.ResourceList{}
	}
	defaults := map[corev1.ResourceName][2]string{corev1.ResourceCPU: {"100m", "500m"}, corev1.ResourceMemory: {"64Mi", "256Mi"}}
	for name, values := range defaults {
		if _, ok := declared.Requests[name]; !ok {
			declared.Requests[name] = resource.MustParse(values[0])
		}
		if _, ok := declared.Limits[name]; !ok {
			declared.Limits[name] = resource.MustParse(values[1])
		}
		request, limit := declared.Requests[name], declared.Limits[name]
		if request.Sign() <= 0 || limit.Sign() <= 0 || request.Cmp(limit) > 0 {
			return declared, errors.New("CPU/memory requests and limits must be positive, with request <= limit")
		}
	}
	surge := 0
	if strategy.Type != appsv1.RecreateDeploymentStrategyType {
		value := intstr.FromString("25%")
		if strategy.RollingUpdate != nil && strategy.RollingUpdate.MaxSurge != nil {
			value = *strategy.RollingUpdate.MaxSurge
		}
		var err error
		surge, err = intstr.GetScaledValueFromIntOrPercent(&value, int(replicas), true)
		if err != nil || surge < 0 {
			return declared, errors.New("invalid rollout maxSurge")
		}
	}
	capacity := int64(replicas) + int64(surge)
	if capacity > 10 {
		return declared, errors.New("rollout surge exceeds the local safety bound")
	}
	cpu, memory := declared.Limits[corev1.ResourceCPU], declared.Limits[corev1.ResourceMemory]
	cpuBudget, memoryBudget := resource.MustParse(budget.WorkloadCPU), resource.MustParse(budget.WorkloadMemory)
	for _, dep := range dependencies {
		cpuBudget.Sub(resource.MustParse(dep.CPULimit))
		memoryBudget.Sub(resource.MustParse(dep.MemoryLimit))
	}
	if cpuBudget.Sign() <= 0 || memoryBudget.Sign() <= 0 {
		return declared, errors.New("dependency resources exceed aggregate budget")
	}
	if cpu.Cmp(*resource.NewMilliQuantity(cpuBudget.MilliValue()/capacity, resource.DecimalSI)) > 0 || memory.Cmp(*resource.NewQuantity(memoryBudget.Value()/capacity, resource.BinarySI)) > 0 {
		return declared, fmt.Errorf("application replicas, rollout surge and dependencies exceed the aggregate %s CPU / %s workload budget", budget.WorkloadCPU, budget.WorkloadMemory)
	}
	return declared, nil
}

func modelResources(value corev1.ResourceRequirements) model.ResourceRequirements {
	cpuRequest, memoryRequest := value.Requests[corev1.ResourceCPU], value.Requests[corev1.ResourceMemory]
	cpuLimit, memoryLimit := value.Limits[corev1.ResourceCPU], value.Limits[corev1.ResourceMemory]
	return model.ResourceRequirements{CPURequest: cpuRequest.String(), MemoryRequest: memoryRequest.String(), CPULimit: cpuLimit.String(), MemoryLimit: memoryLimit.String()}
}
