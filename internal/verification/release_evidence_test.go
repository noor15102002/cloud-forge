package verification

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/internal/executor/kubernetes"
	"github.com/noor15102002/cloud-forge/internal/render"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

func TestReleaseNodeObservationSeparatesStartupFailure(t *testing.T) {
	for _, tc := range []struct {
		name, body                string
		truncated, commandFailure bool
		want                      model.Status
		exit                      int
		diagnostic                string
	}{
		{"healthy", `{"items":[{"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}`, false, false, model.StatusFail, 1, "readiness_failed"},
		{"missing", `{"items":[]}`, false, false, model.StatusError, 2, "runtime_environment_unobserved"},
		{"malformed", `{`, false, false, model.StatusError, 2, "runtime_environment_unobserved"},
		{"truncated", `{"items":[{"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}`, true, false, model.StatusError, 2, "runtime_environment_unobserved"},
		{"unavailable", "", false, true, model.StatusError, 2, "runtime_environment_unobserved"},
		{"condition_absent", `{"items":[{}]}`, false, false, model.StatusError, 2, "runtime_environment_unobserved"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			startupAttempted := false
			s := fixedService(runnerFunc(func(_ context.Context, r command.Request) model.CommandResult {
				result := successfulCommand(r)
				if r.Name == "kubectl" && containsArgument(r.Args, "rollout") {
					startupAttempted = true
					result.ExitCode = 1
					result.FailureType = model.FailureTimeout
				}
				if startupAttempted && r.Name == "kubectl" && containsArgument(r.Args, "nodes") {
					result.Stdout = tc.body
					result.Truncated = tc.truncated
					if tc.commandFailure {
						result.ExitCode = 1
						result.FailureType = model.FailureExit
					}
				}
				return result
			}))
			out := s.Run(context.Background(), fixturePath(t), testOptions())
			e := evidenceByID(out.Run.Evidence, "deployment-readiness")
			if e == nil || e.Status != tc.want || out.Run.Status != tc.want || out.ExitCode != tc.exit || !hasDiagnosticCode(out.Run.Diagnostics, tc.diagnostic) {
				t.Fatalf("evidence=%v status=%s exit=%d diagnostics=%v", e, out.Run.Status, out.ExitCode, out.Run.Diagnostics)
			}
			if e := evidenceByID(out.Run.Evidence, "environment-cleanup"); e == nil || e.Status != model.StatusPass {
				t.Fatal("startup result prevented cleanup")
			}
			for _, f := range out.Run.Findings {
				if tc.want == model.StatusError && f.ID == "container.startup" && f.Status == model.StatusFail {
					t.Fatal("unobserved environment blamed application")
				}
			}
		})
	}
}

func TestReleaseStartupDiagnosticReportsOnlyObservedFacts(t *testing.T) {
	exit := int32(1)
	pods := []kubernetes.PodState{{ApplicationStatusObserved: true, PreviousTermination: &kubernetes.ContainerTermination{Reason: "error", ExitCode: &exit}}}
	message := startupFailureSummary(0, 2, 8, 120*time.Second, pods)
	for _, want := range []string{"2m0s", "0/2", "restarts: 8", "reason error: 1", "exit code 1: 1", "signal unknown: 1", "Root cause: not established"} {
		if !strings.Contains(message, want) {
			t.Fatalf("missing %q in %s", want, message)
		}
	}
	for _, bad := range []string{"OOMKilled", "doctor", "missing configuration", "SIGTERM defect"} {
		if strings.Contains(message, bad) {
			t.Fatalf("invented cause %s", message)
		}
	}
}

func TestReleaseStartupAndFinalProbeReasons(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		err    error
		class  string
	}{
		{"refused", 0, syscall.ECONNREFUSED, "connection_refused"},
		{"closed", 0, io.EOF, "connection_closed"},
		{"reset", 0, syscall.ECONNRESET, "connection_reset"},
		{"timeout", 0, context.DeadlineExceeded, "timeout"},
		{"429", 429, nil, "http_status"},
		{"404", 404, nil, "http_status"},
		{"503", 503, nil, "http_status"},
		{"semantic", 200, &readinessProbeError{class: "semantic_mismatch"}, "semantic_mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := fixedService(successRunner())
			calls := 0
			s.probe = func(context.Context, string) (int, error) {
				calls++
				if calls == 1 {
					return tc.status, tc.err
				}
				return 200, nil
			}
			o := s.waitForHTTP(context.Background(), "http://PRIVATE_TOKEN/")
			if !o.Success || o.Failures != 1 || measurementValue(o.diagnostics(), "startup_probe_failure_"+tc.class) != "1" {
				t.Fatalf("startup observation lost reason: %+v", o)
			}
			for _, name := range []string{"graceful-shutdown", "pod-recovery", "rolling-deployment"} {
				t.Run(name, func(t *testing.T) {
					s := fixedService(successRunner())
					current := experimentTestPlan(t)
					s.probe = func(_ context.Context, url string) (int, error) {
						if url == current.healthURL {
							return tc.status, tc.err
						}
						return 200, nil
					}
					result := runTestLifecycle(t, name, s, current)
					if result.Evidence.Status != model.StatusFail || result.ExitCode != 1 || result.Diagnostic == nil || !result.MutationAttempted {
						t.Fatalf("final unmet health contract misclassified: %+v", result)
					}
					key := "final_probe_failure_" + tc.class
					if measurementValue(result.Evidence.Measurements, key) != "1" || measurementValue(result.Evidence.Measurements, "final_http_status") != fmt.Sprint(tc.status) {
						t.Fatalf("lost final category/status: %+v", result.Evidence.Measurements)
					}
					run := model.VerificationRun{SchemaVersion: model.VerificationSchemaVersion, Status: model.StatusFail, Evidence: []model.Evidence{result.Evidence}}
					raw, _ := json.Marshal(run)
					var markdown, terminal bytes.Buffer
					if err := render.VerificationMarkdown(&markdown, run); err != nil {
						t.Fatal(err)
					}
					if err := render.VerificationText(&terminal, run); err != nil {
						t.Fatal(err)
					}
					for _, v := range []string{string(raw), html.UnescapeString(markdown.String()), terminal.String()} {
						if !strings.Contains(v, tc.class) || strings.Contains(v, "PRIVATE_TOKEN") {
							t.Fatalf("reason %s lost or secret retained in rendered output", tc.class)
						}
					}
				})
			}
		})
	}
}

func TestReleaseFinalFailureSurvivesRestorationAndCleanup(t *testing.T) {
	s := fixedService(successRunner())
	failedOnce := false
	s.probe = func(_ context.Context, url string) (int, error) {
		if strings.HasSuffix(url, "/health") && !failedOnce {
			failedOnce = true
			return 0, syscall.ECONNRESET
		}
		return 200, nil
	}
	out := s.Run(context.Background(), fixturePath(t), testOptions())
	if out.Run.Status != model.StatusFail || out.ExitCode != 1 {
		t.Fatalf("original failure lost: status=%s exit=%d diagnostics=%v", out.Run.Status, out.ExitCode, out.Run.Diagnostics)
	}
	e := evidenceByID(out.Run.Evidence, "graceful-shutdown")
	if e == nil || e.Status != model.StatusFail || measurementValue(e.Measurements, "final_probe_failure_connection_reset") != "1" {
		t.Fatalf("lost original failure: %v", e)
	}
	for _, id := range []string{"pod-recovery", "rolling-deployment", "environment-cleanup"} {
		if e := evidenceByID(out.Run.Evidence, id); e == nil || e.Status != model.StatusPass {
			t.Fatalf("later evidence %s=%v", id, e)
		}
	}
	if !hasDiagnosticCode(out.Run.Diagnostics, "graceful_shutdown_failed") {
		t.Fatalf("missing original diagnostic: %v", out.Run.Diagnostics)
	}
}

func TestReleaseRequirementsOnlyIdentityUsesSelectedPath(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "services", "ai")
	if err := os.MkdirAll(app, 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"services/ai/requirements.txt": "fastapi==0.115.0\nuvicorn==0.30.0\n",
		"services/ai/Dockerfile":       "FROM python:3.12-slim\nEXPOSE 8000\nUSER 1000\nCMD [\"python\",\"app.py\"]\n",
		"cloudforge.yaml":              "schema_version: v1alpha3\nbuild: {app: services/ai, dockerfile: services/ai/Dockerfile, context: .}\n",
	}
	for path, data := range files {
		if err := os.WriteFile(filepath.Join(root, path), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	out := New(runnerFunc(func(context.Context, command.Request) model.CommandResult {
		t.Fatal("read-only plan executed a command")
		return model.CommandResult{}
	})).Run(context.Background(), root, Options{PlanOnly: true})
	if out.ExitCode != 0 || out.Run.Application != "services/ai" || out.Run.Plan.Build.App != "services/ai" {
		t.Fatalf("identity/selection lost: %s %v", out.Run.Application, out.Run.Diagnostics)
	}
	var txt, md bytes.Buffer
	_ = render.VerificationText(&txt, out.Run)
	_ = render.VerificationMarkdown(&md, out.Run)
	raw, _ := json.Marshal(out.Run)
	for _, value := range []string{txt.String(), md.String(), string(raw)} {
		if !strings.Contains(value, "services/ai") {
			t.Fatal("identity not rendered")
		}
	}
	if got := applicationIdentity("source-name", "services/ai", root); got != "source-name" {
		t.Fatal("source name lost precedence")
	}
	if got := applicationIdentity("", "", filepath.Join(root, "standalone")); got != "standalone" {
		t.Fatal("directory fallback missing")
	}
}

func TestReleaseTopologyUsesObservationsWithoutContinuousClaims(t *testing.T) {
	for _, replicas := range []int32{1, 2} {
		for _, status := range []model.Status{model.StatusPass, model.StatusFail} {
			failed, window := "0", "0"
			if status == model.StatusFail {
				failed, window = "1", "500"
			}
			result := recoveryOutcome{Evidence: model.Evidence{ExperimentID: "rolling-deployment", Status: status, Measurements: []model.Measurement{{Name: "request_count", Value: "14"}, {Name: "failed_requests", Value: failed}, {Name: "downtime_ms", Value: window}, {Name: "probe_poll_interval_ms", Value: "500"}, {Name: "final_http_status", Value: "200"}}}}
			qualifyTopology(&result, plan{desiredReplicas: replicas, topology: &model.TestTopology{Origin: "explicit_test_configuration", Replicas: replicas, Strategy: "rolling_update"}})
			if result.Evidence.Status != status || measurementValue(result.Evidence.Measurements, "downtime_ms") != window {
				t.Fatal("wording changed measured outcome")
			}
			for _, want := range []string{"14", "500 ms", "Final HTTP status: 200", "between samples", "same-source", "not separately scanned", "explicit test configuration"} {
				if !strings.Contains(result.Evidence.Summary, want) {
					t.Fatalf("missing %q: %s", want, result.Evidence.Summary)
				}
			}
			for _, bad := range []string{"zero downtime", "Availability requirement maintained", "new application version"} {
				if strings.Contains(result.Evidence.Summary, bad) {
					t.Fatalf("overclaim: %s", result.Evidence.Summary)
				}
			}
		}
	}
}

func TestReleaseUnissuedSemanticProbeHasNoFailureCategory(t *testing.T) {
	checker := newReadinessChecker(nil, model.ReadinessAcceptance{Status: 200})
	if got := measurementValue(checker.evidence(0).Measurements, "readiness_failure_category"); got != "" {
		t.Fatalf("unissued probe acquired a failure category: %s", got)
	}
}
