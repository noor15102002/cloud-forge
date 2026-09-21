package kubernetes

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func preparationObservationObjects() (batchv1.Job, corev1.PodList) {
	controller := true
	started := metav1.NewTime(time.Unix(100, 0))
	finished := metav1.NewTime(time.Unix(101, 0))
	job := batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "cf-preparation", Namespace: "cloudforge", UID: "job-uid"},
		Spec:       batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "preparation", Image: "owned:a", Command: []string{"secret-command"}, Env: []corev1.EnvVar{{Name: "SECRET", Value: "secret-value"}}}}}}},
		Status:     batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue, Message: "secret-status"}}},
	}
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "cf-preparation-abc", Namespace: "cloudforge", OwnerReferences: []metav1.OwnerReference{{Kind: "Job", Name: job.Name, UID: job.UID, Controller: &controller}}},
		Spec:       job.Spec.Template.Spec,
		Status:     corev1.PodStatus{Phase: corev1.PodSucceeded, ContainerStatuses: []corev1.ContainerStatus{{Name: "preparation", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{StartedAt: started, FinishedAt: finished, ExitCode: 0, Message: "secret-termination"}}}}},
	}
	return job, corev1.PodList{Items: []corev1.Pod{pod}}
}

func TestObserveJobSanitizesCommandAndPodContent(t *testing.T) {
	job, pods := preparationObservationObjects()
	calls := 0
	client := New(runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		calls++
		if request.Name != "kubectl" || !strings.Contains(strings.Join(request.Args, " "), "--context k3d-run") || request.Timeout > 6*time.Second || request.OutputLimit > 256*1024 {
			t.Fatal("job observation escaped its bounded explicit context")
		}
		if strings.Contains(strings.Join(request.Args, " "), "logs") {
			t.Fatal("preparation logs were accessed")
		}
		var body []byte
		if calls == 1 {
			body, _ = json.Marshal(job)
		} else {
			body, _ = json.Marshal(pods)
			if !strings.Contains(strings.Join(request.Args, " "), "batch.kubernetes.io/job-name=cf-preparation") {
				t.Fatal("pod query was not scoped to the Job")
			}
		}
		return model.CommandResult{Stdout: string(body), Stderr: "secret-stderr", DurationMS: 3}
	}))
	state, result, err := client.ObserveJob(context.Background(), "run", "cloudforge", "cf-preparation")
	if err != nil || calls != 2 || !state.Complete || state.Failed || len(state.Pods) != 1 || !state.Pods[0].Started || !state.Pods[0].Terminated || state.Pods[0].ExitCode != 0 || state.Image != "owned:a" || result.DurationMS != 6 {
		t.Fatalf("incorrect job observation: %#v %#v %v", state, result, err)
	}
	encoded, _ := json.Marshal([]any{state, result})
	if strings.Contains(string(encoded), "secret-") || result.Stdout != "" || result.Stderr != "" {
		t.Fatal("raw arguments, environment or server content escaped observation")
	}
}

func TestObserveJobRejectsAmbiguousOrUnownedExecution(t *testing.T) {
	for _, name := range []string{"job-identity", "job-missing-image", "job-contradiction", "unowned", "other-image", "multiple-pods", "restarted", "multiple-containers", "truncated-job", "truncated-pods", "malformed-job", "malformed-pods"} {
		t.Run(name, func(t *testing.T) {
			job, pods := preparationObservationObjects()
			switch name {
			case "job-identity":
				job.UID = ""
			case "job-missing-image":
				job.Spec.Template.Spec.Containers[0].Image = ""
			case "job-contradiction":
				job.Status.Conditions = append(job.Status.Conditions, batchv1.JobCondition{Type: batchv1.JobFailed, Status: corev1.ConditionTrue})
			case "unowned":
				pods.Items[0].OwnerReferences[0].UID = "unrelated-job"
			case "other-image":
				pods.Items[0].Spec = *pods.Items[0].Spec.DeepCopy()
				pods.Items[0].Spec.Containers[0].Image = "other:b"
			case "multiple-pods":
				pods.Items = append(pods.Items, *pods.Items[0].DeepCopy())
			case "restarted":
				pods.Items[0].Status.ContainerStatuses[0].RestartCount = 1
			case "multiple-containers":
				pods.Items[0].Spec.Containers = append(pods.Items[0].Spec.Containers, corev1.Container{Name: "other", Image: "other:a"})
			}
			calls := 0
			client := New(runnerFunc(func(_ context.Context, _ command.Request) model.CommandResult {
				calls++
				body, _ := json.Marshal(job)
				if calls == 2 {
					body, _ = json.Marshal(pods)
				}
				if (name == "malformed-job" && calls == 1) || (name == "malformed-pods" && calls == 2) {
					body = []byte("secret-invalid-json")
				}
				return model.CommandResult{Stdout: string(body), Truncated: (name == "truncated-job" && calls == 1) || (name == "truncated-pods" && calls == 2)}
			}))
			_, result, err := client.ObserveJob(context.Background(), "run", "cloudforge", "cf-preparation")
			if err == nil || strings.Contains(err.Error(), "secret") || result.Stdout != "" || result.Stderr != "" {
				t.Fatalf("ambiguous execution was accepted or leaked content: %v %#v", err, result)
			}
		})
	}
}

func TestObserveJobDistinguishesObservedExitAndCommandFailure(t *testing.T) {
	job, pods := preparationObservationObjects()
	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "DeadlineExceeded", Message: "secret-message"}}
	pods.Items[0].Status.ContainerStatuses[0].State.Terminated.ExitCode = 137
	pods.Items[0].Status.ContainerStatuses[0].State.Terminated.Signal = 9
	pods.Items[0].Status.ContainerStatuses[0].State.Terminated.Reason = "OOMKilled"
	calls := 0
	client := New(runnerFunc(func(_ context.Context, _ command.Request) model.CommandResult {
		calls++
		body, _ := json.Marshal(job)
		if calls == 2 {
			body, _ = json.Marshal(pods)
		}
		return model.CommandResult{Stdout: string(body)}
	}))
	state, _, err := client.ObserveJob(context.Background(), "run", "cloudforge", "cf-preparation")
	if err != nil || !state.Failed || !state.DeadlineExceeded || state.Pods[0].ExitCode != 137 || state.Pods[0].Signal != 9 || state.Pods[0].Reason != "out_of_memory" {
		t.Fatalf("observed failure lost: %#v %v", state, err)
	}
	client = New(runnerFunc(func(_ context.Context, _ command.Request) model.CommandResult {
		return model.CommandResult{ExitCode: 1, FailureType: model.FailureExit, Stdout: "secret-output", Stderr: "secret-error"}
	}))
	state, result, err := client.ObserveJob(context.Background(), "run", "cloudforge", "cf-preparation")
	if err != nil || state.Failed || result.FailureType != model.FailureExit || result.Stdout != "" || result.Stderr != "" {
		t.Fatalf("tool failure became a Job failure or leaked: %#v %#v %v", state, result, err)
	}
}
