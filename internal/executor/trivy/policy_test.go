package trivy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

func TestScannerPolicyIsolatesActualSubprocessFromAmbientSettings(t *testing.T) {
	ambient := t.TempDir()
	t.Chdir(ambient)
	for name, data := range map[string]string{
		"trivy.yaml":   "severity: [CRITICAL]\nvulnerability: {ignore-unfixed: true}\n",
		".trivyignore": "CVE-2026-0001\n",
		"ignore.rego":  "package trivy\nignore = true\n",
	} {
		if err := os.WriteFile(filepath.Join(ambient, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name, value := range map[string]string{
		"TRIVY_CONFIG": filepath.Join(ambient, "trivy.yaml"), "TRIVY_SEVERITY": "CRITICAL",
		"TRIVY_IGNOREFILE": filepath.Join(ambient, ".trivyignore"), "TRIVY_IGNORE_POLICY": filepath.Join(ambient, "ignore.rego"),
		"TRIVY_IGNORE_UNFIXED": "true", "TRIVY_IGNORE_STATUS": "affected", "TRIVY_SCANNERS": "secret",
		"TRIVY_SKIP_FILES": "*", "TRIVY_FUTURE_FILTER": "hide-all", "TRIVY_CACHE_DIR": ambient,
		"DOCKER_HOST": "ssh://private.invalid", "DOCKER_CONFIG": ambient, "DOCKER_CONTEXT": "private-context",
		"HOME": ambient, "XDG_CONFIG_HOME": ambient, "XDG_CACHE_HOME": ambient,
		"UNRELATED_PRIVATE_TOKEN": "not-for-scanner", "GITHUB_TOKEN": "not-for-scanner",
		"HTTPS_PROXY": "https://transport.invalid", "SSL_CERT_FILE": filepath.Join(ambient, "certificate.pem"),
	} {
		t.Setenv(name, value)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var directories []string
	runner := scanRunnerFunc(func(ctx context.Context, req command.Request) model.CommandResult {
		if !req.ClearEnv {
			t.Fatal("scanner did not request a controlled environment")
		}
		directories = append(directories, req.Dir)
		// Equivalent to the scoped runtime runner: approved endpoint pins are
		// added after the scanner's allowlist, and survive the shared runner.
		req.Env = append(req.Env, "DOCKER_HOST=unix:///var/run/docker.sock", "DOCKER_CONFIG="+filepath.Join(req.Dir, "docker"), "DOCKER_CONTEXT=")
		req.Name = executable
		req.Args = append([]string{"-test.run=^TestScannerPolicyHelper$", "--", "--scanner-policy-helper"}, req.Args...)
		return (command.ExecRunner{}).Run(ctx, req)
	})
	scan, err := New(runner).ScanImage(context.Background(), "example:test", testImageID)
	if err != nil || scan.Command.FailureType != model.FailureNone || len(scan.Findings) != 2 {
		t.Fatalf("controlled subprocess lost known LOW/unfixed finding: error=%v failure=%s findings=%d", err, scan.Command.FailureType, len(scan.Findings))
	}
	if scan.Findings[0].Severity != model.SeverityLow || !slices.ContainsFunc(scan.Measurements, func(m model.Measurement) bool { return m.Name == "scan_policy" && m.Value == scanPolicyID }) {
		t.Fatal("effective policy or known finding missing")
	}
	version := Version(context.Background(), runner)
	if version.FailureType != model.FailureNone || version.Stdout != "Version: 0.74.0\n" {
		t.Fatal("version probe inherited ambient scanner configuration")
	}
	for _, directory := range directories {
		if _, err := os.Stat(directory); !os.IsNotExist(err) {
			t.Fatal("private scanner files retained after subprocess completed")
		}
	}
	for _, measurement := range scan.Measurements {
		if strings.Contains(measurement.Value, ambient) || strings.Contains(measurement.Value, "private") && measurement.Name != "scan_cache" || strings.Contains(measurement.Value, "transport.invalid") {
			t.Fatal("ambient value retained in evidence")
		}
	}
}

// This is an executable boundary probe, not a replacement vulnerability scanner:
// it rejects any leaked setting and emits fixed LOW/unfixed and CRITICAL records.
// Runtime qualification additionally tests the pinned real Trivy implementation.
func TestScannerPolicyHelper(_ *testing.T) {
	marker := slices.Index(os.Args, "--scanner-policy-helper")
	if marker < 0 {
		return
	}
	args := os.Args[marker+1:]
	require := func(ok bool) {
		if !ok {
			fmt.Fprintln(os.Stderr, "scanner isolation boundary was not maintained")
			os.Exit(9)
		}
	}
	for _, env := range os.Environ() {
		name, _, _ := strings.Cut(env, "=")
		require(!strings.HasPrefix(name, "TRIVY_") && name != "UNRELATED_PRIVATE_TOKEN" && name != "GITHUB_TOKEN")
	}
	directory, err := os.Getwd()
	require(err == nil && strings.HasPrefix(filepath.Base(directory), "cloudforge-scan-"))
	info, err := os.Stat(directory)
	require(err == nil && info.Mode().Perm() == 0o700)
	require(os.Getenv("HOME") == filepath.Join(directory, "home"))
	require(os.Getenv("XDG_CONFIG_HOME") == filepath.Join(directory, "config"))
	require(os.Getenv("XDG_CACHE_HOME") == filepath.Join(directory, "cache"))
	require(os.Getenv("DOCKER_HOST") == "unix:///var/run/docker.sock" && os.Getenv("DOCKER_CONTEXT") == "")
	require(os.Getenv("DOCKER_CONFIG") == filepath.Join(directory, "docker"))
	require(os.Getenv("HTTPS_PROXY") == "https://transport.invalid")
	require(strings.HasSuffix(os.Getenv("SSL_CERT_FILE"), "certificate.pem"))
	argument := func(flag string) string {
		index := slices.Index(args, flag)
		require(index >= 0 && index+1 < len(args))
		return args[index+1]
	}
	config := argument("--config")
	require(config == filepath.Join(directory, "trivy.yaml"))
	// #nosec G304 -- the path must equal the private policy path checked above.
	data, err := os.ReadFile(config)
	require(err == nil && string(data) == "{}\n")
	if slices.Contains(args, "--version") {
		fmt.Println("Version: 0.74.0")
		os.Exit(0)
	}
	require(argument("--scanners") == "vuln" && argument("--severity") == scanSeverities)
	require(slices.Contains(args, "--ignore-unfixed=false") && argument("--image-src") == "docker")
	require(argument("--cache-dir") == filepath.Join(directory, "cache"))
	ignore := argument("--ignorefile")
	require(ignore == filepath.Join(directory, ".trivyignore"))
	// #nosec G304 -- the path must equal the private policy path checked above.
	data, err = os.ReadFile(ignore)
	require(err == nil && len(data) == 0)
	fmt.Println(envelope(findingsResult))
	os.Exit(0)
}

func TestScannerPolicyCleanupPreservesFailures(t *testing.T) {
	for _, failure := range []model.FailureType{model.FailureExit, model.FailureTimeout, model.FailureCanceled} {
		t.Run(string(failure), func(t *testing.T) {
			var directory string
			runner := scanRunnerFunc(func(_ context.Context, req command.Request) model.CommandResult {
				directory = req.Dir
				return model.CommandResult{ExitCode: -1, FailureType: failure}
			})
			scan, err := New(runner).ScanImage(context.Background(), "example:test", testImageID)
			if err != nil || scan.Command.FailureType != failure || len(scan.Findings) != 0 || !reflect.DeepEqual(scan.Measurements, policyMeasurements()) {
				t.Fatal("failed scan lost its failure/policy or fabricated a finding")
			}
			if _, err := os.Stat(directory); !os.IsNotExist(err) {
				t.Fatal("private scanner files survived a failed invocation")
			}
		})
	}
}
