package dependency

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

func TestRedisIsOwnedInternalPinnedAndBounded(t *testing.T) {
	objects := RedisObjects("cloudforge", "12345678")
	deployment := objects[1].(*appsv1.Deployment)
	service := objects[2].(*corev1.Service)
	container := deployment.Spec.Template.Spec.Containers[0]
	if service.Spec.Type != corev1.ServiceTypeClusterIP || service.Spec.Ports[0].NodePort != 0 || container.Ports[0].HostPort != 0 {
		t.Fatal("dependency exposed outside cluster")
	}
	if !strings.Contains(container.Image, "@sha256:") || strings.Contains(container.Image, "latest") || *deployment.Spec.Replicas != 1 {
		t.Fatal("unpinned/unbounded dependency")
	}
	if deployment.Labels["cloudforge.dev/run-id"] != "12345678" || service.Labels["cloudforge.dev/run-id"] != "12345678" {
		t.Fatal("ownership missing")
	}
	if container.Resources.Limits.Memory().String() != "256Mi" || container.ReadinessProbe.Exec.Command[0] != "redis-cli" || len(deployment.Spec.Template.Spec.Volumes) != 0 || !*container.SecurityContext.ReadOnlyRootFilesystem {
		t.Fatal("unsafe dependency settings")
	}
}
