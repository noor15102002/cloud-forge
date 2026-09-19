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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/internal/executor/kubernetes"
	"github.com/noor15102002/cloud-forge/internal/regression"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

func TestKnownSkewBlocksBeforeBuildAndPlanRequiresNoTools(t *testing.T) {
	for _, planOnly := range []bool{false, true} {
		var calls []command.Request
		runner := runnerFunc(func(_ context.Context, req command.Request) model.CommandResult {
			calls = append(calls, req)
			r := successfulCommand(req)
			if req.Name == "kubectl" && containsArgument(req.Args, "version") {
				r.Stdout = `{"clientVersion":{"gitVersion":"v1.37.0"}}`
			}
			return r
		})
		options := testOptions()
		options.PlanOnly = planOnly
		if planOnly {
			options.Version = ""
			options.Commit = ""
		}
		out := fixedService(runner).Run(context.Background(), fixturePath(t), options)
		if planOnly {
			if out.ExitCode != 0 || len(calls) > 0 {
				t.Fatalf("plan executed tools: %#v", calls)
			}
			continue
		}
		if out.Run.Status != model.StatusBlocked || out.ExitCode != 1 || out.Run.Compatibility.Status != "unsupported" {
			t.Fatalf("skew not blocked: %+v", out)
		}
		if hasCommand(calls, "docker", "build") || hasCommand(calls, "k3d", "create") {
			t.Fatal("unsupported runtime created resources")
		}
	}
}

func TestActualServerSkewBlocksBeforeApplicationDeployment(t *testing.T) {
	var calls []command.Request
	runner := runnerFunc(func(_ context.Context, req command.Request) model.CommandResult {
		calls = append(calls, req)
		r := successfulCommand(req)
		if req.Name == "kubectl" && containsArgument(req.Args, "version") && !containsArgument(req.Args, "--client=true") {
			r.Stdout = `{"clientVersion":{"gitVersion":"v1.35.5"},"serverVersion":{"gitVersion":"v1.38.0"}}`
		}
		return r
	})
	out := fixedService(runner).Run(context.Background(), fixturePath(t), testOptions())
	if out.Run.Status != model.StatusBlocked || out.Run.Compatibility.ObservedKubernetes != "1.38.0" {
		t.Fatalf("server skew not blocked: %+v", out)
	}
	if hasCommand(calls, "kubectl", "apply") || !hasCommand(calls, "k3d", "delete") {
		t.Fatal("server gate did not precede deployment and cleanup")
	}
}

func TestUntestedVersionsAreExplicitAndRetainMeasurements(t *testing.T) {
	runner := runnerFunc(func(_ context.Context, req command.Request) model.CommandResult {
		r := successfulCommand(req)
		if req.Name == "docker" && containsArgument(req.Args, "info") {
			r.Stdout = "28.0.5"
		}
		return r
	})
	out := fixedService(runner).Run(context.Background(), fixturePath(t), testOptions())
	if out.ExitCode != 0 || out.Run.Status != model.StatusWarn || out.Run.Compatibility.Status != "not_validated" {
		t.Fatalf("untested tuple conflated with incompatibility: %+v", out)
	}
	for _, name := range []string{"docker", "k3d", "kubectl", "kubernetes", "k6", "trivy"} {
		if toolVersion(out.Run.Fingerprint, name) == "" {
			t.Fatal("missing version", name)
		}
	}
	if evidenceByID(out.Run.Evidence, "pod-recovery").Status != model.StatusPass {
		t.Fatal("valid observations were reinterpreted")
	}
}

func TestUnobservableVersionAndMissingBuildIdentityAreErrors(t *testing.T) {
	for _, scenario := range []string{"identity", "malformed", "timeout", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			var calls []command.Request
			runner := runnerFunc(func(_ context.Context, req command.Request) model.CommandResult {
				calls = append(calls, req)
				r := successfulCommand(req)
				if req.Name == "kubectl" && containsArgument(req.Args, "version") {
					switch scenario {
					case "malformed":
						r.Stdout = "secret text"
					case "timeout":
						r.FailureType = model.FailureTimeout
						r.ExitCode = -1
					case "canceled":
						r.FailureType = model.FailureCanceled
						r.ExitCode = -1
					}
				}
				return r
			})
			options := testOptions()
			if scenario == "identity" {
				options.Commit = "unknown"
			}
			out := fixedService(runner).Run(context.Background(), fixturePath(t), options)
			if out.ExitCode != 2 || out.Run.Status != model.StatusError || hasCommand(calls, "k3d", "create") {
				t.Fatalf("invalid preflight: %+v", out)
			}
			data, _ := json.Marshal(out.Run)
			if strings.Contains(string(data), "secret text") {
				t.Fatal("raw tool output leaked")
			}
		})
	}
}

func TestEarlyFailureRestoresAndContinuesWithoutErasingFailure(t *testing.T) {
	var deletes atomic.Int32
	runner := runnerFunc(func(_ context.Context, req command.Request) model.CommandResult {
		if req.Name == "kubectl" && containsArgument(req.Args, "delete") && containsArgument(req.Args, "pod") {
			deletes.Add(1)
		}
		return successfulCommand(req)
	})
	service := fixedService(runner)
	var failedOnce atomic.Bool
	service.probe = func(context.Context, string) (int, error) {
		if deletes.Load() == 1 && failedOnce.CompareAndSwap(false, true) {
			return 503, nil
		}
		return 200, nil
	}
	out := service.Run(context.Background(), fixturePath(t), testOptions())
	shutdown := evidenceByID(out.Run.Evidence, "graceful-shutdown")
	if out.ExitCode != 1 || out.Run.Status != model.StatusFail || shutdown.Status != model.StatusFail || shutdown.Recovery == nil || shutdown.Recovery.Status != model.StatusPass {
		t.Fatalf("failure or restoration lost: %+v", out)
	}
	for _, name := range []string{"pod-recovery", "rolling-deployment", "load-profile"} {
		e := evidenceByID(out.Run.Evidence, name)
		if e == nil || e.Status != model.StatusPass || !e.Execution.Executed {
			t.Fatalf("later independent evidence missing: %s %+v", name, e)
		}
	}
	if !strings.Contains(shutdown.Summary, "do not establish") {
		t.Fatal("SIGTERM claim lacks its limit")
	}
	// The report imported by the Action retains the original failure and later evidence.
	data, _ := json.Marshal(out.Run)
	path := filepath.Join(t.TempDir(), "result.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := regression.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != model.StatusFail || evidenceByID(loaded.Evidence, "rolling-deployment").Status != model.StatusPass {
		t.Fatal("serialized report erased coverage or failure")
	}
}

func TestUnrestoredStateBlocksDependentsButKeepsOriginalFailure(t *testing.T) {
	var deleted atomic.Bool
	runner := runnerFunc(func(_ context.Context, req command.Request) model.CommandResult {
		r := successfulCommand(req)
		if req.Name == "kubectl" && containsArgument(req.Args, "delete") && containsArgument(req.Args, "pod") {
			deleted.Store(true)
		}
		if deleted.Load() && req.Name == "kubectl" && containsArgument(req.Args, "pods") {
			r.Stdout = degradedPodList
		}
		return r
	})
	service := fixedService(runner)
	service.recoveryTimeout = 200 * time.Millisecond
	service.baselineTimeout = 200 * time.Millisecond
	out := service.Run(context.Background(), fixturePath(t), testOptions())
	shutdown := evidenceByID(out.Run.Evidence, "graceful-shutdown")
	if out.ExitCode != 1 || shutdown.Status != model.StatusFail || shutdown.Recovery.Status != model.StatusBlocked {
		t.Fatalf("unrestored state misclassified: %+v", out)
	}
	for _, name := range []string{"pod-recovery", "rolling-deployment", "load-profile", "horizontal-autoscaling"} {
		e := evidenceByID(out.Run.Evidence, name)
		if e.Status != model.StatusBlocked || e.Execution.Executed {
			t.Fatalf("dependent experiment ran: %+v", e)
		}
	}
}

func TestObservationErrorCanRecoverButNeverBecomesApplicationFailure(t *testing.T) {
	var deleted atomic.Bool
	runner := runnerFunc(func(_ context.Context, req command.Request) model.CommandResult {
		r := successfulCommand(req)
		if req.Name == "kubectl" && containsArgument(req.Args, "delete") && containsArgument(req.Args, "pod") && deleted.CompareAndSwap(false, true) {
			r.FailureType = model.FailureExit
			r.ExitCode = 1
			r.Stderr = "API access denied"
		}
		return r
	})
	out := fixedService(runner).Run(context.Background(), fixturePath(t), testOptions())
	e := evidenceByID(out.Run.Evidence, "graceful-shutdown")
	if out.ExitCode != 2 || out.Run.Status != model.StatusError || e.Status != model.StatusError || e.Recovery.Status != model.StatusPass {
		t.Fatalf("execution error changed meaning: %+v", out)
	}
	if evidenceByID(out.Run.Evidence, "rolling-deployment").Status != model.StatusPass {
		t.Fatal("independent experiment did not continue after a validated restore")
	}
	if findingByID(out.Run.Findings, "runtime.graceful-shutdown") != nil {
		t.Fatal("API failure became an application finding")
	}
}

func TestCancellationPreservesEvidenceStopsSchedulingAndCleansUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls []command.Request
	runner := runnerFunc(func(_ context.Context, req command.Request) model.CommandResult {
		calls = append(calls, req)
		r := successfulCommand(req)
		if req.Name == "kubectl" && containsArgument(req.Args, "delete") && containsArgument(req.Args, "pod") {
			cancel()
			r.FailureType = model.FailureCanceled
			r.ExitCode = -1
		}
		return r
	})
	out := fixedService(runner).Run(ctx, fixturePath(t), testOptions())
	if out.ExitCode != 2 || evidenceByID(out.Run.Evidence, "container-build").Status != model.StatusPass || evidenceByID(out.Run.Evidence, "graceful-shutdown").Status != model.StatusError {
		t.Fatal("cancellation lost evidence")
	}
	for _, name := range []string{"pod-recovery", "rolling-deployment", "load-profile"} {
		e := evidenceByID(out.Run.Evidence, name)
		if e.Status != model.StatusSkipped || e.Execution.Executed {
			t.Fatalf("scheduled after cancellation: %+v", e)
		}
	}
	if !hasCommand(calls, "k3d", "delete") || hasCommand(calls, "kubectl", "set") {
		t.Fatal("cancellation scheduling/cleanup violated")
	}
}

func TestBaselineRequiresImageRevisionReplicasAndReadyPods(t *testing.T) {
	for _, broken := range []string{"image", "revision", "replicas", "ready", "http", "api"} {
		t.Run(broken, func(t *testing.T) {
			// Exercise baseline validation directly; no commands mutate anything.
			runner := runnerFunc(func(_ context.Context, req command.Request) model.CommandResult {
				r := model.CommandResult{}
				switch {
				case containsArgument(req.Args, "deployment"):
					image := "image-a"
					replicas := 1
					if broken == "image" {
						image = "image-b"
					}
					if broken == "replicas" {
						replicas = 2
					}
					data := map[string]any{"metadata": map[string]any{"uid": "d", "generation": 1, "annotations": map[string]string{"deployment.kubernetes.io/revision": "2"}}, "spec": map[string]any{"replicas": replicas, "template": map[string]any{"spec": map[string]any{"containers": []any{map[string]string{"image": image}}}}}, "status": map[string]int{"observedGeneration": 1}}
					encoded, _ := json.Marshal(data)
					r.Stdout = string(encoded)
				case containsArgument(req.Args, "replicasets"):
					r.Stdout = `{"items":[{"metadata":{"uid":"rs","annotations":{"deployment.kubernetes.io/revision":"2"},"ownerReferences":[{"kind":"Deployment","uid":"d","controller":true}]},"spec":{"template":{"spec":{"containers":[{"image":"image-a"}]}}}}]}`
					if broken == "revision" {
						r.Stdout = strings.ReplaceAll(r.Stdout, `"2"`, `"1"`)
					}
				case containsArgument(req.Args, "pods"):
					r.Stdout = `{"items":[{"metadata":{"ownerReferences":[{"kind":"ReplicaSet","uid":"rs","controller":true}]},"spec":{"containers":[{"image":"image-a"}]},"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}`
					if broken == "ready" {
						r.Stdout = strings.ReplaceAll(r.Stdout, `"True"`, `"False"`)
					}
				}
				if broken == "api" {
					r.FailureType = model.FailureExit
					r.ExitCode = 1
				}
				return r
			})
			s := New(runner)
			s.baselineTimeout = 10 * time.Millisecond
			s.poll = time.Millisecond
			s.probe = func(context.Context, string) (int, error) {
				if broken == "http" {
					return 503, errors.New("unavailable")
				}
				return 200, nil
			}
			result := s.validateBaseline(context.Background(), kubernetes.New(runner), plan{image: "image-a", desiredReplicas: 1, readinessURL: "http://unused"})
			expected := model.StatusBlocked
			if broken == "api" {
				expected = model.StatusError
			}
			if result.Status != expected {
				t.Fatalf("%s baseline accepted: %+v", broken, result)
			}
		})
	}
}

func TestTopologyPreservesDefaultsAndSameEvidenceMeaning(t *testing.T) {
	for _, replicas := range []int32{1, 2} {
		a := verificationAnalysis([]model.Endpoint{{Purpose: "readiness", Path: "/ready", Port: "8080", Protocol: "HTTP"}})
		a.Application.Kubernetes.Deployments[0].Replicas = &replicas
		current, err := buildPlan(a, "test")
		if err != nil {
			t.Fatal(err)
		}
		topology := current.topology
		if topology.Origin != "source" || topology.ReplicaOrigin != "source" || topology.Replicas != replicas || *topology.MaxSurge != "25%" || topology.Probes[0].PeriodSeconds != 10 {
			t.Fatalf("effective defaults lost: %+v", topology)
		}
		outcome := lifecycleFailure("graceful-shutdown", "title", "id", "old", "guidance", 11911, trafficObservation{Requests: 54, Failures: 44, MaxDowntimeMS: 11911}, nil)
		qualifyTopology(&outcome, current)
		if outcome.Evidence.Status != model.StatusFail || !strings.Contains(outcome.Evidence.Summary, "44/54") {
			t.Fatal("topology changed valid failed evidence")
		}
		if strings.Contains(outcome.Evidence.Summary, "only replica") != (replicas == 1) {
			t.Fatal("topology attribution mismatch")
		}
	}
	a := verificationAnalysis(nil)
	a.Application.Kubernetes.Deployments = nil
	current, err := buildPlan(a, "test")
	if err != nil {
		t.Fatal(err)
	}
	if current.topology.Origin != "generated" || current.topology.Replicas != 1 || current.topology.ReadinessOrigin != "generated" {
		t.Fatal("generated topology undisclosed")
	}
}

func TestAvailabilityAndSemanticProbesUseFreshConnections(t *testing.T) {
	var mu sync.Mutex
	addresses := map[string]bool{}
	allClosed := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		addresses[r.RemoteAddr] = true
		allClosed = allClosed && r.Close
		mu.Unlock()
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	}))
	defer server.Close()
	probes := []probeFunc{httpProbe(directHTTPClient()), newReadinessChecker(directHTTPClient(), model.ReadinessAcceptance{Status: 200, JSON: map[string]string{"status": "ready"}}).probe}
	for _, probe := range probes {
		for range 2 {
			status, err := probe(context.Background(), server.URL)
			if err != nil || status != 200 {
				t.Fatalf("probe failed: %d %v", status, err)
			}
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(addresses) != 4 || !allClosed {
		t.Fatal("Service availability reused a previously selected backend connection")
	}
}
