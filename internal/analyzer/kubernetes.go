package analyzer

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"

	"github.com/noor15102002/cloud-forge/pkg/model"
)

type resourceIdentity struct {
	APIVersion string            `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Metadata   metav1.ObjectMeta `json:"metadata"`
}

func (a *Analyzer) analyzeKubernetes(root string, files []string, result *model.AnalysisResult) {
	for _, path := range files {
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		base := filepath.Base(path)
		if base == "compose.yaml" || base == "compose.yml" || base == "docker-compose.yaml" || base == "docker-compose.yml" || base == "Chart.yaml" || strings.Contains(path, ".github/workflows/") {
			continue
		}
		data, err := readBounded(root, path)
		if err != nil {
			result.Diagnostics = append(result.Diagnostics, diagnostic("manifest_unreadable", "Could not read YAML manifest.", path, err.Error()))
			continue
		}
		if !bytes.Contains(data, []byte("apiVersion")) || !bytes.Contains(data, []byte("kind")) {
			continue
		}
		decoder := yamlutil.NewYAMLOrJSONDecoder(bytes.NewReader(data), 64*1024)
		for document := 1; ; document++ {
			var raw json.RawMessage
			err := decoder.Decode(&raw)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				item := diagnostic("kubernetes_invalid", "Kubernetes manifest could not be decoded.", path, "Fix the YAML syntax: "+err.Error())
				item.Source.Document = document
				result.Diagnostics = append(result.Diagnostics, item)
				break
			}
			if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				continue
			}
			var identity resourceIdentity
			if err := json.Unmarshal(raw, &identity); err != nil {
				item := diagnostic("kubernetes_invalid", "Kubernetes document is not a valid object.", path, err.Error())
				item.Source.Document = document
				result.Diagnostics = append(result.Diagnostics, item)
				continue
			}
			if identity.APIVersion == "" || identity.Kind == "" {
				continue
			}
			source := model.SourceReference{Path: path, Document: document}
			if err := decodeKubernetesResource(raw, identity, source, result); err != nil {
				item := diagnostic("kubernetes_invalid", fmt.Sprintf("%s %s could not be decoded.", identity.Kind, identity.Metadata.Name), path, err.Error())
				item.Source.Document = document
				result.Diagnostics = append(result.Diagnostics, item)
			}
		}
	}
}

func decodeKubernetesResource(raw []byte, identity resourceIdentity, source model.SourceReference, result *model.AnalysisResult) error {
	switch identity.APIVersion + "/" + identity.Kind {
	case "apps/v1/Deployment":
		var value appsv1.Deployment
		if err := json.Unmarshal(raw, &value); err != nil {
			return err
		}
		result.Application.Kubernetes.Deployments = append(result.Application.Kubernetes.Deployments, convertDeployment(value, source))
	case "v1/Service":
		var value corev1.Service
		if err := json.Unmarshal(raw, &value); err != nil {
			return err
		}
		result.Application.Kubernetes.Services = append(result.Application.Kubernetes.Services, convertService(value, source))
	case "autoscaling/v1/HorizontalPodAutoscaler":
		var value autoscalingv1.HorizontalPodAutoscaler
		if err := json.Unmarshal(raw, &value); err != nil {
			return err
		}
		result.Application.Kubernetes.HorizontalPodScalers = append(result.Application.Kubernetes.HorizontalPodScalers, model.HorizontalPodAutoscaler{
			Name: value.Name, Namespace: value.Namespace, TargetKind: value.Spec.ScaleTargetRef.Kind,
			TargetName: value.Spec.ScaleTargetRef.Name, MinReplicas: value.Spec.MinReplicas,
			MaxReplicas: value.Spec.MaxReplicas, TargetCPU: value.Spec.TargetCPUUtilizationPercentage, Source: source,
		})
	case "autoscaling/v2/HorizontalPodAutoscaler":
		var value autoscalingv2.HorizontalPodAutoscaler
		if err := json.Unmarshal(raw, &value); err != nil {
			return err
		}
		result.Application.Kubernetes.HorizontalPodScalers = append(result.Application.Kubernetes.HorizontalPodScalers, convertHPAv2(value, source))
	default:
		result.Application.Kubernetes.OtherResources = append(result.Application.Kubernetes.OtherResources, model.KubernetesResource{
			APIVersion: identity.APIVersion, Kind: identity.Kind, Name: identity.Metadata.Name,
			Namespace: identity.Metadata.Namespace, Source: source,
		})
	}
	return nil
}

func convertDeployment(value appsv1.Deployment, source model.SourceReference) model.Deployment {
	deployment := model.Deployment{Name: value.Name, Namespace: value.Namespace, Replicas: value.Spec.Replicas, Source: source}
	for _, item := range value.Spec.Template.Spec.Containers {
		container := model.Container{Name: item.Name, Image: item.Image, Source: source}
		for _, port := range item.Ports {
			container.Ports = append(container.Ports, model.ContainerPort{Name: port.Name, Port: port.ContainerPort, Protocol: string(port.Protocol), Source: source})
		}
		container.Resources = convertResources(item.Resources)
		deployment.Containers = append(deployment.Containers, container)
		deployment.Endpoints = appendProbeEndpoints(deployment.Endpoints, item.ReadinessProbe, "readiness", source)
		deployment.Endpoints = appendProbeEndpoints(deployment.Endpoints, item.LivenessProbe, "liveness", source)
		deployment.Endpoints = appendProbeEndpoints(deployment.Endpoints, item.StartupProbe, "startup", source)
	}
	return deployment
}

func convertResources(value corev1.ResourceRequirements) model.ResourceRequirements {
	resources := model.ResourceRequirements{}
	if quantity, ok := value.Requests[corev1.ResourceCPU]; ok {
		resources.CPURequest = quantity.String()
	}
	if quantity, ok := value.Requests[corev1.ResourceMemory]; ok {
		resources.MemoryRequest = quantity.String()
	}
	if quantity, ok := value.Limits[corev1.ResourceCPU]; ok {
		resources.CPULimit = quantity.String()
	}
	if quantity, ok := value.Limits[corev1.ResourceMemory]; ok {
		resources.MemoryLimit = quantity.String()
	}
	return resources
}

func appendProbeEndpoints(values []model.Endpoint, probe *corev1.Probe, purpose string, source model.SourceReference) []model.Endpoint {
	if probe == nil || probe.HTTPGet == nil {
		return values
	}
	return append(values, model.Endpoint{Purpose: purpose, Path: probe.HTTPGet.Path, Port: probe.HTTPGet.Port.String(), Protocol: strings.ToLower(string(probe.HTTPGet.Scheme)), Source: source})
}

func convertService(value corev1.Service, source model.SourceReference) model.Service {
	service := model.Service{Name: value.Name, Namespace: value.Namespace, Type: string(value.Spec.Type), Source: source}
	for _, item := range value.Spec.Ports {
		service.Ports = append(service.Ports, model.ServicePort{Name: item.Name, Port: item.Port, TargetPort: item.TargetPort.String(), Protocol: string(item.Protocol)})
	}
	return service
}

func convertHPAv2(value autoscalingv2.HorizontalPodAutoscaler, source model.SourceReference) model.HorizontalPodAutoscaler {
	hpa := model.HorizontalPodAutoscaler{Name: value.Name, Namespace: value.Namespace, TargetKind: value.Spec.ScaleTargetRef.Kind, TargetName: value.Spec.ScaleTargetRef.Name, MinReplicas: value.Spec.MinReplicas, MaxReplicas: value.Spec.MaxReplicas, Source: source}
	for _, metric := range value.Spec.Metrics {
		if metric.Type == autoscalingv2.ResourceMetricSourceType && metric.Resource != nil && metric.Resource.Name == corev1.ResourceCPU && metric.Resource.Target.AverageUtilization != nil {
			hpa.TargetCPU = metric.Resource.Target.AverageUtilization
			break
		}
	}
	return hpa
}
