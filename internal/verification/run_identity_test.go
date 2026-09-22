package verification

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

func TestDefaultRunIdentityFitsK3dAndPreservesCleanup(t *testing.T) {
	var builder, ownership, cluster string
	buildObserved := false
	service := New(clusterProvisionFixture(runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		args := request.Args
		if request.Name == "docker" && len(args) > 3 && args[0] == "buildx" {
			switch args[1] {
			case "create":
				builder = args[3]
				_, ownership, _ = strings.Cut(args[len(args)-1], "env.CLOUDFORGE_OWNER_ID=")
			case "build":
				buildObserved = true
				if !containsArgument(args, "cloudforge.dev/run-id="+builder) || !containsArgument(args, "cloudforge.dev/ownership="+ownership) {
					t.Fatal("image build lost the public identity or private ownership marker")
				}
			}
		}
		if request.Name == "k3d" && len(args) > 2 && args[0] == "cluster" && args[1] == "create" {
			cluster = args[2]
			if len(cluster) > 32 || cluster != builder || !containsArgument(args, "cloudforge.dev/run-id="+cluster+"@all") || !containsArgument(args, "cloudforge.dev/ownership="+ownership+"@all") {
				t.Fatal("cluster creation exceeded the supported name limit or changed resource identity")
			}
			// Stop at the execution boundary after confirming that the default
			// generated identity passed the real Docker and k3d adapter checks.
			return model.CommandResult{Command: "k3d", ExitCode: 1, FailureType: model.FailureExit}
		}
		return successfulCommand(request)
	})))
	out := service.Run(context.Background(), fixturePath(t), testOptions())
	publicBytes, publicErr := hex.DecodeString(out.Run.RunID)
	privateBytes, privateErr := hex.DecodeString(ownership)
	if publicErr != nil || len(publicBytes) != 10 || len(cluster) != 31 || cluster != "cloudforge-"+out.Run.RunID || out.Run.Environment.ClusterName != cluster || !buildObserved {
		t.Fatalf("default run did not reach k3d with its bounded public identity: id=%q cluster=%q build=%v", out.Run.RunID, cluster, buildObserved)
	}
	if privateErr != nil || len(privateBytes) != 16 || ownership == out.Run.RunID {
		t.Fatal("shortening the public identifier weakened or reused the private ownership token")
	}
	if out.ExitCode != 2 || out.Run.Status != model.StatusError || !hasDiagnosticCode(out.Run.Diagnostics, "cluster_create_failed") {
		t.Fatalf("injected creation failure lost its status or diagnostic: exit=%d status=%s diagnostics=%+v", out.ExitCode, out.Run.Status, out.Run.Diagnostics)
	}
	for _, id := range []string{"container-build", "container-scan", "environment-cleanup"} {
		evidence := evidenceByID(out.Run.Evidence, id)
		if evidence == nil || evidence.Status != model.StatusPass {
			t.Fatalf("creation failure did not preserve %s evidence: %+v", id, evidence)
		}
	}
	report, err := json.Marshal(out.Run)
	if err != nil || strings.Contains(string(report), ownership) {
		t.Fatal("private ownership identity leaked into the public report")
	}
}
