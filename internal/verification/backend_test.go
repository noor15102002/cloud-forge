package verification

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/internal/dependency"
	"github.com/noor15102002/cloud-forge/internal/executor/kubernetes"
	"github.com/noor15102002/cloud-forge/pkg/model"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
)

func backendTestConfig() model.RuntimeConfiguration {
	config := defaultConfiguration()
	config.SchemaVersion = "v1alpha5"
	config.Safety = &model.SafetySettings{Profile: "bounded_backend"}
	config.Network = &model.NetworkSettings{Outbound: "declared_dependencies_only"}
	config.Dependencies = map[string]model.DependencySpec{"postgresql": {Enabled: true}, "redis": {Enabled: true}, "clamav": {Enabled: true}}
	config.Environment = map[string]model.EnvironmentBinding{"DATABASE_URL": {From: "dependency.postgresql.url"}, "TEST_SECRET": {Generate: &model.GeneratedValueSpec{Bytes: 32, Encoding: "hex"}}}
	config.Preparation = &model.PreparationSettings{Command: []string{"node", "prepare.js"}, Timeout: "2m"}
	return config
}

func TestBackendConfigurationBoundsBeforeExecution(t *testing.T) {
	config := backendTestConfig()
	if err := validateConfiguration(config); err != nil {
		t.Fatal(err)
	}
	for _, modify := range []func(*model.RuntimeConfiguration){
		func(c *model.RuntimeConfiguration) { c.SchemaVersion = "v1alpha4" },
		func(c *model.RuntimeConfiguration) { c.Network = nil },
		func(c *model.RuntimeConfiguration) { c.Safety = nil },
		func(c *model.RuntimeConfiguration) { c.Preparation.Command = []string{"bash", "prepare.sh"} },
		func(c *model.RuntimeConfiguration) { c.Preparation.Command = []string{"node", "--eval", "arbitrary"} },
		func(c *model.RuntimeConfiguration) { c.Preparation.Timeout = "6m" },
		func(c *model.RuntimeConfiguration) {
			c.Environment["TEST_SECRET"] = model.EnvironmentBinding{Generate: &model.GeneratedValueSpec{Bytes: 1000, Encoding: "hex"}}
		},
		func(c *model.RuntimeConfiguration) {
			v := "secret"
			c.Environment["TEST_SECRET"] = model.EnvironmentBinding{Value: &v}
		},
		func(c *model.RuntimeConfiguration) {
			c.Environment["DATABASE_URL"] = model.EnvironmentBinding{From: "process.DATABASE_URL"}
		},
	} {
		c := backendTestConfig()
		modify(&c)
		if validateConfiguration(c) == nil {
			t.Fatal("unsafe backend configuration accepted")
		}
	}
	for _, section := range []string{"preparation", "network", "safety"} {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "cloudforge.yaml"), []byte("schema_version: v1alpha5\n"+section+": null\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfiguration(root, ""); err == nil {
			t.Fatal("null section accepted")
		}
	}
}

func TestGeneratedTestSecretsRemainPrivateAndStableWithinManifest(t *testing.T) {
	c := backendTestConfig()
	a, err := generatedEnvironment(c, "one")
	if err != nil {
		t.Fatal(err)
	}
	b, err := generatedEnvironment(c, "two")
	if err != nil {
		t.Fatal(err)
	}
	secretA := a[len(a)-1].(*corev1.Secret).StringData
	secretB := b[len(b)-1].(*corev1.Secret).StringData
	if secretA["TEST_SECRET"] == secretB["TEST_SECRET"] || secretA["DATABASE_URL"] == secretB["DATABASE_URL"] {
		t.Fatal("credentials reused across runs")
	}
	bytes, err := hex.DecodeString(secretA["TEST_SECRET"])
	if err != nil || len(bytes) != 32 {
		t.Fatal("incorrect generated entropy")
	}
	base, err := generateValue(model.GeneratedValueSpec{Bytes: 32, Encoding: "base64"})
	if err != nil {
		t.Fatal(err)
	}
	bytes, err = base64.StdEncoding.DecodeString(base)
	if err != nil || len(bytes) != 32 {
		t.Fatal("incorrect base64")
	}
	public, _ := json.Marshal(safeConfiguration(c))
	if strings.Contains(string(public), secretA["TEST_SECRET"]) || strings.Contains(string(public), "prepare.js") {
		t.Fatal("private configuration escaped")
	}
	env := applicationEnvironment(c)
	if env[0].Name != "DATABASE_URL" || env[1].Name != "TEST_SECRET" {
		t.Fatal("unsorted environment")
	}
	for _, item := range env {
		if item.Value != "" || item.ValueFrom == nil || item.ValueFrom.SecretKeyRef.Name != applicationSecretName {
			t.Fatal("secret in workload manifest")
		}
	}
	first, _ := json.Marshal(safeConfiguration(c))
	second, _ := json.Marshal(safeConfiguration(c))
	if string(first) != string(second) {
		t.Fatal("non-deterministic public configuration")
	}
}

func TestClamDaemonObservationRequiresFreshFullTuple(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	value, stamp, err := parseClamAVVersion("ClamAV 1.5.4/28000/Mon Sep 21 09:00:00 2026\n", now)
	if err != nil || value != "28000" || stamp != "2026-09-21T09:00:00Z" {
		t.Fatalf("valid tuple rejected: %v", err)
	}
	for _, raw := range []string{"ClamAV 1.5.4\n", "not version", "ClamAV 1.5.4/28000/not date"} {
		_, _, err := parseClamAVVersion(raw, now)
		if !errors.Is(err, errUnobservedClam) {
			t.Fatal("unobserved daemon was accepted")
		}
	}
	for _, raw := range []string{"ClamAV 1.5.3/28000/Mon Sep 21 09:00:00 2026", "ClamAV 1.5.4/28000/Thu Sep 17 09:00:00 2026", "ClamAV 1.5.4/28000/Tue Sep 22 09:00:00 2026"} {
		if _, _, err := parseClamAVVersion(raw, now); err == nil {
			t.Fatal("incompatible/stale/future database accepted")
		}
	}
}

func TestObservedPolicyMustIncludeRevocation(t *testing.T) {
	expected := dependency.NetworkObjects(namespace, "test", []string{"postgresql", "redis", "clamav"}, false)
	list := networkingv1.NetworkPolicyList{}
	for _, item := range expected {
		list.Items = append(list.Items, *item.(*networkingv1.NetworkPolicy))
	}
	data, _ := json.Marshal(list)
	if !matchingNetworkPolicies(string(data), expected) {
		t.Fatal("matching policy rejected")
	}
	startup := dependency.NetworkObjects(namespace, "test", []string{"postgresql", "redis", "clamav"}, true)
	for i, item := range startup {
		list.Items[i] = *item.(*networkingv1.NetworkPolicy)
	}
	data, _ = json.Marshal(list)
	if matchingNetworkPolicies(string(data), expected) {
		t.Fatal("unrevoked signature access accepted")
	}
}

func TestBackendProviderObservationCannotInventReadiness(t *testing.T) {
	for _, test := range []struct {
		name   string
		output string
		want   model.Status
	}{
		{"fallback", "ClamAV 1.5.4", model.StatusError},
		{"stale", "ClamAV 1.5.4/28000/Thu Sep 17 09:00:00 2026", model.StatusFail},
		{"ready", "ClamAV 1.5.4/28000/Mon Sep 21 09:00:00 2026", model.StatusPass},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := runnerFunc(func(_ context.Context, r command.Request) model.CommandResult {
				res := successfulCommand(r)
				if containsArgument(r.Args, "pods") {
					res.Stdout = `{"items":[{"metadata":{"name":"clam"},"spec":{"containers":[{"image":"` + dependency.ClamAVImage + `"}]},"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}`
				}
				if containsArgument(r.Args, "exec") {
					res.Stdout = test.output
				}
				return res
			})
			s := New(runner)
			s.now = func() time.Time { return time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC) }
			out := Outcome{}
			p := plan{clusterName: "cloudforge-test", config: backendTestConfig()}
			pass := s.startBackendProvider(context.Background(), kubernetes.New(runner), p, t.TempDir(), "clamav", &out)
			if pass != (test.want == model.StatusPass) || len(out.Run.Dependencies) != 1 || out.Run.Dependencies[0].Status != test.want {
				t.Fatalf("wrong provider evidence: %+v", out)
			}
		})
	}
}

func TestPreparationConfigurationHashChangesWithoutRevealingCommand(t *testing.T) {
	runner := runnerFunc(func(_ context.Context, r command.Request) model.CommandResult { return successfulCommand(r) })
	p := plan{config: backendTestConfig(), clusterName: "cloudforge-one", image: "one", workloadName: "app"}
	a := newFingerprint(context.Background(), runner, ".", p, testOptions())
	p.config.Preparation.Command = []string{"node", "other.js"}
	b := newFingerprint(context.Background(), runner, ".", p, testOptions())
	if a.PreparationHash == b.PreparationHash || !reflect.DeepEqual(a.Configuration.Preparation.Command, []string{"<omitted>"}) {
		t.Fatal("preparation identity missing or exposed")
	}
}

func TestRestrictedBooleanFlagsAreNotConnectionAddresses(t *testing.T) {
	c := backendTestConfig()
	for _, value := range []string{"true", "false"} {
		flag := value
		c.Environment["TRUST_HOST"] = model.EnvironmentBinding{Value: &flag}
		if err := validateConfiguration(c); err != nil {
			t.Fatalf("boolean flag rejected: %v", err)
		}
	}
	external := "production.example"
	c.Environment["TRUST_HOST"] = model.EnvironmentBinding{Value: &external}
	if validateConfiguration(c) == nil {
		t.Fatal("external hostname accepted")
	}
	c = backendTestConfig()
	flag := "true"
	c.Environment["HOST_SECRET"] = model.EnvironmentBinding{Value: &flag}
	if validateConfiguration(c) == nil {
		t.Fatal("credential literal accepted")
	}
}
