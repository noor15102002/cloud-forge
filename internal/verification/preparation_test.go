package verification

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/internal/executor/kubernetes"
	"github.com/noor15102002/cloud-forge/pkg/model"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func preparationTestPlan() plan {
	return plan{clusterName: "cloudforge-test", image: "owned:image-a", rolloutImage: "owned:image-b", config: model.RuntimeConfiguration{Preparation: &model.PreparationSettings{Command: []string{"node", "private-command-marker", "migrate", "deploy"}, Timeout: "1s"}}}
}

func TestPreparationJobPreservesImageCommandAndIsolation(t *testing.T) {
	current := preparationTestPlan()
	job := preparationJob(current, 5*time.Minute)
	pod := job.Spec.Template.Spec
	container := pod.Containers[0]
	if job.Name != preparationName || job.Namespace != namespace || *job.Spec.BackoffLimit != 0 || *job.Spec.Parallelism != 1 || *job.Spec.Completions != 1 || *job.Spec.ActiveDeadlineSeconds != 300 || pod.RestartPolicy != corev1.RestartPolicyNever {
		t.Fatal("preparation job is not a bounded single execution")
	}
	if container.Image != current.image || container.ImagePullPolicy != corev1.PullNever || container.WorkingDir != "" || !reflect.DeepEqual(append(container.Command, container.Args...), current.config.Preparation.Command) {
		t.Fatal("preparation changed image, cwd, or direct argument semantics")
	}
	if pod.HostNetwork || pod.HostPID || pod.HostIPC || *pod.AutomountServiceAccountToken || *pod.EnableServiceLinks || pod.SecurityContext.RunAsUser != nil || pod.SecurityContext.RunAsGroup != nil || pod.SecurityContext.RunAsNonRoot != nil || pod.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Fatal("preparation escaped container isolation, replaced image USER or inferred a non-root identity")
	}
	if *container.SecurityContext.AllowPrivilegeEscalation || !*container.SecurityContext.ReadOnlyRootFilesystem || !reflect.DeepEqual(container.SecurityContext.Capabilities.Drop, []corev1.Capability{"ALL"}) || len(pod.Volumes) != 1 || pod.Volumes[0].EmptyDir.SizeLimit.String() != "64Mi" {
		t.Fatal("preparation privileges/storage are not bounded")
	}
	for _, labels := range []map[string]string{job.Labels, job.Spec.Template.Labels} {
		if labels["cloudforge.dev/run-id"] != "test" || labels["cloudforge.dev/role"] != "preparation" || labels["app.kubernetes.io/managed-by"] != "cloudforge" {
			t.Fatal("preparation ownership/network labels missing")
		}
	}
	resources := preparationResources()
	if container.Resources.Requests.Cpu().String() != resources.CPURequest || container.Resources.Requests.Memory().String() != resources.MemoryRequest || container.Resources.Limits.Cpu().String() != resources.CPULimit || container.Resources.Limits.Memory().String() != resources.MemoryLimit {
		t.Fatal("preparation accounting differs from actual pod limits")
	}
	for _, timeout := range []string{"0s", "-1s", "1500ms", "6m", "unrecognized"} {
		current.config.Preparation.Timeout = timeout
		if _, err := preparationTimeout(current.config.Preparation); err == nil {
			t.Fatalf("accepted out-of-bounds timeout %q", timeout)
		}
	}
}

func preparationTestResponse(current plan, mode string, request command.Request) model.CommandResult {
	job := preparationJob(current, time.Second)
	job.UID = "preparation-uid"
	controller := true
	started, finished := metav1.NewTime(time.Unix(100, 0)), metav1.NewTime(time.Unix(101, 0))
	termination := &corev1.ContainerStateTerminated{StartedAt: started, FinishedAt: finished, ExitCode: 0, Message: "private-termination-marker"}
	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue, Message: "private-job-marker"}}
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, OwnerReferences: []metav1.OwnerReference{{Kind: "Job", Name: job.Name, UID: job.UID, Controller: &controller}}},
		Spec:       job.Spec.Template.Spec,
		Status:     corev1.PodStatus{Phase: corev1.PodSucceeded, ContainerStatuses: []corev1.ContainerStatus{{Name: "preparation", State: corev1.ContainerState{Terminated: termination}}}},
	}
	switch mode {
	case "exit-failure":
		termination.ExitCode = 7
		job.Status.Conditions[0].Type = batchv1.JobFailed
	case "running", "deadline-observation-failure":
		job.Status.Conditions = nil
		pod.Status.Phase = corev1.PodRunning
		pod.Status.ContainerStatuses[0].State = corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: started}}
	case "pending":
		job.Status.Conditions = nil
		pod.Status.Phase = corev1.PodPending
		pod.Status.ContainerStatuses = nil
	case "controller-incomplete":
		job.Status.Conditions = nil
	case "wrong-image":
		job.Spec.Template.Spec.Containers[0].Image = "unexpected:image"
		pod.Spec.Containers[0].Image = "unexpected:image"
	case "missing-pod":
		// A complete Job without a retained execution cannot prove this contract.
	case "missing-start":
		termination.StartedAt = metav1.Time{}
	case "image-unavailable":
		job.Status.Conditions = nil
		pod.Status.ContainerStatuses[0].State = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ErrImageNeverPull", Message: "private-image-marker"}}
	}
	var body []byte
	if containsArgument(request.Args, "job") {
		body, _ = json.Marshal(job)
	} else {
		pods := corev1.PodList{Items: []corev1.Pod{pod}}
		if mode == "missing-pod" {
			pods.Items = nil
		}
		body, _ = json.Marshal(pods)
	}
	return model.CommandResult{Stdout: string(body), Stderr: "private-stderr-marker"}
}

func TestPreparationPreservesObservedFailureAndOmitsSensitiveContent(t *testing.T) {
	for _, test := range []struct {
		mode     string
		status   model.Status
		evidence model.Status
		success  bool
		exit     int
	}{
		{"complete", model.StatusPass, model.StatusPass, true, 0},
		{"exit-failure", model.StatusBlocked, model.StatusFail, false, 1},
		{"malformed", model.StatusError, model.StatusError, false, 2},
		{"tool-failure", model.StatusError, model.StatusError, false, 2},
		{"apply-failure", model.StatusError, model.StatusError, false, 2},
		{"wrong-image", model.StatusError, model.StatusError, false, 2},
		{"missing-pod", model.StatusError, model.StatusError, false, 2},
		{"missing-start", model.StatusError, model.StatusError, false, 2},
		{"image-unavailable", model.StatusError, model.StatusError, false, 2},
	} {
		t.Run(test.mode, func(t *testing.T) {
			current := preparationTestPlan()
			temporary := t.TempDir()
			applies := 0
			runner := runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
				if containsArgument(request.Args, "apply") {
					applies++
					path := request.Args[len(request.Args)-1]
					info, err := os.Stat(path)
					if err != nil || info.Mode().Perm() != 0o600 || filepath.Dir(path) != temporary {
						t.Fatal("Job manifest did not stay in the private workspace")
					}
					if test.mode == "apply-failure" {
						return model.CommandResult{ExitCode: 1, FailureType: model.FailureExit, Stderr: "private-apply-marker"}
					}
					return model.CommandResult{}
				}
				if containsArgument(request.Args, "logs") || containsArgument(request.Args, "exec") {
					t.Fatal("preparation attempted to execute on the host or retrieve logs")
				}
				if test.mode == "malformed" {
					return model.CommandResult{Stdout: "private-invalid-marker"}
				}
				if test.mode == "tool-failure" {
					return model.CommandResult{ExitCode: 1, FailureType: model.FailureExit, Stderr: "private-kubectl-marker"}
				}
				return preparationTestResponse(current, test.mode, request)
			})
			service := New(runner)
			service.poll = time.Millisecond
			out := Outcome{Run: model.VerificationRun{Status: model.StatusPass, Evidence: []model.Evidence{{ExperimentID: "earlier", Status: model.StatusFail, Summary: "Earlier evidence remains."}}}}
			passed := service.runPreparation(context.Background(), kubernetes.New(runner), current, temporary, &out)
			if passed != test.success || out.Run.Status != test.status || out.ExitCode != test.exit || len(out.Run.Evidence) != 2 || out.Run.Evidence[1].Status != test.evidence || applies != 1 {
				t.Fatalf("wrong preparation outcome: %t %#v", passed, out)
			}
			if out.Run.Evidence[0].Status != model.StatusFail {
				t.Fatal("preparation overwrote earlier failure evidence")
			}
			encoded, _ := json.Marshal(out)
			if strings.Contains(string(encoded), "private-") || strings.Contains(string(encoded), "node_modules") {
				t.Fatal("preparation emitted command, environment, server message or log content")
			}
			if test.mode == "exit-failure" {
				if !out.Run.Evidence[1].Execution.Executed || !containsMeasurement(out.Run.Evidence[1].Measurements, "exit_code", "7") {
					t.Fatal("observed command failure was lost")
				}
			}
		})
	}
}

func containsMeasurement(measurements []model.Measurement, name, value string) bool {
	for _, measurement := range measurements {
		if measurement.Name == name && measurement.Value == value {
			return true
		}
	}
	return false
}

func TestPreparationDeadlineRequiresFreshReliableObservation(t *testing.T) {
	for _, mode := range []string{"running", "pending", "deadline-observation-failure", "controller-incomplete"} {
		t.Run(mode, func(t *testing.T) {
			current := preparationTestPlan()
			started := time.Now()
			runner := runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
				if containsArgument(request.Args, "apply") {
					return model.CommandResult{}
				}
				if mode == "deadline-observation-failure" && time.Since(started) >= time.Second {
					return model.CommandResult{ExitCode: 1, FailureType: model.FailureExit}
				}
				return preparationTestResponse(current, mode, request)
			})
			service := New(runner)
			service.poll = 10 * time.Millisecond
			out := Outcome{}
			if service.runPreparation(context.Background(), kubernetes.New(runner), current, t.TempDir(), &out) {
				t.Fatal("unfinished preparation passed")
			}
			if len(out.Run.Evidence) != 1 {
				t.Fatal("missing timeout evidence")
			}
			if mode == "running" {
				if out.Run.Status != model.StatusBlocked || out.Run.Evidence[0].Status != model.StatusFail || !containsMeasurement(out.Run.Evidence[0].Measurements, "timed_out", "true") {
					t.Fatalf("observed execution did not produce a bounded requirement failure: %#v", out)
				}
			} else if out.Run.Status != model.StatusError || out.Run.Evidence[0].Status != model.StatusError {
				t.Fatalf("missing execution/observation became an application failure: %#v", out)
			}
		})
	}
}

func TestPreparationCancellationAndOmission(t *testing.T) {
	current := preparationTestPlan()
	ctx, cancel := context.WithCancel(context.Background())
	requests := 0
	runner := runnerFunc(func(ctx context.Context, request command.Request) model.CommandResult {
		requests++
		if containsArgument(request.Args, "apply") {
			return model.CommandResult{}
		}
		cancel()
		<-ctx.Done()
		return model.CommandResult{ExitCode: -1, FailureType: model.FailureCanceled}
	})
	service := New(runner)
	out := Outcome{}
	if service.runPreparation(ctx, kubernetes.New(runner), current, t.TempDir(), &out) || out.Run.Status != model.StatusError || requests != 2 || len(out.Run.Evidence) != 1 || out.Run.Evidence[0].Status != model.StatusError {
		t.Fatalf("preparation did not stop promptly on cancellation: %#v calls=%d", out, requests)
	}
	current.config.Preparation = nil
	requests = 0
	out = Outcome{}
	if !service.runPreparation(context.Background(), kubernetes.New(runner), current, t.TempDir(), &out) || requests != 0 || len(out.Run.Evidence) != 0 {
		t.Fatal("omitted preparation executed or fabricated evidence")
	}
}
