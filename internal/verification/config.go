package verification

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/noor15102002/cloud-forge/internal/safefile"
	"github.com/noor15102002/cloud-forge/pkg/model"
	"sigs.k8s.io/yaml"
)

func defaultConfiguration() model.RuntimeConfiguration {
	return model.RuntimeConfiguration{SchemaVersion: "v1alpha1", Load: model.LoadSettings{VUs: 5, Duration: "20s"}}
}

func loadConfiguration(root, explicit string) (model.RuntimeConfiguration, error) {
	config := defaultConfiguration()
	path := explicit
	if path == "" {
		path = filepath.Join(root, "cloudforge.yaml")
	}
	data, err := safefile.Read(filepath.Dir(path), filepath.Base(path), 64<<10)
	if err != nil {
		if explicit == "" && errors.Is(err, os.ErrNotExist) {
			return config, nil
		}
		return config, fmt.Errorf("read verification configuration: %w", err)
	}
	// UnmarshalStrict rejects duplicate keys and unknown fields. Error text is not
	// echoed because malformed user configuration may contain sensitive values.
	config.SchemaVersion = ""
	if err := yaml.UnmarshalStrict(data, &config); err != nil {
		return config, errors.New("invalid verification configuration: use the documented fields and types without duplicate keys")
	}
	var supplied map[string]json.RawMessage
	if yaml.UnmarshalStrict(data, &supplied) != nil {
		return config, errors.New("invalid configuration object")
	}
	for _, key := range []string{"runtime", "load", "endpoints", "experiments"} {
		if string(supplied[key]) == "null" {
			return config, errors.New("configuration sections cannot be null")
		}
	}
	var runtimeFields map[string]json.RawMessage
	_ = json.Unmarshal(supplied["runtime"], &runtimeFields)
	if _, present := runtimeFields["port"]; present && config.Runtime.Port == 0 {
		return config, errors.New("explicit runtime.port must be between 1 and 65535")
	}
	if err := validateConfiguration(config); err != nil {
		return config, err
	}
	return config, nil
}

func validateConfiguration(config model.RuntimeConfiguration) error {
	if config.SchemaVersion != "v1alpha1" {
		return errors.New("configuration schema_version must be v1alpha1")
	}
	if config.Runtime.Port < 0 || config.Runtime.Port > 65535 {
		return errors.New("runtime.port must be between 1 and 65535 when supplied")
	}
	if config.Load.VUs < 1 || config.Load.VUs > 32 {
		return errors.New("load.vus must be between 1 and 32")
	}
	duration, err := time.ParseDuration(config.Load.Duration)
	if err != nil || duration < time.Second || duration > time.Minute {
		return errors.New("load.duration must be between 1s and 1m")
	}
	for _, path := range []string{config.Endpoints.Health, config.Endpoints.Readiness, config.Endpoints.Load, config.Experiments.ControlPath} {
		if path == "" {
			continue
		}
		parsed, err := url.ParseRequestURI(path)
		if err != nil || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || parsed.IsAbs() || parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" || strings.Contains(path, "\\") || len(path) > 256 {
			return errors.New("endpoint paths must be application-relative paths without queries, fragments or external hosts (maximum 256 bytes)")
		}
	}
	return nil
}

func safetyBudget() model.SafetyBudget {
	return model.SafetyBudget{MaxReplicas: 5, WorkloadCPU: "4", WorkloadMemory: "2Gi", ClusterMemory: "4g", BuildMemory: "2g", BuildCPUs: 2, BuildTimeout: "10m", ReadinessTimeout: "2m", ExperimentTimeout: "2m"}
}
