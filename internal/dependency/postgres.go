package dependency

import (
	"strings"

	"github.com/noor15102002/cloud-forge/pkg/model"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// PostgresImage pins the pgvector project's PostgreSQL 16 image. The registry
// index and amd64 image configuration were inspected on 2026-09-21; that
// configuration declares PostgreSQL 16.15 and the image contains pgvector 0.8.6.
const PostgresImage = "pgvector/pgvector:0.8.6-pg16-bookworm@sha256:ccc6e83d6e35e931dc7c5def2022729d5a6c370318d099181995567ff1fb4d6b"

// PostgresName is scoped to the unique disposable cluster.
const PostgresName = "cf-dependency-postgres"

// PostgresVersion identifies the PostgreSQL version in the pinned image.
const PostgresVersion = "16.15"

// PostgresVectorVersion identifies the explicitly initialized extension.
const PostgresVectorVersion = "0.8.6"

// PostgresMode records the fixed authentication, storage and extension contract.
// The generated role is a disposable database superuser, not a production role.
const PostgresMode = "ephemeral-scram-superuser-pgvector-0.8.6-initialized-data-1g"

const postgresInitializationSQL = "CREATE EXTENSION IF NOT EXISTS vector VERSION '0.8.6';\n"

// The TCP connection requires the generated password. The query fails if the
// expected server/extension is absent, unlike pg_isready or an empty SELECT.
const postgresReadinessSQL = "SELECT 1 / CASE WHEN current_setting('server_version_num')::integer = 160015 AND EXISTS (SELECT FROM pg_extension WHERE extname = 'vector' AND extversion = '0.8.6') THEN 1 ELSE 0 END;"

// PostgresResources counts against the same aggregate budget as application pods.
func PostgresResources() model.ResourceRequirements {
	return model.ResourceRequirements{CPURequest: "100m", MemoryRequest: "128Mi", CPULimit: "500m", MemoryLimit: "512Mi"}
}

// PostgresFingerprint excludes generated database names and credentials.
func PostgresFingerprint() model.DependencyFingerprint {
	return model.DependencyFingerprint{Kind: "postgresql", Image: PostgresImage, Digest: strings.Split(PostgresImage, "@")[1], Version: PostgresVersion, Resources: PostgresResources(), ConfigurationMode: PostgresMode}
}

// PostgresObjects requires a run-owned Secret with username, password and
// database keys. It never accepts SQL, source credentials or host storage.
// PostgreSQL's pinned entrypoint initializes the fresh volume and executes the
// one fixed extension statement before accepting authenticated TCP connections.
func PostgresObjects(namespace, runID, secretName string) []any {
	labels := backendDependencyLabels(PostgresName, "postgresql", runID)
	one, grace := int32(1), int64(20)
	disabled := false
	configName := PostgresName + "-init"
	readiness := &corev1.Probe{
		ProbeHandler:  corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"psql", "--host=127.0.0.1", "--no-password", "--no-psqlrc", "--set=ON_ERROR_STOP=1", "--tuples-only", "--no-align", "--command=" + postgresReadinessSQL}}},
		PeriodSeconds: 2, TimeoutSeconds: 3, FailureThreshold: 3,
	}
	startup := readiness.DeepCopy()
	startup.FailureThreshold = 60
	container := corev1.Container{
		Name: "postgres", Image: PostgresImage, ImagePullPolicy: corev1.PullIfNotPresent,
		Command: []string{"docker-entrypoint.sh"},
		Args:    []string{"postgres", "-c", "shared_buffers=128MB", "-c", "max_connections=40", "-c", "password_encryption=scram-sha-256"},
		Env: []corev1.EnvVar{
			backendSecretEnvironment("POSTGRES_USER", secretName, "username"),
			backendSecretEnvironment("POSTGRES_PASSWORD", secretName, "password"),
			backendSecretEnvironment("POSTGRES_DB", secretName, "database"),
			backendSecretEnvironment("PGUSER", secretName, "username"),
			backendSecretEnvironment("PGPASSWORD", secretName, "password"),
			backendSecretEnvironment("PGDATABASE", secretName, "database"),
			{Name: "PGDATA", Value: "/var/lib/postgresql/data/pgdata"},
			{Name: "POSTGRES_HOST_AUTH_METHOD", Value: "scram-sha-256"},
			{Name: "POSTGRES_INITDB_ARGS", Value: "--auth-host=scram-sha-256"},
			{Name: "PGCONNECT_TIMEOUT", Value: "2"},
		},
		Ports:     []corev1.ContainerPort{{Name: "postgres", ContainerPort: 5432}},
		Resources: backendDependencyResources(PostgresResources()), SecurityContext: backendContainerSecurity(),
		StartupProbe: startup, ReadinessProbe: readiness,
		VolumeMounts: []corev1.VolumeMount{
			{Name: "data", MountPath: "/var/lib/postgresql/data"},
			{Name: "socket", MountPath: "/var/run/postgresql"},
			{Name: "temporary", MountPath: "/tmp"},
			{Name: "initialization", MountPath: "/docker-entrypoint-initdb.d", ReadOnly: true},
		},
	}
	return []any{
		backendNamespace(namespace, runID),
		&corev1.ConfigMap{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
			ObjectMeta: metav1.ObjectMeta{Name: configName, Namespace: namespace, Labels: labels},
			Data:       map[string]string{"001-vector.sql": postgresInitializationSQL},
		},
		&appsv1.Deployment{
			TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
			ObjectMeta: metav1.ObjectMeta{Name: PostgresName, Namespace: namespace, Labels: labels},
			Spec: appsv1.DeploymentSpec{
				Replicas: &one, Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
				Selector: &metav1.LabelSelector{MatchLabels: labels},
				Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{
					AutomountServiceAccountToken: &disabled, TerminationGracePeriodSeconds: &grace,
					SecurityContext: backendPodSecurity(999), Containers: []corev1.Container{container},
					Volumes: []corev1.Volume{
						backendEphemeralVolume("data", "1Gi"), backendEphemeralVolume("socket", "16Mi"), backendEphemeralVolume("temporary", "64Mi"),
						{Name: "initialization", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: configName}}}},
					},
				}},
			},
		},
		backendService(namespace, PostgresName, labels, "postgres", 5432),
	}
}

func backendSecretEnvironment(name, secretName, key string) corev1.EnvVar {
	return corev1.EnvVar{Name: name, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: secretName}, Key: key}}}
}

func backendDependencyLabels(name, kind, runID string) map[string]string {
	return map[string]string{"app.kubernetes.io/name": name, "app.kubernetes.io/managed-by": "cloudforge", "cloudforge.dev/run-id": runID, "cloudforge.dev/dependency": kind}
}

func backendNamespace(namespace, runID string) *corev1.Namespace {
	return &corev1.Namespace{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"}, ObjectMeta: metav1.ObjectMeta{Name: namespace, Labels: map[string]string{"app.kubernetes.io/managed-by": "cloudforge", "cloudforge.dev/run-id": runID}}}
}

func backendPodSecurity(uid int64) *corev1.PodSecurityContext {
	nonroot := true
	changePolicy := corev1.FSGroupChangeOnRootMismatch
	return &corev1.PodSecurityContext{RunAsUser: &uid, RunAsGroup: &uid, RunAsNonRoot: &nonroot, FSGroup: &uid, FSGroupChangePolicy: &changePolicy, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}
}

func backendContainerSecurity() *corev1.SecurityContext {
	disabled, readOnly := false, true
	return &corev1.SecurityContext{AllowPrivilegeEscalation: &disabled, ReadOnlyRootFilesystem: &readOnly, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}
}

func backendDependencyResources(value model.ResourceRequirements) corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(value.CPURequest), corev1.ResourceMemory: resource.MustParse(value.MemoryRequest)},
		Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(value.CPULimit), corev1.ResourceMemory: resource.MustParse(value.MemoryLimit)},
	}
}

func backendEphemeralVolume(name, size string) corev1.Volume {
	limit := resource.MustParse(size)
	return corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &limit}}}
}

func backendService(namespace, name string, labels map[string]string, portName string, port int32) *corev1.Service {
	return &corev1.Service{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"}, ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Selector: labels, Ports: []corev1.ServicePort{{Name: portName, Port: port, TargetPort: intstr.FromInt32(port)}}},
	}
}
