package kubernetes

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

func TestCompatibleClientServerAcceptsMatchedMinors(t *testing.T) {
	client := New(runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		if strings.Join(request.Args, " ") != "--context k3d-test version --output=json" {
			t.Fatalf("unexpected kubectl invocation: %#v", request)
		}
		return model.CommandResult{Stdout: `{"clientVersion":{"gitVersion":"v1.35.5"},"serverVersion":{"gitVersion":"v1.35.5+k3s1"}}`}
	}))
	result, reason := client.CompatibleClientServer(context.Background(), "test")
	if result.FailureType != model.FailureNone || reason != "" {
		t.Fatalf("matched minors rejected: %#v %s", result, reason)
	}
}

func TestCompatibleClientServerAcceptsOneMinorSkew(t *testing.T) {
	client := New(runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		return model.CommandResult{Stdout: `{"clientVersion":{"gitVersion":"v1.36.0"},"serverVersion":{"gitVersion":"v1.35.5+k3s1"}}`}
	}))
	result, reason := client.CompatibleClientServer(context.Background(), "test")
	if result.FailureType != model.FailureNone || reason != "" {
		t.Fatalf("supported one-minor skew rejected: %#v %s", result, reason)
	}
}

func TestCompatibleClientServerRejectsTwoMinorSkew(t *testing.T) {
	client := New(runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		return model.CommandResult{Stdout: `{"clientVersion":{"gitVersion":"v1.37.0"},"serverVersion":{"gitVersion":"v1.35.5+k3s1"}}`}
	}))
	result, reason := client.CompatibleClientServer(context.Background(), "test")
	if result.FailureType != model.FailureExecution || reason != "kubectl_version_skew" {
		t.Fatalf("two-minor skew accepted: %#v %s", result, reason)
	}
}

func TestCompatibleClientServerRejectsUnrecognizedOutput(t *testing.T) {
	client := New(runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		return model.CommandResult{Stdout: "Client Version: v1.35"}
	}))
	result, reason := client.CompatibleClientServer(context.Background(), "test")
	if result.FailureType != model.FailureExecution || reason != "kubectl_version_unrecognized" {
		t.Fatalf("malformed version passed: %#v %s", result, reason)
	}
}

func TestCompatibleClientServerPropagatesCommandFailure(t *testing.T) {
	client := New(runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		return model.CommandResult{FailureType: model.FailureExit, ExitCode: 1}
	}))
	result, reason := client.CompatibleClientServer(context.Background(), "test")
	if result.FailureType != model.FailureExit || reason != "kubectl_version_unavailable" {
		t.Fatalf("command failure misclassified: %#v %s", result, reason)
	}
}

func TestInstallToolsPinsKubectlToK3dMinor(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "scripts", "install-tools.sh"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !strings.Contains(body, "https://dl.k8s.io/release/v1.35.") {
		t.Fatalf("installer kubectl is not aligned to the k3d Kubernetes 1.35 minor:\n%s", body)
	}
	if strings.Contains(body, "https://dl.k8s.io/release/v1.37.") {
		t.Fatal("installer still pins kubectl v1.37 outside k3d's Kubernetes minor")
	}
}
