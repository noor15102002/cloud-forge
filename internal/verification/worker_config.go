package verification

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/noor15102002/cloud-forge/pkg/model"
)

var workerAgePattern = regexp.MustCompile(`^(?:[1-9]|[1-5][0-9]|60)s$|^1m$`)

var workerKeyPattern = regexp.MustCompile(`^[A-Za-z0-9:_./-]{1,128}$`)

func advancedConfiguration(config model.RuntimeConfiguration) bool {
	return config.SchemaVersion == "v1alpha5" || config.SchemaVersion == "v1alpha6" || config.SchemaVersion == "v1alpha7"
}
func isWorker(config model.RuntimeConfiguration) bool { return config.Runtime.Kind == "worker" }

func validateWorker(config model.RuntimeConfiguration) error {
	if config.Runtime.Kind != "" && config.Runtime.Kind != "http" && config.Runtime.Kind != "worker" {
		return errors.New("runtime.kind must be http or worker")
	}
	if (config.Runtime.Kind != "" || config.Worker != nil) && config.SchemaVersion != "v1alpha6" && config.SchemaVersion != "v1alpha7" {
		return errors.New("explicit runtime.kind and worker settings require configuration schema_version v1alpha6 or v1alpha7")
	}
	if !isWorker(config) {
		if config.Worker != nil {
			return errors.New("worker settings require runtime.kind worker")
		}
		return nil
	}
	if config.Worker == nil {
		return errors.New("worker runtime requires an explicit command and heartbeat contract")
	}
	if config.Runtime.Port != 0 || config.Readiness != nil || config.Probes != nil || config.Endpoints != (model.EndpointSettings{}) || config.Experiments.ControlPath != "" {
		return errors.New("worker runtime cannot include a port, HTTP readiness, load endpoint or control protocol")
	}
	if !config.Dependencies["redis"].Enabled || config.Network == nil || config.Network.Outbound != "declared_dependencies_only" {
		return errors.New("worker heartbeat requires the run-owned Redis provider and restricted outbound networking")
	}
	if config.Topology == nil || config.Topology.Replicas != 1 || config.Topology.Rollout == nil || config.Topology.Rollout.Strategy != "recreate" || config.Topology.Rollout.MaxSurge != nil || config.Topology.Rollout.MaxUnavailable != nil {
		return errors.New("worker test topology requires exactly one replica and explicit recreate without surge or unavailable settings")
	}
	if err := validatePreparation(model.PreparationSettings{Command: config.Worker.Command}); err != nil {
		return errors.New(strings.ReplaceAll(err.Error(), "preparation", "worker"))
	}
	heartbeat := config.Worker.Heartbeat
	if !workerKeyPattern.MatchString(heartbeat.Key) || !assertionKeyPattern.MatchString(heartbeat.TimestampField) {
		return errors.New("worker heartbeat requires a bounded plain Redis key and a flat timestamp property name")
	}
	age, err := time.ParseDuration(heartbeat.MaxAge)
	if !workerAgePattern.MatchString(heartbeat.MaxAge) || err != nil || age < time.Second || age > time.Minute || age%time.Second != 0 {
		return errors.New("worker heartbeat max_age must use 1s through 60s or 1m")
	}
	return nil
}

func workerContract(config model.RuntimeConfiguration) *model.WorkerContract {
	if config.Worker == nil {
		return nil
	}
	encoded, _ := json.Marshal(config.Worker)
	return &model.WorkerContract{Kind: "redis_json_heartbeat", Claim: "process_liveness_only", ContractHash: hashBytes(encoded), MaxAge: config.Worker.Heartbeat.MaxAge, MaxTTL: "1m", RequiredBeats: 2}
}
