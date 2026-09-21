package dependency

import (
	"reflect"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

func TestClamAVUpdatesSeededSignaturesBeforeReadOnlyDaemon(t *testing.T) {
	objects := ClamAVObjects("isolated", "run-a")
	deployment := findBackendDeployment(t, objects)
	pod := deployment.Spec.Template.Spec
	if len(pod.InitContainers) != 2 || !reflect.DeepEqual(pod.InitContainers[0].Command, []string{"cp"}) || !reflect.DeepEqual(pod.InitContainers[1].Command, []string{"freshclam"}) {
		t.Fatal("signatures must be seeded then updated with direct bounded provider commands")
	}
	seed, update := pod.InitContainers[0], pod.InitContainers[1]
	if !reflect.DeepEqual(seed.Args, []string{"-R", "--no-preserve=ownership,mode", "/var/lib/clamav/.", "/prepared"}) || len(seed.VolumeMounts) != 1 || seed.VolumeMounts[0].MountPath != "/prepared" {
		t.Fatal("seed source must remain the pinned image, copied to isolated storage")
	}
	for _, argument := range update.Args {
		if argument == "--daemon" || strings.Contains(argument, "--on-") {
			t.Fatal("signature updater must not persist or execute hooks")
		}
	}
	for _, mount := range update.VolumeMounts {
		if mount.Name == "database" && mount.ReadOnly {
			t.Fatal("initial signature update cannot write the database")
		}
	}
	if len(pod.Containers) != 1 || !reflect.DeepEqual(pod.Containers[0].Command, []string{"clamd"}) || len(pod.Containers[0].Env) != 0 {
		t.Fatal("runtime must contain only the direct scanner, without updater or entrypoint daemon management")
	}
	readOnly := false
	for _, mount := range pod.Containers[0].VolumeMounts {
		if mount.Name == "database" {
			readOnly = mount.ReadOnly
		}
	}
	if !readOnly {
		t.Fatal("signatures must remain fixed during measured runtime")
	}
	var config *corev1.ConfigMap
	for _, object := range objects {
		if value, ok := object.(*corev1.ConfigMap); ok {
			config = value
		}
	}
	if config == nil || len(config.Data) != 2 || !strings.Contains(config.Data["freshclam.conf"], "DatabaseMirror database.clamav.net\n") || !strings.Contains(config.Data["freshclam.conf"], "MaxAttempts 3\n") || strings.Contains(config.Data["freshclam.conf"], "NotifyClamd") {
		t.Fatal("updater config must have one official mirror, bounded attempts and no executable hooks")
	}
	for _, directive := range []string{"Foreground yes\n", "MaxThreads 2\n", "SelfCheck 0\n", "ConcurrentDatabaseReload no\n"} {
		if !strings.Contains(config.Data["clamd.conf"], directive) {
			t.Fatalf("daemon is missing bounded configuration %q", directive)
		}
	}
}

func TestClamAVProbeIsPingAndDoesNotClaimSignatureFreshness(t *testing.T) {
	deployment := findBackendDeployment(t, ClamAVObjects("isolated", "run-a"))
	container := deployment.Spec.Template.Spec.Containers[0]
	expected := []string{"clamdscan", "--config-file=" + ClamAVConfigPath, "--ping=1"}
	if !reflect.DeepEqual(container.ReadinessProbe.Exec.Command, expected) || container.ReadinessProbe.TCPSocket != nil {
		t.Fatal("scanner readiness must establish actual PING/PONG, not merely a listening TCP port")
	}
	if container.StartupProbe.FailureThreshold*container.StartupProbe.PeriodSeconds > 600 || container.ReadinessProbe.TimeoutSeconds > 5 {
		t.Fatal("scanner probes exceed their time bounds")
	}
	fingerprint := ClamAVFingerprint()
	if strings.Contains(fingerprint.ConfigurationMode, "freshness-verified") || fingerprint.Image != ClamAVImage || fingerprint.Version != ClamAVVersion || !strings.HasSuffix(ClamAVImage, fingerprint.Digest) {
		t.Fatal("static fingerprint must identify the provider without claiming observed signature freshness")
	}
}

func TestClamAVIsEphemeralOwnedAndBounded(t *testing.T) {
	objects := ClamAVObjects("isolated", "run-a")
	deployment := findBackendDeployment(t, objects)
	assertBackendIsolation(t, objects, deployment, 1000)
	assertBackendResources(t, deployment.Spec.Template.Spec.Containers[0], ClamAVResources())
	if *deployment.Spec.Replicas != 1 || deployment.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType {
		t.Fatal("scanner must not duplicate its signature memory during replacement")
	}
	pod := deployment.Spec.Template.Spec
	if len(pod.Volumes) != 3 || pod.Volumes[0].EmptyDir.SizeLimit.String() != "1Gi" || pod.Volumes[1].EmptyDir.SizeLimit.String() != "64Mi" {
		t.Fatal("scanner data/temporary disk storage must be bounded and ephemeral")
	}
	for _, container := range pod.InitContainers {
		if container.Resources.Limits.Memory().Cmp(*pod.Containers[0].Resources.Limits.Memory()) > 0 || container.Resources.Limits.Cpu().Cmp(*pod.Containers[0].Resources.Limits.Cpu()) > 0 {
			t.Fatal("signature preparation peak exceeds reported provider resources")
		}
	}
}
