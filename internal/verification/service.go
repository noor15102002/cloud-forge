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
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/yaml"

	"github.com/noor15102002/cloud-forge/internal/analyzer"
	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/internal/executor/docker"
	"github.com/noor15102002/cloud-forge/internal/executor/k3d"
	k6executor "github.com/noor15102002/cloud-forge/internal/executor/k6"
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
	PlanOnly        bool
	OnPlan          func(model.VerificationPlan)
	KeepEnvironment bool
	ConfigPath      string
	Version         string
	Commit          string
}

// Outcome contains the public report and the CLI exit code it implies.
type Outcome struct {
	Run      model.VerificationRun
	ExitCode int
}

// Service runs verification through injected command execution.
type Service struct {
	runner                command.Runner
	backendCapacity       func(context.Context, command.Runner, *Outcome) bool
	controlledExperiments bool
	now                   func() time.Time
	newID                 func() (string, error)
	probe                 probeFunc
	probePacer            *probePacer
	workerPoll            time.Duration
	poll                  time.Duration
	trafficPoll           time.Duration
	readinessTimeout      time.Duration
	recoveryTimeout       time.Duration
	baselineTimeout       time.Duration
	rolloutTimeout        time.Duration
	hpaMetricsTimeout     time.Duration
	hpaScaleTimeout       time.Duration
	cleanupTimeout        time.Duration
	removeWorkspace       func(string) error
	loadProfile           k6executor.Profile
}

// New creates a verification service.
func New(runner command.Runner) *Service {
	return &Service{
		runner: runner, now: time.Now, newID: randomID, controlledExperiments: true, backendCapacity: checkBackendCapacity,
		probe:      httpProbe(directHTTPClient()),
		workerPoll: time.Second,
		poll:       200 * time.Millisecond, trafficPoll: 20 * time.Millisecond, readinessTimeout: readinessWindow,
		recoveryTimeout: recoveryWindow, rolloutTimeout: recoveryWindow, baselineTimeout: recoveryWindow,
		hpaMetricsTimeout: time.Minute, hpaScaleTimeout: time.Minute,
		cleanupTimeout: 2 * time.Minute,
		loadProfile:    k6executor.Profile{VirtualUsers: 16, Duration: 20 * time.Second},
	}
}

// Run builds an image, deploys it to an isolated k3d cluster, observes
// readiness, and removes the environment unless the caller explicitly keeps it.
func (s *Service) Run(ctx context.Context, path string, options Options) (out Outcome) {
	started := s.now()
	out.Run = model.VerificationRun{
		SchemaVersion: model.VerificationSchemaVersion,
		Producer:      &model.BuildIdentity{Version: options.Version, Commit: options.Commit},
		Status:        model.StatusError,
		StartedAt:     started.UTC().Format(time.RFC3339Nano),
		Environment:   model.VerificationEnvironment{Backend: "k3d", Namespace: namespace},
		Evidence:      []model.Evidence{},
	}
	out.ExitCode = 2
	defer func() { appendUnexecuted(&out) }()
	defer func() {
		if ctx.Err() != nil {
			out.addError("verification_canceled", "Verification was canceled; no further experiments were scheduled.", "Evidence collected before cancellation is preserved; owned resources are cleaned up with bounded independent contexts.")
		}
	}()
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
	config, err := loadConfiguration(root, options.ConfigPath)
	if err != nil {
		out.addError("configuration_invalid", "CloudForge rejected the verification configuration before execution.", err.Error())
		return out
	}
	analysis, err := analyzer.New().AnalyzeSelected(root, config.Build)
	if err != nil {
		out.addError("analysis_failed", "CloudForge could not analyze the application.", err.Error())
		return out
	}
	config.Build = analysis.Build
	selectedApp := ""
	if config.Build != nil {
		selectedApp = config.Build.App
	}
	out.Run.Application = applicationIdentity(analysis.Application.Name, selectedApp, root)
	out.Run.Findings = append(out.Run.Findings, verificationFindings(analysis.Findings, config)...)
	out.Run.Diagnostics = append(out.Run.Diagnostics, analysis.Diagnostics...)
	if !analysis.Supported {
		out.Run.Status = model.StatusFail
		out.ExitCode = 1
		return out
	}
	if diagnostic := blockingAnalysisDiagnostic(analysis.Diagnostics); diagnostic != nil {
		out.Run.Status = model.StatusFail
		out.ExitCode = 1
		out.Run.Diagnostics = append(out.Run.Diagnostics, model.Diagnostic{
			Code: "verification_analysis_incomplete", Status: model.StatusFail,
			Message:  "CloudForge stopped before execution because repository metadata could not be analyzed safely.",
			Guidance: fmt.Sprintf("Resolve %s in %s and retry verification.", diagnostic.Code, diagnosticPath(diagnostic.Source)),
		})
		return out
	}

	if !s.controlledExperiments {
		config.Experiments.ControlPath = ""
	}
	plan, err := buildConfiguredPlan(analysis, id, config)
	out.Run.Plan = capabilityPlan(analysis, plan, config, err)
	if options.OnPlan != nil {
		options.OnPlan(*out.Run.Plan)
	}
	if out.Run.Plan.Status == model.StatusBlocked {
		out.Run.Status = model.StatusBlocked
		out.ExitCode = 1
		out.Run.Diagnostics = append(out.Run.Diagnostics, model.Diagnostic{Code: "verification_blocked", Status: model.StatusBlocked, Message: "Required runtime capabilities could not be planned safely.", Guidance: "Resolve blocked capabilities in the plan before retrying."})
		return out
	}
	if options.PlanOnly {
		out.Run.Status = model.StatusSkipped
		out.ExitCode = 0
		return out
	}
	if !validIdentity(*out.Run.Producer) {
		out.addError("build_identity_missing", "Runtime verification requires CloudForge version and commit identity.", "Use a VCS-stamped build or inject the version and full commit with Go linker flags.")
		return out
	}
	out.Run.Fingerprint = newFingerprint(ctx, s.runner, root, plan, options)
	if !checkRuntimeTools(ctx, scopedRunner{runner: s.runner, kubeconfig: os.DevNull, backendDocker: backendProfile(config)}, &out) {
		return out
	}
	if backendProfile(config) && !s.backendCapacity(ctx, s.runner, &out) {
		return out
	}
	out.Run.Environment.ClusterName = plan.clusterName
	s.loadProfile.VirtualUsers = config.Load.VUs
	s.loadProfile.Duration, _ = time.ParseDuration(config.Load.Duration)
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
	var cleanupStarted time.Time
	defer func() {
		status, summary := model.StatusPass, "Owned temporary resources and the private workspace were verified absent."
		if out.Run.Environment.Kept {
			status, summary = model.StatusSkipped, "The disposable cluster was retained by explicit request; other owned temporary resources and the private workspace were cleaned."
		}
		for _, diagnostic := range out.Run.Diagnostics {
			if diagnostic.Status == model.StatusError && strings.Contains(diagnostic.Code, "cleanup") {
				status, summary = model.StatusError, "Removal of all owned temporary resources could not be established; inspect the cleanup diagnostics."
				break
			}
		}
		out.Run.Evidence = append(out.Run.Evidence, model.Evidence{ExperimentID: "environment-cleanup", Title: "Environment cleanup", Status: status, Summary: summary, DurationMS: elapsedMilliseconds(time.Since(cleanupStarted)), Execution: &model.ExperimentExecution{Executed: true}})
	}()
	defer func() {
		if cleanupStarted.IsZero() {
			cleanupStarted = time.Now()
		}
		remove := s.removeWorkspace
		if remove == nil {
			remove = os.RemoveAll
		}
		if err := remove(temporary); err != nil {
			out.addError("workspace_cleanup_failed", "CloudForge could not remove its private temporary workspace.", "Remove the private workspace after inspecting local filesystem permissions; generated credentials may remain at "+temporary+".")
		} else if _, err := os.Lstat(temporary); !os.IsNotExist(err) {
			out.addError("workspace_cleanup_failed", "CloudForge could not establish removal of its private temporary workspace.", "Inspect and remove the private workspace; generated credentials may remain at "+temporary+".")
		}
	}()
	manifestPath := filepath.Join(temporary, "workload.yaml")
	if err := os.WriteFile(manifestPath, plan.manifest, 0o600); err != nil {
		out.addError("manifest_write_failed", "CloudForge could not write the generated manifest.", err.Error())
		return out
	}
	hpaManifestPath := ""
	if len(plan.hpaManifest) > 0 {
		hpaManifestPath = filepath.Join(temporary, "autoscaler.yaml")
		if err := os.WriteFile(hpaManifestPath, plan.hpaManifest, 0o600); err != nil {
			out.addError("manifest_write_failed", "CloudForge could not write the generated autoscaler manifest.", err.Error())
			return out
		}
	}

	kubeconfigPath := filepath.Join(temporary, "kubeconfig")
	dockerConfig := filepath.Join(temporary, "docker")
	if err := os.Mkdir(dockerConfig, 0o700); err != nil {
		out.addError("workspace_failed", "CloudForge could not create its private Docker configuration.", err.Error())
		return out
	}
	scoped := scopedRunner{runner: s.runner, kubeconfig: kubeconfigPath, dockerConfig: dockerConfig, backendDocker: backendProfile(config)}
	dockerClient := docker.New(scoped)
	dockerClient.IsolateBuild(plan.clusterName)
	builderAttempted := false
	k3dClient := k3d.New(scoped)
	k3dClient.SetOwnership(dockerClient.OwnershipToken())
	kubernetesClient := kubernetes.New(scoped)
	clusterAttempted := false
	clusterCreated := false
	imagesToCleanup := []string{plan.image}
	defer func() {
		if options.KeepEnvironment && clusterCreated {
			out.Run.Environment.Kept = true
		} else if clusterCreated {
			result := s.cleanupCommand(func(cleanupCtx context.Context) model.CommandResult {
				if proof := dockerClient.VerifyClusterOwnership(cleanupCtx, plan.clusterName); cleanupFailed(proof) {
					return proof
				}
				return k3dClient.Delete(cleanupCtx, plan.clusterName)
			})
			if cleanupFailed(result) {
				out.Run.Status = model.StatusError
				out.ExitCode = 2
				out.addCommandDiagnostic("cluster_cleanup_failed", "CloudForge could not remove its k3d cluster.", result)
			}
		}
		if clusterAttempted && (!options.KeepEnvironment || !clusterCreated) {
			for _, kind := range []string{"container", "network", "volume"} {
				result := s.cleanupCommand(func(cleanupCtx context.Context) model.CommandResult {
					return dockerClient.RemoveClusterRemnants(cleanupCtx, plan.clusterName, kind)
				})
				if cleanupFailed(result) {
					out.addError("cluster_remnant_cleanup_failed", "CloudForge could not establish removal of its run-owned cluster resources.", "Cleanup requires a complete Docker inventory and matching invocation ownership; inspect the remaining local resources before retrying.")
				}
			}
		}
		if builderAttempted {
			result := s.cleanupCommand(func(cleanupCtx context.Context) model.CommandResult {
				return dockerClient.RemoveBuilderRemnants(cleanupCtx)
			})
			if cleanupFailed(result) {
				out.addError("builder_remnant_cleanup_failed", "CloudForge could not verify removal of its owned builder remnants.", commandGuidance(result, nil))
			}
		}
		for _, image := range imagesToCleanup {
			imageResult := s.cleanupCommand(func(cleanupCtx context.Context) model.CommandResult {
				return dockerClient.RemoveImage(cleanupCtx, image)
			})
			if cleanupFailed(imageResult) {
				out.Run.Status = model.StatusError
				out.ExitCode = 2
				out.addCommandDiagnostic("image_cleanup_failed", "CloudForge could not establish removal of a temporary Docker image.", imageResult)
			}
		}
		if len(imagesToCleanup) > 0 {
			result := s.cleanupCommand(func(cleanupCtx context.Context) model.CommandResult {
				return dockerClient.RemoveOwnedImages(cleanupCtx)
			})
			if cleanupFailed(result) {
				out.addError("image_cleanup_failed", "CloudForge could not establish removal of all owned temporary image objects.", "A complete Docker image inventory and matching invocation ownership are required, including untagged images.")
			}
		}
	}()

	defer func() {
		cleanupStarted = time.Now()
		if builderAttempted {
			result := s.cleanupCommand(func(cleanupCtx context.Context) model.CommandResult { return dockerClient.RemoveBuilder(cleanupCtx) })
			if cleanupFailed(result) {
				out.addError("builder_cleanup_failed", "CloudForge could not remove its owned builder.", commandGuidance(result, nil))
			}
		}
	}()
	if result := dockerClient.PrepareBuild(ctx, plan.image, plan.rolloutImage); cleanupFailed(result) {
		out.addError("resource_ownership_unavailable", "CloudForge could not establish unused names for its temporary build resources.", "Existing resources were preserved; inspect the local Docker resource inventory and retry with a new run.")
		return out
	}
	if result := dockerClient.CreateBuilder(ctx); cleanupFailed(result) {
		out.addCommandDiagnostic("builder_create_failed", "CloudForge could not create its bounded build environment.", result)
		return out
	}
	builderAttempted = true
	buildResult := dockerClient.BuildSelected(ctx, root, plan.image, "a", plan.config.Build)
	buildStatus := model.StatusPass
	buildSummary := "Container image built successfully."
	if failed(buildResult) {
		if isApplicationBuildFailure(buildResult) {
			buildStatus = model.StatusFail
			buildSummary = "Container image build failed."
		} else {
			buildStatus = model.StatusError
			buildSummary = "Container image build could not be completed."
		}
	}
	out.Run.Evidence = append(out.Run.Evidence, model.Evidence{
		ExperimentID: "container-build", Title: "Container build", Status: buildStatus,
		Summary: buildSummary, DurationMS: buildResult.DurationMS,
	})
	buildFinding := model.Finding{
		ID: "container.build", Category: "container", Status: buildStatus, Severity: model.SeverityHigh,
		Summary: buildSummary, Observed: strings.ToLower(string(buildStatus)), Expected: "image builds successfully",
		DurationMS: buildResult.DurationMS, Source: plan.buildSource(),
	}
	switch buildStatus {
	case model.StatusFail:
		buildFinding.Remediation = "Run the Docker build locally, correct the failing instruction, and retry verification."
	case model.StatusError:
		buildFinding.Remediation = "Resolve the Docker execution error and retry verification."
	}
	out.Run.Findings = append(out.Run.Findings, buildFinding)
	if failed(buildResult) {
		out.Run.Status = buildStatus
		if buildStatus == model.StatusFail {
			out.ExitCode = 1
		} else {
			out.ExitCode = 2
		}
		out.addCommandDiagnostic("container_build_failed", "Docker could not build the application image.", buildResult)
		return out
	}

	fingerprintImage(ctx, scoped, plan.image, out.Run.Fingerprint)
	trivyClient := trivy.New(scoped)
	scan, scanErr := trivyClient.ScanImage(ctx, plan.image, out.Run.Fingerprint.ImageID)
	if scanErr != nil || failed(scan.Command) {
		out.Run.Evidence = append(out.Run.Evidence, model.Evidence{
			ExperimentID: "container-scan", Title: "Container vulnerability scan (image A)", Status: model.StatusError,
			Summary:    "The image A vulnerability observation was unusable; no finding count is established.",
			DurationMS: scan.Command.DurationMS,
			Execution:  &model.ExperimentExecution{Executed: scan.Started},
		})
		if scanErr != nil {
			out.addError("trivy_output_invalid", "CloudForge could not establish a valid image A vulnerability observation.", scanErr.Error())
		} else {
			out.addCommandDiagnostic("trivy_scan_failed", "Trivy could not scan the application image reliably.", scan.Command)
		}
		return out
	}
	out.Run.Findings = append(out.Run.Findings, scan.Findings...)
	scanStatus := model.StatusPass
	if countFindingStatus(scan.Findings, model.StatusWarn) > 0 {
		scanStatus = model.StatusWarn
	}
	out.Run.Evidence = append(out.Run.Evidence, model.Evidence{
		ExperimentID: "container-scan", Title: "Container vulnerability scan (image A)", Status: scanStatus,
		Summary: scanSummary(scan.Findings), DurationMS: scan.Command.DurationMS,
		Measurements: scan.Measurements,
	})

	var prebuiltRollout *model.CommandResult
	if (backendProfile(config) || isWorker(config)) && (plannedCapability(out.Run.Plan, "rolling-deployment").Disposition == "supported" || plannedCapability(out.Run.Plan, "worker-image-replacement").Disposition == "supported") {
		imagesToCleanup = append(imagesToCleanup, plan.rolloutImage)
		result := dockerClient.BuildSelected(ctx, root, plan.rolloutImage, "b", plan.config.Build)
		prebuiltRollout = &result
		status, summary := model.StatusPass, "Rollout image B was built before starting backend resources."
		if failed(result) {
			status, summary = model.StatusError, "Rollout image B could not be built reliably; independent image A observations may still proceed."
			if isApplicationBuildFailure(result) {
				status, summary = model.StatusFail, "Rollout image B failed to build; independent image A observations may still proceed."
			}
		}
		out.Run.Evidence = append(out.Run.Evidence, model.Evidence{ExperimentID: "rollout-image-build", Title: "Rollout image build", Status: status, Summary: summary, DurationMS: result.DurationMS, Execution: &model.ExperimentExecution{Executed: true}})
		// Preserve a failed B build for its experiment, while image A can still
		// produce independent startup and lifecycle evidence.
		if ctx.Err() != nil {
			return out
		}
		if cleanup := dockerClient.RemoveBuilder(ctx); cleanupFailed(cleanup) {
			out.addCommandDiagnostic("builder_cleanup_failed", "The bounded builder could not be stopped before starting backend resources.", cleanup)
			return out
		}
		builderAttempted = false
	} else if backendProfile(config) {
		if cleanup := dockerClient.RemoveBuilder(ctx); cleanupFailed(cleanup) {
			out.addCommandDiagnostic("builder_cleanup_failed", "The bounded builder could not be stopped before starting backend resources.", cleanup)
			return out
		}
		builderAttempted = false
	}
	if result := dockerClient.PrepareCluster(ctx, plan.clusterName); cleanupFailed(result) {
		out.addError("resource_ownership_unavailable", "CloudForge could not establish unused names for its disposable cluster.", "Existing resources were preserved; inspect the local Docker resource inventory and retry with a new run.")
		return out
	}
	clusterAttempted = true
	publishedNodePort := nodePort
	if isWorker(config) {
		publishedNodePort = 0
	}
	if result := k3dClient.CreateWithMemory(ctx, plan.clusterName, publishedNodePort, budgetFor(config).ClusterMemory); failed(result) {
		out.addCommandDiagnostic("cluster_create_failed", "k3d could not create the verification cluster.", result)
		return out
	}
	clusterCreated = true
	dockerClient.ClusterCreated()
	credentials := k3dClient.Kubeconfig(ctx, plan.clusterName)
	if failed(credentials) || credentials.Truncated {
		out.addError("kubeconfig_failed", "CloudForge could not obtain its private kubeconfig.", "Inspect the disposable cluster and retry.")
		return out
	}
	if err := os.WriteFile(kubeconfigPath, []byte(credentials.Stdout), 0o600); err != nil {
		out.addError("kubeconfig_failed", "CloudForge could not write its private kubeconfig.", err.Error())
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
		if config.Endpoints.Load != "" {
			plan.loadURL = localEndpointURL("http", config.Endpoints.Load, hostPort)
		}
		out.Run.Environment.Endpoint = plan.readinessURL
	}
	if result, reason := kubernetesClient.WaitReady(ctx, plan.clusterName); failed(result) {
		out.addError("cluster_not_ready", "The isolated cluster did not become ready before application deployment.", "Cluster condition: "+reason+"; inspect Docker capacity and cluster health before retrying.")
		return out
	}
	if !checkRuntimeServer(ctx, scoped, plan, &out) {
		return out
	}
	if result := k3dClient.ImportImage(ctx, plan.clusterName, plan.image); failed(result) {
		out.addCommandDiagnostic("image_import_failed", "The application image could not be confirmed in the isolated node after import.", result)
		return out
	}
	if !s.startDependencies(ctx, kubernetesClient, plan, temporary, &out) {
		return out
	}
	plan.dependencyFingerprints = append([]model.DependencyFingerprint{}, out.Run.Fingerprint.Dependencies...)
	completeFingerprint(out.Run.Fingerprint)
	if config.Preparation != nil && !s.runPreparation(ctx, kubernetesClient, plan, temporary, &out) {
		return out
	}
	if isWorker(config) {
		s.runWorkerSuite(ctx, kubernetesClient, k3dClient, plan, manifestPath, prebuiltRollout, &out)
		return out
	}
	if result := kubernetesClient.Apply(ctx, plan.clusterName, manifestPath); failed(result) {
		out.addCommandDiagnostic("deployment_apply_failed", "kubectl could not apply the generated workload.", result)
		return out
	}

	var acceptance *readinessChecker
	if config.Readiness != nil {
		acceptance = newReadinessChecker(directHTTPClient(), *config.Readiness)
		original := s.probe
		s.probe = func(ctx context.Context, url string) (int, error) {
			if url == plan.readinessURL {
				return acceptance.probe(ctx, url)
			}
			return original(ctx, url)
		}
		defer func() { s.probe = original }()
	}
	var httpResultChannel chan httpObservation
	restoreProbePacing := s.configureProbePacing(config)
	defer restoreProbePacing()
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
	if ctx.Err() != nil {
		out.Run.Evidence = append(out.Run.Evidence, model.Evidence{
			ExperimentID: "deployment-readiness", Title: "Deployment readiness", Status: model.StatusError,
			Summary:    "Application readiness observation was interrupted; the readiness requirement was not assessed to completion.",
			DurationMS: maxInt64(waitResult.DurationMS, httpResult.DurationMS),
			Measurements: []model.Measurement{
				{Name: "readiness_http_status", Value: strconv.Itoa(httpResult.Status)},
				{Name: "readiness_attempts", Value: strconv.Itoa(httpResult.Attempts), Unit: "requests"},
				{Name: "failed_startup_requests", Value: strconv.Itoa(httpResult.Failures), Unit: "requests"},
			},
		})
		index := len(out.Run.Evidence) - 1
		out.Run.Evidence[index].Measurements = append(out.Run.Evidence[index].Measurements, httpResult.diagnostics()...)
		if acceptance != nil {
			evidence := acceptance.evidence(httpResult.DurationMS)
			if !httpResult.Success {
				evidence.Status = model.StatusError
				evidence.Summary = "Semantic readiness observation was interrupted; partial observations are retained."
			}
			out.Run.Evidence = append(out.Run.Evidence, evidence)
		}
		return out
	}
	if acceptance != nil {
		out.Run.Evidence = append(out.Run.Evidence, acceptance.evidence(httpResult.DurationMS))
	}

	if failed(waitResult) && !isRolloutFailure(waitResult) {
		out.Run.Evidence = append(out.Run.Evidence, model.Evidence{ExperimentID: "deployment-readiness", Title: "Deployment readiness", Status: model.StatusError, Summary: "Kubernetes readiness could not be observed reliably."})
		blockUnobservedReadiness(&out, "Kubernetes readiness could not be observed reliably.")
		out.addError("readiness_observation_failed", "Kubernetes readiness could not be observed reliably.", "Check the isolated API and kubectl access; no application startup failure is inferred.")
		return out
	}
	pods, podResult, podErr := kubernetesClient.ObservePods(ctx, plan.clusterName, namespace, "app.kubernetes.io/name="+plan.workloadName)
	ready, total, restarts := summarizePods(pods)
	readinessStatus := model.StatusPass
	readinessSummary := "Deployment became available."
	if failed(waitResult) {
		readinessStatus = model.StatusFail
		readinessSummary = "Deployment did not become available before the deadline."
	}
	if plan.readinessURL != "" && !httpResult.Success {
		readinessStatus = model.StatusFail
		readinessSummary = "The readiness endpoint did not satisfy the configured HTTP readiness contract."
	}
	if podErr != nil || failed(podResult) {
		out.Run.Evidence = append(out.Run.Evidence, model.Evidence{ExperimentID: "deployment-readiness", Title: "Deployment readiness", Status: model.StatusError, Summary: "Pod state could not be observed reliably.", Measurements: httpResult.diagnostics()})
		blockUnobservedReadiness(&out, "Pod state could not be observed reliably.")
		out.addError("pod_observation_failed", "CloudForge could not decode the deployed pod state.", commandGuidance(podResult, podErr))
		return out
	}
	readinessMeasurements := []model.Measurement{
		{Name: "ready_pods", Value: strconv.Itoa(ready), Unit: "pods"},
		{Name: "total_pods", Value: strconv.Itoa(total), Unit: "pods"},
		{Name: "container_restarts", Value: strconv.FormatInt(int64(restarts), 10), Unit: "restarts"},
		{Name: "readiness_duration_ms", Value: strconv.FormatInt(waitResult.DurationMS, 10), Unit: "ms"},
	}
	readinessMeasurements = append(readinessMeasurements, podTerminationMeasurements(pods)...)
	readinessMeasurements = append(readinessMeasurements, httpResult.diagnostics()...)
	if plan.readinessURL != "" {
		readinessMeasurements = append(readinessMeasurements,
			model.Measurement{Name: "startup_duration_ms", Value: strconv.FormatInt(httpResult.DurationMS, 10), Unit: "ms"},
			model.Measurement{Name: "readiness_http_status", Value: strconv.Itoa(httpResult.Status)},
			model.Measurement{Name: "readiness_attempts", Value: strconv.Itoa(httpResult.Attempts), Unit: "requests"},
			model.Measurement{Name: "failed_startup_requests", Value: strconv.Itoa(httpResult.Failures), Unit: "requests"},
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
		reasons := map[string]int{}
		for _, pod := range pods {
			if !pod.Ready {
				reasons[pod.Reason]++
			}
		}
		keys := make([]string, 0, len(reasons))
		for reason := range reasons {
			keys = append(keys, reason)
		}
		sort.Strings(keys)
		for _, reason := range keys {
			index := len(out.Run.Evidence) - 1
			out.Run.Evidence[index].Measurements = append(out.Run.Evidence[index].Measurements, model.Measurement{Name: "pods_" + reason, Value: strconv.Itoa(reasons[reason]), Unit: "pods"})
		}
		if reasons["image_unavailable"] > 0 {
			index := len(out.Run.Evidence) - 1
			out.Run.Evidence[index].Status = model.StatusError
			out.Run.Evidence[index].Summary = "The imported application image was unavailable in the test node; application startup could not be assessed."
			blockUnobservedReadiness(&out, "The application image was unavailable in the test node.")
			out.addError("runtime_image_unavailable", out.Run.Evidence[index].Summary, "Inspect the isolated image import and node image storage before retrying.")
			return out
		}
		problems, nodeResult, nodeErr := kubernetesClient.NodeProblems(ctx, plan.clusterName)
		if nodeErr != nil || failed(nodeResult) || nodeResult.Truncated {
			index := len(out.Run.Evidence) - 1
			out.Run.Evidence[index].Status = model.StatusError
			out.Run.Evidence[index].Summary = "The test node health could not be observed reliably; application startup could not be assessed."
			blockUnobservedReadiness(&out, "Test node health was unobservable.")
			out.addError("runtime_environment_unobserved", out.Run.Evidence[index].Summary, "Restore reliable node observation before attributing the unmet startup requirement to the application.")
			return out
		}
		if len(problems) > 0 {
			index := len(out.Run.Evidence) - 1
			out.Run.Evidence[index].Status = model.StatusError
			out.Run.Evidence[index].Summary = "The test node was unhealthy; application startup could not be assessed reliably."
			blockUnobservedReadiness(&out, "The test node was unhealthy.")
			out.addError("runtime_environment_unhealthy", out.Run.Evidence[index].Summary, strings.Join(problems, ", "))
			return out
		}
		out.Run.Findings = append(out.Run.Findings, model.Finding{
			ID: "container.startup", Category: "container", Status: model.StatusFail, Severity: model.SeverityHigh,
			Summary: "The application did not start with all requested replicas ready.", Observed: readinessSummary,
			Expected: "all requested replicas become ready", Remediation: "Inspect the container entry point, application logs, port, and readiness probe.",
			DurationMS: waitResult.DurationMS, Source: plan.buildSource(),
		})
		out.Run.Status = model.StatusFail
		out.ExitCode = 1
		out.Run.Diagnostics = append(out.Run.Diagnostics, model.Diagnostic{
			Code: "readiness_failed", Status: model.StatusFail,
			Message:  startupFailureSummary(ready, int(plan.desiredReplicas), restarts, s.readinessTimeout, pods),
			Guidance: "Root cause is not established by retained evidence. Inspect bounded private startup diagnostics and the declared test configuration; generated limits are not application sizing measurements.",
		})
		return out
	}

	out.Run.Findings = append(out.Run.Findings, model.Finding{
		ID: "container.startup", Category: "container", Status: model.StatusPass, Severity: model.SeverityInfo,
		Summary: "The application started with all requested replicas ready.", Observed: fmt.Sprintf("%d/%d pods ready", ready, total),
		Expected: "all requested replicas become ready", DurationMS: waitResult.DurationMS, Source: plan.buildSource(),
	})

	blocked := ""
	experiments := []struct {
		name string
		run  func() recoveryOutcome
	}{
		{"readiness-gating", func() recoveryOutcome { return s.runReadinessGating(ctx, kubernetesClient, plan) }},
		{"inflight-shutdown", func() recoveryOutcome { return s.runInFlightShutdown(ctx, kubernetesClient, plan) }},
		{"graceful-shutdown", func() recoveryOutcome { return s.runGracefulShutdown(ctx, kubernetesClient, plan) }},
		{"pod-recovery", func() recoveryOutcome { return s.runPodRecovery(ctx, kubernetesClient, plan) }},
		{"rolling-deployment", func() recoveryOutcome {
			var build model.CommandResult
			if prebuiltRollout != nil {
				build = *prebuiltRollout
			} else {
				imagesToCleanup = append(imagesToCleanup, plan.rolloutImage)
				build = dockerClient.BuildSelected(ctx, root, plan.rolloutImage, "b", plan.config.Build)
			}
			return s.runRollingDeployment(ctx, k3dClient, kubernetesClient, plan, build)
		}},
	}
	for _, experiment := range experiments {
		if ctx.Err() != nil {
			break
		}
		if !s.prepareExperiment(ctx, kubernetesClient, plan, &out, experiment.name, &blocked) {
			continue
		}
		result := experiment.run()
		s.finishExperiment(ctx, kubernetesClient, plan, manifestPath, &out, result, false, &blocked)
	}
	if ctx.Err() == nil && s.prepareExperiment(ctx, kubernetesClient, plan, &out, "load-profile", &blocked) {
		load, autoscaling := s.runLoadAndAutoscaling(ctx, k6executor.New(scoped), kubernetesClient, plan, temporary, hpaManifestPath)
		load.Evidence.Execution = &model.ExperimentExecution{Executed: load.Evidence.Status != model.StatusSkipped && load.Evidence.Status != model.StatusBlocked, MutationAttempted: load.MutationAttempted}
		load.Evidence.Topology = plan.topology
		applyExperimentOutcome(&out, load)
		// HPA and load share one bounded observation; restore after both results.
		s.finishExperiment(ctx, kubernetesClient, plan, manifestPath, &out, autoscaling, hpaManifestPath != "", &blocked)
	} else if ctx.Err() == nil {
		capability := plannedCapability(out.Run.Plan, "horizontal-autoscaling")
		status, reason := model.StatusSkipped, capability.Reason
		if capability.Disposition == "supported" {
			status, reason = model.StatusBlocked, "The required load/baseline prerequisite was not established."
		}
		recordNotExecuted(&out, plan, "horizontal-autoscaling", status, reason)
	}
	finalEvidenceStatus(&out)

	return out
}

func applyExperimentOutcome(out *Outcome, experiment recoveryOutcome) {
	if experiment.Evidence.ExperimentID != "" {
		out.Run.Evidence = append(out.Run.Evidence, experiment.Evidence)
	}
	if experiment.Finding != nil {
		out.Run.Findings = append(out.Run.Findings, *experiment.Finding)
	}
	if experiment.Diagnostic != nil {
		out.Run.Diagnostics = append(out.Run.Diagnostics, *experiment.Diagnostic)
	}
	if experiment.ExitCode == 0 {
		return
	}
	out.ExitCode = experiment.ExitCode
	if experiment.ExitCode == 1 {
		out.Run.Status = model.StatusFail
	} else {
		out.Run.Status = model.StatusError
	}
}

type plan struct {
	topology               *model.TestTopology
	clusterName            string
	workloadName           string
	image                  string
	rolloutImage           string
	desiredReplicas        int32
	readinessScheme        string
	readinessPath          string
	healthScheme           string
	healthPath             string
	readinessURL           string
	healthURL              string
	loadURL                string
	config                 model.RuntimeConfiguration
	effectiveResources     model.ResourceRequirements
	dependencyFingerprints []model.DependencyFingerprint
	httpSkipReason         string
	manifest               []byte
	hpaManifest            []byte
	hpaName                string
	hpaMinReplicas         int32
	hpaMaxReplicas         int32
	hpaTargetCPU           int32
	hpaDemandWindow        time.Duration
	hpaScaleDisabled       bool
	hpaSkipReason          string
}

func buildPlan(analysis model.AnalysisResult, id string) (plan, error) {
	return buildConfiguredPlan(analysis, id, defaultConfiguration())
}

func buildConfiguredPlan(analysis model.AnalysisResult, id string, config model.RuntimeConfiguration) (plan, error) {
	if err := validateTopology(config); err != nil {
		return plan{}, err
	}
	config.Topology = effectiveTopology(config.Topology)
	application := analysis.Application
	if config.Topology != nil && len(application.Kubernetes.HorizontalPodScalers) > 0 {
		return plan{}, errors.New("fixed test topology cannot be combined with a source HPA; autoscaling would change the controlled replica count")
	}
	dockerfile := "Dockerfile"
	if config.Build != nil {
		if analysis.Build == nil || *analysis.Build != *config.Build {
			return plan{}, errors.New("build selection must match the analyzed workload")
		}
		dockerfile = config.Build.Dockerfile
	}
	if len(application.Containers) != 1 || application.Containers[0].Source.Path != dockerfile {
		return plan{}, errors.New("verification requires exactly one root Dockerfile or an explicit build selection")
	}
	if len(application.Kubernetes.Deployments) > 1 {
		return plan{}, errors.New("verification requires zero or one Kubernetes Deployment; select a narrower application directory")
	}
	if isWorker(config) {
		return buildWorkerPlan(analysis, id, config)
	}
	port, portName, err := selectPort(application)
	if config.Runtime.Port > 0 {
		port, portName, err = config.Runtime.Port, "http", nil
	}
	if err != nil {
		return plan{}, err
	}
	name := dnsName(application.Name)
	if name == "" {
		name = "application"
	}
	workloadName := trimDNSName("cf-" + name + "-" + id)
	clusterName := trimDNSName("cloudforge-" + id)
	image := "cloudforge/" + name + ":" + id + "-a"
	rolloutImage := "cloudforge/" + name + ":" + id + "-b"

	replicas := int32(1)
	resources := corev1.ResourceRequirements{}
	var readinessProbe, livenessProbe, startupProbe *corev1.Probe
	var grace *int64
	strategy := appsv1.DeploymentStrategy{}
	var minReady int32
	var progressDeadline *int32
	readinessScheme, readinessPath := "", ""
	healthScheme, healthPath := "", ""
	httpSkipReason := ""
	if len(application.Kubernetes.Deployments) == 1 {
		deployment := application.Kubernetes.Deployments[0]
		if len(deployment.Unsupported) > 0 {
			return plan{}, fmt.Errorf("unsupported deployment settings: %s", strings.Join(deployment.Unsupported, ", "))
		}
		grace = deployment.TerminationGracePeriodSeconds
		if grace != nil && (*grace < 1 || *grace > 120) {
			return plan{}, errors.New("termination grace period must be between 1 and 120 seconds")
		}
		if deployment.Strategy != nil {
			strategy = *deployment.Strategy.DeepCopy()
		}
		minReady, progressDeadline = deployment.MinReadySeconds, deployment.ProgressDeadlineSeconds
		if config.Topology == nil && deployment.Replicas != nil && (*deployment.Replicas < 1 || *deployment.Replicas > 5) {
			return plan{}, errors.New("deployment replicas must be between 1 and 5; excessive replicas are rejected before execution")
		}
		if deployment.Replicas != nil && *deployment.Replicas > 0 {
			replicas = *deployment.Replicas
		}
		if len(deployment.Containers) != 1 {
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
		for _, value := range deployment.Probes {
			resolvedPort := value.Port
			if _, parseErr := strconv.Atoi(resolvedPort); parseErr != nil {
				for _, container := range deployment.Containers {
					for _, candidate := range container.Ports {
						if candidate.Name == resolvedPort {
							resolvedPort = strconv.Itoa(int(candidate.Port))
							break
						}
					}
				}
			}
			if resolvedPort != strconv.Itoa(int(port)) {
				return plan{}, fmt.Errorf("unsupported %s probe port: probes must use the selected application port", value.Purpose)
			}
			value.Port = strconv.Itoa(int(port))
			probe, probeErr := preservedProbe(value)
			if probeErr != nil {
				return plan{}, probeErr
			}
			switch value.Purpose {
			case "readiness":
				readinessProbe = probe
			case "liveness":
				livenessProbe = probe
			case "startup":
				startupProbe = probe
			}
		}

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
	if config.Topology != nil {
		replicas = config.Topology.Replicas
		strategy = topologyStrategy(config.Topology)
	}
	if config.Endpoints.Readiness != "" {
		if strings.EqualFold(readinessScheme, "https") {
			return plan{}, errors.New("HTTPS readiness cannot be replaced with an HTTP experiment endpoint in this pilot")
		}
		readinessScheme, readinessPath = "http", config.Endpoints.Readiness
	}
	if config.Endpoints.Health != "" {
		healthScheme, healthPath = "http", config.Endpoints.Health
	}
	if readinessProbe == nil && readinessPath != "" {
		readinessProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: readinessPath, Port: intstr.FromInt32(port)}}}
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

	if config.Endpoints.Load != "" && readinessPath == "" {
		return plan{}, errors.New("load testing requires an explicit HTTP readiness endpoint")
	}
	peakReplicas := replicas
	for _, hpa := range application.Kubernetes.HorizontalPodScalers {
		if hpa.MaxReplicas > 5 || hpa.MaxReplicas < 1 {
			return plan{}, errors.New("HPA maxReplicas must be between 1 and 5; no silent cap is applied")
		}
		if len(hpa.Unsupported) > 0 {
			return plan{}, fmt.Errorf("unsupported HPA settings: %s", strings.Join(hpa.Unsupported, ", "))
		}
		if hpa.MaxReplicas > peakReplicas {
			peakReplicas = hpa.MaxReplicas
		}
	}
	var dependencyResources []model.ResourceRequirements
	for _, name := range enabledProviders(config) {
		dependencyResources = append(dependencyResources, providerFingerprint(name).Resources)
	}
	resources, err = boundedResourcesFor(budgetFor(config), resources, peakReplicas, strategy, dependencyResources...)
	if err != nil {
		return plan{}, err
	}
	if config.Preparation != nil {
		prep, _ := kubernetesResources(preparationResources())
		if _, err := boundedResourcesFor(budgetFor(config), prep, 1, appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}, dependencyResources...); err != nil {
			return plan{}, err
		}
	}
	config.Runtime.Port = port
	config.Endpoints.Readiness, config.Endpoints.Health = readinessPath, healthPath
	labels := map[string]string{"app.kubernetes.io/name": workloadName, "app.kubernetes.io/managed-by": "cloudforge", "cloudforge.dev/run-id": id, "cloudforge.dev/role": "application"}
	objects := []any{
		&corev1.Namespace{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"}, ObjectMeta: metav1.ObjectMeta{Name: namespace, Labels: map[string]string{"app.kubernetes.io/managed-by": "cloudforge", "cloudforge.dev/run-id": id}}},
		&appsv1.Deployment{
			TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
			ObjectMeta: metav1.ObjectMeta{Name: workloadName, Namespace: namespace, Labels: labels},
			Spec: appsv1.DeploymentSpec{
				Strategy: strategy, MinReadySeconds: minReady, ProgressDeadlineSeconds: progressDeadline,
				Replicas: &replicas, Selector: &metav1.LabelSelector{MatchLabels: labels},
				Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{TerminationGracePeriodSeconds: grace, Containers: []corev1.Container{{
					Name: "application", Image: image, ImagePullPolicy: corev1.PullNever, Env: applicationEnvironment(config),
					Ports:     []corev1.ContainerPort{{Name: portName, ContainerPort: port, Protocol: corev1.ProtocolTCP}},
					Resources: resources, ReadinessProbe: readinessProbe, LivenessProbe: livenessProbe, StartupProbe: startupProbe,
				}}}},
			},
		},
		&corev1.Service{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
			ObjectMeta: metav1.ObjectMeta{Name: workloadName, Namespace: namespace, Labels: labels},
			Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeNodePort, Selector: labels, Ports: []corev1.ServicePort{{Name: portName, Port: port, TargetPort: intstr.FromString(portName), NodePort: nodePort, Protocol: corev1.ProtocolTCP}}},
		},
	}
	if advancedConfiguration(config) {
		pod := &objects[1].(*appsv1.Deployment).Spec.Template.Spec
		disabled := false
		pod.AutomountServiceAccountToken = &disabled
		pod.EnableServiceLinks = &disabled
		pod.SecurityContext = &corev1.PodSecurityContext{SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}
		pod.Containers[0].SecurityContext = &corev1.SecurityContext{AllowPrivilegeEscalation: &disabled, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}
	}
	manifest, err := marshalDocuments(objects)
	if err != nil {
		return plan{}, fmt.Errorf("encode generated Kubernetes resources: %w", err)
	}
	result := plan{
		clusterName: clusterName, workloadName: workloadName, image: image, rolloutImage: rolloutImage, desiredReplicas: replicas,
		readinessScheme: readinessScheme, readinessPath: readinessPath, healthScheme: healthScheme, healthPath: healthPath,
		httpSkipReason: httpSkipReason, manifest: manifest, config: config, effectiveResources: modelResources(resources),
	}
	result.topology = testTopology(application, replicas, strategy, grace, minReady, []*corev1.Probe{livenessProbe, readinessProbe, startupProbe}, config)
	result.hpaSkipReason = "No supported HPA targets the selected Deployment."
	if len(application.Kubernetes.HorizontalPodScalers) == 1 && len(application.Kubernetes.Deployments) == 1 {
		source := application.Kubernetes.HorizontalPodScalers[0]
		deployment := application.Kubernetes.Deployments[0]
		if source.TargetKind == "Deployment" && source.TargetName == deployment.Name && normalizedNamespace(source.Namespace) == normalizedNamespace(deployment.Namespace) && source.MaxReplicas > replicas && source.TargetCPU != nil && *source.TargetCPU > 0 {
			minimum := int32(1)
			if source.MinReplicas != nil && *source.MinReplicas > 0 {
				minimum = *source.MinReplicas
			}
			maximum := source.MaxReplicas
			if minimum > maximum {
				return plan{}, errors.New("HPA minReplicas exceeds maxReplicas")
			}
			targetCPU := *source.TargetCPU
			hpa := &autoscalingv2.HorizontalPodAutoscaler{
				TypeMeta:   metav1.TypeMeta{APIVersion: "autoscaling/v2", Kind: "HorizontalPodAutoscaler"},
				ObjectMeta: metav1.ObjectMeta{Name: workloadName, Namespace: namespace, Labels: labels},
				Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
					ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: workloadName},
					MinReplicas:    &minimum, MaxReplicas: maximum, Behavior: source.Behavior.DeepCopy(),
					Metrics: []autoscalingv2.MetricSpec{{Type: autoscalingv2.ResourceMetricSourceType, Resource: &autoscalingv2.ResourceMetricSource{Name: corev1.ResourceCPU, Target: autoscalingv2.MetricTarget{Type: autoscalingv2.UtilizationMetricType, AverageUtilization: &targetCPU}}}},
				},
			}
			result.hpaManifest, err = marshalDocuments([]any{hpa})
			if err != nil {
				return plan{}, fmt.Errorf("encode generated HPA: %w", err)
			}
			result.hpaName, result.hpaMinReplicas, result.hpaMaxReplicas, result.hpaTargetCPU = workloadName, minimum, maximum, targetCPU
			result.hpaSkipReason = ""
			if source.Behavior != nil && source.Behavior.ScaleUp != nil {
				up := source.Behavior.ScaleUp
				if up.StabilizationWindowSeconds != nil {
					result.hpaDemandWindow = time.Duration(*up.StabilizationWindowSeconds) * time.Second
				}
				if up.SelectPolicy != nil && *up.SelectPolicy == autoscalingv2.DisabledPolicySelect {
					result.hpaScaleDisabled = true
				}
			}
		} else {
			result.hpaSkipReason = "The discovered HPA must target the selected Deployment, define a positive CPU utilization target, and allow more replicas than the workload starts with."
		}
	} else if len(application.Kubernetes.HorizontalPodScalers) > 1 {
		result.hpaSkipReason = "Multiple HPAs were discovered, so CloudForge did not choose one implicitly."
	}
	return result, nil
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
	value := make([]byte, 16)
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
		request.Close = true // Sample new Service connections, not a previously selected backend.
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

func blockingAnalysisDiagnostic(values []model.Diagnostic) *model.Diagnostic {
	for index := range values {
		switch values[index].Code {
		case "dockerfile_invalid", "dockerfile_unreadable", "file_limit", "kubernetes_invalid", "manifest_invalid", "manifest_unreadable", "path_unreadable":
			return &values[index]
		}
	}
	return nil
}

func diagnosticPath(source *model.SourceReference) string {
	if source == nil || source.Path == "" {
		return "the selected application root"
	}
	return source.Path
}

func isDockerDaemonFailure(output string) bool {
	for _, fragment := range []string{
		"cannot connect to the docker daemon",
		"is the docker daemon running",
		"permission denied while trying to connect to the docker daemon",
		"permission denied while trying to connect to the docker socket",
		"docker daemon access",
		"failed to connect to the docker api",
		"error during connect",
	} {
		if strings.Contains(output, fragment) {
			return true
		}
	}
	return strings.Contains(output, "dial unix") && strings.Contains(output, "docker.sock") &&
		(strings.Contains(output, "permission denied") || strings.Contains(output, "connection refused"))
}

func (s *Service) cleanupCommand(run func(context.Context) model.CommandResult) model.CommandResult {
	timeout := s.cleanupTimeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return run(cleanupCtx)
}

func cleanupFailed(result model.CommandResult) bool {
	return failed(result) || result.Truncated
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
	if result.Command == "docker" && isDockerDaemonFailure(output) && strings.Contains(output, "permission denied") {
		return "Docker daemon access was denied; grant the current user daemon access and run cloudforge doctor again."
	}
	if result.Command == "docker" && isDockerDaemonFailure(output) {
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
		return "Trivy reported zero normalized vulnerability findings in scanned image A."
	}
	return fmt.Sprintf("Trivy reported %d normalized vulnerability findings in scanned image A.", count)
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

func normalizedNamespace(value string) string {
	if value == "" {
		return "default"
	}
	return value
}

func isRolloutFailure(result model.CommandResult) bool {
	return result.FailureType == model.FailureTimeout || (result.FailureType == model.FailureExit && (strings.Contains(result.Stderr, "timed out waiting for the condition") || strings.Contains(result.Stderr, "exceeded its progress deadline")))
}
