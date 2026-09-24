package verification

import (
	"context"
	"strings"
	"testing"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

func TestReleaseScanObservationStatusesAndCleanup(t *testing.T) {
	cases := []struct {
		name, body        string
		failure           model.FailureType
		truncated, cancel bool
		scan, overall     model.Status
		exit              int
		diagnostic        string
	}{
		{name: "null", body: "null", scan: model.StatusError, overall: model.StatusError, exit: 2, diagnostic: "trivy_output_invalid"},
		{name: "empty", body: "{}", scan: model.StatusError, overall: model.StatusError, exit: 2, diagnostic: "trivy_output_invalid"},
		{name: "unrelated", body: `{"SchemaVersion":2,"unexpected":true}`, scan: model.StatusError, overall: model.StatusError, exit: 2, diagnostic: "trivy_output_invalid"},
		{name: "truncated_json", body: `{"Results":`, scan: model.StatusError, overall: model.StatusError, exit: 2, diagnostic: "trivy_output_invalid"},
		{name: "truncated_output", truncated: true, scan: model.StatusError, overall: model.StatusError, exit: 2, diagnostic: "trivy_output_invalid"},
		{name: "scanner_exit", failure: model.FailureExit, scan: model.StatusError, overall: model.StatusError, exit: 2, diagnostic: "trivy_scan_failed"},
		{name: "scanner_canceled", failure: model.FailureCanceled, cancel: true, scan: model.StatusError, overall: model.StatusError, exit: 2, diagnostic: "verification_canceled"},
		{name: "clean", scan: model.StatusPass, overall: model.StatusPass},
		{name: "findings", body: "findings", scan: model.StatusWarn, overall: model.StatusWarn},
		{name: "case_alias", body: "case_alias", scan: model.StatusError, overall: model.StatusError, exit: 2, diagnostic: "trivy_output_invalid"},
		{name: "wrong_subject", body: "wrong_subject", scan: model.StatusError, overall: model.StatusError, exit: 2, diagnostic: "trivy_output_invalid"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			runner := runnerFunc(func(_ context.Context, req command.Request) model.CommandResult {
				r := successfulCommand(req)
				if req.Name == "trivy" && containsArgument(req.Args, "image") {
					r.Stdout = validTrivyReport(req)
					if tt.body != "" {
						r.Stdout = tt.body
					}
					if tt.body == "findings" {
						r.Stdout = trivyReportWithFindings(req, `[{"Target":"image","Class":"os-pkgs","Type":"alpine","Vulnerabilities":[{"VulnerabilityID":"CVE-2026-123","PkgName":"test","InstalledVersion":"1","FixedVersion":"2","Severity":"HIGH"}]}]`)
					}
					if tt.body == "case_alias" {
						r.Stdout = strings.TrimSuffix(trivyReportWithFindings(req, `[{"Target":"image","Class":"os-pkgs","Type":"alpine","Vulnerabilities":[{"VulnerabilityID":"CVE-2026-1","PkgName":"test","InstalledVersion":"1","Severity":"HIGH"}]}]`), "}") + `,"results":[]}`
					}
					if tt.body == "wrong_subject" {
						r.Stdout = strings.Replace(validTrivyReport(req), strings.Repeat("a", 64), strings.Repeat("b", 64), 1)
					}
					r.Truncated = tt.truncated
					r.FailureType = tt.failure
					if tt.failure != "" {
						r.ExitCode = 1
					}
					if tt.cancel {
						cancel()
					}
				}
				return r
			})
			out := fixedService(runner).Run(ctx, fixturePath(t), testOptions())
			scan := evidenceByID(out.Run.Evidence, "container-scan")
			if scan == nil || scan.Status != tt.scan || out.Run.Status != tt.overall || out.ExitCode != tt.exit {
				t.Fatalf("scan=%v overall=%s exit=%d diagnostics=%v", scan, out.Run.Status, out.ExitCode, out.Run.Diagnostics)
			}
			if tt.diagnostic != "" && !hasDiagnosticCode(out.Run.Diagnostics, tt.diagnostic) {
				t.Fatalf("diagnostic missing: %v", out.Run.Diagnostics)
			}
			if scan.Status == model.StatusError {
				if measurementValue(scan.Measurements, "vulnerabilities") != "" {
					t.Fatal("unusable scan synthesized a count")
				}
				for _, f := range out.Run.Findings {
					if strings.HasPrefix(f.ID, "security.trivy.") {
						t.Fatal("unusable scan synthesized findings")
					}
				}
			}
			cleanup := evidenceByID(out.Run.Evidence, "environment-cleanup")
			if cleanup == nil || cleanup.Status != model.StatusPass {
				t.Fatalf("cleanup=%v", cleanup)
			}
			build := evidenceByID(out.Run.Evidence, "container-build")
			if build == nil || build.Status != model.StatusPass {
				t.Fatal("scan rewrote build result")
			}
		})
	}
}

func TestReleaseBuildFailuresDistinguishApplicationAndEnvironment(t *testing.T) {
	for _, tt := range []struct {
		name, output string
		truncated    bool
		want         model.Status
		exit         int
	}{
		{"disk", "failed to solve: no space left on device", false, model.StatusError, 2},
		{"registry", "failed to resolve source metadata: unexpected HTTP status 503 Service Unavailable", false, model.StatusError, 2},
		{"unknown", "unrecognized tool failure", false, model.StatusError, 2},
		{"truncated", "process did not complete successfully", true, model.StatusError, 2},
		{"dockerfile", "dockerfile parse error: unknown instruction", false, model.StatusFail, 1},
		{"compiler", "process /bin/sh -c npm run build did not complete successfully: exit code: 1", false, model.StatusFail, 1},
		{"missing_source", "failed to calculate checksum of ref: /missing not found", false, model.StatusFail, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out := fixedService(runnerFunc(func(_ context.Context, req command.Request) model.CommandResult {
				if req.Name == "docker" && containsArgument(req.Args, "build") {
					return model.CommandResult{Command: req.Name, Arguments: req.Args, ExitCode: 1, FailureType: model.FailureExit, Stderr: tt.output, Truncated: tt.truncated}
				}
				return successfulCommand(req)
			})).Run(context.Background(), fixturePath(t), testOptions())
			build := evidenceByID(out.Run.Evidence, "container-build")
			if build == nil || build.Status != tt.want || out.Run.Status != tt.want || out.ExitCode != tt.exit {
				t.Fatalf("build=%v overall=%s exit=%d diagnostics=%v", build, out.Run.Status, out.ExitCode, out.Run.Diagnostics)
			}
			if !hasDiagnosticCode(out.Run.Diagnostics, "container_build_failed") {
				t.Fatal("missing build diagnostic")
			}
			cleanup := evidenceByID(out.Run.Evidence, "environment-cleanup")
			if cleanup == nil || cleanup.Status != model.StatusPass {
				t.Fatalf("cleanup=%v", cleanup)
			}
		})
	}
}
