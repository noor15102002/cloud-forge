package verification

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/noor15102002/cloud-forge/internal/executor/kubernetes"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

const baselineRecoveryStrategy = "restore_original_deployment_and_validate"

func (s *Service) validateBaseline(ctx context.Context, client *kubernetes.Client, current plan) model.RecoveryEvidence {
	return s.validateBaselineFor(ctx, client, current, s.baselineTimeout)
}

func (s *Service) validateBaselineFor(ctx context.Context, client *kubernetes.Client, current plan, timeout time.Duration) model.RecoveryEvidence {
	started := time.Now()
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result := model.RecoveryEvidence{Status: model.StatusBlocked, Strategy: baselineRecoveryStrategy, Summary: "The intended runtime baseline was not established before the deadline.", Checks: []model.BaselineCheck{}}
	finish := func() model.RecoveryEvidence {
		result.DurationMS = elapsedMilliseconds(time.Since(started))
		sort.Slice(result.Checks, func(i, j int) bool { return result.Checks[i].Name < result.Checks[j].Name })
		return result
	}
	observationError := func() model.RecoveryEvidence {
		result.Status = model.StatusError
		result.Summary = "The runtime baseline could not be observed reliably."
		return finish()
	}
	for {
		if ctx.Err() != nil {
			if parent.Err() != nil {
				return observationError()
			}
			return finish()
		}
		checks := []model.BaselineCheck{}
		allHealthy := true
		add := func(name string, healthy bool, reason string) {
			status := model.StatusPass
			if !healthy {
				status = model.StatusBlocked
				allHealthy = false
			}
			checks = append(checks, model.BaselineCheck{Name: name, Status: status, Reason: reason})
		}
		deployment, res, err := client.ObserveDeployment(ctx, current.clusterName, namespace, current.workloadName)
		if failed(res) || err != nil {
			return observationError()
		}
		add("expected_image", deployment.Image == current.image, fmt.Sprintf("Expected image %s; observed %s.", current.image, deployment.Image))
		add("expected_replicas", deployment.Replicas == current.desiredReplicas, fmt.Sprintf("Expected %d replicas; Deployment requests %d.", current.desiredReplicas, deployment.Replicas))
		revisionSets, res, err := client.RevisionReplicaSets(ctx, current.clusterName, namespace, "app.kubernetes.io/name="+current.workloadName, deployment)
		if failed(res) || err != nil {
			return observationError()
		}
		pods, res, err := client.ObservePods(ctx, current.clusterName, namespace, "app.kubernetes.io/name="+current.workloadName)
		if failed(res) || err != nil {
			return observationError()
		}
		ready := len(pods) == int(current.desiredReplicas)
		revision := deployment.Generation > 0 && deployment.ObservedGeneration >= deployment.Generation && len(revisionSets) > 0
		for _, pod := range pods {
			ready = ready && pod.Ready && !pod.Terminating
			revision = revision && revisionSets[pod.ReplicaSetUID] && pod.Image == current.image
		}
		add("ready_pods", ready, fmt.Sprintf("Expected %d Ready non-terminating pods; observed %d total pods; complete readiness=%t.", current.desiredReplicas, len(pods), ready))
		add("expected_revision", revision, fmt.Sprintf("Controller revision %s, generation %d/%d; all pods at the intended image/revision=%t.", deployment.Revision, deployment.ObservedGeneration, deployment.Generation, revision))
		for _, name := range enabledProviders(current.config) {
			pods, res, err := client.ObservePods(ctx, current.clusterName, namespace, "cloudforge.dev/dependency="+name)
			if failed(res) || err != nil {
				return observationError()
			}
			healthy := len(pods) == 1 && pods[0].Ready && !pods[0].Terminating && pods[0].Image == providerFingerprint(name).Image
			add("dependency."+name, healthy, "The pinned provider must have one Ready, non-terminating pod satisfying its provider readiness probe; data is not reset during restoration.")
			if healthy && name == "clamav" {
				matched, err := s.clamFingerprintMatches(ctx, client, current, pods[0].Name)
				if err != nil {
					return observationError()
				}
				add("dependency.clamav_signatures", matched, "The active antivirus database must remain fresh and match the originally observed version/date; restoration does not change the tested dependency data.")
			}

		}
		if ctx.Err() != nil {
			result.Checks = checks
			if parent.Err() != nil {
				return observationError()
			}
			return finish()
		}
		status, probeErr := s.pacedProbe(ctx, current.readinessURL)
		if errors.Is(probeErr, errProbePacingInterrupted) {
			if parent.Err() != nil {
				return observationError()
			}
			// Preserve the last completed baseline attempt. Reaching our own
			// deadline while waiting to probe cannot erase a valid unhealthy
			// observation or invent a new HTTP failure.
			if len(result.Checks) == 0 {
				result.Checks = append(checks, model.BaselineCheck{Name: "service_reachable", Status: model.StatusBlocked, Reason: "The paced HTTP request did not start before the baseline deadline; Service readiness was not established."})
			}
			return finish()
		}
		add("service_reachable", status >= 200 && status < 300, fmt.Sprintf("Service HTTP readiness status %d; expected 2xx.", status))
		if current.config.Readiness != nil {
			add("semantic_readiness", probeErr == nil && status >= 200 && status < 300, "The configured HTTP/JSON readiness contract must be satisfied.")
		} else {
			allHealthy = allHealthy && probeErr == nil
		}
		result.Checks = checks
		if parent.Err() != nil {
			return observationError()
		}
		if allHealthy {
			result.Status = model.StatusPass
			result.Summary = "The intended image/revision, replicas, dependencies and Service readiness were validated."
			return finish()
		}
		if !pause(ctx, s.poll) {
			if parent.Err() != nil {
				return observationError()
			}
			return finish()
		}
	}
}

func (s *Service) restoreBaseline(ctx context.Context, client *kubernetes.Client, current plan, manifestPath string, resetControl, removeHPA bool) model.RecoveryEvidence {
	parent := ctx
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, s.baselineTimeout)
	defer cancel()
	failure := func(message string) model.RecoveryEvidence {
		return model.RecoveryEvidence{Status: model.StatusError, Strategy: baselineRecoveryStrategy, Summary: message, DurationMS: elapsedMilliseconds(time.Since(started)), Checks: []model.BaselineCheck{}}
	}
	if ctx.Err() != nil {
		return failure("Restoration was canceled; bounded environment cleanup remains scheduled.")
	}
	if removeHPA && current.hpaName != "" {
		if res := client.DeleteHPA(ctx, current.clusterName, namespace, current.hpaName); failed(res) {
			return failure("The run-owned autoscaler could not be removed before restoring fixed replicas.")
		}
	}
	if res := client.Apply(ctx, current.clusterName, manifestPath); failed(res) {
		return failure("The intended workload manifest could not be restored.")
	}
	if resetControl {
		pods, res, err := client.ObservePods(ctx, current.clusterName, namespace, "app.kubernetes.io/name="+current.workloadName)
		if failed(res) || err != nil {
			return failure("Controlled readiness could not be inspected for restoration.")
		}
		for _, pod := range pods {
			if pod.Terminating {
				continue
			}
			state, err := controlResult(client.PodProxy(ctx, current.clusterName, namespace, pod.Name, current.config.Runtime.Port, current.config.Experiments.ControlPath+"/ready"))
			if err != nil || state.Pod != pod.Name || !state.Ready {
				return failure("The explicit control protocol did not acknowledge restored readiness.")
			}
		}
	}
	result := s.validateBaselineFor(parent, client, current, s.baselineTimeout-time.Since(started))
	result.DurationMS = elapsedMilliseconds(time.Since(started))
	return result
}

func plannedCapability(current *model.VerificationPlan, name string) model.Capability {
	for _, c := range current.Capabilities {
		if c.Name == name {
			return c
		}
	}
	return model.Capability{Name: name, Disposition: "skipped", Reason: "No planned capability."}
}

func recordNotExecuted(out *Outcome, current plan, id string, status model.Status, reason string) {
	out.Run.Evidence = append(out.Run.Evidence, model.Evidence{ExperimentID: id, Title: id, Status: status, Summary: reason, Topology: current.topology, Execution: &model.ExperimentExecution{Executed: false}})
}

// prepareExperiment checks existing capability intent and the runtime baseline.
// All lifecycle experiments are sequential siblings depending on this baseline,
// not on the previous experiment passing its tested requirement.
func (s *Service) prepareExperiment(ctx context.Context, client *kubernetes.Client, current plan, out *Outcome, id string, blocked *string) bool {
	if ctx.Err() != nil {
		return false
	}
	capability := plannedCapability(out.Run.Plan, id)
	if capability.Disposition != "supported" {
		recordNotExecuted(out, current, id, model.StatusSkipped, capability.Reason)
		return false
	}
	if *blocked != "" {
		recordNotExecuted(out, current, id, model.StatusBlocked, *blocked)
		return false
	}
	baseline := s.validateBaseline(ctx, client, current)
	if baseline.Status != model.StatusPass {
		*blocked = "The required runtime baseline could not be established; dependent experiments were not started."
		recordNotExecuted(out, current, id, baseline.Status, *blocked)
		out.Run.Evidence[len(out.Run.Evidence)-1].Recovery = &baseline
		return false
	}
	return true
}

func (s *Service) finishExperiment(ctx context.Context, client *kubernetes.Client, current plan, manifestPath string, out *Outcome, result recoveryOutcome, removeHPA bool, blocked *string) {
	if result.Evidence.Execution == nil {
		result.Evidence.Execution = &model.ExperimentExecution{Executed: true, MutationAttempted: result.MutationAttempted}
	}
	if (result.Evidence.Status == model.StatusSkipped || result.Evidence.Status == model.StatusBlocked) && !result.MutationAttempted {
		result.Evidence.Execution.Executed = false
	}
	result.Evidence.Topology = current.topology
	if result.MutationAttempted && ctx.Err() == nil {
		recovery := s.restoreBaseline(ctx, client, current, manifestPath, result.Evidence.ExperimentID == "readiness-gating", removeHPA)
		result.Evidence.Recovery = &recovery
		if recovery.Status != model.StatusPass {
			*blocked = fmt.Sprintf("The runtime baseline could not be restored after %s; dependent experiments were not started.", result.Evidence.ExperimentID)
		}
	}
	applyExperimentOutcome(out, result)
}

func finalEvidenceStatus(out *Outcome) {
	status, code := outcomeForFindings(out.Run.Findings)
	for _, e := range out.Run.Evidence {
		candidate := e.Status
		if e.Recovery != nil && e.Recovery.Status == model.StatusError {
			candidate = model.StatusError
		}
		if e.Recovery != nil && e.Recovery.Status == model.StatusBlocked && candidate != model.StatusError && candidate != model.StatusFail {
			candidate = model.StatusBlocked
		}
		if candidate == model.StatusError {
			status, code = model.StatusError, 2
		} else if code != 2 {
			if candidate == model.StatusFail {
				status, code = model.StatusFail, 1
			} else if candidate == model.StatusBlocked && status != model.StatusFail {
				status, code = model.StatusBlocked, 1
			} else if candidate == model.StatusWarn && status == model.StatusPass {
				status = model.StatusWarn
			}
		}
	}
	if out.Run.Compatibility != nil && out.Run.Compatibility.Status == "not_validated" && status == model.StatusPass {
		status = model.StatusWarn
	}
	out.Run.Status, out.ExitCode = status, code
}
