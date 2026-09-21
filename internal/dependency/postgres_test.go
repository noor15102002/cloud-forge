package dependency

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/noor15102002/cloud-forge/pkg/model"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPostgresUsesGeneratedCredentialsAndAuthenticatedExtensionProbe(t *testing.T) {
	objects := PostgresObjects("isolated", "run-a", "owned-test-credentials")
	deployment := findBackendDeployment(t, objects)
	container := deployment.Spec.Template.Spec.Containers[0]
	keys := map[string]string{"POSTGRES_USER": "username", "PGUSER": "username", "POSTGRES_PASSWORD": "password", "PGPASSWORD": "password", "POSTGRES_DB": "database", "PGDATABASE": "database"}
	for _, env := range container.Env {
		if key, ok := keys[env.Name]; ok {
			if env.Value != "" || env.ValueFrom == nil || env.ValueFrom.SecretKeyRef == nil || env.ValueFrom.SecretKeyRef.Name != "owned-test-credentials" || env.ValueFrom.SecretKeyRef.Key != key {
				t.Fatalf("credential binding %s did not use the owned Secret", env.Name)
			}
			delete(keys, env.Name)
		}
	}
	if len(keys) != 0 {
		t.Fatalf("missing credential bindings: %v", keys)
	}
	command := strings.Join(container.ReadinessProbe.Exec.Command, " ")
	for _, required := range []string{"psql", "--host=127.0.0.1", "--no-password", "--no-psqlrc", "ON_ERROR_STOP=1", "pg_extension", "extversion = '0.8.6'", "160015", "ELSE 0"} {
		if !strings.Contains(command, required) {
			t.Fatalf("readiness does not establish authenticated pinned extension/server readiness: missing %s", required)
		}
	}
	if container.ReadinessProbe.TCPSocket != nil || container.ReadinessProbe.TimeoutSeconds > 5 || container.StartupProbe.FailureThreshold*container.StartupProbe.PeriodSeconds > 120 {
		t.Fatal("PostgreSQL probe is not bounded authenticated SQL")
	}
	var initialization *corev1.ConfigMap
	for _, object := range objects {
		if config, ok := object.(*corev1.ConfigMap); ok {
			initialization = config
		}
	}
	if initialization == nil || len(initialization.Data) != 1 || initialization.Data["001-vector.sql"] != "CREATE EXTENSION IF NOT EXISTS vector VERSION '0.8.6';\n" {
		t.Fatal("provider initialization must be only the fixed vector extension, with no application schema or arbitrary SQL")
	}
	if !strings.Contains(PostgresFingerprint().ConfigurationMode, "initialized") || !strings.Contains(PostgresFingerprint().ConfigurationMode, "superuser") {
		t.Fatal("fingerprint omitted the extension or disposable superuser contract")
	}
}

func TestPostgresIsEphemeralOwnedAndBounded(t *testing.T) {
	objects := PostgresObjects("isolated", "run-a", "owned-test-credentials")
	deployment := findBackendDeployment(t, objects)
	assertBackendIsolation(t, objects, deployment, 999)
	assertBackendResources(t, deployment.Spec.Template.Spec.Containers[0], PostgresResources())
	if *deployment.Spec.Replicas != 1 || deployment.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType {
		t.Fatal("stateful test provider must have one replica without rolling overlap")
	}
	volumes := deployment.Spec.Template.Spec.Volumes
	if len(volumes) != 4 || volumes[0].EmptyDir.SizeLimit.String() != "1Gi" {
		t.Fatal("database storage must be bounded ephemeral storage")
	}
	for _, volume := range volumes {
		if volume.HostPath != nil || volume.PersistentVolumeClaim != nil || (volume.EmptyDir != nil && (volume.EmptyDir.SizeLimit == nil || volume.EmptyDir.SizeLimit.Sign() <= 0)) {
			t.Fatal("provider mounted host/persistent/unbounded storage")
		}
	}
	if PostgresFingerprint().Image != PostgresImage || PostgresFingerprint().Version != PostgresVersion || !strings.HasSuffix(PostgresImage, PostgresFingerprint().Digest) {
		t.Fatal("fingerprint diverged from the actual image")
	}
	if PostgresFingerprint().Kind != "postgresql" || deployment.Labels["cloudforge.dev/dependency"] != "postgresql" {
		t.Fatal("provider identity must match the analyzer's canonical PostgreSQL dependency name")
	}
}

func TestBackendProviderObjectsAreDeterministicAndIndependent(t *testing.T) {
	for _, test := range []struct {
		name string
		make func() []any
	}{
		{"postgres", func() []any { return PostgresObjects("isolated", "run-a", "owned-credentials") }},
		{"clamav", func() []any { return ClamAVObjects("isolated", "run-a") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			first, second := test.make(), test.make()
			a, err := json.Marshal(first)
			if err != nil {
				t.Fatal(err)
			}
			b, err := json.Marshal(second)
			if err != nil || string(a) != string(b) {
				t.Fatal("same provider inputs were not deterministic")
			}
			findBackendDeployment(t, first).Spec.Template.Spec.Containers[0].Resources.Limits[corev1.ResourceCPU] = *findBackendDeployment(t, first).Spec.Template.Spec.Containers[0].Resources.Requests.Cpu()
			c, err := json.Marshal(second)
			if err != nil || string(b) != string(c) {
				t.Fatal("provider constructors shared mutable object state across runs")
			}
		})
	}
}

func findBackendDeployment(t *testing.T, objects []any) *appsv1.Deployment {
	t.Helper()
	for _, object := range objects {
		if deployment, ok := object.(*appsv1.Deployment); ok {
			return deployment
		}
	}
	t.Fatal("provider deployment absent")
	return nil
}

func assertBackendResources(t *testing.T, container corev1.Container, declared model.ResourceRequirements) {
	t.Helper()
	if container.Resources.Requests.Cpu().String() != declared.CPURequest || container.Resources.Requests.Memory().String() != declared.MemoryRequest || container.Resources.Limits.Cpu().String() != declared.CPULimit || container.Resources.Limits.Memory().String() != declared.MemoryLimit {
		t.Fatalf("provider resource accounting diverged from actual resources: %#v", container.Resources)
	}
}

func assertBackendIsolation(t *testing.T, objects []any, deployment *appsv1.Deployment, uid int64) {
	t.Helper()
	pod := deployment.Spec.Template.Spec
	if pod.HostNetwork || pod.HostPID || pod.HostIPC || pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken {
		t.Fatal("provider acquired host namespaces or API credentials")
	}
	security := pod.SecurityContext
	if security == nil || security.RunAsUser == nil || *security.RunAsUser != uid || security.RunAsGroup == nil || *security.RunAsGroup != uid || security.RunAsNonRoot == nil || !*security.RunAsNonRoot || security.FSGroup == nil || *security.FSGroup != uid || security.SeccompProfile == nil || security.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Fatal("provider must run under its pinned non-root identity and filesystem group")
	}
	for _, object := range objects {
		metadata, ok := object.(metav1.Object)
		if !ok || metadata.GetLabels()["cloudforge.dev/run-id"] != "run-a" || metadata.GetLabels()["app.kubernetes.io/managed-by"] != "cloudforge" {
			t.Fatal("provider object missing ownership")
		}
		if service, ok := object.(*corev1.Service); ok {
			if service.Spec.Type != corev1.ServiceTypeClusterIP || len(service.Spec.ExternalIPs) != 0 || !reflect.DeepEqual(service.Spec.Selector, deployment.Spec.Template.Labels) {
				t.Fatal("provider Service escaped its run or private cluster endpoint")
			}
			for _, port := range service.Spec.Ports {
				if port.NodePort != 0 {
					t.Fatal("provider exposed a node port")
				}
			}
		}
	}
	containers := append(append([]corev1.Container{}, pod.Containers...), pod.InitContainers...)
	for _, container := range containers {
		security := container.SecurityContext
		if security == nil || security.AllowPrivilegeEscalation == nil || *security.AllowPrivilegeEscalation || security.ReadOnlyRootFilesystem == nil || !*security.ReadOnlyRootFilesystem || security.Capabilities == nil || !reflect.DeepEqual(security.Capabilities.Drop, []corev1.Capability{"ALL"}) {
			t.Fatal("provider container privileges are not bounded")
		}
		if !strings.Contains(container.Image, "@sha256:") || strings.Contains(container.Image, "latest") || container.Resources.Limits.Cpu().Sign() <= 0 || container.Resources.Limits.Memory().Sign() <= 0 {
			t.Fatal("provider container lacks an immutable image/resource limit")
		}
		for _, port := range container.Ports {
			if port.HostPort != 0 {
				t.Fatal("provider exposed a host port")
			}
		}
	}
}
