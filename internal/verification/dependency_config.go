package verification

import (
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/noor15102002/cloud-forge/pkg/model"
)

var dependencyNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
var environmentNamePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
var testLiteralPattern = regexp.MustCompile(`^[a-zA-Z0-9_. -]{0,64}$`)
var assertionKeyPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,63}$`)

func validateExtensions(config model.RuntimeConfiguration) error {
	if config.SchemaVersion == model.SchemaVersion && (len(config.Dependencies) > 0 || len(config.Environment) > 0 || config.Readiness != nil) {
		return errors.New("dependency, environment and readiness extensions require schema_version v1alpha2")
	}
	if len(config.Dependencies) > 8 || len(config.Environment) > 32 {
		return errors.New("configuration exceeds dependency/environment entry limits")
	}
	for name, spec := range config.Dependencies {
		if !dependencyNamePattern.MatchString(name) {
			return errors.New("invalid dependency name")
		}
		if spec.StartupTimeout != "" {
			d, err := time.ParseDuration(spec.StartupTimeout)
			if err != nil || d < time.Second || d > 2*time.Minute {
				return errors.New("dependency startup_timeout must be between 1s and 2m")
			}
		}
	}
	for name, binding := range config.Environment {
		if !environmentNamePattern.MatchString(name) {
			return errors.New("invalid environment variable name")
		}
		if (binding.From == "") == (binding.Value == nil) {
			return errors.New("each environment binding requires exactly one of from or value")
		}
		if binding.From != "" {
			if binding.From != "dependency.redis.url" || !config.Dependencies["redis"].Enabled {
				return errors.New("environment binding requires an enabled supported dependency endpoint")
			}
		} else {
			if !testLiteralPattern.MatchString(*binding.Value) {
				return errors.New("test literals must contain at most 64 plain letters, digits, spaces, underscores, dots or hyphens")
			}
			for _, part := range []string{"KEY", "TOKEN", "SECRET", "PASSWORD", "CREDENTIAL"} {
				if strings.Contains(name, part) {
					return errors.New("credential-like environment literals are not supported")
				}
			}
			for _, part := range []string{"URL", "URI", "HOST", "ENDPOINT"} {
				if strings.Contains(name, part) && *binding.Value != "" {
					return errors.New("connection settings must use generated dependency bindings or an empty disabled value")
				}
			}
		}
	}
	if config.Readiness != nil {
		if config.Readiness.Status < 200 || config.Readiness.Status > 299 {
			return errors.New("readiness.status must be a successful HTTP status (200–299)")
		}
		if len(config.Readiness.JSON) > 16 {
			return errors.New("readiness supports at most 16 flat JSON assertions")
		}
		for key, value := range config.Readiness.JSON {
			if !assertionKeyPattern.MatchString(key) || !testLiteralPattern.MatchString(value) {
				return errors.New("readiness JSON assertions require simple property names and bounded string literals")
			}
		}
	}
	return nil
}

// safeConfiguration deliberately omits even user-declared harmless literal values.
func safeConfiguration(config model.RuntimeConfiguration) model.RuntimeConfiguration {
	result := config
	result.Environment = make(map[string]model.EnvironmentBinding, len(config.Environment))
	for name, binding := range config.Environment {
		if binding.Value != nil {
			omitted := "<omitted>"
			binding.Value = &omitted
		}
		result.Environment[name] = binding
	}
	return result
}
