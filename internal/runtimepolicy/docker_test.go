package runtimepolicy

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

type testRunner func(context.Context, command.Request) model.CommandResult

func (f testRunner) Run(ctx context.Context, req command.Request) model.CommandResult {
	return f(ctx, req)
}
func TestDockerResolutionHonorsSelectionPrecedenceAndRejectsUnsupported(t *testing.T) {
	for _, tc := range []struct {
		name, host, context, observed, status, origin string
		inspect                                       bool
	}{
		{"configured-local", "", "", `"unix:///var/run/docker.sock"`, "supported", "configured_context", true},
		{"configured-remote", "", "", `"ssh://PRIVATE_USER@PRIVATE_HOST"`, "unsupported", "configured_context", true},
		{"environment-local", DockerEndpoint, "", "", "supported", "environment_host", false},
		{"environment-remote", "tcp://PRIVATE_HOST:2376", "", "", "unsupported", "environment_host", false},
		{"nondefault-socket", "unix:///private/rootless.sock", "", "", "unsupported", "environment_host", false},
		{"context-wins-over-remote-host", "ssh://PRIVATE_HOST", "PRIVATE_CONTEXT", `"unix:///var/run/docker.sock"`, "supported", "environment_context", true},
		{"context-wins-over-local-host", DockerEndpoint, "PRIVATE_CONTEXT", `"ssh://PRIVATE_HOST"`, "unsupported", "environment_context", true},
		{"default-context-keeps-host", "ssh://PRIVATE_HOST", "default", "", "unsupported", "environment_context", false},
		{"malformed-context", "", "PRIVATE_CONTEXT", `PRIVATE_ERROR`, "not_validated", "environment_context", true},
		{"empty-context", "", "", `""`, "not_validated", "configured_context", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			runner := testRunner(func(_ context.Context, req command.Request) model.CommandResult {
				calls++
				expected := "context inspect --format {{json .Endpoints.docker.Host}}"
				if tc.context != "" {
					expected += " -- " + tc.context
				}
				if strings.Join(req.Args, " ") != expected || req.Name != "docker" || req.Timeout != 10*time.Second || req.OutputLimit != 4096 || (tc.context == "" && len(req.Env) != 0) || (tc.context != "" && (len(req.Env) != 1 || req.Env[0] != "DOCKER_HOST=")) {
					t.Fatalf("resolution changed user's selection or exceeded bounds: %+v", req)
				}
				return model.CommandResult{Stdout: tc.observed, Stderr: "PRIVATE_STDERR", Arguments: []string{"PRIVATE_ARGUMENT"}}
			})
			selection := ResolveDocker(context.Background(), runner, func(name string) string {
				switch name {
				case "DOCKER_HOST":
					return tc.host
				case "DOCKER_CONTEXT":
					return tc.context
				}
				return ""
			})
			if selection.Status != tc.status || selection.Origin != tc.origin || (calls == 1) != tc.inspect || calls > 1 {
				t.Fatalf("wrong endpoint resolution: %+v calls=%d", selection, calls)
			}
			data, _ := json.Marshal(selection)
			if strings.Contains(string(data), "PRIVATE_") || strings.Contains(string(data), "rootless.sock") {
				t.Fatalf("private selection escaped: %s", data)
			}
		})
	}
}
func TestDockerResolutionRejectsFailedTruncatedAndCanceledObservation(t *testing.T) {
	for _, kind := range []string{"exit", "unclassified-exit", "timeout", "not-found", "truncated", "canceled-before", "canceled-during"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if kind == "canceled-before" {
				cancel()
			}
			calls := 0
			runner := testRunner(func(_ context.Context, _ command.Request) model.CommandResult {
				calls++
				result := model.CommandResult{Stdout: `"unix:///var/run/docker.sock"`}
				switch kind {
				case "exit":
					result.ExitCode, result.FailureType = 1, model.FailureExit
				case "unclassified-exit":
					result.ExitCode = 1
				case "timeout":
					result.FailureType = model.FailureTimeout
				case "not-found":
					result.FailureType = model.FailureNotFound
				case "truncated":
					result.Truncated = true
				case "canceled-during":
					cancel()
				}
				return result
			})
			selection := ResolveDocker(ctx, runner, func(string) string { return "" })
			if selection.Status != "not_validated" || calls > 1 || (kind == "canceled-before" && calls != 0) {
				t.Fatalf("unreliable endpoint accepted: %+v calls=%d", selection, calls)
			}
		})
	}
}
func TestDockerPinOverridesEveryAmbientSelectorAndPreservesControlledEnvironment(t *testing.T) {
	for _, clear := range []bool{false, true} {
		req := PinDocker(command.Request{ClearEnv: clear, Env: []string{"DOCKER_HOST=ssh://PRIVATE_HOST", "DOCKER_CONTEXT=PRIVATE_CONTEXT", "DOCKER_TLS=1", "DOCKER_TLS_VERIFY=1", "DOCKER_CERT_PATH=/private/certs", "DOCKER_CONFIG=/private/per-run", "KEEP=unchanged"}})
		env := map[string]string{}
		for _, entry := range req.Env {
			key, value, _ := strings.Cut(entry, "=")
			env[key] = value
		}
		if req.ClearEnv != clear || env["DOCKER_HOST"] != DockerEndpoint || env["DOCKER_CONFIG"] != "/private/per-run" || env["KEEP"] != "unchanged" {
			t.Fatalf("pin destroyed request policy: %+v", req)
		}
		for _, key := range []string{"DOCKER_CONTEXT", "DOCKER_TLS", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH"} {
			if value, ok := env[key]; !ok || value != "" {
				t.Fatalf("selector %s not cleared", key)
			}
		}
	}
}
func TestSharedCompatibilityPolicy(t *testing.T) {
	for _, tc := range []struct{ client, server, status string }{{"1.34.1", "1.35.5", "supported"}, {"1.36.0", "1.35.5", "supported"}, {"1.33.9", "1.35.5", "unsupported"}, {"1.37.0", "1.35.5", "unsupported"}, {"2.35.0", "1.35.5", "unsupported"}, {"", "1.35.5", "not_validated"}} {
		if got, _ := KubectlCompatibility(tc.client, tc.server); got != tc.status {
			t.Errorf("%+v: %s", tc, got)
		}
	}
	if !PlatformSupported("linux", "amd64") || PlatformSupported("linux", "arm64") || PlatformSupported("darwin", "amd64") {
		t.Fatal("qualified platform expanded")
	}
	tools := Tools()
	for _, tool := range tools {
		expected := "supported"
		if tool.Tested == "" {
			expected = "not_validated"
		}
		version := tool.Tested
		if version == "" {
			version = "0.21.2"
		}
		if status, _ := VersionDisposition(tool, version); status != expected {
			t.Fatalf("wrong tool classification %+v: %s", tool, status)
		}
	}
	tools[0].Args[0] = "MUTATED"
	if Tools()[0].Args[0] != "info" {
		t.Fatal("caller mutated shared policy")
	}
}

func TestBuildxPreflightUsesPrivatePluginVisibilityAndCleansTemporaryState(t *testing.T) {
	var directory string
	result := BuildxVersion(context.Background(), testRunner(func(_ context.Context, req command.Request) model.CommandResult {
		env := map[string]string{}
		for _, entry := range req.Env {
			key, value, _ := strings.Cut(entry, "=")
			env[key] = value
		}
		directory = env["DOCKER_CONFIG"]
		if directory == "" || env["BUILDX_CONFIG"] != filepath.Join(directory, "buildx") || env["BUILDX_BUILDER"] != "" || env["TMPDIR"] != directory {
			t.Fatalf("plugin preflight differs from private runtime: %+v", req)
		}
		entries, err := os.ReadDir(directory)
		if err != nil || len(entries) != 0 {
			t.Fatal("preflight inherited user Docker metadata")
		}
		if req.Timeout != 10*time.Second || req.OutputLimit != 16*1024 {
			t.Fatal("plugin observation unbounded")
		}
		if err := os.WriteFile(filepath.Join(env["TMPDIR"], "preflight-tool-residue"), []byte("synthetic"), 0o600); err != nil {
			t.Fatal(err)
		}
		return model.CommandResult{Stdout: "github.com/docker/buildx v0.21.2"}
	}))
	if result.ExitCode != 0 || directory == "" {
		t.Fatalf("preflight failed: %+v", result)
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatal("plugin preflight leaked temporary configuration")
	}
}

// When Docker is installed, use its real context reader against disposable
// metadata. This never contacts a daemon or creates application resources.
func TestRealDockerContextResolutionUsesMetadataBeforePinning(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("Docker CLI unavailable; injected policy tests remain mandatory")
	}
	directory := t.TempDir()
	name := "private-selected-context"
	metaDirectory := filepath.Join(directory, "contexts", "meta", fmt.Sprintf("%x", sha256.Sum256([]byte(name))))
	if err := os.MkdirAll(metaDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	metadata := map[string]any{"Name": name, "Metadata": map[string]string{}, "Endpoints": map[string]any{"docker": map[string]any{"Host": DockerEndpoint, "SkipTLSVerify": false}}}
	encoded, _ := json.Marshal(metadata)
	if err := os.WriteFile(filepath.Join(metaDirectory, "meta.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "config.json"), []byte(`{"currentContext":"private-selected-context"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, origin := range []string{"configured_context", "environment_context", "default_context"} {
		t.Run(origin, func(t *testing.T) {
			env := map[string]string{"DOCKER_CONFIG": directory, "DOCKER_CONTEXT": "", "DOCKER_HOST": "", "DOCKER_TLS_VERIFY": "", "DOCKER_CERT_PATH": ""}
			if origin == "environment_context" || origin == "default_context" {
				env["DOCKER_CONTEXT"], env["DOCKER_HOST"] = name, "ssh://private-ignored-host"
				if origin == "default_context" {
					env["DOCKER_CONTEXT"] = "default"
				}
			}
			runner := testRunner(func(ctx context.Context, req command.Request) model.CommandResult {
				overrides := req.Env
				req.Env = nil
				for key, value := range env {
					req.Env = append(req.Env, key+"="+value)
				}
				req.Env = append(req.Env, overrides...)
				return (command.ExecRunner{}).Run(ctx, req)
			})
			selected := ResolveDocker(context.Background(), runner, func(key string) string { return env[key] })
			expectedOrigin := origin
			if origin == "default_context" {
				expectedOrigin = "environment_context"
			}
			expectedStatus := "supported"
			if origin == "default_context" {
				expectedStatus = "unsupported"
			}
			if selected.Status != expectedStatus || selected.Origin != expectedOrigin {
				t.Fatalf("real CLI selected unexpected metadata: %+v", selected)
			}
		})
	}
}

func TestDockerResolutionRejectsTLSSelectorsWithoutObservation(t *testing.T) {
	for _, key := range []string{"DOCKER_TLS", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH"} {
		selected := ResolveDocker(context.Background(), testRunner(func(context.Context, command.Request) model.CommandResult {
			t.Fatal("TLS selection reached tool observation")
			return model.CommandResult{}
		}), func(name string) string {
			if name == key {
				return "PRIVATE_TLS_SETTING"
			}
			return ""
		})
		if selected.Status != "unsupported" {
			t.Fatalf("TLS selector accepted: %s", key)
		}
	}
}
