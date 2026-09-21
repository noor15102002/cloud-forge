package verification

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/noor15102002/cloud-forge/internal/executor/kubernetes"
	"github.com/noor15102002/cloud-forge/pkg/model"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const preparationName = "cf-preparation"

// preparationResources counts against the provider-plus-preparation phase;
// application pods are not deployed until this one execution succeeds.
func preparationResources() model.ResourceRequirements {
	return model.ResourceRequirements{CPURequest: "100m", MemoryRequest: "128Mi", CPULimit: "1", MemoryLimit: "512Mi"}
}

func preparationTimeout(settings *model.PreparationSettings) (time.Duration, error) {
	if settings == nil || len(settings.Command) == 0 || settings.Command[0] == "" {
		return 0, errors.New("preparation requires one direct executable argument vector")
	}
	duration := 2 * time.Minute
	if settings.Timeout != "" {
		parsed, err := time.ParseDuration(settings.Timeout)
		if err != nil {
			return 0, errors.New("preparation timeout is invalid")
		}
		duration = parsed
	}
	if duration < time.Second || duration > 5*time.Minute || duration%time.Second != 0 {
		return 0, errors.New("preparation timeout must be whole seconds between 1s and 5m")
	}
	return duration, nil
}

func preparationJob(current plan, timeout time.Duration) *batchv1.Job {
	labels := map[string]string{
		"app.kubernetes.io/name": preparationName, "app.kubernetes.io/managed-by": "cloudforge",
		"cloudforge.dev/run-id": strings.TrimPrefix(current.clusterName, "cloudforge-"), "cloudforge.dev/role": "preparation",
	}
	zero, one := int32(0), int32(1)
	deadline := int64((timeout + time.Second - 1) / time.Second)
	grace := int64(10)
	disabled, enabled := false, true
	temporaryLimit := resource.MustParse("64Mi")
	resources := preparationResources()
	return &batchv1.Job{
		TypeMeta:   metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"},
		ObjectMeta: metav1.ObjectMeta{Name: preparationName, Namespace: namespace, Labels: labels},
		Spec: batchv1.JobSpec{
			Parallelism: &one, Completions: &one, BackoffLimit: &zero, ActiveDeadlineSeconds: &deadline,
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{
				AutomountServiceAccountToken: &disabled, EnableServiceLinks: &disabled,
				RestartPolicy: corev1.RestartPolicyNever, TerminationGracePeriodSeconds: &grace,
				// Preserve the image's declared USER, including named users. No
				// numeric identity or non-root attestation is inferred here.
				SecurityContext: &corev1.PodSecurityContext{SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
				Containers: []corev1.Container{{
					Name: "preparation", Image: current.image, ImagePullPolicy: corev1.PullNever,
					Command: []string{current.config.Preparation.Command[0]}, Args: append([]string{}, current.config.Preparation.Command[1:]...),
					// WorkingDir remains empty to preserve the image's original cwd.
					Env: applicationEnvironment(current.config),
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(resources.CPURequest), corev1.ResourceMemory: resource.MustParse(resources.MemoryRequest)},
						Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(resources.CPULimit), corev1.ResourceMemory: resource.MustParse(resources.MemoryLimit)},
					},
					SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &disabled, ReadOnlyRootFilesystem: &enabled, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
					VolumeMounts:    []corev1.VolumeMount{{Name: "temporary", MountPath: "/tmp"}},
				}},
				Volumes: []corev1.Volume{{Name: "temporary", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &temporaryLimit}}}},
			}},
		},
	}
}

// runPreparation executes image A's one explicit preparation command before the
// HTTP workload. It never reruns during restoration and never captures logs.
func (s *Service) runPreparation(ctx context.Context, client *kubernetes.Client, current plan, temporary string, out *Outcome) bool {
	if current.config.Preparation == nil {
		return true
	}
	started := time.Now()
	evidence := model.Evidence{ExperimentID: "application-preparation", Title: "Application preparation", Status: model.StatusError, Summary: "Preparation could not be executed or observed reliably.", Execution: &model.ExperimentExecution{}}
	defer func() {
		evidence.DurationMS = elapsedMilliseconds(time.Since(started))
		out.Run.Evidence = append(out.Run.Evidence, evidence)
	}()
	failExecution := func(code, message string) bool {
		evidence.Status, evidence.Summary = model.StatusError, message
		out.addError(code, message, "Application startup remains blocked; run-owned resources are removed by bounded cleanup.")
		return false
	}
	blockApplication := func(message string) bool {
		evidence.Status, evidence.Summary = model.StatusFail, message
		out.Run.Status, out.ExitCode = model.StatusBlocked, 1
		out.Run.Diagnostics = append(out.Run.Diagnostics, model.Diagnostic{Code: "preparation_failed", Status: model.StatusBlocked, Message: message, Guidance: "Correct the configured preparation requirement in the unchanged application image before retrying. No application startup verdict was produced."})
		return false
	}
	if ctx.Err() != nil {
		return failExecution("preparation_canceled", "Preparation was canceled before execution.")
	}
	timeout, err := preparationTimeout(current.config.Preparation)
	if err != nil {
		return failExecution("preparation_invalid", "Preparation configuration could not be executed safely.")
	}
	evidence.Measurements = []model.Measurement{{Name: "timeout_seconds", Value: strconv.FormatInt(int64((timeout+time.Second-1)/time.Second), 10), Unit: "seconds"}}
	data, err := marshalDocuments([]any{preparationJob(current, timeout)})
	if err != nil {
		return failExecution("preparation_manifest_failed", "CloudForge could not encode its preparation Job.")
	}
	path := filepath.Join(temporary, "preparation.yaml")
	if os.WriteFile(path, data, 0o600) != nil {
		return failExecution("preparation_manifest_failed", "CloudForge could not write its private preparation Job.")
	}
	evidence.Execution.MutationAttempted = true
	if result := client.Apply(ctx, current.clusterName, path); failed(result) {
		if ctx.Err() != nil {
			return failExecution("preparation_canceled", "Preparation was canceled while creating its Job.")
		}
		return failExecution("preparation_apply_failed", "CloudForge could not create the preparation Job reliably.")
	}
	waitContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// A fresh final observation has its own small bound after the execution
	// deadline. An inability to observe is ERROR, never an inferred application FAIL.
	assess := func(state kubernetes.JobState, deadline bool) (bool, bool) {
		if state.Image != current.image {
			return true, failExecution("preparation_image_mismatch", "Preparation did not use the intended application image.")
		}
		if len(state.Pods) == 1 {
			pod := state.Pods[0]
			evidence.Execution.Executed = evidence.Execution.Executed || pod.Started
			if pod.Started && pod.Terminated {
				evidence.Measurements = append(evidence.Measurements, model.Measurement{Name: "exit_code", Value: strconv.FormatInt(int64(pod.ExitCode), 10)})
				if pod.ExitCode != 0 {
					return true, blockApplication("The preparation command exited unsuccessfully; application verification was blocked.")
				}
				if state.Complete && !state.Failed {
					evidence.Status, evidence.Summary = model.StatusPass, "The preparation command completed successfully in application image A before HTTP startup."
					return true, true
				}
				if deadline || state.Failed {
					return true, failExecution("preparation_completion_unavailable", "The preparation command exited successfully, but its Job completion could not be established.")
				}
				// Do not append exit_code again while waiting for the Job controller.
				evidence.Measurements = evidence.Measurements[:len(evidence.Measurements)-1]
			}
			if pod.Reason == "image_unavailable" || pod.Reason == "container_start_error" {
				return true, failExecution("preparation_container_unavailable", "The preparation container could not start in the isolated environment.")
			}
			if (state.DeadlineExceeded || deadline) && pod.Started && !state.Complete {
				evidence.Measurements = append(evidence.Measurements, model.Measurement{Name: "timed_out", Value: "true"})
				return true, blockApplication("The observed preparation execution did not complete within its configured deadline; application verification was blocked.")
			}
		}
		if state.Complete || state.Failed || deadline {
			return true, failExecution("preparation_outcome_unavailable", "Preparation ended or exhausted its deadline without a reliable single-command outcome.")
		}
		return false, false
	}
	for {
		if ctx.Err() != nil {
			return failExecution("preparation_canceled", "Preparation observation was canceled; collected evidence was preserved.")
		}
		state, result, observationError := client.ObserveJob(waitContext, current.clusterName, namespace, preparationName)
		if ctx.Err() != nil {
			return failExecution("preparation_canceled", "Preparation observation was canceled; collected evidence was preserved.")
		}
		if waitContext.Err() != nil {
			finalContext, stop := context.WithTimeout(ctx, 5*time.Second)
			state, result, observationError = client.ObserveJob(finalContext, current.clusterName, namespace, preparationName)
			stop()
			if ctx.Err() != nil {
				return failExecution("preparation_canceled", "Preparation observation was canceled; collected evidence was preserved.")
			}
			if failed(result) || observationError != nil {
				return failExecution("preparation_observation_failed", "CloudForge could not observe preparation reliably at its deadline.")
			}
			_, success := assess(state, true)
			return success
		}
		if failed(result) || observationError != nil {
			return failExecution("preparation_observation_failed", "CloudForge could not observe the preparation Job reliably.")
		}
		if complete, success := assess(state, false); complete {
			return success
		}
		_ = pause(waitContext, s.poll)
	}
}
