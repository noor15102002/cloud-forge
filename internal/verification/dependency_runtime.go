package verification

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/noor15102002/cloud-forge/internal/dependency"
	"github.com/noor15102002/cloud-forge/internal/executor/kubernetes"
	"github.com/noor15102002/cloud-forge/pkg/model"
	corev1 "k8s.io/api/core/v1"
)

func applicationEnvironment(config model.RuntimeConfiguration) []corev1.EnvVar {
	names := make([]string, 0, len(config.Environment))
	for name := range config.Environment {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]corev1.EnvVar, 0, len(names))
	for _, name := range names {
		binding := config.Environment[name]
		value := ""
		if binding.From == "dependency.redis.url" {
			value = dependency.RedisURL(namespace)
		} else if binding.Value != nil {
			value = *binding.Value
		}
		result = append(result, corev1.EnvVar{Name: name, Value: value})
	}
	return result
}

func (s *Service) startRedis(ctx context.Context, client *kubernetes.Client, current plan, temporary string, out *Outcome) bool {
	started := time.Now()
	fp := dependency.RedisFingerprint()
	evidence := model.DependencyEvidence{Name: "redis", Kind: "redis", Image: fp.Image, Digest: fp.Digest, Version: fp.Version, Resources: fp.Resources, ConfigurationMode: fp.ConfigurationMode, NetworkExposure: "cluster-internal", Authentication: "disabled-test-only", Status: model.StatusError, Reason: "Dependency startup could not be observed."}
	defer func() {
		evidence.StartupMS = elapsedMilliseconds(time.Since(started))
		out.Run.Dependencies = append(out.Run.Dependencies, evidence)
		out.Run.Evidence = append(out.Run.Evidence, model.Evidence{ExperimentID: "dependency.redis", Title: "Redis dependency startup", Status: evidence.Status, Summary: evidence.Reason, DurationMS: evidence.StartupMS})
	}()
	data, err := marshalDocuments(dependency.RedisObjects(namespace, current.clusterName[len("cloudforge-"):]))
	if err != nil {
		out.addError("dependency_manifest_failed", "CloudForge could not encode Redis resources.", "No application deployment was attempted.")
		return false
	}
	path := filepath.Join(temporary, "dependency-redis.yaml")
	if os.WriteFile(path, data, 0o600) != nil {
		out.addError("dependency_manifest_failed", "CloudForge could not write Redis resources.", "Check private workspace access.")
		return false
	}
	if result := client.Apply(ctx, current.clusterName, path); failed(result) {
		out.addCommandDiagnostic("dependency_apply_failed", "CloudForge could not apply its Redis resources.", result)
		return false
	}
	deadline := 2 * time.Minute
	if value := current.config.Dependencies["redis"].StartupTimeout; value != "" {
		deadline, _ = time.ParseDuration(value)
	}
	waitCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	result := client.WaitAvailable(waitCtx, current.clusterName, namespace, dependency.RedisName)
	if ctx.Err() != nil {
		evidence.Reason = "Dependency startup was canceled."
		out.addError("dependency_canceled", evidence.Reason, "Owned resources will be cleaned up.")
		return false
	}
	if failed(result) {
		if result.FailureType == model.FailureNotFound || result.FailureType == model.FailureExecution {
			out.addError("dependency_execution_failed", "Dependency readiness could not be executed.", "Check kubectl availability.")
			return false
		}
		evidence.Status = model.StatusFail
		evidence.Reason = "Redis did not become ready before its startup deadline; application verification was blocked."
		out.Run.Status = model.StatusBlocked
		out.ExitCode = 1
		out.Run.Diagnostics = append(out.Run.Diagnostics, model.Diagnostic{Code: "dependency_startup_failed", Status: model.StatusBlocked, Message: evidence.Reason, Guidance: "Inspect the isolated dependency image availability, node capacity and startup configuration."})
		return false
	}
	pods, result, err := client.ObservePods(ctx, current.clusterName, namespace, "cloudforge.dev/dependency=redis")
	if err != nil || failed(result) {
		out.addError("dependency_observation_failed", "Redis pod readiness could not be inspected.", "No application readiness result is available.")
		return false
	}
	if len(pods) != 1 || !pods[0].Ready {
		evidence.Status = model.StatusFail
		evidence.Reason = "Redis rollout completed without one ready dependency pod."
		out.Run.Status = model.StatusBlocked
		out.ExitCode = 1
		return false
	}
	// The immutable image reference establishes the dependency version/configuration.
	// The pod observation establishes readiness; raw pod content is not retained.
	evidence.Status = model.StatusPass
	evidence.Reason = "Redis is ready on its internal ClusterIP endpoint; persistence and authentication are disabled for this disposable test."
	return true
}
