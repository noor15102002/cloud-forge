package verification

import (
	"errors"
	"path"
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
	advanced := config.SchemaVersion == "v1alpha5"
	restricted := config.Network != nil && config.Network.Outbound == "declared_dependencies_only"
	if config.Safety != nil && (!advanced || config.Safety.Profile != "bounded_backend") {
		return errors.New("safety.profile requires v1alpha5 and the fixed bounded_backend profile")
	}
	if config.Network != nil && (!advanced || !restricted) {
		return errors.New("network.outbound requires v1alpha5 and declared_dependencies_only")
	}
	if config.Preparation != nil {
		if !advanced || !restricted {
			return errors.New("preparation requires v1alpha5 and restricted outbound networking")
		}
		if err := validatePreparation(*config.Preparation); err != nil {
			return err
		}
	}
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
		if spec.Enabled && (name == "postgresql" || name == "clamav") && (!advanced || !restricted) {
			return errors.New("backend dependencies require v1alpha5 and restricted outbound networking")
		}
		if spec.Enabled && name == "clamav" && !backendProfile(config) {
			return errors.New("ClamAV requires the explicit bounded_backend safety profile")
		}
		if spec.StartupTimeout != "" {
			d, err := time.ParseDuration(spec.StartupTimeout)
			maximum := 2 * time.Minute
			if name == "clamav" {
				maximum = 10 * time.Minute
			}
			if err != nil || d < time.Second || d > maximum {
				return errors.New("dependency startup_timeout must be between 1s and 2m (10m for ClamAV)")
			}
		}
	}
	for name, binding := range config.Environment {
		if !environmentNamePattern.MatchString(name) {
			return errors.New("invalid environment variable name")
		}
		choices := 0
		if binding.From != "" {
			choices++
		}
		if binding.Value != nil {
			choices++
		}
		if binding.Generate != nil {
			choices++
		}
		if choices != 1 {
			return errors.New("each environment binding requires exactly one of from, value or generate")
		}
		if binding.Generate != nil {
			g := binding.Generate
			if !advanced || !restricted || g.Bytes < 16 || g.Bytes > 64 || (g.Encoding != "hex" && g.Encoding != "base64") {
				return errors.New("generated test values require v1alpha5, restricted networking, 16–64 bytes and hex or base64 encoding")
			}
		} else if binding.From != "" {
			provider := map[string]string{"dependency.redis.url": "redis", "dependency.postgresql.url": "postgresql", "dependency.clamav.host": "clamav", "dependency.clamav.port": "clamav"}[binding.From]
			disabled := binding.From == "disabled.http_url" || binding.From == "disabled.https_url" || binding.From == "disabled.host" || binding.From == "disabled.email"
			if (provider == "" && !disabled) || (provider != "" && !config.Dependencies[provider].Enabled) {
				return errors.New("environment binding requires an enabled supported dependency endpoint")
			}
			if binding.From != "dependency.redis.url" && (!advanced || !restricted) {
				return errors.New("backend and disabled endpoint bindings require v1alpha5 and restricted outbound networking")
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
	if config.Preparation != nil {
		settings := *config.Preparation
		settings.Command = []string{"<omitted>"}
		result.Preparation = &settings
	}
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

func validatePreparation(settings model.PreparationSettings) error {
	if len(settings.Command) < 1 || len(settings.Command) > 16 {
		return errors.New("preparation.command requires 1–16 direct arguments")
	}
	for _, argument := range settings.Command {
		if argument == "" || len(argument) > 256 || strings.ContainsAny(argument, "\x00\n\r") {
			return errors.New("preparation arguments must contain 1–256 bytes without control line breaks")
		}
		if argument == "-e" || argument == "--eval" || argument == "-c" || argument == "--command" {
			return errors.New("preparation accepts direct executable arguments, not inline scripts")
		}
	}
	switch path.Base(settings.Command[0]) {
	case "sh", "bash", "dash", "zsh", "env", "busybox", "sudo":
		return errors.New("preparation shell wrappers are unsupported")
	}
	if settings.Timeout != "" {
		d, err := time.ParseDuration(settings.Timeout)
		if err != nil || d < time.Second || d > 5*time.Minute || d%time.Second != 0 {
			return errors.New("preparation.timeout must be whole seconds between 1s and 5m")
		}
	}
	return nil
}
