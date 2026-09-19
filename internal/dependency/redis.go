// Package dependency builds the explicitly supported, run-owned dependency resources.
package dependency

import (
	"fmt"
	"strings"

	"github.com/noor15102002/cloud-forge/pkg/model"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// RedisImage pins the official multi-platform image by version and registry digest.
const RedisImage = "redis:7.4.6-alpine@sha256:3b73847e72874be07e6657b129a94761662b79bc0f679273757d4218573b2a98"

// RedisName is scoped to the unique disposable cluster.
const RedisName = "cf-dependency-redis"

// RedisVersion identifies the pinned provider version.
const RedisVersion = "7.4.6"

// RedisMode identifies the provider settings included in compatibility checks.
const RedisMode = "ephemeral-no-persistence-maxmemory-128mb-noeviction"

// RedisResources counts against the same aggregate budget as application replicas.
func RedisResources() model.ResourceRequirements {
	return model.ResourceRequirements{CPURequest: "50m", MemoryRequest: "64Mi", CPULimit: "250m", MemoryLimit: "256Mi"}
}

// RedisURL is an internal-only endpoint in a unique owned cluster.
func RedisURL(namespace string) string {
	return fmt.Sprintf("redis://%s.%s.svc.cluster.local:6379/0", RedisName, namespace)
}

// RedisFingerprint contains no run-specific address or credential.
func RedisFingerprint() model.DependencyFingerprint {
	return model.DependencyFingerprint{Kind: "redis", Image: RedisImage, Digest: strings.Split(RedisImage, "@")[1], Version: RedisVersion, Resources: RedisResources(), ConfigurationMode: RedisMode}
}

// RedisObjects uses official Kubernetes types; no source manifests or shell commands run.
func RedisObjects(namespace, runID string) []any {
	labels := map[string]string{"app.kubernetes.io/name": RedisName, "app.kubernetes.io/managed-by": "cloudforge", "cloudforge.dev/run-id": runID, "cloudforge.dev/dependency": "redis"}
	one := int32(1)
	uid := int64(999)
	nonroot := true
	disabled := false
	readOnly := true
	grace := int64(10)
	resources := corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("64Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m"), corev1.ResourceMemory: resource.MustParse("256Mi")}}
	return []any{
		&corev1.Namespace{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"}, ObjectMeta: metav1.ObjectMeta{Name: namespace, Labels: map[string]string{"app.kubernetes.io/managed-by": "cloudforge", "cloudforge.dev/run-id": runID}}},
		&appsv1.Deployment{TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"}, ObjectMeta: metav1.ObjectMeta{Name: RedisName, Namespace: namespace, Labels: labels}, Spec: appsv1.DeploymentSpec{Replicas: &one, Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}, Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{AutomountServiceAccountToken: &disabled, TerminationGracePeriodSeconds: &grace, SecurityContext: &corev1.PodSecurityContext{RunAsUser: &uid, RunAsNonRoot: &nonroot, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}, Containers: []corev1.Container{{Name: "redis", Image: RedisImage, ImagePullPolicy: corev1.PullIfNotPresent, Command: []string{"redis-server"}, Args: []string{"--save", "", "--appendonly", "no", "--maxmemory", "128mb", "--maxmemory-policy", "noeviction", "--protected-mode", "no"}, Resources: resources, SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &disabled, ReadOnlyRootFilesystem: &readOnly, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}, Ports: []corev1.ContainerPort{{Name: "redis", ContainerPort: 6379}}, ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"redis-cli", "ping"}}}, PeriodSeconds: 1, TimeoutSeconds: 1, FailureThreshold: 3}}}}}}},
		&corev1.Service{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"}, ObjectMeta: metav1.ObjectMeta{Name: RedisName, Namespace: namespace, Labels: labels}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Selector: labels, Ports: []corev1.ServicePort{{Name: "redis", Port: 6379, TargetPort: intstr.FromInt32(6379)}}}},
	}
}
