package verification

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/noor15102002/cloud-forge/internal/analyzer"
	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/internal/executor/kubernetes"
	"github.com/noor15102002/cloud-forge/internal/regression"
	"github.com/noor15102002/cloud-forge/pkg/model"
	networkingv1 "k8s.io/api/networking/v1"
)

const workerTestConfiguration = `schema_version: v1alpha6
runtime: {kind: worker}
worker:
  command: [node, worker-contract-marker.js]
  heartbeat: {key: contract-key-marker, timestamp_field: at, max_age: 30s}
topology:
  replicas: 1
  rollout: {strategy: recreate}
network: {outbound: declared_dependencies_only}
dependencies:
  redis: {enabled: true}
environment:
  REDIS_URL: {from: dependency.redis.url}
`

func workerFixture(t *testing.T) (string, plan) {
	t.Helper()
	root := t.TempDir()
	for name, value := range map[string]string{"cloudforge.yaml": workerTestConfiguration, "package.json": `{"name":"worker-reference","dependencies":{"redis":"5.0.0"}}`, "Dockerfile": "FROM node:24-alpine\nUSER node\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	config, err := LoadConfiguration(root, "")
	if err != nil {
		t.Fatal(err)
	}
	analysis, err := analyzer.New().Analyze(root)
	if err != nil {
		t.Fatal(err)
	}
	current, err := buildConfiguredPlan(analysis, "0123abcd", config)
	if err != nil {
		t.Fatal(err)
	}
	return root, current
}

type workerRunner struct {
	t                                              *testing.T
	mu                                             sync.Mutex
	base                                           *backendIntegrationRunner
	current                                        plan
	active                                         bool
	boot, tick, podReads, beats, expiry, scaleZero int
	image, mode                                    string
	cancel                                         context.CancelFunc
	importedB                                      bool
	blockNext                                      bool
}

func newWorkerRunner(t *testing.T, current plan, mode string) *workerRunner {
	return &workerRunner{t: t, current: current, image: current.image, mode: mode, base: &backendIntegrationRunner{t: t, current: current, policies: map[string]networkingv1.NetworkPolicy{}, providers: map[string]bool{}}}
}
func (r *workerRunner) start() {
	if r.active || r.expiry > 0 {
		r.t.Fatal("replacement started before predecessor termination and heartbeat expiry")
	}
	r.active = true
	r.boot++
	r.beats = 0
	r.podReads = 0
}
func (r *workerRunner) Run(ctx context.Context, request command.Request) model.CommandResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := r.base.Run(ctx, request)
	args := request.Args
	if request.Name == "k3d" && containsArgument(args, "import") && containsArgument(args, r.current.rolloutImage) {
		r.importedB = true
	}
	if request.Name != "kubectl" {
		return result
	}
	for _, provider := range enabledProviders(r.current.config) {
		for _, arg := range args {
			if strings.HasPrefix(arg, "cloudforge.dev/dependency="+provider) {
				result.Stdout = fmt.Sprintf(`{"items":[{"metadata":{"name":%q},"spec":{"containers":[{"image":%q}]},"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}`, providerName(provider), providerFingerprint(provider).Image)
			}
		}
	}
	if containsArgument(args, "apply") && strings.HasSuffix(args[len(args)-1], "workload.yaml") {
		r.image = r.current.image
		r.start()
	}
	if containsArgument(args, "scale") {
		if containsArgument(args, "--replicas=0") {
			r.active = false
			r.expiry = 2
			r.scaleZero++
		} else {
			r.start()
		}
	}
	if containsArgument(args, "set") && containsArgument(args, "image") {
		if r.active {
			r.t.Fatal("changed worker image while predecessor was running")
		}
		r.image = strings.TrimPrefix(args[len(args)-1], "application=")
	}
	if containsArgument(args, "get") && containsArgument(args, "deployment") {
		result.Stdout = fmt.Sprintf(`{"metadata":{"uid":"deployment-1","generation":1,"annotations":{"deployment.kubernetes.io/revision":"1"}},"spec":{"replicas":1,"template":{"spec":{"containers":[{"image":%q}]}}},"status":{"observedGeneration":1}}`, r.image)
	}
	if containsArgument(args, "replicasets") {
		result.Stdout = fmt.Sprintf(`{"items":[{"metadata":{"uid":"replicaset-1","annotations":{"deployment.kubernetes.io/revision":"1"},"ownerReferences":[{"kind":"Deployment","uid":"deployment-1","controller":true}]},"spec":{"template":{"spec":{"containers":[{"image":%q}]}}}}]}`, r.image)
	}
	if containsArgument(args, "pods") && containsArgument(args, workerSelector(r.current)) {
		if r.blockNext {
			r.blockNext = false
			<-ctx.Done()
			return model.CommandResult{ExitCode: -1, FailureType: model.FailureTimeout}
		}
		r.podReads++
		result.Stdout = `{"items":[]}`
		if r.active || r.mode == "overlap-empty" {
			restart := 0
			started := "2050-01-01T00:00:00Z"
			if r.mode == "restart" && r.podReads > 1 || r.mode == "restart-after-beat" && r.beats >= 2 || r.mode == "import-restart" && r.importedB {
				restart = 1
			}
			if r.mode == "instance-change" && r.podReads > 1 {
				started = "2050-01-01T00:00:01Z"
			}
			pod := fmt.Sprintf(`{"metadata":{"name":"worker-%d","uid":"pod-%d","ownerReferences":[{"kind":"ReplicaSet","uid":"replicaset-1","controller":true}]},"spec":{"containers":[{"name":"application","image":%q}]},"status":{"conditions":[{"type":"Ready","status":"True"}],"containerStatuses":[{"name":"application","restartCount":%d,"state":{"running":{"startedAt":%q}}}]}}`, r.boot, r.boot, r.image, restart, started)
			if r.mode == "overlap" || r.mode == "overlap-empty" {
				pod += "," + pod
			}
			result.Stdout = `{"items":[` + pod + `]}`
		}
		if r.cancel != nil && (r.mode == "cancel-startup" && r.active || r.mode == "cancel-recovery" && r.boot == 2) {
			r.cancel()
			return model.CommandResult{ExitCode: -1, FailureType: model.FailureCanceled}
		}
	}
	if containsArgument(args, "EVAL") {
		if r.mode == "observer-error" {
			return model.CommandResult{ExitCode: -1, FailureType: model.FailureTimeout}
		}
		r.tick++
		now := time.Date(2050, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(r.tick) * time.Second)
		present := r.active || r.expiry > 0
		if !r.active && r.expiry > 0 {
			r.expiry--
		}
		if r.active {
			r.beats++
		}
		timestamp := now.Add(-time.Second)
		ttl := int64(3000)
		mode := r.mode
		if mode == "fail-second" && r.boot == 2 || mode == "first-only" && r.boot > 1 {
			mode = "missing"
		}
		if mode == "missing" {
			present = false
			r.expiry = 0
		}
		if mode == "stale" {
			timestamp = now.Add(-time.Minute)
		}
		if mode == "frozen" {
			timestamp = time.Date(2050, 1, 1, 0, 0, 0, 0, time.UTC)
		}
		if mode == "persistent" {
			ttl = -1
		}
		if mode == "future" {
			timestamp = now.Add(3 * time.Second)
		}
		if mode == "malformed" {
			result.Stdout = `invalid observer JSON`
			return result
		}
		if !present {
			ttl = -2
		}
		value := fmt.Sprintf(`{"at":%q,"private":"payload-marker"}`, timestamp.Format(time.RFC3339Nano))
		body, _ := json.Marshal(map[string]any{"seconds": fmt.Sprint(now.Unix()), "microseconds": "0", "ttl_ms": ttl, "present": present, "value": value})
		result.Stdout = string(body)
	}
	return result
}
func workerTestService(r *workerRunner) *Service {
	s := New(r)
	s.newID = func() (string, error) { return "0123abcd", nil }
	s.workerPoll = time.Millisecond
	s.readinessTimeout = 200 * time.Millisecond
	s.recoveryTimeout = 200 * time.Millisecond
	s.baselineTimeout = 200 * time.Millisecond
	return s
}

func TestWorkerRuntimeRestoresContinuesAndRetainsFailure(t *testing.T) {
	for _, mode := range []string{"healthy", "fail-second", "first-only", "import-restart"} {
		t.Run(mode, func(t *testing.T) {
			root, current := workerFixture(t)
			runner := newWorkerRunner(t, current, mode)
			out := workerTestService(runner).Run(context.Background(), root, testOptions())
			for _, id := range []string{"container-build", "dependency.redis", "network-isolation", "worker-startup"} {
				e := evidenceByID(out.Run.Evidence, id)
				if e == nil || e.Status != model.StatusPass {
					t.Fatalf("%s: evidence=%+v diagnostics=%+v", id, e, out.Run.Diagnostics)
				}
			}
			recovery := evidenceByID(out.Run.Evidence, "worker-recovery")
			replacement := evidenceByID(out.Run.Evidence, "worker-image-replacement")
			if recovery == nil || replacement == nil {
				t.Fatalf("worker evidence incomplete: %+v", out.Run.Evidence)
			}
			switch mode {
			case "healthy":
				if out.Run.Status != model.StatusPass || recovery.Status != model.StatusPass || replacement.Status != model.StatusPass || runner.boot != 5 {
					t.Fatalf("healthy worker failed: %+v", out.Run.Evidence)
				}
			case "fail-second":
				if out.Run.Status != model.StatusFail || recovery.Status != model.StatusFail || recovery.Recovery == nil || recovery.Recovery.Status != model.StatusPass || replacement.Status != model.StatusPass || runner.boot != 5 {
					t.Fatalf("failure erased or continuation blocked: %+v", out.Run.Evidence)
				}
			case "first-only":
				if out.Run.Status != model.StatusFail || recovery.Status != model.StatusFail || recovery.Recovery == nil || recovery.Recovery.Status != model.StatusBlocked || replacement.Status != model.StatusBlocked || replacement.Execution.Executed {
					t.Fatalf("residual heartbeat accepted: %+v", out.Run.Evidence)
				}
			case "import-restart":
				if replacement.Status != model.StatusBlocked || replacement.Execution.Executed || runner.scaleZero != 2 {
					t.Fatalf("image import disruption attributed to replacement: %+v", replacement)
				}
			}
			for _, e := range []*model.Evidence{recovery, replacement} {
				if e.Status == model.StatusPass {
					w := e.Worker
					if w == nil || len(w.Samples) != 2 || w.Samples[0].Timestamp >= w.Samples[1].Timestamp || !w.KeyAbsentBeforeStart || !w.PredecessorTerminated || !w.PreviousHeartbeatGone || w.PodUID == w.PreviousPodUID || w.MaximumRunningPods != 1 || w.ContainerRestarts != 0 || e.Recovery == nil || e.Recovery.Worker == nil || e.Recovery.Worker.PodUID == w.PodUID {
						t.Fatalf("missing identity/expiry/progress/restoration proof: %+v", e)
					}
				}
			}
			for _, id := range []string{"deployment-readiness", "semantic-readiness", "graceful-shutdown", "pod-recovery", "rolling-deployment", "load-profile", "horizontal-autoscaling"} {
				e := evidenceByID(out.Run.Evidence, id)
				if e == nil || e.Status != model.StatusSkipped {
					t.Fatalf("HTTP evidence invented for worker: %s %+v", id, e)
				}
			}
			for _, call := range runner.base.calls {
				if call.Name == "k6" || containsArgument(call.Args, "127.0.0.1:0:30080@server:0") {
					t.Fatal("worker requires HTTP tool/port")
				}
			}
			if !hasCommand(runner.base.calls, "k3d", "delete") {
				t.Fatal("cluster cleanup missing")
			}
			for _, err := range runner.base.cleanupErrors {
				if err != nil {
					t.Fatal("cleanup inherited cancellation")
				}
			}
			body, err := json.Marshal(out.Run)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"contract-key-marker", "worker-contract-marker.js", "payload-marker"} {
				if strings.Contains(string(body), secret) {
					t.Fatalf("raw worker contract leaked: %s", secret)
				}
			}
			path := filepath.Join(t.TempDir(), "report.json")
			if os.WriteFile(path, body, 0600) != nil {
				t.Fatal("report write")
			}
			if _, err := regression.Load(path); err != nil {
				t.Fatalf("native worker report failed strict reload: %v", err)
			}
		})
	}
}

func TestWorkerObservedFailuresAndUnreliableObservationsStayDistinct(t *testing.T) {
	for mode, want := range map[string]model.Status{"missing": model.StatusFail, "stale": model.StatusFail, "frozen": model.StatusFail, "persistent": model.StatusFail, "restart": model.StatusFail, "instance-change": model.StatusFail, "restart-after-beat": model.StatusFail, "overlap": model.StatusFail, "future": model.StatusError, "malformed": model.StatusError, "observer-error": model.StatusError} {
		t.Run(mode, func(t *testing.T) {
			_, current := workerFixture(t)
			r := newWorkerRunner(t, current, mode)
			r.start()
			result := workerTestService(r).waitWorker(context.Background(), kubernetes.New(r), current, current.image, 40*time.Millisecond, model.WorkerObservation{})
			if result.status != want {
				t.Fatalf("%s = %s, want %s: %s", mode, result.status, want, result.summary)
			}
			if want == model.StatusFail && (mode == "missing" || mode == "stale" || mode == "frozen" || mode == "persistent") && (!result.deadline || !result.finalObserved) {
				t.Fatalf("configured deadline classified as cancellation or missing final state: %+v", result)
			}
		})
	}
}

func TestWorkerDeadlineFinalObservationAndRootCancellation(t *testing.T) {
	_, current := workerFixture(t)
	t.Run("ordinary observation deadline", func(t *testing.T) {
		r := newWorkerRunner(t, current, "missing")
		r.start()
		r.blockNext = true
		got := workerTestService(r).waitWorker(context.Background(), kubernetes.New(r), current, current.image, time.Millisecond, model.WorkerObservation{})
		if got.status != model.StatusFail || !got.deadline || !got.finalObserved || got.observation.HeartbeatState != "missing" {
			t.Fatalf("ordinary deadline became cancellation: %+v", got)
		}
	})
	t.Run("overlap before stop never accepted", func(t *testing.T) {
		r := newWorkerRunner(t, current, "overlap-empty")
		got := workerTestService(r).waitWorkerEmpty(context.Background(), kubernetes.New(r), current, time.Second, model.WorkerObservation{})
		if got.status != model.StatusFail || got.observation.MaximumRunningPods != 2 || r.tick != 0 {
			t.Fatalf("overlap discarded: %+v", got)
		}
	})
	for _, mode := range []string{"cancel-startup", "cancel-recovery"} {
		t.Run(mode, func(t *testing.T) {
			root, current := workerFixture(t)
			r := newWorkerRunner(t, current, mode)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r.cancel = cancel
			out := workerTestService(r).Run(ctx, root, testOptions())
			if out.Run.Status != model.StatusError || !hasDiagnosticCode(out.Run.Diagnostics, "verification_canceled") || !hasCommand(r.base.calls, "k3d", "delete") {
				t.Fatalf("cancellation/cleanup lost: %+v", out.Run)
			}
			later := evidenceByID(out.Run.Evidence, "worker-image-replacement")
			if later == nil || later.Execution.Executed {
				t.Fatalf("scheduled worker after cancellation: %+v", later)
			}
			if mode == "cancel-recovery" && evidenceByID(out.Run.Evidence, "worker-startup").Status != model.StatusPass {
				t.Fatal("prior evidence discarded")
			}
			for _, err := range r.base.cleanupErrors {
				if err != nil {
					t.Fatal("cleanup reused canceled context")
				}
			}
		})
	}
}

func TestWorkerFinalDeadlineSnapshotKeepsClocksAndEvidence(t *testing.T) {
	for _, mode := range []string{"stale", "frozen", "healthy", "missing"} {
		t.Run(mode, func(t *testing.T) {
			_, current := workerFixture(t)
			r := newWorkerRunner(t, current, mode)
			r.start()
			s := workerTestService(r)
			earlier := time.Date(2050, 1, 1, 0, 0, 0, 0, time.UTC)
			got := s.workerDeadline(context.Background(), kubernetes.New(r), current, current.image, model.WorkerObservation{}, earlier, earlier, time.Now())
			want := model.StatusFail
			if mode == "healthy" {
				want = model.StatusFail /* one fresh final sample equals the prior timestamp */
			}
			if mode == "stale" {
				want = model.StatusError /* timestamp rollback is clock uncertainty */
			}
			if got.status != want || !got.finalObserved || got.observation.RedisObservedAt == "" {
				t.Fatalf("lost final observation/classification: %+v", got)
			}
			if mode != "missing" && (len(got.observation.Samples) != 1 || got.observation.Samples[0].RedisTime == "") {
				t.Fatal("final_observed lacks normalized final sample")
			}
		})
	}
	t.Run("Redis clock rollback is ERROR", func(t *testing.T) {
		_, current := workerFixture(t)
		r := newWorkerRunner(t, current, "healthy")
		r.start()
		future := time.Date(2050, 1, 1, 0, 0, 10, 0, time.UTC)
		got := workerTestService(r).workerDeadline(context.Background(), kubernetes.New(r), current, current.image, model.WorkerObservation{}, time.Time{}, future, time.Now())
		if got.status != model.StatusError || !got.finalObserved {
			t.Fatalf("clock uncertainty became application failure: %+v", got)
		}
	})
	t.Run("new progress only after deadline is ERROR", func(t *testing.T) {
		_, current := workerFixture(t)
		r := newWorkerRunner(t, current, "healthy")
		r.start()
		got := workerTestService(r).workerDeadline(context.Background(), kubernetes.New(r), current, current.image, model.WorkerObservation{}, time.Time{}, time.Time{}, time.Now())
		if got.status != model.StatusError || got.observation.HeartbeatState != "late_progress" {
			t.Fatalf("late first progress became PASS: %+v", got)
		}
	})
	t.Run("absence only after stop deadline never starts worker", func(t *testing.T) {
		_, current := workerFixture(t)
		r := newWorkerRunner(t, current, "healthy")
		got := workerTestService(r).workerEmptyDeadline(context.Background(), kubernetes.New(r), current, model.WorkerObservation{PreviousPodUID: "predecessor"}, time.Now())
		if got.status != model.StatusError || r.boot != 0 || !got.finalObserved || got.observation.HeartbeatState != "absent_after_deadline" {
			t.Fatalf("late boundary improperly extended transition: %+v", got)
		}
	})
}

func TestWorkerBaselineReattestsAntivirusFingerprint(t *testing.T) {
	for mode, want := range map[string]model.Status{"same": model.StatusPass, "changed": model.StatusBlocked, "stale": model.StatusBlocked, "unobserved": model.StatusError} {
		t.Run(mode, func(t *testing.T) {
			_, current := workerFixture(t)
			current.config.Dependencies["clamav"] = model.DependencySpec{Enabled: true}
			current.dependencyFingerprints = []model.DependencyFingerprint{{Kind: "clamav", DataVersion: "28000", DataTimestamp: "2026-09-21T09:00:00Z"}}
			r := newWorkerRunner(t, current, "healthy")
			r.start()
			runner := runnerFunc(func(ctx context.Context, request command.Request) model.CommandResult {
				result := r.Run(ctx, request)
				if containsArgument(request.Args, "clamdscan") {
					switch mode {
					case "changed":
						result.Stdout = "ClamAV 1.5.4/28001/Mon Sep 21 09:00:00 2026"
					case "stale":
						result.Stdout = "ClamAV 1.5.4/28000/Thu Sep 17 09:00:00 2026"
					case "unobserved":
						result.Stdout = "malformed"
					}
				}
				return result
			})
			s := workerTestService(r)
			s.now = func() time.Time { return time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC) }
			got := s.waitWorker(context.Background(), kubernetes.New(runner), current, current.image, time.Second, model.WorkerObservation{})
			if got.status != want {
				t.Fatalf("%s fingerprint check=%s want%s: %s", mode, got.status, want, got.summary)
			}
		})
	}
}
