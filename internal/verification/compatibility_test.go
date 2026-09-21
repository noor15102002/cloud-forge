package verification

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

func TestVersionObservationFailuresPreserveSafeDiagnosticsAndStopExecution(t *testing.T) {
	const privateOutput = "PRIVATE_VERSION_OUTPUT"
	const privateArgument = "PRIVATE_VERSION_ARGUMENT"
	scenarios := []struct {
		name      string
		failure   model.FailureType
		exitCode  int
		truncated bool
		validData bool
		stderr    string
		want      []string
	}{
		{name: "timeout", failure: model.FailureTimeout, exitCode: -1, validData: true, want: []string{"failed (timeout; 1234 ms)", "execution deadline"}},
		{name: "canceled", failure: model.FailureCanceled, exitCode: -1, validData: true, want: []string{"failed (canceled; 1234 ms)", "verification was canceled"}},
		{name: "exit", failure: model.FailureExit, exitCode: 77, validData: true, want: []string{"failed (exit; 1234 ms)", "exited with code 77"}},
		{name: "exit-without-class", exitCode: 77, want: []string{"failed (exit; 1234 ms)", "exited with code 77"}},
		{name: "not-found", failure: model.FailureNotFound, exitCode: -1, want: []string{"failed (not_found; 1234 ms)", "available on PATH"}},
		{name: "execution", failure: model.FailureExecution, exitCode: -1, want: []string{"failed (execution; 1234 ms)", "could not be executed"}},
		{name: "unknown-class", failure: model.FailureType(privateOutput), exitCode: -1, want: []string{"failed (execution; 1234 ms)"}},
		{name: "malformed", want: []string{"completed in 1234 ms", "did not contain the required version information"}},
		{name: "truncated", truncated: true, validData: true, want: []string{"completed in 1234 ms", "output was truncated"}},
		{name: "failed-truncated", failure: model.FailureTimeout, exitCode: -1, truncated: true, validData: true, want: []string{"failed (timeout; 1234 ms)"}},
		{name: "daemon-denied", failure: model.FailureExit, exitCode: 1, stderr: "dial unix /private/docker.sock: permission denied " + privateOutput, want: []string{"failed (exit; 1234 ms)"}},
	}
	for _, phase := range []string{"docker", "kubectl-client", "kubectl-server"} {
		for _, scenario := range scenarios {
			t.Run(phase+"/"+scenario.name, func(t *testing.T) {
				var calls []command.Request
				observations := 0
				runner := runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
					calls = append(calls, request)
					result := successfulCommand(request)
					client := request.Name == "kubectl" && containsArgument(request.Args, "version") && containsArgument(request.Args, "--client=true")
					server := request.Name == "kubectl" && containsArgument(request.Args, "version") && !containsArgument(request.Args, "--client=true")
					targetProbe := phase == "docker" && request.Name == "docker" && containsArgument(request.Args, "info") || phase == "kubectl-client" && client || phase == "kubectl-server" && server
					if !targetProbe {
						return result
					}
					observations++
					if request.Timeout != 10*time.Second || request.OutputLimit != 16*1024 {
						t.Fatalf("version observation bounds changed: %+v", request)
					}
					result.Arguments = append(result.Arguments, privateArgument)
					result.DurationMS, result.FailureType, result.ExitCode = 1234, scenario.failure, scenario.exitCode
					result.Truncated = scenario.truncated
					result.Stderr = privateOutput + " " + scenario.stderr
					if !scenario.validData {
						result.Stdout = privateOutput
					}
					return result
				})
				out := fixedService(runner).Run(context.Background(), fixturePath(t), testOptions())
				if observations != 1 || out.ExitCode != 2 || out.Run.Status != model.StatusError || out.Run.Compatibility.Status != "not_validated" {
					t.Fatalf("failed prerequisite was retried or reclassified: observations=%d outcome=%+v", observations, out)
				}
				var diagnostic *model.Diagnostic
				for i := range out.Run.Diagnostics {
					if out.Run.Diagnostics[i].Code == "runtime_version_unavailable" {
						diagnostic = &out.Run.Diagnostics[i]
					}
				}
				if diagnostic == nil || diagnostic.Status != model.StatusError {
					t.Fatalf("missing native observation error: %+v", out.Run.Diagnostics)
				}
				for _, text := range scenario.want {
					if !strings.Contains(diagnostic.Guidance, text) {
						t.Fatalf("missing safe failure detail %q: %+v", text, diagnostic)
					}
				}
				if phase == "docker" && scenario.name == "daemon-denied" && !strings.Contains(diagnostic.Guidance, "Docker daemon access was denied") {
					t.Fatalf("actionable daemon guidance was lost: %+v", diagnostic)
				}
				if phase == "kubectl-server" {
					if toolVersion(out.Run.Fingerprint, "kubernetes") != "unknown" || !hasCommand(calls, "k3d", "delete") || !strings.Contains(diagnostic.Guidance, "No application deployment was attempted") {
						t.Fatalf("server failure did not retain its unknown version and cleanup boundary: %+v", out)
					}
				} else if hasCommand(calls, "docker", "buildx") || hasCommand(calls, "k3d", "create") || !strings.Contains(diagnostic.Guidance, "no application build was started") {
					t.Fatal("unobservable local tool version reached build or cluster creation")
				}
				if hasCommand(calls, "kubectl", "apply") {
					t.Fatal("unobservable version reached application deployment")
				}
				for _, evidence := range out.Run.Evidence {
					if phase == "kubectl-server" && (evidence.ExperimentID == "container-build" || evidence.ExperimentID == "container-scan") {
						continue // These observations precede the isolated server gate.
					}
					if evidence.Execution != nil && evidence.Execution.Executed {
						t.Fatalf("application experiment ran after failed prerequisite: %+v", evidence)
					}
				}
				data, err := json.Marshal(out.Run)
				if err != nil {
					t.Fatal(err)
				}
				for _, secret := range []string{privateOutput, privateArgument, "/private/docker.sock"} {
					if strings.Contains(string(data), secret) {
						t.Fatalf("report exposed raw command data %q", secret)
					}
				}
			})
		}
	}
}
