package model

// RuntimeConfiguration is the explicit, bounded verification contract.
type RuntimeConfiguration struct {
	Build         *BuildSelection               `json:"build,omitempty"`
	Safety        *SafetySettings               `json:"safety,omitempty"`
	Network       *NetworkSettings              `json:"network,omitempty"`
	Preparation   *PreparationSettings          `json:"preparation,omitempty"`
	Topology      *TopologySettings             `json:"topology,omitempty"`
	Dependencies  map[string]DependencySpec     `json:"dependencies,omitempty"`
	Environment   map[string]EnvironmentBinding `json:"environment,omitempty"`
	Readiness     *ReadinessAcceptance          `json:"readiness,omitempty"`
	Worker        *WorkerSettings               `json:"worker,omitempty"`
	SchemaVersion string                        `json:"schema_version"`
	Runtime       RuntimeSettings               `json:"runtime"`
	Endpoints     EndpointSettings              `json:"endpoints,omitzero"`
	Load          LoadSettings                  `json:"load,omitzero"`
	Experiments   ExperimentSettings            `json:"experiments,omitzero"`
}

// SafetySettings selects a qualified fixed budget, never arbitrary host limits.
type SafetySettings struct {
	Profile string `json:"profile"`
}

// NetworkSettings restricts application and preparation traffic to declared providers.
type NetworkSettings struct {
	Outbound string `json:"outbound"`
}

// PreparationSettings runs one bounded command in image A before application startup.
// The command is omitted from reports; its hash remains part of compatibility.
type PreparationSettings struct {
	Command []string `json:"command"`
	Timeout string   `json:"timeout,omitempty"`
}

// TopologySettings replaces only the replica count and rollout policy for a
// bounded test. It never describes the application's production topology.
type TopologySettings struct {
	Replicas int32            `json:"replicas"`
	Rollout  *RolloutSettings `json:"rollout,omitempty"`
}

// RolloutSettings accepts integer counts only. Omitted fields default to
// rolling_update, zero unavailable pods and one surge pod.
type RolloutSettings struct {
	Strategy       string `json:"strategy,omitempty"`
	MaxUnavailable *int32 `json:"max_unavailable,omitempty"`
	MaxSurge       *int32 `json:"max_surge,omitempty"`
}

// BuildSelection uses repository-relative paths, never shell expressions or URLs.
// App selects metadata; Context selects Docker COPY inputs; Dockerfile selects
// the one image definition used throughout verification.
type BuildSelection struct {
	App        string `json:"app"`
	Dockerfile string `json:"dockerfile"`
	Context    string `json:"context"`
}

// RuntimeSettings selects the application port; replica/resource bounds are enforced independently.
type RuntimeSettings struct {
	Kind string `json:"kind,omitempty"`
	Port int32  `json:"port"`
}

// WorkerSettings selects one direct image command and an isolated Redis heartbeat.
// Command and key values are omitted from public reports and hashed for comparison.
type WorkerSettings struct {
	Command   []string               `json:"command"`
	Heartbeat RedisHeartbeatSettings `json:"heartbeat"`
}

// RedisHeartbeatSettings is a bounded process-liveness contract, never a job assertion.
type RedisHeartbeatSettings struct {
	Key            string `json:"key"`
	TimestampField string `json:"timestamp_field"`
	MaxAge         string `json:"max_age"`
}

// EndpointSettings contains application-relative paths, never external URLs.
type EndpointSettings struct {
	Health    string `json:"health"`
	Readiness string `json:"readiness"`
	Load      string `json:"load"`
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
