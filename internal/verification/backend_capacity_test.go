package verification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

func testCapacityReaders() backendCapacityReaders {
	return backendCapacityReaders{
		platform: "linux", getenv: func(string) string { return "" },
		memory:   func() (uint64, uint64, error) { return 8 << 30, 7 << 30, nil },
		hostname: func() (string, error) { return "private-host", nil },
		kernel:   func() (string, error) { return "private-kernel", nil },
		disk:     func(string) (uint64, error) { return 32 << 30, nil },
	}
}

func capacityDockerInfo(memory uint64, host, kernel string) string {
	data, _ := json.Marshal(map[string]any{"memory": memory, "root": "/private/docker/root", "os": "linux", "name": host, "kernel": kernel})
	return string(data)
}

func testCapacityRunner(t *testing.T, info string, calls *[]command.Request) command.Runner {
	t.Helper()
	return runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		*calls = append(*calls, request)
		result := model.CommandResult{}
		switch {
		case request.Name == "docker" && containsArgument(request.Args, "context"):
			result.Stdout = `"unix:///var/run/docker.sock"`
		case request.Name == "docker" && containsArgument(request.Args, backendDockerInfoFormat):
			result.Stdout = info
			if !containsArgument(request.Env, "DOCKER_HOST="+backendDockerEndpoint) || !containsArgument(request.Env, "DOCKER_CONTEXT=") || !containsArgument(request.Env, "DOCKER_TLS_VERIFY=") {
				t.Error("capacity observation used a different daemon selection than the backend runtime")
			}
		default:
			t.Errorf("capacity gate attempted a non-observation command: %+v", request)
		}
		if request.Timeout <= 0 || request.OutputLimit <= 0 || request.OutputLimit > 16<<10 {
			t.Error("capacity command is not bounded")
		}
		return result
	})
}

func TestBackendCapacityRequiresEveryFixedThreshold(t *testing.T) {
	for _, scenario := range []string{"exact-boundary", "host-total", "host-available", "docker-total", "docker-storage"} {
		t.Run(scenario, func(t *testing.T) {
			readers := testCapacityReaders()
			total, available, daemon, disk := backendMinimumMemory, backendMinimumAvailable, backendMinimumMemory, backendMinimumDisk
			switch scenario {
			case "host-total":
				total--
			case "host-available":
				available--
			case "docker-total":
				daemon--
			case "docker-storage":
				disk--
			}
			readers.memory = func() (uint64, uint64, error) { return total, available, nil }
			readers.disk = func(path string) (uint64, error) {
				if path != "/private/docker/root" {
					t.Fatal("capacity was read from a different filesystem")
				}
				return disk, nil
			}
			var calls []command.Request
			var out Outcome
			ok := readers.check(context.Background(), testCapacityRunner(t, capacityDockerInfo(daemon, "private-host", "private-kernel"), &calls), &out)
			if ok != (scenario == "exact-boundary") || len(calls) != 2 {
				t.Fatalf("capacity decision was incorrect: %+v", out)
			}
			evidence := out.Run.Evidence[0]
			if len(evidence.Measurements) != 8 || !evidence.Execution.Executed || evidence.Execution.MutationAttempted {
				t.Fatalf("capacity snapshot incomplete: %+v", evidence)
			}
			if !ok && (out.Run.Status != model.StatusBlocked || out.ExitCode != 1 || out.Run.Diagnostics[0].Code != "backend_capacity_insufficient") {
				t.Fatal("valid insufficiency became an execution/application error")
			}
			if ok && !strings.Contains(evidence.Summary, "does not reserve") {
				t.Fatal("snapshot incorrectly claims a resource reservation")
			}
			assertCapacityPrivacy(t, out)
		})
	}
}

func TestBackendCapacityRejectsRemoteAndUnmatchedDaemonBeforeFilesystemRead(t *testing.T) {
	for _, scenario := range []string{"client-os", "remote-host", "nondefault-socket", "vm-hostname", "vm-kernel"} {
		t.Run(scenario, func(t *testing.T) {
			readers := testCapacityReaders()
			host, kernel := "private-host", "private-kernel"
			switch scenario {
			case "client-os":
				readers.platform = "darwin"
			case "remote-host":
				readers.getenv = func(name string) string {
					if name == "DOCKER_HOST" {
						return "ssh://private-host"
					}
					return ""
				}
			case "nondefault-socket":
				readers.getenv = func(name string) string {
					if name == "DOCKER_HOST" {
						return "unix:///private/other-daemon.sock"
					}
					return ""
				}
			case "vm-hostname":
				host = "private-vm"
			case "vm-kernel":
				kernel = "private-vm-kernel"
			}
			readers.disk = func(string) (uint64, error) {
				t.Fatal("remote daemon root was interpreted as a local filesystem")
				return 0, nil
			}
			var calls []command.Request
			var out Outcome
			if readers.check(context.Background(), testCapacityRunner(t, capacityDockerInfo(8<<30, host, kernel), &calls), &out) || out.Run.Status != model.StatusBlocked || out.Run.Diagnostics[0].Code != "backend_capacity_unsupported" {
				t.Fatalf("unsupported environment passed: %+v", out)
			}
			if (scenario == "client-os" || scenario == "remote-host" || scenario == "nondefault-socket") && len(calls) != 0 {
				t.Fatal("unsupported endpoint executed Docker tools")
			}
			assertCapacityPrivacy(t, out)
		})
	}
}

func TestBackendCapacityHonorsDockerContextPrecedence(t *testing.T) {
	readers := testCapacityReaders()
	readers.getenv = func(name string) string {
		if name == "DOCKER_HOST" {
			return "ssh://private-remote"
		}
		if name == "DOCKER_CONTEXT" {
			return "private-selected-context"
		}
		return ""
	}
	var calls []command.Request
	var out Outcome
	if !readers.check(context.Background(), testCapacityRunner(t, capacityDockerInfo(8<<30, "private-host", "private-kernel"), &calls), &out) || len(calls) != 2 || !containsArgument(calls[0].Args, "context") {
		t.Fatalf("Docker context precedence changed: %+v", out)
	}
	assertCapacityPrivacy(t, out)
}

func TestBackendCapacityUnobservableInputsAreErrors(t *testing.T) {
	for _, scenario := range []string{"memory", "disk", "hostname", "kernel", "malformed-context", "malformed-info", "truncated-info", "docker-timeout", "zero-daemon-memory"} {
		t.Run(scenario, func(t *testing.T) {
			readers := testCapacityReaders()
			rawError := errors.New("PRIVATE_ERROR /private/docker/root")
			switch scenario {
			case "memory":
				readers.memory = func() (uint64, uint64, error) { return 0, 0, rawError }
			case "disk":
				readers.disk = func(string) (uint64, error) { return 0, rawError }
			case "hostname":
				readers.hostname = func() (string, error) { return "", rawError }
			case "kernel":
				readers.kernel = func() (string, error) { return "", rawError }
			}
			var calls []command.Request
			base := testCapacityRunner(t, capacityDockerInfo(8<<30, "private-host", "private-kernel"), &calls)
			runner := runnerFunc(func(ctx context.Context, req command.Request) model.CommandResult {
				result := base.Run(ctx, req)
				if scenario == "malformed-context" && containsArgument(req.Args, "context") {
					result.Stdout = "PRIVATE_ERROR"
				}
				if containsArgument(req.Args, "info") {
					switch scenario {
					case "malformed-info":
						result.Stdout = "PRIVATE_ERROR"
					case "truncated-info":
						result.Truncated = true
					case "docker-timeout":
						result.ExitCode, result.FailureType, result.Stderr = -1, model.FailureTimeout, "PRIVATE_ERROR"
					case "zero-daemon-memory":
						result.Stdout = capacityDockerInfo(0, "private-host", "private-kernel")
					}
				}
				return result
			})
			var out Outcome
			if readers.check(context.Background(), runner, &out) || out.Run.Status != model.StatusError || out.ExitCode != 2 || out.Run.Diagnostics[0].Code != "backend_capacity_unobservable" {
				t.Fatalf("missing observation became valid insufficiency: %+v", out)
			}
			assertCapacityPrivacy(t, out)
		})
	}
}

func TestBackendCapacityCancellationStartsNoFurtherChecks(t *testing.T) {
	for _, during := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		if !during {
			cancel()
		}
		calls := 0
		readers := testCapacityReaders()
		readers.disk = func(string) (uint64, error) { t.Fatal("filesystem read after cancellation"); return 0, nil }
		runner := runnerFunc(func(context.Context, command.Request) model.CommandResult {
			calls++
			cancel()
			return model.CommandResult{Stdout: `"unix:///private/docker.sock"`}
		})
		var out Outcome
		if readers.check(ctx, runner, &out) || out.Run.Status != model.StatusError || calls > 1 || (!during && calls != 0) {
			t.Fatalf("cancellation scheduled additional observations: %+v", out)
		}
		cancel()
	}
}

func TestBackendMemoryParsingRequiresAvailableAndRejectsOverflow(t *testing.T) {
	for _, input := range []string{
		"MemTotal: 7340032 kB\n", "MemAvailable: 6815744 kB\n",
		"MemTotal: 7340032 kB\nMemAvailable: 9999999 kB\n",
		"MemTotal: 7340032 MB\nMemAvailable: 6815744 kB\n",
		"MemTotal: 18446744073709551615 kB\nMemAvailable: 0 kB\n",
		"MemTotal: 7340032 kB\nMemTotal: 7340032 kB\nMemAvailable: 0 kB\n",
	} {
		if _, _, err := parseBackendMemory([]byte(input)); err == nil {
			t.Fatalf("invalid memory input accepted: %q", input)
		}
	}
	total, available, err := parseBackendMemory([]byte("MemTotal: 7340032 kB\nUnrelated: ignored\nMemAvailable: 6815744 kB\n"))
	if err != nil || total != backendMinimumMemory || available != backendMinimumAvailable {
		t.Fatalf("memory units/boundary changed: %d %d %v", total, available, err)
	}
}

func TestBackendRunnerPinsQualifiedDaemonWithoutChangingLegacySelection(t *testing.T) {
	for _, backend := range []bool{false, true} {
		for _, tool := range []string{"docker", "k3d", "trivy", "kubectl"} {
			runner := scopedRunner{backendDocker: backend, dockerConfig: "/private/config", kubeconfig: "/private/kubeconfig", runner: runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
				values := map[string]string{}
				for _, entry := range request.Env {
					key, value, _ := strings.Cut(entry, "=")
					values[key] = value
				}
				expected := "ssh://private-remote"
				if backend {
					expected = backendDockerEndpoint
					for _, key := range []string{"DOCKER_CONTEXT", "DOCKER_TLS", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH"} {
						if value, exists := values[key]; !exists || value != "" {
							t.Errorf("backend %s selection not pinned: %s=%q", tool, key, value)
						}
					}
				}
				if values["DOCKER_HOST"] != expected || values["DOCKER_CONFIG"] != "/private/config" || values["UNCHANGED"] != "kept" {
					t.Fatal("scoped Docker selection or private config changed")
				}
				return model.CommandResult{}
			})}
			runner.Run(context.Background(), command.Request{Name: tool, Env: []string{"DOCKER_HOST=ssh://private-remote", "DOCKER_CONTEXT=private-context", "DOCKER_TLS=1", "DOCKER_TLS_VERIFY=1", "DOCKER_CERT_PATH=/private/certs", "UNCHANGED=kept"}})
		}
	}
}

func assertCapacityPrivacy(t *testing.T, out Outcome) {
	t.Helper()
	data, err := json.Marshal(out.Run)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private-host", "private-kernel", "/private", "PRIVATE_ERROR", "unix://", "ssh://"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("capacity evidence leaked %s", forbidden)
		}
	}
	for _, measurement := range out.Run.Evidence[0].Measurements {
		var value uint64
		if _, err := fmt.Sscan(measurement.Value, &value); err != nil || measurement.Unit != "bytes" {
			t.Fatal("capacity evidence is not numeric bytes")
		}
	}
}
