package model

// BuildIdentity records the verifier that produced a report, including early exits.
type BuildIdentity struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

// CompatibilityCheck distinguishes policy support from measured application results.
type CompatibilityCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}

// RuntimeCompatibility records tool policy without changing historical evidence.
type RuntimeCompatibility struct {
	Status             string               `json:"status"`
	ExpectedKubernetes string               `json:"expected_kubernetes"`
	ObservedKubernetes string               `json:"observed_kubernetes,omitempty"`
	Checks             []CompatibilityCheck `json:"checks"`
}

// TestTopology describes the effective test deployment, including defaults.
// It is evidence context, not a claim about the application's production topology.
type TestTopology struct {
	Origin                  string               `json:"origin"`
	Source                  *SourceReference     `json:"source,omitempty"`
	Replicas                int32                `json:"replicas"`
	ReplicaOrigin           string               `json:"replica_origin"`
	Strategy                string               `json:"strategy"`
	StrategyOrigin          string               `json:"strategy_origin"`
	MaxUnavailable          *string              `json:"max_unavailable"`
	MaxSurge                *string              `json:"max_surge"`
	TerminationGraceSeconds int64                `json:"termination_grace_seconds"`
	MinReadySeconds         int32                `json:"min_ready_seconds"`
	Probes                  []Probe              `json:"probes"`
	ReadinessOrigin         string               `json:"readiness_origin"`
	ReadinessPath           string               `json:"readiness_path,omitempty"`
	ReadinessScheme         string               `json:"readiness_scheme,omitempty"`
	ReadinessAcceptance     *ReadinessAcceptance `json:"readiness_acceptance,omitempty"`
}

// BaselineCheck records a bounded observation without retaining pod manifests.
type BaselineCheck struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Reason string `json:"reason"`
}

// RecoveryEvidence never replaces the experiment's original status.
type RecoveryEvidence struct {
	Status     Status          `json:"status"`
	Strategy   string          `json:"strategy"`
	Summary    string          `json:"summary"`
	DurationMS int64           `json:"duration_ms"`
	Checks     []BaselineCheck `json:"checks"`
}

// ExperimentExecution separates actual scheduling from planned capability.
type ExperimentExecution struct {
	Executed          bool `json:"executed"`
	MutationAttempted bool `json:"mutation_attempted"`
}
