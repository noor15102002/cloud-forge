package dependency

import (
	"strings"

	"github.com/noor15102002/cloud-forge/pkg/model"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ClamAVImage pins the official multi-platform image including seed signatures.
// The registry index and image configuration were inspected on 2026-09-21.
// Seed signatures are updated once before clamd starts; image identity alone
// does not establish the signature version or freshness used by a runtime run.
const ClamAVImage = "clamav/clamav:1.5.4-debian13-slim@sha256:df80497be841a8ad57f95e04f978216241457f8f8ad608f1f682e3cd0fe63c45"

// ClamAVName is scoped to the unique disposable cluster.
const ClamAVName = "cf-dependency-clamav"

// ClamAVVersion identifies the engine in the pinned image.
const ClamAVVersion = "1.5.4"

// ClamAVMode excludes any assertion of observed signature freshness.
const ClamAVMode = "ephemeral-seeded-signatures-one-update-no-runtime-updater-maxthreads-2"

// ClamAVConfigPath is used by the direct daemon and observation clients.
const ClamAVConfigPath = "/etc/cloudforge/clamd.conf"

// ClamAVResources sets a fixed memory bound for loading the full signature DB.
// Its sufficiency requires an explicit backend profile and runtime qualification.
func ClamAVResources() model.ResourceRequirements {
	return model.ResourceRequirements{CPURequest: "250m", MemoryRequest: "1Gi", CPULimit: "1", MemoryLimit: "3Gi"}
}

// ClamAVFingerprint identifies the provider, not the subsequently updated DB.
// Runtime code must separately record actual database version/date and reject
// missing/stale signatures before claiming dependency readiness.
func ClamAVFingerprint() model.DependencyFingerprint {
	return model.DependencyFingerprint{Kind: "clamav", Image: ClamAVImage, Digest: strings.Split(ClamAVImage, "@")[1], Version: ClamAVVersion, Resources: ClamAVResources(), ConfigurationMode: ClamAVMode}
}

const clamavDaemonConfig = `Foreground yes
User clamav
TCPSocket 3310
TCPAddr 0.0.0.0
LocalSocket /tmp/clamd.sock
DatabaseDirectory /var/lib/clamav
TemporaryDirectory /tmp
MaxThreads 2
MaxQueue 8
MaxFileSize 16M
MaxScanSize 32M
StreamMaxLength 16M
MaxRecursion 16
ReadTimeout 10
CommandReadTimeout 5
SendBufTimeout 500
IdleTimeout 30
SelfCheck 0
ConcurrentDatabaseReload no
`

// No shell, arbitrary mirror, daemon mode, NotifyClamd hook or user-provided
// configuration is accepted. Seeded data permits incremental official updates.
const clamavFreshclamConfig = `DatabaseDirectory /var/lib/clamav
DatabaseOwner clamav
DatabaseMirror database.clamav.net
DNSDatabaseInfo current.cvd.clamav.net
ScriptedUpdates yes
TestDatabases no
ConnectTimeout 10
ReceiveTimeout 30
MaxAttempts 3
`

// ClamAVObjects seeds a bounded volume, performs one official signature update,
// then starts clamd directly as the image's non-root UID/GID 1000. The runtime
// orchestrator must bound startup to at most 10m and revoke provider-only update
// egress before deploying application/preparation pods. These objects do not
// create an egress allowance themselves.
//
// Kubernetes probes establish PING/PONG only. The orchestrator must also observe
// VERSION and validate its full engine/database/date tuple and freshness. In
// particular, clamdscan --version can return a local-version fallback with exit
// zero when the daemon cannot be reached, so exit status is insufficient.
func ClamAVObjects(namespace, runID string) []any {
	labels := backendDependencyLabels(ClamAVName, "clamav", runID)
	one, grace := int32(1), int64(20)
	disabled := false
	configName := ClamAVName + "-config"
	probe := &corev1.Probe{
		ProbeHandler:  corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"clamdscan", "--config-file=" + ClamAVConfigPath, "--ping=1"}}},
		PeriodSeconds: 2, TimeoutSeconds: 3, FailureThreshold: 3,
	}
	startup := probe.DeepCopy()
	startup.FailureThreshold = 300
	configuration := corev1.VolumeMount{Name: "configuration", MountPath: "/etc/cloudforge", ReadOnly: true}
	database := corev1.VolumeMount{Name: "database", MountPath: "/var/lib/clamav"}
	temporary := corev1.VolumeMount{Name: "temporary", MountPath: "/tmp"}
	initialResources := backendDependencyResources(model.ResourceRequirements{CPURequest: "100m", MemoryRequest: "128Mi", CPULimit: "500m", MemoryLimit: "512Mi"})
	return []any{
		backendNamespace(namespace, runID),
		&corev1.ConfigMap{
			TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"}, ObjectMeta: metav1.ObjectMeta{Name: configName, Namespace: namespace, Labels: labels},
			Data: map[string]string{"clamd.conf": clamavDaemonConfig, "freshclam.conf": clamavFreshclamConfig},
		},
		&appsv1.Deployment{
			TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"}, ObjectMeta: metav1.ObjectMeta{Name: ClamAVName, Namespace: namespace, Labels: labels},
			Spec: appsv1.DeploymentSpec{
				Replicas: &one, Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
				Selector: &metav1.LabelSelector{MatchLabels: labels},
				Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{
					AutomountServiceAccountToken: &disabled, TerminationGracePeriodSeconds: &grace, SecurityContext: backendPodSecurity(1000),
					InitContainers: []corev1.Container{
						{Name: "seed-signatures", Image: ClamAVImage, ImagePullPolicy: corev1.PullIfNotPresent,
							Command: []string{"cp"}, Args: []string{"-R", "--no-preserve=ownership,mode", "/var/lib/clamav/.", "/prepared"},
							Resources: initialResources, SecurityContext: backendContainerSecurity(),
							VolumeMounts: []corev1.VolumeMount{{Name: "database", MountPath: "/prepared"}},
						},
						{Name: "update-signatures", Image: ClamAVImage, ImagePullPolicy: corev1.PullIfNotPresent,
							Command: []string{"freshclam"}, Args: []string{"--foreground", "--stdout", "--config-file=/etc/cloudforge/freshclam.conf"},
							Resources: initialResources, SecurityContext: backendContainerSecurity(),
							VolumeMounts: []corev1.VolumeMount{configuration, database, temporary},
						},
					},
					Containers: []corev1.Container{{
						Name: "clamav", Image: ClamAVImage, ImagePullPolicy: corev1.PullIfNotPresent,
						Command: []string{"clamd"}, Args: []string{"--config-file=" + ClamAVConfigPath},
						Ports:     []corev1.ContainerPort{{Name: "clamav", ContainerPort: 3310}},
						Resources: backendDependencyResources(ClamAVResources()), SecurityContext: backendContainerSecurity(),
						StartupProbe: startup, ReadinessProbe: probe,
						VolumeMounts: []corev1.VolumeMount{configuration, {Name: database.Name, MountPath: database.MountPath, ReadOnly: true}, temporary},
					}},
					Volumes: []corev1.Volume{
						backendEphemeralVolume("database", "1Gi"), backendEphemeralVolume("temporary", "64Mi"),
						{Name: "configuration", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: configName}}}},
					},
				}},
			},
		},
		backendService(namespace, ClamAVName, labels, "clamav", 3310),
	}
}
