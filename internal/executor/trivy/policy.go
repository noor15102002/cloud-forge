package trivy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

const scanPolicyID = "cloudforge-default-v1"
const scanSeverities = "UNKNOWN,LOW,MEDIUM,HIGH,CRITICAL"

// Version observes the scanner version under the same environment/configuration
// boundary as image scanning. It performs no database update or image scan.
func Version(ctx context.Context, runner command.Runner) (result model.CommandResult) {
	policy, err := newExecutionPolicy()
	if err != nil {
		return model.CommandResult{Command: "trivy", ExitCode: -1, FailureType: model.FailureExecution}
	}
	defer func() {
		if policy.remove() != nil {
			result.ExitCode = -1
			result.FailureType = model.FailureExecution
		}
	}()
	return runner.Run(ctx, command.Request{
		Name: "trivy", Args: []string{"--version", "--config", policy.config},
		Dir: policy.directory, Env: policy.env, ClearEnv: true,
		Timeout: 10 * time.Second, OutputLimit: 16 * 1024,
	})
}

// Empty configuration and ignore files are deliberate. Together with a fresh
// working directory/home/cache and an allowlisted environment, they exclude
// present and future ambient Trivy policy settings rather than enumerating only
// the settings known today. Supported Trivy versions remain compatibility-gated.
type executionPolicy struct {
	directory string
	config    string
	ignore    string
	cache     string
	env       []string
}

func newExecutionPolicy() (executionPolicy, error) {
	directory, err := os.MkdirTemp("", "cloudforge-scan-")
	if err != nil {
		return executionPolicy{}, fmt.Errorf("private scanner execution directory could not be created")
	}
	p := executionPolicy{
		directory: directory,
		config:    filepath.Join(directory, "trivy.yaml"),
		ignore:    filepath.Join(directory, ".trivyignore"),
		cache:     filepath.Join(directory, "cache"),
	}
	fail := func() (executionPolicy, error) {
		if cleanupErr := p.remove(); cleanupErr != nil {
			return executionPolicy{}, cleanupErr
		}
		return executionPolicy{}, fmt.Errorf("private scanner execution files could not be prepared")
	}
	for _, name := range []string{"home", "config", "cache", "temp"} {
		if err := os.Mkdir(filepath.Join(directory, name), 0o700); err != nil {
			return fail()
		}
	}
	if err := os.WriteFile(p.config, []byte("{}\n"), 0o600); err != nil {
		return fail()
	}
	if err := os.WriteFile(p.ignore, nil, 0o600); err != nil {
		return fail()
	}
	// These settings affect executable lookup and authenticated network transport,
	// not scan filters. Their values are never retained in evidence. Docker
	// endpoint/configuration is supplied afterwards by the scoped runtime runner.
	for _, name := range []string{"PATH", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
		if value, ok := os.LookupEnv(name); ok {
			p.env = append(p.env, name+"="+value)
		}
	}
	p.env = append(p.env,
		"HOME="+filepath.Join(directory, "home"),
		"XDG_CONFIG_HOME="+filepath.Join(directory, "config"),
		"XDG_CACHE_HOME="+p.cache,
		"TMPDIR="+filepath.Join(directory, "temp"),
	)
	return p, nil
}

func (p executionPolicy) remove() error {
	if err := os.RemoveAll(p.directory); err != nil {
		return fmt.Errorf("private scanner execution files could not be removed")
	}
	return nil
}

func policyMeasurements() []model.Measurement {
	return []model.Measurement{
		{Name: "scan_policy", Value: scanPolicyID},
		{Name: "scan_scanners", Value: "vuln"},
		{Name: "scan_severities", Value: scanSeverities},
		{Name: "scan_ignore_policy", Value: "none"},
		{Name: "scan_include_unfixed", Value: "true"},
		{Name: "scan_ambient_configuration", Value: "disabled"},
		{Name: "scan_ambient_ignore_files", Value: "disabled"},
		{Name: "scan_cache", Value: "private_fresh"},
		{Name: "scan_image_source", Value: "docker"},
	}
}
