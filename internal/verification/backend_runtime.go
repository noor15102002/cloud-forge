package verification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/noor15102002/cloud-forge/internal/dependency"
	"github.com/noor15102002/cloud-forge/internal/executor/kubernetes"
	"github.com/noor15102002/cloud-forge/pkg/model"
	networkingv1 "k8s.io/api/networking/v1"
)

func applyPrivateObjects(ctx context.Context, client *kubernetes.Client, current plan, directory, filename string, objects []any, out *Outcome) bool {
	data, err := marshalDocuments(objects)
	if err != nil {
		out.addError("runtime_manifest_failed", "CloudForge could not encode its isolated runtime configuration.", "No application deployment was attempted.")
		return false
	}
	path := filepath.Join(directory, filename)
	if os.WriteFile(path, data, 0o600) != nil {
		out.addError("runtime_manifest_failed", "CloudForge could not write its private runtime configuration.", "Check workspace access.")
		return false
	}
	if result := client.Apply(ctx, current.clusterName, path); failed(result) {
		out.addCommandDiagnostic("runtime_apply_failed", "CloudForge could not apply its isolated runtime configuration.", result)
		return false
	}
	return true
}

func (s *Service) startDependencies(ctx context.Context, client *kubernetes.Client, current plan, directory string, out *Outcome) bool {
	if advancedConfiguration(current.config) {
		objects, err := generatedEnvironment(current.config, runIdentifier(current))
		if err != nil {
			out.addError("test_configuration_failed", "CloudForge could not generate isolated test configuration.", "No application deployment was attempted.")
			return false
		}
		if !applyPrivateObjects(ctx, client, current, directory, "test-configuration.yaml", objects, out) {
			return false
		}
	}
	if current.config.Network != nil && !applyPrivateObjects(ctx, client, current, directory, "network-startup.yaml", dependency.NetworkObjects(namespace, runIdentifier(current), enabledProviders(current.config), current.config.Dependencies["clamav"].Enabled), out) {
		return false
	}
	for _, name := range enabledProviders(current.config) {
		if name == "redis" {
			if !s.startRedis(ctx, client, current, directory, out) {
				return false
			}
			continue
		}
		if !s.startBackendProvider(ctx, client, current, directory, name, out) {
			return false
		}
	}
	if current.config.Network != nil {
		expected := dependency.NetworkObjects(namespace, runIdentifier(current), enabledProviders(current.config), false)
		if !applyPrivateObjects(ctx, client, current, directory, "network-runtime.yaml", expected, out) {
			return false
		}
		result := client.NetworkPolicies(ctx, current.clusterName, namespace)
		if failed(result) || result.Truncated || !matchingNetworkPolicies(result.Stdout, expected) {
			out.addError("network_policy_unobserved", "The restricted runtime network policies could not be confirmed after revoking signature-update access.", "Application and preparation execution were blocked.")
			return false
		}
		out.Run.Evidence = append(out.Run.Evidence, model.Evidence{ExperimentID: "network-isolation", Title: "Restricted runtime network configuration", Status: model.StatusPass, Summary: "All declared-dependency-only egress policies were observed; temporary signature-update egress was revoked before application execution. This confirms policy configuration, not an arbitrary-code sandbox; Kubernetes node/host traffic exceptions remain."})
	}
	return true
}

func matchingNetworkPolicies(data string, expected []any) bool {
	var actual networkingv1.NetworkPolicyList
	if json.Unmarshal([]byte(data), &actual) != nil || len(actual.Items) != len(expected) {
		return false
	}
	for _, object := range expected {
		policy, ok := object.(*networkingv1.NetworkPolicy)
		if !ok {
			return false
		}
		found := false
		for _, item := range actual.Items {
			if item.Name == policy.Name && item.Namespace == policy.Namespace && reflect.DeepEqual(item.Spec, policy.Spec) {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func (s *Service) startBackendProvider(ctx context.Context, client *kubernetes.Client, current plan, directory, name string, out *Outcome) bool {
	started := time.Now()
	fp := providerFingerprint(name)
	evidence := model.DependencyEvidence{Name: name, Kind: name, Image: fp.Image, Digest: fp.Digest, Version: fp.Version, Resources: fp.Resources, ConfigurationMode: fp.ConfigurationMode, NetworkExposure: "cluster-internal", Authentication: "generated-test-credentials", Status: model.StatusError, Reason: "Dependency startup could not be observed reliably."}
	if name == "clamav" {
		evidence.Authentication = "isolated-test-network"
	}
	defer func() {
		evidence.StartupMS = elapsedMilliseconds(time.Since(started))
		out.Run.Dependencies = append(out.Run.Dependencies, evidence)
		out.Run.Evidence = append(out.Run.Evidence, model.Evidence{ExperimentID: "dependency." + name, Title: name + " dependency startup", Status: evidence.Status, Summary: evidence.Reason, DurationMS: evidence.StartupMS})
	}()
	objects := dependency.PostgresObjects(namespace, runIdentifier(current), postgresSecretName)
	timeout := 2 * time.Minute
	if name == "clamav" {
		objects = dependency.ClamAVObjects(namespace, runIdentifier(current))
		timeout = 10 * time.Minute
	}
	if configured := current.config.Dependencies[name].StartupTimeout; configured != "" {
		timeout, _ = time.ParseDuration(configured)
	}
	if !applyPrivateObjects(ctx, client, current, directory, "dependency-"+name+".yaml", objects, out) {
		return false
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result := client.WaitDependency(waitCtx, current.clusterName, namespace, providerName(name), timeout)
	if ctx.Err() != nil {
		evidence.Reason = "Dependency startup was canceled."
		out.addError("dependency_canceled", evidence.Reason, "Owned resources will be cleaned up.")
		return false
	}
	block := func(reason string) bool {
		evidence.Status = model.StatusFail
		evidence.Reason = reason
		out.Run.Status = model.StatusBlocked
		out.ExitCode = 1
		out.Run.Diagnostics = append(out.Run.Diagnostics, model.Diagnostic{Code: "dependency_startup_failed", Status: model.StatusBlocked, Message: reason, Guidance: "Application readiness has not been tested; inspect provider availability, capacity and startup requirements."})
		return false
	}
	if failed(result) {
		if isRolloutFailure(result) {
			return block("The isolated " + name + " provider did not become ready within its startup bound; application execution was blocked.")
		}
		out.addError("dependency_execution_failed", evidence.Reason, "Check the isolated cluster API and runtime tools.")
		return false
	}
	pods, result, err := client.ObservePods(ctx, current.clusterName, namespace, "cloudforge.dev/dependency="+name)
	if err != nil || failed(result) {
		out.addError("dependency_observation_failed", evidence.Reason, "No application readiness result is available.")
		return false
	}
	if len(pods) != 1 || !pods[0].Ready || pods[0].Terminating || pods[0].Image != fp.Image {
		return block("The provider did not have exactly one Ready pod running its pinned image.")
	}
	if name == "clamav" {
		result := client.ClamAVVersion(ctx, current.clusterName, namespace, pods[0].Name)
		if failed(result) || result.Truncated {
			out.addError("clamav_version_unobserved", "The active antivirus engine and signature database could not be observed.", "Application readiness was not attempted.")
			return false
		}
		version, timestamp, err := parseClamAVVersion(result.Stdout, s.now())
		if errors.Is(err, errUnobservedClam) {
			out.addError("clamav_version_unobserved", "The active antivirus daemon did not return a complete observable engine/database/date tuple.", "Application execution was blocked.")
			return false
		}
		if err != nil {
			return block("The antivirus daemon did not establish the pinned engine and a signature database no older than 48 hours.")
		}
		evidence.DataVersion = version
		evidence.DataTimestamp = timestamp
		if out.Run.Fingerprint != nil {
			for i := range out.Run.Fingerprint.Dependencies {
				if out.Run.Fingerprint.Dependencies[i].Kind == name {
					out.Run.Fingerprint.Dependencies[i].DataVersion = version
					out.Run.Fingerprint.Dependencies[i].DataTimestamp = timestamp
				}
			}
		}
	}
	evidence.Status = model.StatusPass
	evidence.Reason = "The pinned " + name + " provider is ready on its internal endpoint with the declared disposable configuration."
	return true
}

var errUnobservedClam = errors.New("unobserved daemon version")
var clamAVVersionPattern = regexp.MustCompile(`^ClamAV ([0-9]+\.[0-9]+\.[0-9]+)/([0-9]{1,12})/(.{1,80})$`)

func parseClamAVVersion(value string, now time.Time) (string, string, error) {
	match := clamAVVersionPattern.FindStringSubmatch(strings.TrimSpace(value))
	if len(match) != 4 {
		return "", "", errUnobservedClam
	}
	if match[1] != dependency.ClamAVVersion {
		return "", "", errors.New("unexpected daemon version")
	}
	var timestamp time.Time
	var err error
	for _, layout := range []string{"Mon Jan _2 15:04:05 2006", time.RFC3339, "Mon Jan _2 15:04:05 MST 2006"} {
		timestamp, err = time.Parse(layout, match[3])
		if err == nil {
			break
		}
	}
	if err != nil {
		return "", "", errUnobservedClam
	}
	if now.Sub(timestamp) > 48*time.Hour || timestamp.After(now.Add(5*time.Minute)) {
		return "", "", fmt.Errorf("unaccepted signature date")
	}
	return match[2], timestamp.UTC().Format(time.RFC3339), nil
}

// clamFingerprintMatches shares the same immutable, fresh database requirement
// between HTTP and worker baseline restoration.
func (s *Service) clamFingerprintMatches(ctx context.Context, client *kubernetes.Client, current plan, pod string) (bool, error) {
	observed := client.ClamAVVersion(ctx, current.clusterName, namespace, pod)
	if failed(observed) || observed.Truncated {
		return false, errUnobservedClam
	}
	version, timestamp, err := parseClamAVVersion(observed.Stdout, s.now())
	if errors.Is(err, errUnobservedClam) {
		return false, err
	}
	if err != nil {
		return false, nil
	}
	for _, expected := range current.dependencyFingerprints {
		if expected.Kind == "clamav" && expected.DataVersion == version && expected.DataTimestamp == timestamp {
			return true, nil
		}
	}
	return false, nil
}
