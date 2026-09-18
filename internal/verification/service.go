// Package verification orchestrates CloudForge's first runtime evidence path.
package verification

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/yaml"

	"github.com/noor15102002/cloud-forge/internal/analyzer"
	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/internal/executor/docker"
	"github.com/noor15102002/cloud-forge/internal/executor/k3d"
	"github.com/noor15102002/cloud-forge/internal/executor/kubernetes"
	"github.com/noor15102002/cloud-forge/internal/executor/trivy"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

const (
	namespace       = "cloudforge"
	nodePort        = 30080
	httpTimeout     = 2 * time.Second
	readinessWindow = 2 * time.Minute
	recoveryWindow  = 2 * time.Minute
)

type probeFunc func(context.Context, string) (int, error)

// Options controls one verification run.
type Options struct {
	KeepEnvironment bool
}

// Outcome contains the public report and the CLI exit code it implies.
type Outcome struct {
	Run      model.VerificationRun
	ExitCode int
}

// Service runs verification through injected command execution.
type Service struct {
	runner           command.Runner
	now              func() time.Time
	newID            func() (string, error)
	probe            probeFunc
	poll             time.Duration
	readinessTimeout time.Duration
	recoveryTimeout  time.Duration
}

// New creates a verification service.
func New(runner command.Runner) *Service {
	return &Service{
		runner: runner, now: time.Now, newID: randomID,
		probe: httpProbe(directHTTPClient()),
		poll:  200 * time.Millisecond, readinessTimeout: readinessWindow, recoveryTimeout: recoveryWindow,
	}
}

// Run builds an image, deploys it to an isolated k3d cluster, observes
// readiness, and removes the environment unless the caller explicitly keeps it.
func (s *Service) Run(ctx context.Context, path string, options Options) (out Outcome) {
	started := s.now()
	out.Run = model.VerificationRun{
		SchemaVersion: model.SchemaVersion,
		Status:        model.StatusError,
		StartedAt:     started.UTC().Format(time.RFC3339Nano),
		Environment:   model.VerificationEnvironment{Backend: "k3d", Namespace: namespace},
		Evidence:      []model.Evidence{},
	}
	out.ExitCode = 2
	defer func() { out.Run.DurationMS = elapsedMilliseconds(s.now().Sub(started)) }()
	defer func() {
		sort.Slice(out.Run.Findings, func(i, j int) bool { return out.Run.Findings[i].ID < out.Run.Findings[j].ID })
	}()

	id, err := s.newID()
	if err != nil {
		out.addError("run_id_failed", "CloudForge could not create a verification run identifier.", err.Error())
		return out
	}
	out.Run.RunID = id

	root, err := resolveRoot(path)
	if err != nil {
		out.addError("repository_invalid", "CloudForge could not resolve the application directory.", err.Error())
		return out
	}
	analysis, err := analyzer.New().Analyze(root)
	if err != nil {
		out.addError("analysis_failed", "CloudForge could not analyze the application.", err.Error())
		return out
	}
	if !analysis.Supported {
		out.Run.Status = model.StatusFail
		out.ExitCode = 1
		out.Run.Diagnostics = append(out.Run.Diagnostics, model.Diagnostic{
			Code: "unsupported_application", Status: model.StatusFail,
			Message:  "The application is not supported for verification.",
			Guidance: "Use a Node.js, TypeScript, or Python application with a root Dockerfile.",
		})
		return out
	}

	plan, err := buildPlan(analysis, id)
	if err != nil {
		out.Run.Status = model.StatusFail
		out.ExitCode = 1
		out.Run.Diagnostics = append(out.Run.Diagnostics, model.Diagnostic{
			Code: "verification_ambiguous", Status: model.StatusFail,
			Message: "CloudForge could not derive one safe verification workload.", Guidance: err.Error(),
		})
		return out
	}
	out.Run.Application = analysis.Application.Name
	out.Run.Findings = append(out.Run.Findings, analysis.Findings...)
	out.Run.Environment.ClusterName = plan.clusterName
	if plan.httpSkipReason != "" {
		out.Run.Diagnostics = append(out.Run.Diagnostics, model.Diagnostic{
			Code: "http_experiment_skipped", Status: model.StatusWarn,
			Message: "One or more HTTP probe measurements were limited.", Guidance: plan.httpSkipReason,
		})
	}

	temporary, err := os.MkdirTemp("", "cloudforge-verify-")
	if err != nil {
		out.addError("workspace_failed", "CloudForge could not create its temporary workspace.", err.Error())
		return out
	}
	defer func() { _ = os.RemoveAll(temporary) }()
	manifestPath := filepath.Join(temporary, "workload.yaml")
	if err := os.WriteFile(manifestPath, plan.manifest, 0o600); err != nil {
		out.addError("manifest_write_failed", "CloudForge could not write the generated manifest.", err.Error())
		return out
	}

	dockerClient := docker.New(s.runner)
	k3dClient := k3d.New(s.runner)
	kubernetesClient := kubernetes.New(s.runner)

	buildResult := dockerClient.Build(ctx, root, plan.image)
	buildStatus := model.StatusPass
	buildSummary := "Container image built successfully."
	if failed(buildResult) {
		buildStatus = model.StatusFail
		buildSummary = "Container image build failed."
	}
	out.Run.Evidence = append(out.Run.Evidence, model.Evidence{
		ExperimentID: "container-build", Title: "Container build", Status: buildStatus,
		Summary: buildSummary, DurationMS: buildResult.DurationMS,
	})
	buildFinding := model.Finding{
		ID: "container.build", Category: "container", Status: buildStatus, Severity: model.SeverityHigh,
		Summary: buildSummary, Observed: strings.ToLower(string(buildStatus)), Expected: "image builds successfully",
		DurationMS: buildResult.DurationMS, Source: &model.SourceReference{Path: "Dockerfile"},
	}
	if buildStatus == model.StatusFail {
		buildFinding.Remediation = "Run the Docker build locally, correct the failing instruction, and retry verification."
	}
	out.Run.Findings = append(out.Run.Findings, buildFinding)
	if failed(buildResult) {
		out.Run.Status = model.StatusFail
		out.ExitCode = 1
		out.addCommandDiagnostic("container_build_failed", "Docker could not build the application image.", buildResult)
		return out
	}

	clusterAttempted := false
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if options.KeepEnvironment && clusterAttempted {
			out.Run.Environment.Kept = true
		} else if clusterAttempted {
			result := k3dClient.Delete(cleanupCtx, plan.clusterName)
			if failed(result) {
				out.Run.Status = model.StatusError
				out.ExitCode = 2
				out.addCommandDiagnostic("cluster_cleanup_failed", "CloudForge could not remove its k3d cluster.", result)
			}
		}
		imageResult := dockerClient.RemoveImage(cleanupCtx, plan.image)
		if failed(imageResult) {
			out.Run.Status = model.StatusError
			out.ExitCode = 2
			out.addCommandDiagnostic("image_cleanup_failed", "CloudForge could not remove its temporary Docker image.", imageResult)
		}
	}()

	trivyClient := trivy.New(s.runner)
	scan, scanErr := trivyClient.ScanImage(ctx, plan.image)
	if scanErr != nil {
		out.addError("trivy_output_invalid", "CloudForge could not parse Trivy's JSON report.", scanErr.Error())
		return out
	}
	if failed(scan.Command) {
		out.addCommandDiagnostic("trivy_scan_failed", "Trivy could not scan the application image.", scan.Command)
		return out
	}
	out.Run.Findings = append(out.Run.Findings, scan.Findings...)
	scanStatus := model.StatusPass
	if countFindingStatus(scan.Findings, model.StatusWarn) > 0 {
		scanStatus = model.StatusWarn
	}
	out.Run.Evidence = append(out.Run.Evidence, model.Evidence{
		ExperimentID: "container-scan", Title: "Container vulnerability scan", Status: scanStatus,
		Summary: scanSummary(scan.Findings), DurationMS: scan.Command.DurationMS,
		Measurements: []model.Measurement{{Name: "vulnerabilities", Value: strconv.Itoa(countVulnerabilities(scan.Findings)), Unit: "findings"}},
	})

	clusterAttempted = true
	if result := k3dClient.Create(ctx, plan.clusterName, nodePort); failed(result) {
		out.addCommandDiagnostic("cluster_create_failed", "k3d could not create the verification cluster.", result)
		return out
	}
	if plan.readinessPath != "" {
		hostPort, portResult, portErr := dockerClient.PublishedPort(ctx, plan.clusterName, nodePort)
		if portErr != nil || failed(portResult) {
			out.addError("published_port_failed", "CloudForge could not resolve the loopback verification port.", commandGuidance(portResult, portErr))
			return out
		}
		plan.readinessURL = localEndpointURL(plan.readinessScheme, plan.readinessPath, hostPort)
		plan.healthURL = localEndpointURL(plan.healthScheme, plan.healthPath, hostPort)
		out.Run.Environment.Endpoint = plan.readinessURL
	}
	if result := k3dClient.ImportImage(ctx, plan.clusterName, plan.image); failed(result) {
		out.addCommandDiagnostic("image_import_failed", "k3d could not import the application image.", result)
		return out
	}
	if result := kubernetesClient.Apply(ctx, plan.clusterName, manifestPath); failed(result) {
		out.addCommandDiagnostic("deployment_apply_failed", "kubectl could not apply the generated workload.", result)
		return out
	}

	var httpResultChannel chan httpObservation
	var stopHTTP context.CancelFunc
	if plan.readinessURL != "" {
		httpContext, cancel := context.WithTimeout(ctx, s.readinessTimeout)
		stopHTTP = cancel
		httpResultChannel = make(chan httpObservation, 1)
		go func() { httpResultChannel <- s.waitForHTTP(httpContext, plan.readinessURL) }()
	}
	waitResult := kubernetesClient.WaitAvailable(ctx, plan.clusterName, namespace, plan.workloadName)
	if failed(waitResult) && stopHTTP != nil {
		stopHTTP()
	}
	httpResult := httpObservation{}
	if httpResultChannel != nil {
		httpResult = <-httpResultChannel
		stopHTTP()
	}
	ready, total, restarts, podResult, podErr := kubernetesClient.ReadyPods(ctx, plan.clusterName, namespace, "app.kubernetes.io/name="+plan.workloadName)
	readinessStatus := model.StatusPass
	readinessSummary := "Deployment became available."
	if failed(waitResult) {
		readinessStatus = model.StatusFail
		readinessSummary = "Deployment did not become available before the deadline."
	}
	if plan.readinessURL != "" && !httpResult.Success {
		readinessStatus = model.StatusFail
		readinessSummary = "The readiness endpoint did not return a successful HTTP status."
	}
	if podErr != nil || failed(podResult) {
		out.addError("pod_observation_failed", "CloudForge could not decode the deployed pod state.", commandGuidance(podResult, podErr))
		return out
	}
	readinessMeasurements := []model.Measurement{
		{Name: "ready_pods", Value: strconv.Itoa(ready), Unit: "pods"},
		{Name: "total_pods", Value: strconv.Itoa(total), Unit: "pods"},
		{Name: "container_restarts", Value: strconv.FormatInt(int64(restarts), 10), Unit: "restarts"},
		{Name: "readiness_duration_ms", Value: strconv.FormatInt(waitResult.DurationMS, 10), Unit: "ms"},
	}
	if plan.readinessURL != "" {
		readinessMeasurements = append(readinessMeasurements,
			model.Measurement{Name: "startup_duration_ms", Value: strconv.FormatInt(httpResult.DurationMS, 10), Unit: "ms"},
			model.Measurement{Name: "readiness_http_status", Value: strconv.Itoa(httpResult.Status)},
			model.Measurement{Name: "readiness_attempts", Value: strconv.Itoa(httpResult.Attempts), Unit: "requests"},
			model.Measurement{Name: "gated_attempts", Value: strconv.Itoa(httpResult.Failures), Unit: "requests"},
		)
	}
	out.Run.Evidence = append(out.Run.Evidence, model.Evidence{
		ExperimentID: "deployment-readiness", Title: "Deployment readiness", Status: readinessStatus,
		Summary: readinessSummary, DurationMS: maxInt64(waitResult.DurationMS, httpResult.DurationMS),
		Measurements: readinessMeasurements,
	})
	if readinessStatus == model.StatusPass && (ready != int(plan.desiredReplicas) || total != int(plan.desiredReplicas)) {
		readinessStatus = model.StatusFail
		readinessSummary = "Deployment did not reach the requested ready replica count."
		out.Run.Evidence[len(out.Run.Evidence)-1].Status = readinessStatus
		out.Run.Evidence[len(out.Run.Evidence)-1].Summary = readinessSummary
	}
	if readinessStatus == model.StatusFail {
		out.Run.Findings = append(out.Run.Findings, model.Finding{
			ID: "container.startup", Category: "container", Status: model.StatusFail, Severity: model.SeverityHigh,
			Summary: "The application did not start with all requested replicas ready.", Observed: readinessSummary,
			Expected: "all requested replicas become ready", Remediation: "Inspect the container entry point, application logs, port, and readiness probe.",
			DurationMS: waitResult.DurationMS, Source: &model.SourceReference{Path: "Dockerfile"},
		})
		out.Run.Status = model.StatusFail
		out.ExitCode = 1
		if failed(waitResult) {
			out.addCommandDiagnostic("readiness_failed", readinessSummary, waitResult)
		} else if plan.readinessURL != "" && !httpResult.Success {
			out.Run.Diagnostics = append(out.Run.Diagnostics, model.Diagnostic{
				Code: "readiness_http_failed", Status: model.StatusFail, Message: readinessSummary,
				Guidance: fmt.Sprintf("Observed HTTP status %d after %d attempts; verify the readiness path and Service port.", httpResult.Status, httpResult.Attempts),
			})
		} else {
			out.Run.Diagnostics = append(out.Run.Diagnostics, model.Diagnostic{
				Code: "readiness_failed", Status: model.StatusFail, Message: readinessSummary,
				Guidance: fmt.Sprintf("Requested %d replicas; observed %d ready of %d total pods.", plan.desiredReplicas, ready, total),
			})
		}
		return out
	}

	out.Run.Findings = append(out.Run.Findings, model.Finding{
		ID: "container.startup", Category: "container", Status: model.StatusPass, Severity: model.SeverityInfo,
		Summary: "The application started with all requested replicas ready.", Observed: fmt.Sprintf("%d/%d pods ready", ready, total),
		Expected: "all requested replicas become ready", DurationMS: waitResult.DurationMS, Source: &model.SourceReference{Path: "Dockerfile"},
	})
	if plan.readinessURL == "" {
		out.Run.Evidence = append(out.Run.Evidence, model.Evidence{
			ExperimentID: "pod-recovery", Title: "Pod recovery under traffic", Status: model.StatusSkipped,
			Summary: "Pod recovery traffic requires an explicit HTTP readiness endpoint.",
		})
	} else {
		recovery := s.runPodRecovery(ctx, kubernetesClient, plan)
		if recovery.Evidence.ExperimentID != "" {
			out.Run.Evidence = append(out.Run.Evidence, recovery.Evidence)
		}
		if recovery.Finding != nil {
			out.Run.Findings = append(out.Run.Findings, *recovery.Finding)
		}
		if recovery.Diagnostic != nil {
			out.Run.Diagnostics = append(out.Run.Diagnostics, *recovery.Diagnostic)
		}
		if recovery.ExitCode != 0 {
			out.ExitCode = recovery.ExitCode
			if recovery.ExitCode == 1 {
				out.Run.Status = model.StatusFail
			} else {
				out.Run.Status = model.StatusError
			}
			return out
		}
	}
	out.Run.Status, out.ExitCode = outcomeForFindings(out.Run.Findings)
	return out
}

type plan struct {
	clusterName     string
	workloadName    string
	image           string
	desiredReplicas int32
	readinessScheme string
	readinessPath   string
	healthScheme    string
	healthPath      string
	readinessURL    string
	healthURL       string
	httpSkipReason  string
	manifest        []byte
}

func buildPlan(analysis model.AnalysisResult, id string) (plan, error) {
	application := analysis.Application
	if len(application.Containers) != 1 || application.Containers[0].Source.Path != "Dockerfile" {
		return plan{}, errors.New("verification requires exactly one root Dockerfile")
	}
	if len(application.Kubernetes.Deployments) > 1 {
		return plan{}, errors.New("verification requires zero or one Kubernetes Deployment; select a narrower application directory")
	}
	port, portName, err := selectPort(application)
	if err != nil {
		return plan{}, err
	}
	name := dnsName(application.Name)
	if name == "" {
		name = "application"
	}
	workloadName := trimDNSName("cf-" + name + "-" + id)
	clusterName := trimDNSName("cloudforge-" + id)
	image := "cloudforge/" + name + ":" + id

	replicas := int32(1)
	resources := corev1.ResourceRequirements{}
	var readinessProbe, livenessProbe *corev1.Probe
	readinessScheme, readinessPath := "", ""
	healthScheme, healthPath := "", ""
	httpSkipReason := ""
	if len(application.Kubernetes.Deployments) == 1 {
		deployment := application.Kubernetes.Deployments[0]
		if deployment.Replicas != nil && *deployment.Replicas > 0 {
			replicas = *deployment.Replicas
		}
		if len(deployment.Containers) > 1 {
			return plan{}, errors.New("verification currently requires a Deployment with one container")
		}
		if len(deployment.Containers) == 1 {
			resources, err = kubernetesResources(deployment.Containers[0].Resources)
			if err != nil {
				return plan{}, err
			}
		}
		readinessProbe = probeFor(deployment.Endpoints, "readiness", port, portName)
		livenessProbe = probeFor(deployment.Endpoints, "liveness", port, portName)
		readinessScheme, readinessPath, err = endpointRoute(deployment.Endpoints, "readiness", port, portName)
		if err != nil {
			return plan{}, err
		}
		healthScheme, healthPath, err = endpointRoute(deployment.Endpoints, "liveness", port, portName)
		if err != nil {
			return plan{}, err
		}
		if healthPath == "" {
			healthScheme, healthPath, err = endpointRoute(deployment.Endpoints, "startup", port, portName)
			if err != nil {
				return plan{}, err
			}
		}
	}
	if readinessProbe == nil {
		readinessProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(port)}}}
	}
	if strings.EqualFold(readinessScheme, "https") {
		httpSkipReason = "HTTPS application probes are not measured in this release because CloudForge does not bypass certificate validation."
		readinessScheme, readinessPath = "", ""
	}
	if strings.EqualFold(healthScheme, "https") {
		healthScheme, healthPath = readinessScheme, readinessPath
		if httpSkipReason == "" {
			httpSkipReason = "The HTTPS health probe was not used; final health falls back to the HTTP readiness endpoint."
		}
	}
	if healthPath == "" {
		healthScheme, healthPath = readinessScheme, readinessPath
	}

	labels := map[string]string{"app.kubernetes.io/name": workloadName, "app.kubernetes.io/managed-by": "cloudforge"}
	objects := []any{
		&corev1.Namespace{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"}, ObjectMeta: metav1.ObjectMeta{Name: namespace, Labels: map[string]string{"app.kubernetes.io/managed-by": "cloudforge"}}},
		&appsv1.Deployment{
			TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
			ObjectMeta: metav1.ObjectMeta{Name: workloadName, Namespace: namespace, Labels: labels},
			Spec: appsv1.DeploymentSpec{
				Replicas: &replicas, Selector: &metav1.LabelSelector{MatchLabels: labels},
				Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{Containers: []corev1.Container{{
					Name: "application", Image: image, ImagePullPolicy: corev1.PullNever,
					Ports:     []corev1.ContainerPort{{Name: portName, ContainerPort: port, Protocol: corev1.ProtocolTCP}},
					Resources: resources, ReadinessProbe: readinessProbe, LivenessProbe: livenessProbe,
				}}}},
			},
		},
		&corev1.Service{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
			ObjectMeta: metav1.ObjectMeta{Name: workloadName, Namespace: namespace, Labels: labels},
			Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeNodePort, Selector: labels, Ports: []corev1.ServicePort{{Name: portName, Port: port, TargetPort: intstr.FromString(portName), NodePort: nodePort, Protocol: corev1.ProtocolTCP}}},
		},
	}
	manifest, err := marshalDocuments(objects)
	if err != nil {
		return plan{}, fmt.Errorf("encode generated Kubernetes resources: %w", err)
	}
	return plan{
		clusterName: clusterName, workloadName: workloadName, image: image, desiredReplicas: replicas,
		readinessScheme: readinessScheme, readinessPath: readinessPath, healthScheme: healthScheme, healthPath: healthPath,
		httpSkipReason: httpSkipReason, manifest: manifest,
	}, nil
}

func selectPort(application model.Application) (int32, string, error) {
	ports := map[int32]string{}
	for _, container := range application.Containers {
		for _, item := range container.Ports {
			if item.Protocol == "" || strings.EqualFold(item.Protocol, "TCP") {
				ports[item.Port] = "http"
			}
		}
	}
	if len(application.Kubernetes.Deployments) == 1 {
		deployment := application.Kubernetes.Deployments[0]
		for _, endpoint := range deployment.Endpoints {
			if endpoint.Purpose != "readiness" {
				continue
			}
			if parsed, err := strconv.ParseInt(endpoint.Port, 10, 32); err == nil && parsed > 0 && parsed <= 65535 {
				return int32(parsed), "http", nil
			}
			for _, container := range deployment.Containers {
				for _, item := range container.Ports {
					if item.Name == endpoint.Port {
						return item.Port, safePortName(item.Name), nil
					}
				}
			}
		}
	}
	if len(ports) != 1 {
		return 0, "", errors.New("verification requires one unambiguous TCP port from Docker EXPOSE or the Deployment readiness probe")
	}
	for value, name := range ports {
		return value, name, nil
	}
	panic("unreachable")
}

func probeFor(endpoints []model.Endpoint, purpose string, port int32, portName string) *corev1.Probe {
	for _, endpoint := range endpoints {
		if endpoint.Purpose != purpose || endpoint.Path == "" {
			continue
		}
		probePort := intstr.FromInt32(port)
		if endpoint.Port != "" && endpoint.Port == portName {
			probePort = intstr.FromString(portName)
		}
		scheme := corev1.URISchemeHTTP
		if strings.EqualFold(endpoint.Protocol, "https") {
			scheme = corev1.URISchemeHTTPS
		}
		return &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: endpoint.Path, Port: probePort, Scheme: scheme}}}
	}
	return nil
}

func endpointRoute(endpoints []model.Endpoint, purpose string, selectedPort int32, portName string) (string, string, error) {
	for _, endpoint := range endpoints {
		if endpoint.Purpose != purpose || endpoint.Path == "" {
			continue
		}
		if endpoint.Port != "" {
			if numeric, err := strconv.ParseInt(endpoint.Port, 10, 32); err == nil {
				if int32(numeric) != selectedPort {
					return "", "", fmt.Errorf("%s probe uses port %s, but verification selected port %d", purpose, endpoint.Port, selectedPort)
				}
			} else if endpoint.Port != portName {
				return "", "", fmt.Errorf("%s probe uses named port %q, but verification selected %q", purpose, endpoint.Port, portName)
			}
		}
		scheme := "http"
		if strings.EqualFold(endpoint.Protocol, "https") {
			scheme = "https"
		}
		path := endpoint.Path
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		return scheme, path, nil
	}
	return "", "", nil
}

func kubernetesResources(value model.ResourceRequirements) (corev1.ResourceRequirements, error) {
	result := corev1.ResourceRequirements{}
	for _, item := range []struct {
		name corev1.ResourceName
		text string
		into *corev1.ResourceList
	}{
		{corev1.ResourceCPU, value.CPURequest, &result.Requests},
		{corev1.ResourceMemory, value.MemoryRequest, &result.Requests},
		{corev1.ResourceCPU, value.CPULimit, &result.Limits},
		{corev1.ResourceMemory, value.MemoryLimit, &result.Limits},
	} {
		if item.text == "" {
			continue
		}
		quantity, err := resource.ParseQuantity(item.text)
		if err != nil {
			return corev1.ResourceRequirements{}, fmt.Errorf("invalid Kubernetes resource quantity %q", item.text)
		}
		if *item.into == nil {
			*item.into = corev1.ResourceList{}
		}
		(*item.into)[item.name] = quantity
	}
	return result, nil
}

func marshalDocuments(objects []any) ([]byte, error) {
	var result []byte
	for index, object := range objects {
		encoded, err := yaml.Marshal(object)
		if err != nil {
			return nil, err
		}
		if index > 0 {
			result = append(result, []byte("---\n")...)
		}
		result = append(result, encoded...)
	}
	return result, nil
}

func resolveRoot(path string) (string, error) {
	root, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(root)
}

func randomID() (string, error) {
	value := make([]byte, 4)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func localEndpointURL(scheme, path string, hostPort int) string {
	if scheme == "" || path == "" || hostPort == 0 {
		return ""
	}
	return fmt.Sprintf("%s://127.0.0.1:%d%s", scheme, hostPort, path)
}

func directHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &http.Client{
		Transport: transport, Timeout: httpTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func httpProbe(client *http.Client) probeFunc {
	return func(ctx context.Context, url string) (int, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return 0, err
		}
		response, err := client.Do(request)
		if err != nil {
			return 0, err
		}
		defer func() { _ = response.Body.Close() }()
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1024))
		return response.StatusCode, nil
	}
}

func dnsName(value string) string {
	var builder strings.Builder
	lastHyphen := false
	for _, r := range strings.ToLower(value) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			builder.WriteRune(r)
			lastHyphen = false
		} else if !lastHyphen && builder.Len() > 0 {
			builder.WriteByte('-')
			lastHyphen = true
		}
	}
	return strings.Trim(builder.String(), "-")
}

func trimDNSName(value string) string {
	if len(value) <= 63 {
		return strings.Trim(value, "-")
	}
	return strings.Trim(value[:63], "-")
}

func safePortName(value string) string {
	name := dnsName(value)
	if name == "" || len(name) > 15 {
		return "http"
	}
	return name
}

func failed(result model.CommandResult) bool {
	return result.FailureType != model.FailureNone || result.ExitCode != 0
}

func elapsedMilliseconds(value time.Duration) int64 {
	if value <= 0 {
		return 0
	}
	return value.Milliseconds()
}

func (out *Outcome) addError(code, message, guidance string) {
	out.Run.Status = model.StatusError
	out.ExitCode = 2
	out.Run.Diagnostics = append(out.Run.Diagnostics, model.Diagnostic{Code: code, Status: model.StatusError, Message: message, Guidance: guidance})
}

func (out *Outcome) addCommandDiagnostic(code, message string, result model.CommandResult) {
	status := model.StatusError
	if out.ExitCode == 1 || out.Run.Status == model.StatusFail {
		status = model.StatusFail
	}
	out.Run.Diagnostics = append(out.Run.Diagnostics, model.Diagnostic{
		Code: code, Status: status, Message: message, Guidance: commandGuidance(result, nil),
	})
}

func commandGuidance(result model.CommandResult, err error) string {
	if err != nil {
		return err.Error()
	}
	output := strings.ToLower(result.Stdout + " " + result.Stderr)
	if result.Command == "docker" && strings.Contains(output, "permission denied") {
		return "Docker daemon access was denied; grant the current user daemon access and run cloudforge doctor again."
	}
	if result.Command == "docker" && (strings.Contains(output, "cannot connect") || strings.Contains(output, "connection refused")) {
		return "The Docker daemon is not reachable; start Docker and run cloudforge doctor again."
	}
	switch result.FailureType {
	case model.FailureNotFound:
		return fmt.Sprintf("Install %s and ensure it is available on PATH.", result.Command)
	case model.FailureTimeout:
		return fmt.Sprintf("%s exceeded its %d ms execution deadline.", result.Command, result.DurationMS)
	case model.FailureCanceled:
		return "The verification was canceled."
	case model.FailureExit:
		return fmt.Sprintf("%s exited with code %d; run cloudforge doctor and inspect the tool's local logs.", result.Command, result.ExitCode)
	default:
		return fmt.Sprintf("%s could not be executed; run cloudforge doctor.", result.Command)
	}
}

func countFindingStatus(values []model.Finding, status model.Status) int {
	count := 0
	for _, item := range values {
		if item.Status == status {
			count++
		}
	}
	return count
}

func countVulnerabilities(values []model.Finding) int {
	count := 0
	for _, item := range values {
		if strings.HasPrefix(item.ID, "security.trivy.") && item.ID != "security.trivy.vulnerabilities" {
			count++
		}
	}
	return count
}

func scanSummary(values []model.Finding) string {
	count := countVulnerabilities(values)
	if count == 0 {
		return "Trivy detected no known vulnerabilities."
	}
	return fmt.Sprintf("Trivy detected %d known vulnerabilities.", count)
}

func outcomeForFindings(values []model.Finding) (model.Status, int) {
	for _, item := range values {
		if item.Status == model.StatusFail {
			return model.StatusFail, 1
		}
	}
	for _, item := range values {
		if item.Status == model.StatusWarn {
			return model.StatusWarn, 0
		}
	}
	return model.StatusPass, 0
}
