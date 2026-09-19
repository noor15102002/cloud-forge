package verification

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/noor15102002/cloud-forge/internal/analyzer"
	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/internal/dependency"
	"github.com/noor15102002/cloud-forge/internal/regression"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

func TestDependencyConfigurationAndSafeBindings(t *testing.T) {
	root := t.TempDir()
	for _, body := range []string{
		"dependencies: {redis: {}}", "dependencies: {redis: null}", "dependencies: {redis: {enabled: null}}",
		"dependencies: {../redis: {enabled: true}}", "dependencies: {redis: {enabled: true, image: latest}}",
		"dependencies: {redis: {enabled: true, startup_timeout: 3h}}", "environment: {REDIS_URL: {from: dependency.redis.url}}",
		"environment: {API_KEY: {value: sensitive}}", "environment: {REDIS_URL: {value: production}}",
		"environment: {APP_MODE: {from: process.HOME}}", "environment: {APP_MODE: {value: test, from: dependency.redis.url}}",
		"environment: {APP_MODE: {value: '$(command)'}}", "readiness: {status: 503}", "readiness: {status: 200, json: {nested.key: ready}}",
	} {
		if err := os.WriteFile(filepath.Join(root, "cloudforge.yaml"), []byte("schema_version: v1alpha2\n"+body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadConfiguration(root, ""); err == nil {
			t.Fatalf("accepted unsafe configuration %s", body)
		}
	}
	good := "schema_version: v1alpha2\ndependencies: {redis: {enabled: true}}\nenvironment:\n  REDIS_URL: {from: dependency.redis.url}\n  APP_MODE: {value: test}\nendpoints: {readiness: /ready}\nreadiness: {status: 200, json: {status: ready}}\n"
	if err := os.WriteFile(filepath.Join(root, "cloudforge.yaml"), []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := loadConfiguration(root, "")
	if err != nil {
		t.Fatal(err)
	}
	env := applicationEnvironment(config)
	if len(env) != 2 || env[0].Name != "APP_MODE" || env[1].Value != dependency.RedisURL(namespace) {
		t.Fatalf("incorrect environment bindings: %v", env)
	}
	redacted, _ := json.Marshal(safeConfiguration(config))
	if strings.Contains(string(redacted), `"value":"test"`) {
		t.Fatal("literal leaked into public configuration")
	}
	config.SchemaVersion = model.SchemaVersion
	if validateConfiguration(config) == nil {
		t.Fatal("legacy schema accepted extensions")
	}
}

func TestSemanticReadinessContract(t *testing.T) {
	for _, test := range []struct {
		name, body string
		status     int
		pass       bool
		reason     string
	}{
		{"healthy", `{"status":"ready","secret":"DO_NOT_REPORT"}`, 200, true, "matched"},
		{"degraded", `{"status":"degraded","secret":"DO_NOT_REPORT"}`, 200, false, "assertion_mismatch"},
		{"missing", `{"other":"ready"}`, 200, false, "assertion_mismatch"},
		{"nested", `{"status":{"status":"ready"}}`, 200, false, "assertion_mismatch"},
		{"bad-json", `{`, 200, false, "invalid_json"},
		{"duplicate", `{"status":"ready","status":"degraded"}`, 200, false, "invalid_json"},
		{"trailing", `{"status":"ready"} {}`, 200, false, "invalid_json"},
		{"too-large", strings.Repeat("x", readinessBodyLimit+1), 200, false, "body_limit"},
		{"unavailable", `{"status":"ready"}`, 503, false, "status_mismatch"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			checker := newReadinessChecker(directHTTPClient(), model.ReadinessAcceptance{Status: 200, JSON: map[string]string{"status": "ready"}})
			_, err := checker.probe(context.Background(), server.URL)
			if (err == nil) != test.pass {
				t.Fatalf("wrong verdict: %v", err)
			}
			evidence := checker.evidence(1)
			if !strings.Contains(evidence.Summary, test.reason) {
				t.Fatal(evidence.Summary)
			}
			data, _ := json.Marshal(evidence)
			if strings.Contains(string(data), "DO_NOT_REPORT") || strings.Contains(string(data), "degraded") {
				t.Fatal("response values leaked")
			}
		})
	}
}

func TestDependencyPlanBlocksUnknownBeforeCommands(t *testing.T) {
	root := dependencyTestRoot(t, "dependencies: {postgresql: {enabled: true}}")
	called := false
	service := fixedService(runnerFunc(func(_ context.Context, r command.Request) model.CommandResult {
		called = true
		return successfulCommand(r)
	}))
	out := service.Run(context.Background(), root, Options{PlanOnly: true})
	if called || out.Run.Status != model.StatusBlocked || out.ExitCode != 1 {
		t.Fatalf("unsafe plan: %v %s", called, out.Run.Status)
	}
	if out.Run.Plan == nil || len(out.Run.Plan.Capabilities) == 0 {
		t.Fatal("missing capability evidence")
	}
}

func TestRedisCountsAgainstAggregateBudget(t *testing.T) {
	analysis, err := analyzer.New().Analyze(fixturePath(t))
	if err != nil {
		t.Fatal(err)
	}
	analysis.Application.Kubernetes.HorizontalPodScalers = nil
	analysis.Application.Kubernetes.Deployments[0].Containers[0].Resources.MemoryLimit = "650Mi"
	// Two replicas plus one surge use 1950Mi: fits alone, exceeds 2048Mi with Redis.
	if _, err := buildPlan(analysis, "0123abcd"); err != nil {
		t.Fatal(err)
	}
	config := defaultConfiguration()
	config.Dependencies = map[string]model.DependencySpec{"redis": {Enabled: true}}
	if _, err := buildConfiguredPlan(analysis, "0123abcd", config); err == nil {
		t.Fatal("dependency ignored by aggregate budget")
	}
}

func TestRedisStartupOutcomesCleanupAndApplicationOrdering(t *testing.T) {
	for _, mode := range []string{"ready", "unavailable", "timeout", "cancel", "forbidden", "missing-tool"} {
		t.Run(mode, func(t *testing.T) {
			root := dependencyTestRoot(t, "dependencies: {redis: {enabled: true}}\nenvironment: {REDIS_URL: {from: dependency.redis.url}}")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var calls []command.Request
			dependencyReady := false
			appApplied := false
			temporary := ""
			runner := runnerFunc(func(callCtx context.Context, r command.Request) model.CommandResult {
				calls = append(calls, r)
				result := successfulCommand(r)
				if r.Name == "kubectl" && containsArgument(r.Args, "apply") {
					path := r.Args[len(r.Args)-1]
					temporary = filepath.Dir(path)
					if filepath.Base(path) == "workload.yaml" {
						appApplied = true
						if !dependencyReady {
							t.Error("app deployed before Redis readiness")
						}
					}
				}
				if containsArgument(r.Args, "deployment/"+dependency.RedisName) {
					switch mode {
					case "unavailable":
						result.ExitCode = 1
						result.FailureType = model.FailureExit
						result.Stderr = "error: deployment exceeded its progress deadline"
					case "forbidden":
						result.ExitCode = 1
						result.FailureType = model.FailureExit
						result.Stderr = "Error from server (Forbidden): private identity must not appear in report"
					case "missing-tool":
						result.ExitCode = -1
						result.FailureType = model.FailureNotFound
					case "timeout":
						result.ExitCode = -1
						result.FailureType = model.FailureTimeout
					case "cancel":
						cancel()
						result.ExitCode = -1
						result.FailureType = model.FailureCanceled
					default:
						dependencyReady = true
					}
				}
				if r.Name == "kubectl" && containsArgument(r.Args, "pods") {
					result.Stdout = readySinglePodList
				}
				if containsArgument(r.Args, "delete") && callCtx.Err() != nil {
					t.Error("cleanup inherited canceled context")
				}
				return result
			})
			out := fixedService(runner).Run(ctx, root, testOptions())
			expected := model.StatusPass
			if mode == "unavailable" || mode == "timeout" {
				expected = model.StatusBlocked
			}
			if mode == "cancel" || mode == "forbidden" || mode == "missing-tool" {
				expected = model.StatusError
			}
			if out.Run.Status != expected {
				t.Fatalf("got %s, want %s", out.Run.Status, expected)
			}
			if appApplied != (mode == "ready") || len(out.Run.Dependencies) != 1 {
				t.Fatal("incorrect dependency/application evidence")
			}
			if !hasCommand(calls, "k3d", "delete") || !hasCommand(calls, "docker", "rm") {
				t.Fatal("missing owned cleanup")
			}
			if _, err := os.Stat(temporary); !os.IsNotExist(err) {
				t.Fatal("private runtime directory retained")
			}
			encoded, _ := json.Marshal(out.Run)
			path := filepath.Join(t.TempDir(), "report.json")
			if err := os.WriteFile(path, encoded, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := regression.Load(path); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDependencyFingerprintOmitsLiteralAndChangesCompatibility(t *testing.T) {
	analysis, err := analyzer.New().Analyze(fixturePath(t))
	if err != nil {
		t.Fatal(err)
	}
	value := "private-test-value"
	config := defaultConfiguration()
	config.Environment = map[string]model.EnvironmentBinding{"APP_MODE": {Value: &value}}
	config.Dependencies = map[string]model.DependencySpec{"redis": {Enabled: true}}
	current, err := buildConfiguredPlan(analysis, "0123abcd", config)
	if err != nil {
		t.Fatal(err)
	}
	runner := runnerFunc(func(_ context.Context, r command.Request) model.CommandResult { return successfulCommand(r) })
	fp := newFingerprint(context.Background(), runner, t.TempDir(), current, testOptions())
	data, _ := json.Marshal(fp)
	if strings.Contains(string(data), value) || len(fp.Dependencies) != 1 || fp.EnvironmentHash == "" {
		t.Fatal("unsafe or incomplete fingerprint")
	}
	old := fp.EnvironmentHash
	value = "another-test-value"
	next := newFingerprint(context.Background(), runner, t.TempDir(), current, testOptions())
	if next.EnvironmentHash == old {
		t.Fatal("literal changes silently compatible")
	}
}

func dependencyTestRoot(t *testing.T, extra string) string {
	t.Helper()
	root := t.TempDir()
	for name, value := range map[string]string{"package.json": `{"name":"dependency-test"}`, "Dockerfile": "FROM node:22-alpine\nUSER node\nEXPOSE 8080\n", "cloudforge.yaml": "schema_version: v1alpha2\n" + extra + "\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestSemanticReadinessCancellationIsBounded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := newReadinessChecker(directHTTPClient(), model.ReadinessAcceptance{Status: 200}).probe(ctx, server.URL)
	if err == nil {
		t.Fatal("canceled request passed")
	}
}

func TestExplicitSemanticReadinessPlanRequiresHTTP(t *testing.T) {
	root := dependencyTestRoot(t, "readiness: {status: 200, json: {status: ready}}")
	service := fixedService(runnerFunc(func(context.Context, command.Request) model.CommandResult {
		t.Fatal("blocked contract executed command")
		return model.CommandResult{}
	}))
	out := service.Run(context.Background(), root, testOptions())
	if out.Run.Status != model.StatusBlocked || out.ExitCode != 1 {
		t.Fatal("semantic contract without HTTP endpoint was not blocked")
	}
}

func TestReadinessDeadlineRetainsLastHTTPResponseWithoutPassing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service := fixedService(successRunner())
	calls := 0
	service.probe = func(context.Context, string) (int, error) {
		calls++
		if calls == 1 {
			return 200, errors.New("semantic assertion mismatch")
		}
		cancel()
		return 0, context.Canceled
	}
	observed := service.waitForHTTP(ctx, "http://127.0.0.1/ready")
	if observed.Success || observed.Status != 200 || observed.Failures != 2 {
		t.Fatalf("lost observed response or accepted degraded readiness: %#v", observed)
	}
}
