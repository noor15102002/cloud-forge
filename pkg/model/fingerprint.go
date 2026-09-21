package model

// ToolVersion identifies a measured runtime tool, never its raw command output.
type ToolVersion struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// RunFingerprint separates source/image identity from experiment compatibility.
type RunFingerprint struct {
	WorkerHash        string                  `json:"worker_hash,omitempty"`
	Dependencies      []DependencyFingerprint `json:"dependencies,omitempty"`
	EnvironmentHash   string                  `json:"environment_hash,omitempty"`
	PreparationHash   string                  `json:"preparation_hash,omitempty"`
	SourceCommit      string                  `json:"source_commit,omitempty"`
	SourceDirty       bool                    `json:"source_dirty"`
	ImageID           string                  `json:"image_id,omitempty"`
	ImageDigest       string                  `json:"image_digest,omitempty"`
	CloudForgeVersion string                  `json:"cloudforge_version"`
	CloudForgeCommit  string                  `json:"cloudforge_commit"`
	Platform          string                  `json:"platform"`
	CPUs              int                     `json:"cpus"`
	Tools             []ToolVersion           `json:"tools"`
	Configuration     RuntimeConfiguration    `json:"configuration"`
	Resources         ResourceRequirements    `json:"resources"`
	Budget            SafetyBudget            `json:"budget"`
	WorkloadHash      string                  `json:"workload_hash"`
	CompatibilityKey  string                  `json:"compatibility_key,omitempty"`
}
