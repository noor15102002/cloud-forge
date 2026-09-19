// Package model contains CloudForge's public, machine-readable contracts.
package model

import (
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
)

// SchemaVersion identifies the current machine-readable contract.
const SchemaVersion = "v1alpha1"

// Status describes the outcome of a check or operation.
type Status string

// Supported CloudForge statuses.
const (
	StatusPass    Status = "pass"
	StatusWarn    Status = "warn"
	StatusFail    Status = "fail"
	StatusSkipped Status = "skipped"
	StatusError   Status = "error"
	StatusBlocked Status = "blocked"
)

// SourceReference identifies where a discovered fact came from.
type SourceReference struct {
	Path     string `json:"path"`
	Document int    `json:"document,omitempty"`
	Field    string `json:"field,omitempty"`
}

// Diagnostic explains an analysis limitation or failure.
type Diagnostic struct {
	Code     string           `json:"code"`
	Status   Status           `json:"status"`
	Message  string           `json:"message"`
	Guidance string           `json:"guidance,omitempty"`
	Source   *SourceReference `json:"source,omitempty"`
}

// Runtime describes one detected application runtime.
type Runtime struct {
	Language            string          `json:"language"`
	Framework           string          `json:"framework,omitempty"`
	FrameworkCandidates []string        `json:"framework_candidates,omitempty"`
	Version             string          `json:"version,omitempty"`
	PackageManager      string          `json:"package_manager,omitempty"`
	Source              SourceReference `json:"source"`
}

// Dependency represents a production architecture dependency inferred from metadata.
type Dependency struct {
	Name   string          `json:"name"`
	Type   string          `json:"type"`
	Source SourceReference `json:"source"`
}

// ContainerPort describes a declared container port.
type ContainerPort struct {
	Name     string          `json:"name,omitempty"`
	Port     int32           `json:"port"`
	Protocol string          `json:"protocol,omitempty"`
	Source   SourceReference `json:"source"`
}

// Endpoint describes an explicitly declared health-related endpoint.
type Endpoint struct {
	Purpose  string          `json:"purpose"`
	Path     string          `json:"path,omitempty"`
	Port     string          `json:"port,omitempty"`
	Protocol string          `json:"protocol,omitempty"`
	Source   SourceReference `json:"source"`
}

// ResourceRequirements contains declared CPU and memory values.
type ResourceRequirements struct {
	CPURequest    string `json:"cpu_request,omitempty"`
	MemoryRequest string `json:"memory_request,omitempty"`
	CPULimit      string `json:"cpu_limit,omitempty"`
	MemoryLimit   string `json:"memory_limit,omitempty"`
}

// Container contains safe metadata for a Dockerfile stage or Kubernetes container.
type Container struct {
	UnresolvedPorts bool                 `json:"unresolved_ports,omitempty"`
	Name            string               `json:"name,omitempty"`
	Image           string               `json:"image,omitempty"`
	User            string               `json:"user,omitempty"`
	Ports           []ContainerPort      `json:"ports,omitempty"`
	Resources       ResourceRequirements `json:"resources,omitempty"`
	Source          SourceReference      `json:"source"`
}

// Probe preserves safe probe semantics without command text or header values.
type Probe struct {
	Purpose                       string   `json:"purpose"`
	Type                          string   `json:"type"`
	Path                          string   `json:"path,omitempty"`
	Port                          string   `json:"port,omitempty"`
	Scheme                        string   `json:"scheme,omitempty"`
	GRPCService                   *string  `json:"grpc_service,omitempty"`
	InitialDelaySeconds           int32    `json:"initial_delay_seconds,omitempty"`
	TimeoutSeconds                int32    `json:"timeout_seconds,omitempty"`
	PeriodSeconds                 int32    `json:"period_seconds,omitempty"`
	SuccessThreshold              int32    `json:"success_threshold,omitempty"`
	FailureThreshold              int32    `json:"failure_threshold,omitempty"`
	TerminationGracePeriodSeconds *int64   `json:"termination_grace_period_seconds,omitempty"`
	Unsupported                   []string `json:"unsupported,omitempty"`
}

// Deployment retains supported safe workload settings and unsupported field names.
type Deployment struct {
	Probes                        []Probe                    `json:"probes,omitempty"`
	TerminationGracePeriodSeconds *int64                     `json:"termination_grace_period_seconds,omitempty"`
	Strategy                      *appsv1.DeploymentStrategy `json:"strategy,omitempty"`
	MinReadySeconds               int32                      `json:"min_ready_seconds,omitempty"`
	ProgressDeadlineSeconds       *int32                     `json:"progress_deadline_seconds,omitempty"`
	Unsupported                   []string                   `json:"unsupported,omitempty"`
	Name                          string                     `json:"name"`
	Namespace                     string                     `json:"namespace,omitempty"`
	Replicas                      *int32                     `json:"replicas,omitempty"`
	Containers                    []Container                `json:"containers,omitempty"`
	Endpoints                     []Endpoint                 `json:"endpoints,omitempty"`
	Source                        SourceReference            `json:"source"`
}

// ServicePort describes a Kubernetes Service port mapping.
type ServicePort struct {
	Name       string `json:"name,omitempty"`
	Port       int32  `json:"port"`
	TargetPort string `json:"target_port,omitempty"`
	Protocol   string `json:"protocol,omitempty"`
}

// Service describes a Kubernetes Service.
type Service struct {
	Name      string          `json:"name"`
	Namespace string          `json:"namespace,omitempty"`
	Type      string          `json:"type,omitempty"`
	Ports     []ServicePort   `json:"ports,omitempty"`
	Source    SourceReference `json:"source"`
}

// HorizontalPodAutoscaler describes a supported Kubernetes HPA.
type HorizontalPodAutoscaler struct {
	Behavior    *autoscalingv2.HorizontalPodAutoscalerBehavior `json:"behavior,omitempty"`
	Unsupported []string                                       `json:"unsupported,omitempty"`
	Name        string                                         `json:"name"`
	Namespace   string                                         `json:"namespace,omitempty"`
	TargetKind  string                                         `json:"target_kind"`
	TargetName  string                                         `json:"target_name"`
	MinReplicas *int32                                         `json:"min_replicas,omitempty"`
	MaxReplicas int32                                          `json:"max_replicas"`
	TargetCPU   *int32                                         `json:"target_cpu_utilization,omitempty"`
	Source      SourceReference                                `json:"source"`
}

// KubernetesResource retains safe identity metadata for an unsupported resource.
type KubernetesResource struct {
	APIVersion string          `json:"api_version"`
	Kind       string          `json:"kind"`
	Name       string          `json:"name,omitempty"`
	Namespace  string          `json:"namespace,omitempty"`
	Source     SourceReference `json:"source"`
}

// KubernetesConfiguration contains supported and preserved manifest discoveries.
type KubernetesConfiguration struct {
	Deployments          []Deployment              `json:"deployments,omitempty"`
	Services             []Service                 `json:"services,omitempty"`
	HorizontalPodScalers []HorizontalPodAutoscaler `json:"horizontal_pod_autoscalers,omitempty"`
	OtherResources       []KubernetesResource      `json:"other_resources,omitempty"`
}

// Application is the architecture discovered at one repository root.
type Application struct {
	Name         string                  `json:"name,omitempty"`
	Path         string                  `json:"path"`
	Runtimes     []Runtime               `json:"runtimes,omitempty"`
	Dependencies []Dependency            `json:"dependencies,omitempty"`
	Containers   []Container             `json:"containers,omitempty"`
	Kubernetes   KubernetesConfiguration `json:"kubernetes"`
}

// AnalysisResult is the versioned output of repository analysis.
type AnalysisResult struct {
	SchemaVersion string       `json:"schema_version"`
	Status        Status       `json:"status"`
	Supported     bool         `json:"supported"`
	Application   Application  `json:"application"`
	Findings      []Finding    `json:"findings,omitempty"`
	Diagnostics   []Diagnostic `json:"diagnostics,omitempty"`
}

// Severity communicates a finding's urgency independently from its outcome.
type Severity string

// Supported finding severities.
const (
	SeverityInfo     Severity = "info"
	SeverityLow      Severity = "low"
	SeverityMedium   Severity = "medium"
	SeverityHigh     Severity = "high"
	SeverityCritical Severity = "critical"
)

// Finding is one normalized static or runtime observation.
type Finding struct {
	ID          string           `json:"id"`
	Category    string           `json:"category"`
	Status      Status           `json:"status"`
	Severity    Severity         `json:"severity"`
	Summary     string           `json:"summary"`
	Observed    string           `json:"observed"`
	Expected    string           `json:"expected"`
	Remediation string           `json:"remediation"`
	DurationMS  int64            `json:"duration_ms"`
	Source      *SourceReference `json:"source"`
}

// FailureType classifies command execution failures.
type FailureType string

// Supported command failure classes.
const (
	FailureNone      FailureType = ""
	FailureNotFound  FailureType = "not_found"
	FailureExit      FailureType = "exit"
	FailureTimeout   FailureType = "timeout"
	FailureCanceled  FailureType = "canceled"
	FailureExecution FailureType = "execution"
)

// CommandResult is a normalized, bounded subprocess result.
type CommandResult struct {
	Command     string      `json:"command"`
	Arguments   []string    `json:"arguments,omitempty"`
	ExitCode    int         `json:"exit_code"`
	Stdout      string      `json:"stdout,omitempty"`
	Stderr      string      `json:"stderr,omitempty"`
	DurationMS  int64       `json:"duration_ms"`
	FailureType FailureType `json:"failure_type,omitempty"`
	Truncated   bool        `json:"truncated,omitempty"`
}

// DoctorCheck reports one local prerequisite result.
type DoctorCheck struct {
	Name       string `json:"name"`
	Status     Status `json:"status"`
	Version    string `json:"version,omitempty"`
	Detail     string `json:"detail,omitempty"`
	Guidance   string `json:"guidance,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

// DoctorReport is the versioned environment diagnostic output.
type DoctorReport struct {
	SchemaVersion string        `json:"schema_version"`
	Status        Status        `json:"status"`
	Checks        []DoctorCheck `json:"checks"`
}

// Measurement is one normalized observation produced by an experiment.
type Measurement struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Unit  string `json:"unit,omitempty"`
}

// Evidence records measured behavior for one verification experiment.
type Evidence struct {
	ExperimentID string        `json:"experiment_id"`
	Title        string        `json:"title"`
	Status       Status        `json:"status"`
	Summary      string        `json:"summary"`
	DurationMS   int64         `json:"duration_ms"`
	Measurements []Measurement `json:"measurements,omitempty"`
}

// ComparisonKind identifies the field compared with a baseline report.
type ComparisonKind string

// Supported baseline comparison kinds.
const (
	ComparisonStatus      ComparisonKind = "status"
	ComparisonMeasurement ComparisonKind = "measurement"
)

// ComparisonChange records one comparable change from a baseline run.
type ComparisonChange struct {
	ExperimentID string         `json:"experiment_id"`
	Kind         ComparisonKind `json:"kind"`
	Measurement  string         `json:"measurement,omitempty"`
	Baseline     string         `json:"baseline"`
	Current      string         `json:"current"`
	Unit         string         `json:"unit,omitempty"`
	Summary      string         `json:"summary"`
}

// ComparisonUnavailable explains why baseline evidence could not be compared.
type ComparisonUnavailable struct {
	ExperimentID string         `json:"experiment_id"`
	Kind         ComparisonKind `json:"kind"`
	Measurement  string         `json:"measurement,omitempty"`
	Reason       string         `json:"reason"`
}

// BaselineComparison separates relative changes from absolute run findings.
type BaselineComparison struct {
	BaselineRunID string                  `json:"baseline_run_id"`
	Status        Status                  `json:"status"`
	Regressions   []ComparisonChange      `json:"regressions,omitempty"`
	Improvements  []ComparisonChange      `json:"improvements,omitempty"`
	Unavailable   []ComparisonUnavailable `json:"unavailable,omitempty"`
}

// VerificationEnvironment identifies disposable resources created for a run.
type VerificationEnvironment struct {
	Backend     string `json:"backend"`
	ClusterName string `json:"cluster_name,omitempty"`
	Namespace   string `json:"namespace,omitempty"`
	Endpoint    string `json:"endpoint,omitempty"`
	Kept        bool   `json:"kept"`
}

// VerificationRun is the versioned result of a CloudForge verification.
type VerificationRun struct {
	Plan          *VerificationPlan       `json:"plan,omitempty"`
	Dependencies  []DependencyEvidence    `json:"dependencies,omitempty"`
	Fingerprint   *RunFingerprint         `json:"fingerprint,omitempty"`
	SchemaVersion string                  `json:"schema_version"`
	RunID         string                  `json:"run_id"`
	Status        Status                  `json:"status"`
	Application   string                  `json:"application,omitempty"`
	StartedAt     string                  `json:"started_at"`
	DurationMS    int64                   `json:"duration_ms"`
	Environment   VerificationEnvironment `json:"environment"`
	Findings      []Finding               `json:"findings,omitempty"`
	Evidence      []Evidence              `json:"evidence"`
	Diagnostics   []Diagnostic            `json:"diagnostics,omitempty"`
	Comparison    *BaselineComparison     `json:"comparison,omitempty"`
}
