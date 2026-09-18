package model

// RuntimeConfiguration is the explicit, bounded verification contract.
type RuntimeConfiguration struct {
	SchemaVersion string             `json:"schema_version"`
	Runtime       RuntimeSettings    `json:"runtime"`
	Endpoints     EndpointSettings   `json:"endpoints"`
	Load          LoadSettings       `json:"load"`
	Experiments   ExperimentSettings `json:"experiments"`
}

// RuntimeSettings selects the application port; replica/resource bounds are enforced independently.
type RuntimeSettings struct {
	Port int32 `json:"port"`
}

// EndpointSettings contains application-relative paths, never external URLs.
type EndpointSettings struct {
	Health    string `json:"health,omitempty"`
	Readiness string `json:"readiness,omitempty"`
	Load      string `json:"load,omitempty"`
}

// LoadSettings bounds the generated GET workload.
type LoadSettings struct {
	VUs      int    `json:"vus"`
	Duration string `json:"duration"`
}

// ExperimentSettings opts into the documented instrumentation protocol.
type ExperimentSettings struct {
	ControlPath string `json:"control_path,omitempty"`
}

// SafetyBudget records enforced bounds, including temporary rollout capacity.
type SafetyBudget struct {
	MaxReplicas       int32  `json:"max_replicas"`
	WorkloadCPU       string `json:"workload_cpu"`
	WorkloadMemory    string `json:"workload_memory"`
	ClusterMemory     string `json:"cluster_memory"`
	BuildMemory       string `json:"build_memory"`
	BuildCPUs         int    `json:"build_cpus"`
	BuildTimeout      string `json:"build_timeout"`
	ReadinessTimeout  string `json:"readiness_timeout"`
	ExperimentTimeout string `json:"experiment_timeout"`
}
