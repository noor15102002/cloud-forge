package trivy

import (
	"context"
	"testing"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

func TestParseNormalizesAndSortsVulnerabilities(t *testing.T) {
	input := []byte(`{
  "Results": [{
    "Target": "cloudforge/example:run (alpine 3.23)",
    "Vulnerabilities": [
      {"VulnerabilityID":"CVE-2026-0002","PkgName":"zlib","InstalledVersion":"1.0","FixedVersion":"1.1","Severity":"CRITICAL"},
      {"VulnerabilityID":"CVE-2026-0001","PkgName":"libc","InstalledVersion":"2.0","Severity":"LOW"}
    ]
  }]
}`)
	findings, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 2 || findings[0].ID != "security.trivy.cve-2026-0001.libc" || findings[1].Severity != model.SeverityCritical {
		t.Fatalf("unexpected findings: %#v", findings)
	}
	if findings[1].Remediation != "Update zlib to 1.1 or a later compatible fixed version." {
		t.Fatalf("missing fixed-version remediation: %#v", findings[1])
	}
	if findings[0].Source == nil || findings[0].Source.Field != "alpine 3.23" {
		t.Fatalf("image provenance was not normalized: %#v", findings[0].Source)
	}
}

func TestScanRejectsTruncatedJSON(t *testing.T) {
	client := New(scanRunnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		return model.CommandResult{Command: request.Name, Arguments: request.Args, Stdout: `{"Results":[]}`, Truncated: true}
	}))
	if _, err := client.ScanImage(context.Background(), "example:test"); err == nil {
		t.Fatal("expected truncated scan output to fail")
	}
}

func TestParseNoVulnerabilitiesProducesPassFinding(t *testing.T) {
	findings, err := Parse([]byte(`{"Results":[{"Target":"image","Vulnerabilities":null}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].Status != model.StatusPass {
		t.Fatalf("unexpected clean scan: %#v", findings)
	}
}

func TestParseRejectsMalformedJSON(t *testing.T) {
	if _, err := Parse([]byte(`{"Results":`)); err == nil {
		t.Fatal("expected malformed Trivy JSON to fail")
	}
}

type scanRunnerFunc func(context.Context, command.Request) model.CommandResult

func (f scanRunnerFunc) Run(ctx context.Context, request command.Request) model.CommandResult {
	return f(ctx, request)
}
