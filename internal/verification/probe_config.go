package verification

import (
	"errors"
	"time"

	"github.com/noor15102002/cloud-forge/pkg/model"
)

func validateProbes(config model.RuntimeConfiguration) error {
	if config.Probes == nil {
		return nil
	}
	if config.SchemaVersion != "v1alpha7" || isWorker(config) {
		return errors.New("probes require HTTP configuration schema_version v1alpha7")
	}
	interval, err := time.ParseDuration(config.Probes.Interval)
	if err != nil || len(config.Probes.Interval) > 32 || interval < 20*time.Millisecond || interval > 5*time.Second || interval%time.Millisecond != 0 {
		return errors.New("probes.interval must be a whole number of milliseconds between 20ms and 5s")
	}
	return nil
}

func effectiveProbes(value *model.ProbeSettings) *model.ProbeSettings {
	if value == nil {
		return nil
	}
	result := *value
	if interval, err := time.ParseDuration(value.Interval); err == nil {
		result.Interval = interval.String()
	}
	return &result
}
