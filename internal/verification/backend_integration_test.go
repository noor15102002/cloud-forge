package verification

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/noor15102002/cloud-forge/internal/analyzer"
	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/internal/executor/kubernetes"
	"github.com/noor15102002/cloud-forge/internal/regression"
	"github.com/noor15102002/cloud-forge/pkg/model"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

type backendIntegrationRunner struct {
	t              *testing.T
	mu             sync.Mutex
	current        plan
	mode           string
	cancel         context.CancelFunc
	calls          []command.Request
	events         []string
	policies       map[string]networkingv1.NetworkPolicy
	secrets        []string
	privatePaths   []string
	preparationEnv []corev1.EnvVar
	applicationEnv []corev1.EnvVar
	cleanupErrors  []error
	providers      map[string]bool
	images         map[string]bool
}

func (r *backendIntegrationRunner) Run(ctx context.Context, request command.Request) model.CommandResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, request)
	result := successfulCommand(request)
	if request.Name == "docker" && containsArgument(request.Args, "build") {
		if r.images == nil {
			r.images = map[string]bool{}
		}
		for index, arg := range request.Args {
			if arg == "--tag" && index+1 < len(request.Args) {
				r.images[request.Args[index+1]] = true
			}
		}
		if containsArgument(request.Args, "CLOUDFORGE_VERSION=b") {
			r.events = append(r.events, "build-b")
			if r.mode == "preparation-failure" {
				return model.CommandResult{Command: "docker", FailureType: model.FailureExit, ExitCode: 1, Stderr: "process /bin/sh -c private-build-failure-marker did not complete successfully: exit code: 1"}
			}
		} else {
			r.events = append(r.events, "build-a")
		}
	}
	if request.Name == "docker" && containsArgument(request.Args, "buildx") && containsArgument(request.Args, "rm") {
		r.events = append(r.events, "builder-stop")
		r.cleanupErrors = append(r.cleanupErrors, ctx.Err())
	}
	if request.Name == "docker" && strings.Contains(strings.Join(request.Args, " "), ".RepoDigests") {
		result.Stdout = `"sha256:` + strings.Repeat("a", 64) + `" []`
	}
	if request.Name == "docker" && len(request.Args) > 1 && request.Args[0] == "image" && request.Args[1] == "inspect" && strings.Contains(strings.Join(request.Args, " "), "cloudforge.dev/ownership") && r.images[request.Args[len(request.Args)-1]] {
		result.ExitCode, result.FailureType, result.Stderr = 0, model.FailureNone, ""
		letter := "a"
		if strings.HasSuffix(request.Args[len(request.Args)-1], "-b") {
			letter = "b"
		}
		result.Stdout = `"sha256:` + strings.Repeat(letter, 64) + `" true`
	}
	if request.Name == "k3d" && containsArgument(request.Args, "create") {
		r.events = append(r.events, "cluster-create")
	}
	if request.Name == "k3d" && containsArgument(request.Args, "delete") {
		r.events = append(r.events, "cluster-cleanup")
		r.cleanupErrors = append(r.cleanupErrors, ctx.Err())
	}
	if request.Name == "docker" && containsArgument(request.Args, "image") && containsArgument(request.Args, "rm") {
		for name := range r.images {
			letter := "a"
			if strings.HasSuffix(name, "-b") {
				letter = "b"
			}
			if request.Args[len(request.Args)-1] == "sha256:"+strings.Repeat(letter, 64) {
				r.images[name] = false
			}
		}
		r.events = append(r.events, "image-cleanup")
		r.cleanupErrors = append(r.cleanupErrors, ctx.Err())
	}
	if request.Name == "kubectl" && containsArgument(request.Args, "apply") {
		filename := request.Args[len(request.Args)-1]
		r.privatePaths = append(r.privatePaths, filename)
		// #nosec G304 -- test reads a generated manifest from its private temporary workspace.
		body, err := os.ReadFile(filename)
		if err != nil {
			r.t.Fatal(err)
		}
		for _, document := range strings.Split(string(body), "---") {
			var metadata metav1.TypeMeta
			if yaml.Unmarshal([]byte(document), &metadata) != nil {
				r.t.Fatal("invalid generated document")
			}
			switch metadata.Kind {
			case "Secret":
				var secret corev1.Secret
				if yaml.Unmarshal([]byte(document), &secret) != nil {
					r.t.Fatal("invalid generated Secret")
				}
				for _, value := range secret.StringData {
					if len(value) >= 32 {
						r.secrets = append(r.secrets, value)
					}
				}
			case "NetworkPolicy":
				var policy networkingv1.NetworkPolicy
				if yaml.Unmarshal([]byte(document), &policy) != nil {
					r.t.Fatal("invalid generated NetworkPolicy")
				}
				r.policies[policy.Name] = policy
			case "Job":
				var job batchv1.Job
				if yaml.Unmarshal([]byte(document), &job) != nil {
					r.t.Fatal("invalid generated Job")
				}
				r.events = append(r.events, "preparation")
				r.preparationEnv = job.Spec.Template.Spec.Containers[0].Env
				if job.Spec.Template.Spec.Containers[0].Image != r.current.image {
					r.t.Fatal("preparation used a different image")
				}
			case "Deployment":
				var deployment appsv1.Deployment
				if yaml.Unmarshal([]byte(document), &deployment) != nil {
					r.t.Fatal("invalid generated Deployment")
				}
				if deployment.Name == r.current.workloadName {
					r.events = append(r.events, "application")
					r.applicationEnv = deployment.Spec.Template.Spec.Containers[0].Env
					if r.mode == "cancel-application" {
						r.cancel()
						return model.CommandResult{Command: request.Name, FailureType: model.FailureCanceled, ExitCode: -1}
					}
				}
			}
		}
		if filepath.Base(filename) == "network-runtime.yaml" {
			r.events = append(r.events, "network-revoked")
		}
	}
	if request.Name == "kubectl" && containsArgument(request.Args, "networkpolicies") {
		r.events = append(r.events, "network-confirmed")
		list := networkingv1.NetworkPolicyList{}
		for _, policy := range r.policies {
			list.Items = append(list.Items, policy)
		}
		body, _ := json.Marshal(list)
		result.Stdout = string(body)
	}
	if request.Name == "kubectl" && containsArgument(request.Args, "clamdscan") {
		r.events = append(r.events, "clamav-attested")
		result.Stdout = "ClamAV 1.5.4/28000/Mon Sep 21 09:00:00 2026\n"
	}
	if request.Name == "kubectl" && (containsArgument(request.Args, "job") || containsArgument(request.Args, "--selector=batch.kubernetes.io/job-name="+preparationName)) {
		mode := "complete"
		if r.mode == "preparation-failure" {
			mode = "exit-failure"
		}
		return preparationTestResponse(r.current, mode, request)
	}
	for _, provider := range enabledProviders(r.current.config) {
		if containsArgument(request.Args, "cloudforge.dev/dependency="+provider) {
			r.providers[provider] = true
			r.events = append(r.events, "provider-ready:"+provider)
			result.Stdout = fmt.Sprintf(`{"items":[{"metadata":{"name":%q},"spec":{"containers":[{"image":%q}]},"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}`, providerName(provider), providerFingerprint(provider).Image)
		}
	}
	return result
}

func backendIntegrationFixture(t *testing.T) (string, plan) {
	t.Helper()
	root := t.TempDir()
	config := backendTestConfig()
	config.Runtime.Port = 8080
	config.Endpoints = model.EndpointSettings{Health: "/health", Readiness: "/ready"}
	config.Topology = &model.TopologySettings{Replicas: 2}
	config.Preparation.Command = []string{"node", "private-preparation-marker.js"}
	encoded, err := yaml.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string][]byte{
		"cloudforge.yaml": encoded,
		"package.json":    []byte(`{"name":"backend-integration"}`),
		"Dockerfile":      []byte("FROM node:24-alpine\nUSER node\nEXPOSE 8080\n"),
	} {
		if os.WriteFile(filepath.Join(root, name), value, 0o600) != nil {
			t.Fatal("write test fixture")
		}
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

func TestBackendIntegrationStagesBuildsBeforeClusterAndCleansAfterCancellation(t *testing.T) {
	root, current := backendIntegrationFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := &backendIntegrationRunner{t: t, current: current, mode: "cancel-application", cancel: cancel, policies: map[string]networkingv1.NetworkPolicy{}, providers: map[string]bool{}}
	service := New(clusterProvisionFixture(runner))
	service.newID = func() (string, error) { return "0123abcd", nil }
	service.now = func() time.Time { return time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC) }
	service.backendCapacity = func(context.Context, command.Runner, *Outcome) bool {
		runner.events = append(runner.events, "capacity")
		return true
	}
	// #nosec G304 -- root is a test-owned fixture copy.
	before, err := os.ReadFile(filepath.Join(root, "cloudforge.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	out := service.Run(ctx, root, testOptions())
	if out.Run.Status != model.StatusError || !hasDiagnosticCode(out.Run.Diagnostics, "verification_canceled") {
		t.Fatalf("expected cancellation after qualified preparation: %+v", out.Run.Diagnostics)
	}
	for _, id := range []string{"container-build", "rollout-image-build", "application-preparation", "dependency.clamav", "dependency.postgresql", "dependency.redis", "network-isolation"} {
		evidence := evidenceByID(out.Run.Evidence, id)
		if evidence == nil || evidence.Status != model.StatusPass {
			t.Fatalf("completed %s evidence was lost; diagnostics=%+v events=%v", id, out.Run.Diagnostics, runner.events)
		}
	}
	if out.Run.Fingerprint == nil || out.Run.Fingerprint.CompatibilityKey == "" {
		t.Fatal("completed provider attestation did not finish the environment fingerprint")
	}
	position := func(event string) int {
		for index, observed := range runner.events {
			if event == observed {
				return index
			}
		}
		t.Fatalf("stage %s was not reached: %v", event, runner.events)
		return -1
	}
	previous := -1
	for _, event := range []string{"capacity", "build-a", "build-b", "builder-stop", "cluster-create", "provider-ready:clamav", "clamav-attested", "provider-ready:postgresql", "provider-ready:redis", "network-revoked", "network-confirmed", "preparation", "application", "cluster-cleanup", "image-cleanup"} {
		next := position(event)
		if next <= previous {
			t.Fatalf("incorrect phase order near %s: %v", event, runner.events)
		}
		previous = next
	}
	if !reflect.DeepEqual(runner.preparationEnv, runner.applicationEnv) || len(runner.applicationEnv) == 0 {
		t.Fatal("preparation and application did not share exactly the same generated configuration references")
	}
	for _, env := range runner.applicationEnv {
		if env.Value != "" || env.ValueFrom == nil || env.ValueFrom.SecretKeyRef == nil || env.ValueFrom.SecretKeyRef.Name != applicationSecretName {
			t.Fatal("runtime configuration values escaped Secret references")
		}
	}
	for _, err := range runner.cleanupErrors {
		if err != nil {
			t.Fatal("canceled execution context contaminated cleanup")
		}
	}
	for _, path := range runner.privatePaths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("private preparation/environment material survived cleanup")
		}
	}
	for _, request := range runner.calls {
		if containsArgument(request.Args, "prune") || containsArgument(request.Args, "logs") || containsArgument(request.Args, "use-context") {
			t.Fatal("runtime escaped owned cleanup or read application logs")
		}
		if request.Name == "kubectl" && containsArgument(request.Args, "apply") && !hasEnvPrefix(request.Env, "KUBECONFIG=") {
			t.Fatal("runtime apply used the user's kubectl configuration")
		}
	}
	// #nosec G304 -- verify the same test-owned configuration was not mutated.
	after, _ := os.ReadFile(filepath.Join(root, "cloudforge.yaml"))
	if string(before) != string(after) {
		t.Fatal("runtime altered source configuration")
	}
	encoded, _ := json.Marshal(out.Run)
	for _, secret := range runner.secrets {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("generated credential entered the report")
		}
	}
	if strings.Contains(string(encoded), "private-preparation-marker") {
		t.Fatal("preparation command entered the report")
	}
	file := filepath.Join(t.TempDir(), "backend-report.json")
	if err := os.WriteFile(file, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := regression.Load(file); err != nil {
		t.Fatalf("real orchestration report failed the public schema: %v", err)
	}
}

func hasEnvPrefix(values []string, prefix string) bool {
	for _, value := range values {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func TestBackendIntegrationPreservesPrebuiltFailureBeforePreparationBlocks(t *testing.T) {
	root, current := backendIntegrationFixture(t)
	runner := &backendIntegrationRunner{t: t, current: current, mode: "preparation-failure", policies: map[string]networkingv1.NetworkPolicy{}, providers: map[string]bool{}}
	service := New(clusterProvisionFixture(runner))
	service.newID = func() (string, error) { return "0123abcd", nil }
	service.now = func() time.Time { return time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC) }
	service.backendCapacity = func(context.Context, command.Runner, *Outcome) bool { return true }
	out := service.Run(context.Background(), root, testOptions())
	for _, id := range []string{"rollout-image-build", "application-preparation"} {
		evidence := evidenceByID(out.Run.Evidence, id)
		if evidence == nil || evidence.Status != model.StatusFail || evidence.Execution == nil || !evidence.Execution.Executed {
			t.Fatalf("already observed %s failure disappeared: %+v", id, out.Run.Evidence)
		}
	}
	for _, event := range runner.events {
		if event == "application" {
			t.Fatal("application ran after preparation failed")
		}
	}
	if !hasCommand(runner.calls, "k3d", "delete") || out.Run.Status == model.StatusPass || out.Run.Status == model.StatusWarn {
		t.Fatal("failed preparation hid failures or bypassed cleanup")
	}
}

func TestBackendBaselineRequiresEveryEnabledProvider(t *testing.T) {
	for _, unavailable := range []string{"", "clamav", "postgresql", "redis", "clamav_signatures"} {
		t.Run("unavailable-"+unavailable, func(t *testing.T) {
			_, current := backendIntegrationFixture(t)
			current.readinessURL = "http://127.0.0.1/ready"
			current.dependencyFingerprints = []model.DependencyFingerprint{{Kind: "clamav", DataVersion: "28000", DataTimestamp: "2026-09-21T09:00:00Z"}}
			seen := map[string]bool{}
			runner := runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
				result := model.CommandResult{}
				switch {
				case containsArgument(request.Args, "deployment"):
					result.Stdout = fmt.Sprintf(`{"metadata":{"uid":"deployment","generation":1,"annotations":{"deployment.kubernetes.io/revision":"1"}},"spec":{"replicas":2,"template":{"spec":{"containers":[{"image":%q}]}}},"status":{"observedGeneration":1}}`, current.image)
				case containsArgument(request.Args, "replicasets"):
					result.Stdout = fmt.Sprintf(`{"items":[{"metadata":{"uid":"rs","annotations":{"deployment.kubernetes.io/revision":"1"},"ownerReferences":[{"kind":"Deployment","uid":"deployment","controller":true}]},"spec":{"template":{"spec":{"containers":[{"image":%q}]}}}}]}`, current.image)
				case containsArgument(request.Args, "clamdscan"):
					result.Stdout = "ClamAV 1.5.4/28000/Mon Sep 21 09:00:00 2026"
					if unavailable == "clamav_signatures" {
						result.Stdout = "ClamAV 1.5.4/28001/Mon Sep 21 09:00:00 2026"
					}
				case containsArgument(request.Args, "pods"):
					result.Stdout = fmt.Sprintf(`{"items":[{"metadata":{"name":"observed-pod","ownerReferences":[{"kind":"ReplicaSet","uid":"rs","controller":true}]},"spec":{"containers":[{"image":%q}]},"status":{"conditions":[{"type":"Ready","status":"True"}]}},{"metadata":{"name":"observed-pod","ownerReferences":[{"kind":"ReplicaSet","uid":"rs","controller":true}]},"spec":{"containers":[{"image":%q}]},"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}`, current.image, current.image)
					for _, name := range enabledProviders(current.config) {
						if containsArgument(request.Args, "cloudforge.dev/dependency="+name) {
							seen[name] = true
							ready := "True"
							if name == unavailable {
								ready = "False"
							}
							result.Stdout = fmt.Sprintf(`{"items":[{"metadata":{"name":%q},"spec":{"containers":[{"image":%q}]},"status":{"conditions":[{"type":"Ready","status":%q}]}}]}`, providerName(name), providerFingerprint(name).Image, ready)
						}
					}
				}
				return result
			})
			service := New(runner)
			service.probe = func(context.Context, string) (int, error) { return 200, nil }
			service.now = func() time.Time { return time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC) }
			service.poll = time.Millisecond
			service.baselineTimeout = 20 * time.Millisecond
			result := service.validateBaseline(context.Background(), kubernetes.New(runner), current)
			if (result.Status == model.StatusPass) != (unavailable == "") || len(seen) != 3 {
				t.Fatalf("baseline skipped required provider health: %+v seen=%v", result, seen)
			}
			if unavailable != "" {
				found := false
				for _, check := range result.Checks {
					found = found || check.Name == "dependency."+unavailable && check.Status == model.StatusBlocked
				}
				if !found {
					t.Fatal("baseline did not identify the blocked provider")
				}
			}
		})
	}
}

func TestBackendFingerprintRequiresActualSignatureIdentity(t *testing.T) {
	_, current := backendIntegrationFixture(t)
	fingerprint := newFingerprint(context.Background(), successRunner(), ".", current, testOptions())
	fingerprint.ImageID = "sha256:" + strings.Repeat("b", 64)
	for _, name := range []string{"docker", "k3d", "kubectl", "kubernetes", "k6", "trivy"} {
		fingerprint.Tools = append(fingerprint.Tools, model.ToolVersion{Name: name, Version: "1.0.0"})
	}
	completeFingerprint(fingerprint)
	if fingerprint.CompatibilityKey != "" {
		t.Fatal("unobserved signature database established a compatible baseline")
	}
	for index := range fingerprint.Dependencies {
		if fingerprint.Dependencies[index].Kind == "clamav" {
			fingerprint.Dependencies[index].DataVersion = "28000"
			fingerprint.Dependencies[index].DataTimestamp = "2026-09-21T09:00:00Z"
		}
	}
	completeFingerprint(fingerprint)
	first := fingerprint.CompatibilityKey
	if first == "" {
		t.Fatal("complete attested environment did not establish compatibility")
	}
	for index := range fingerprint.Dependencies {
		if fingerprint.Dependencies[index].Kind == "clamav" {
			fingerprint.Dependencies[index].DataVersion = "28001"
		}
	}
	completeFingerprint(fingerprint)
	if fingerprint.CompatibilityKey == first {
		t.Fatal("different actual signature databases remained compatible")
	}
}
