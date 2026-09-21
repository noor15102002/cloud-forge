package model

// WorkerContract explains the bounded process-liveness observation without
// revealing its Redis key, payload or direct executable arguments.
type WorkerContract struct {
	Kind          string `json:"kind"`
	Claim         string `json:"claim"`
	ContractHash  string `json:"contract_hash"`
	MaxAge        string `json:"max_age"`
	MaxTTL        string `json:"max_ttl"`
	RequiredBeats int    `json:"required_advancing_samples"`
}

// HeartbeatSample retains only normalized timing, never arbitrary Redis content.
type HeartbeatSample struct {
	RedisTime string `json:"redis_time"`
	Timestamp string `json:"timestamp"`
	AgeMS     int64  `json:"age_ms"`
	TTLMS     int64  `json:"ttl_ms"`
}

// WorkerObservation records the single-writer boundary and observed progress.
// Pod identity is Kubernetes UID, never a process ID supplied by the application.
type WorkerObservation struct {
	RedisObservedAt       string            `json:"redis_observed_at,omitempty"`
	ProcessStartedAt      string            `json:"process_started_at,omitempty"`
	PodUID                string            `json:"pod_uid,omitempty"`
	PreviousPodUID        string            `json:"previous_pod_uid,omitempty"`
	Image                 string            `json:"image,omitempty"`
	RunningPods           int               `json:"running_pods"`
	MaximumRunningPods    int               `json:"maximum_running_pods"`
	ContainerRestarts     int32             `json:"container_restarts"`
	KeyAbsentBeforeStart  bool              `json:"key_absent_before_start"`
	PredecessorTerminated bool              `json:"predecessor_terminated"`
	PreviousHeartbeatGone bool              `json:"previous_heartbeat_gone"`
	HeartbeatState        string            `json:"heartbeat_state"`
	Samples               []HeartbeatSample `json:"samples"`
}
