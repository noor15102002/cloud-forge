package verification

import (
	"errors"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/noor15102002/cloud-forge/pkg/model"
)

func buildWorkerPlan(analysis model.AnalysisResult, id string, config model.RuntimeConfiguration) (plan, error) {
	if err := validateWorker(config); err != nil {
		return plan{}, err
	}
	app := analysis.Application
	if len(app.Kubernetes.Services) != 0 || len(app.Kubernetes.HorizontalPodScalers) != 0 {
		return plan{}, errors.New("worker test does not reproduce source Services or autoscalers; select a worker-only source contract")
	}
	resources := corev1.ResourceRequirements{}
	grace := int64(30)
	var minReady int32
	var progressDeadline *int32
	if len(app.Kubernetes.Deployments) == 1 {
		source := app.Kubernetes.Deployments[0]
		minReady, progressDeadline = source.MinReadySeconds, source.ProgressDeadlineSeconds
		if len(source.Unsupported) != 0 || len(source.Containers) != 1 || len(source.Probes) != 0 || len(source.Endpoints) != 0 || len(source.Containers[0].Ports) != 0 {
			return plan{}, errors.New("worker source deployment has unsupported settings or HTTP/port/probe semantics; no source settings will be silently removed")
		}
		var err error
		resources, err = kubernetesResources(source.Containers[0].Resources)
		if err != nil {
			return plan{}, err
		}
		if source.TerminationGracePeriodSeconds != nil {
			grace = *source.TerminationGracePeriodSeconds
		}
	}
	if grace < 1 || grace > 120 {
		return plan{}, errors.New("worker termination grace must be between 1 and 120 seconds")
	}
	strategy := appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}
	var providerResources []model.ResourceRequirements
	for _, name := range enabledProviders(config) {
		providerResources = append(providerResources, providerFingerprint(name).Resources)
	}
	resources, err := boundedResourcesFor(budgetFor(config), resources, 1, strategy, providerResources...)
	if err != nil {
		return plan{}, err
	}
	if config.Preparation != nil {
		preparation, _ := kubernetesResources(preparationResources())
		if _, err := boundedResourcesFor(budgetFor(config), preparation, 1, strategy, providerResources...); err != nil {
			return plan{}, err
		}
	}
	name := dnsName(app.Name)
	if name == "" {
		name = "application"
	}
	workload := trimDNSName("cf-" + name + "-" + id)
	image := "cloudforge/" + name + ":" + id + "-a"
	replicas, disabled := int32(1), false
	labels := map[string]string{"app.kubernetes.io/name": workload, "app.kubernetes.io/managed-by": "cloudforge", "cloudforge.dev/run-id": id, "cloudforge.dev/role": "application"}
	objects := []any{
		&corev1.Namespace{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"}, ObjectMeta: metav1.ObjectMeta{Name: namespace, Labels: map[string]string{"app.kubernetes.io/managed-by": "cloudforge", "cloudforge.dev/run-id": id}}},
		&appsv1.Deployment{TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"}, ObjectMeta: metav1.ObjectMeta{Name: workload, Namespace: namespace, Labels: labels}, Spec: appsv1.DeploymentSpec{
			Replicas: &replicas, Strategy: strategy, MinReadySeconds: minReady, ProgressDeadlineSeconds: progressDeadline, Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{
				AutomountServiceAccountToken: &disabled, EnableServiceLinks: &disabled, TerminationGracePeriodSeconds: &grace,
				SecurityContext: &corev1.PodSecurityContext{SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
				Containers: []corev1.Container{{Name: "application", Image: image, ImagePullPolicy: corev1.PullNever, Command: append([]string{}, config.Worker.Command...), Env: applicationEnvironment(config), Resources: resources,
					SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &disabled, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}}},
			}},
		}},
	}
	manifest, err := marshalDocuments(objects)
	if err != nil {
		return plan{}, fmt.Errorf("encode worker manifest: %w", err)
	}
	result := plan{clusterName: trimDNSName("cloudforge-" + id), workloadName: workload, image: image, rolloutImage: "cloudforge/" + name + ":" + id + "-b", desiredReplicas: 1, config: config, manifest: manifest, effectiveResources: modelResources(resources)}
	result.topology = testTopology(app, 1, strategy, &grace, minReady, []*corev1.Probe{nil, nil, nil}, config)
	result.topology.ConnectionPolicy = "not_applicable"
	result.topology.ReadinessOrigin = "worker_heartbeat"
	result.hpaSkipReason = "Worker heartbeat mode has no HTTP, Service or autoscaling contract."
	return result, nil
}
