package verification

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/noor15102002/cloud-forge/internal/analyzer"
	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

func TestWorkerStaticFindingsAreScopedWithoutWeakeningHTTPAnalysis(t *testing.T) {
	root, current := workerFixture(t)
	const source = `apiVersion: apps/v1
kind: Deployment
metadata: {name: worker}
spec:
  replicas: 1
  strategy: {type: Recreate}
  selector: {matchLabels: {app: worker}}
  template:
    metadata: {labels: {app: worker}}
    spec:
      containers:
        - name: worker
          image: example/worker:test
`
	if err := os.WriteFile(filepath.Join(root, "deployment.yaml"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	analysis, err := analyzer.New().Analyze(root)
	if err != nil {
		t.Fatal(err)
	}
	wanted := map[string]model.Status{
		"container.dockerfile.port":                            model.StatusFail,
		"kubernetes.deployment.default-worker.readiness-probe": model.StatusFail,
		"kubernetes.deployment.default-worker.health-probe":    model.StatusFail,
		"kubernetes.deployment.default-worker.replicas":        model.StatusWarn,
	}
	for id, status := range wanted {
		if finding := findingByID(analysis.Findings, id); finding == nil || finding.Status != status {
			t.Fatalf("standalone finding changed: %s %+v", id, finding)
		}
	}
	s := New(runnerFunc(func(context.Context, command.Request) model.CommandResult {
		t.Fatal("finding qualification invoked runtime tools")
		return model.CommandResult{}
	}))
	options := testOptions()
	options.PlanOnly = true
	out := s.Run(context.Background(), root, options)
	if out.ExitCode != 0 || out.Run.Plan.RuntimeKind != "worker" {
		t.Fatalf("source worker contract not planned: %+v", out.Run.Diagnostics)
	}
	for _, original := range analysis.Findings {
		finding := findingByID(out.Run.Findings, original.ID)
		if _, scoped := wanted[original.ID]; scoped {
			if finding == nil || finding.Status != model.StatusSkipped || !strings.Contains(finding.Summary, "do not apply") ||
				!reflect.DeepEqual(finding.Source, original.Source) || finding.Observed != original.Observed {
				t.Fatalf("worker finding lacks scoped reason/provenance: %+v", finding)
			}
		} else if finding == nil || !reflect.DeepEqual(*finding, original) {
			t.Fatalf("unrelated worker finding changed: %s %+v", original.ID, finding)
		}
	}
	resources := findingByID(out.Run.Findings, "kubernetes.deployment.default-worker.resources")
	if resources == nil || resources.Status != model.StatusFail {
		t.Fatalf("worker resource failure was hidden: %+v", resources)
	}
	for _, kind := range []string{"", "http"} {
		config := current.config
		config.Runtime.Kind = kind
		if got := verificationFindings(analysis.Findings, config); !reflect.DeepEqual(got, analysis.Findings) {
			t.Fatalf("HTTP/default findings changed for kind %q", kind)
		}
	}
	const httpConfig = "schema_version: v1alpha6\nruntime: {kind: http, port: 8080}\ndependencies: {redis: {enabled: true}}\n"
	if err := os.WriteFile(filepath.Join(root, "cloudforge.yaml"), []byte(httpConfig), 0600); err != nil {
		t.Fatal(err)
	}
	httpOut := s.Run(context.Background(), root, options)
	if finding := findingByID(httpOut.Run.Findings, "container.dockerfile.port"); finding == nil || finding.Status != model.StatusFail {
		t.Fatalf("explicit HTTP verification hid missing port: %+v", finding)
	}
	// The worker adapter must not mutate the analyzer's source findings.
	for id, status := range wanted {
		if findingByID(analysis.Findings, id).Status != status {
			t.Fatalf("worker verification mutated standalone analysis: %s", id)
		}
	}
}

func TestWorkerFindingScopePreservesSecurityAndIndependentFailures(t *testing.T) {
	_, current := workerFixture(t)
	values := []model.Finding{
		{ID: "container.dockerfile.port", Category: "container", Status: model.StatusFail},
		{ID: "container.dockerfile.non-root", Category: "container", Status: model.StatusFail, Summary: "root user"},
		{ID: "security.trivy.example.port", Category: "security", Status: model.StatusFail, Summary: "security finding"},
		{ID: "kubernetes.deployment.default-worker.resources", Category: "kubernetes", Status: model.StatusFail, Summary: "missing resources"},
		{ID: "kubernetes.service.default-worker.ports", Category: "kubernetes", Status: model.StatusFail, Summary: "unsupported Service configuration"},
	}
	got := verificationFindings(values, current.config)
	if got[0].Status != model.StatusSkipped || !reflect.DeepEqual(got[1:], values[1:]) {
		t.Fatalf("worker context hid independent findings: %+v", got)
	}
	out := Outcome{Run: model.VerificationRun{Findings: got, Evidence: []model.Evidence{{ExperimentID: "worker-startup", Status: model.StatusPass}}}}
	finalEvidenceStatus(&out)
	if out.Run.Status != model.StatusFail || out.ExitCode != 1 {
		t.Fatalf("passing worker progress hid independent failures: %+v", out)
	}
}
