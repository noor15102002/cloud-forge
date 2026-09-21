package model

// VerificationSchemaVersion adds isolated backend dependencies and preparation.
// Analysis and legacy reports retain their original v1alpha1 contract.
const VerificationSchemaVersion = "v1alpha6"

// DependencySpec explicitly opts a dependency into the isolated test run.
type DependencySpec struct {
	Enabled        bool   `json:"enabled"`
	StartupTimeout string `json:"startup_timeout,omitempty"`
}

// EnvironmentBinding selects a generated endpoint or an explicit test literal.
// Literal values are removed before configuration enters any public report.
type EnvironmentBinding struct {
	From     string              `json:"from,omitempty"`
	Value    *string             `json:"value,omitempty"`
	Generate *GeneratedValueSpec `json:"generate,omitempty"`
}

// GeneratedValueSpec describes run-local random test material, never its value.
type GeneratedValueSpec struct {
	Bytes    int    `json:"bytes"`
	Encoding string `json:"encoding"`
}

// ReadinessAcceptance checks status and flat string-valued JSON properties.
type ReadinessAcceptance struct {
	Status int               `json:"status"`
	JSON   map[string]string `json:"json,omitempty"`
}

// Capability records intent before execution; supported is never an observed pass.
type Capability struct {
	Prerequisites    []string `json:"prerequisites,omitempty"`
	Mutation         string   `json:"mutation,omitempty"`
	RecoveryStrategy string   `json:"recovery_strategy,omitempty"`
	Limitations      []string `json:"limitations,omitempty"`
	Name             string   `json:"name"`
	Disposition      string   `json:"disposition"`
	Reason           string   `json:"reason"`
}

// VerificationPlan exposes safe execution intent without generated credentials.
type VerificationPlan struct {
	Build         *BuildSelection `json:"build,omitempty"`
	Detected      []string        `json:"detected,omitempty"`
	Topology      *TestTopology   `json:"topology,omitempty"`
	Limitations   []string        `json:"limitations,omitempty"`
	SchemaVersion string          `json:"schema_version"`
	Status        Status          `json:"status"`
	Port          int32           `json:"port,omitempty"`
	Capabilities  []Capability    `json:"capabilities"`
	Budget        SafetyBudget    `json:"budget"`
}

// DependencyEvidence keeps dependency infrastructure separate from application failures.
type DependencyEvidence struct {
	DataVersion       string               `json:"data_version,omitempty"`
	DataTimestamp     string               `json:"data_timestamp,omitempty"`
	Name              string               `json:"name"`
	Kind              string               `json:"kind"`
	Image             string               `json:"image"`
	Digest            string               `json:"digest,omitempty"`
	Version           string               `json:"version"`
	Resources         ResourceRequirements `json:"resources"`
	ConfigurationMode string               `json:"configuration_mode"`
	NetworkExposure   string               `json:"network_exposure"`
	Authentication    string               `json:"authentication"`
	Status            Status               `json:"status"`
	StartupMS         int64                `json:"startup_ms"`
	Reason            string               `json:"reason"`
}

// DependencyFingerprint excludes timings, run identities and endpoint values.
type DependencyFingerprint struct {
	DataVersion       string               `json:"data_version,omitempty"`
	DataTimestamp     string               `json:"data_timestamp,omitempty"`
	Kind              string               `json:"kind"`
	Image             string               `json:"image"`
	Digest            string               `json:"digest"`
	Version           string               `json:"version"`
	Resources         ResourceRequirements `json:"resources"`
	ConfigurationMode string               `json:"configuration_mode"`
}
