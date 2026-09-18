package verification

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	k6executor "github.com/noor15102002/cloud-forge/internal/executor/k6"
	"github.com/noor15102002/cloud-forge/internal/executor/kubernetes"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

func TestLoadWithoutHPAProducesMetricsAndExplicitSkip(t *testing.T) {
	runner := loadRunner(`{"metrics":{"http_reqs":{"values":{"count":20,"rate":10}},"http_req_failed":{"values":{"rate":0}},"http_req_duration":{"values":{"p(50)":1,"p(95)":2,"p(99)":3}}}}`, "")
	service := New(runner)
	service.loadProfile = k6executor.Profile{VirtualUsers: 2, Duration: time.Second}
	load, autoscaling := service.runLoadAndAutoscaling(context.Background(), k6executor.New(runner), kubernetes.New(runner), plan{readinessURL: "http://127.0.0.1:8080/ready", hpaSkipReason: "No HPA was discovered."}, t.TempDir(), "")
	if load.ExitCode != 0 || load.Evidence.Status != model.StatusPass || measurementValue(load.Evidence.Measurements, "throughput_rps") != "10.000" {
		t.Fatalf("unexpected load evidence: %#v", load)
	}
	if autoscaling.Evidence.Status != model.StatusSkipped || autoscaling.Evidence.Summary != "No HPA was discovered." {
		t.Fatalf("unexpected autoscaling skip: %#v", autoscaling)
	}
}

func TestUnavailableHPAMetricsAreExplicitlySkipped(t *testing.T) {
	hpa := `{"status":{"currentReplicas":2,"desiredReplicas":2,"conditions":[{"type":"ScalingActive","status":"False","reason":"FailedGetResourceMetric"}]}}`
	runner := loadRunner(`{"metrics":{"http_reqs":{"values":{"count":20,"rate":10}},"http_req_failed":{"values":{"rate":0}},"http_req_duration":{"values":{"p(50)":1,"p(95)":2,"p(99)":3}}}}`, hpa)
	service := New(runner)
	service.poll = time.Millisecond
	service.hpaMetricsTimeout = 3 * time.Millisecond
	service.loadProfile = k6executor.Profile{VirtualUsers: 2, Duration: time.Second}
	current := plan{clusterName: "test", workloadName: "api", readinessURL: "http://127.0.0.1:8080/ready", hpaName: "api", hpaTargetCPU: 70}
	load, autoscaling := service.runLoadAndAutoscaling(context.Background(), k6executor.New(runner), kubernetes.New(runner), current, t.TempDir(), "hpa.yaml")
	if load.Evidence.Status != model.StatusPass || autoscaling.Evidence.Status != model.StatusSkipped || autoscaling.Diagnostic == nil || autoscaling.Diagnostic.Code != "hpa_metrics_unavailable" || autoscaling.Diagnostic.Guidance != "FailedGetResourceMetric" {
		t.Fatalf("unexpected unavailable metrics outcome: load=%#v autoscaling=%#v", load, autoscaling)
	}
}

func TestLoadErrorsRemainApplicationFailures(t *testing.T) {
	runner := loadRunner(`{"metrics":{"http_reqs":{"values":{"count":20,"rate":10}},"http_req_failed":{"values":{"rate":0.1}},"http_req_duration":{"values":{"p(50)":1,"p(95)":2,"p(99)":3}}}}`, "")
	service := New(runner)
	service.loadProfile = k6executor.Profile{VirtualUsers: 2, Duration: time.Second}
	load, autoscaling := service.runLoadAndAutoscaling(context.Background(), k6executor.New(runner), kubernetes.New(runner), plan{readinessURL: "http://127.0.0.1:8080/ready"}, t.TempDir(), "")
	if load.ExitCode != 1 || load.Evidence.Status != model.StatusFail || measurementValue(load.Evidence.Measurements, "failed_requests") != "2" || autoscaling.Evidence.Status != model.StatusSkipped {
		t.Fatalf("unexpected load failure: load=%#v autoscaling=%#v", load, autoscaling)
	}
}

func loadRunner(summary, hpa string) command.Runner {
	return runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		result := model.CommandResult{Command: request.Name, Arguments: request.Args}
		if request.Name == "k6" {
			for index, argument := range request.Args {
				if argument == "--summary-export" && index+1 < len(request.Args) {
					_ = os.WriteFile(request.Args[index+1], []byte(summary), 0o600)
				}
			}
		}
		if request.Name == "kubectl" && containsArgument(request.Args, "horizontalpodautoscaler") {
			result.Stdout = hpa
		}
		return result
	})
}
