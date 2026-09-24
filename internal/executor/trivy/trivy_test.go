package trivy

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

const testImageID = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func envelope(results string) string {
	suffix := ""
	if results != "omitted" {
		suffix = `,"Results":` + results
	}
	return `{"SchemaVersion":2,"ArtifactName":"example:test","ArtifactType":"container_image","Metadata":{"ImageID":"` + testImageID + `"}` + suffix + `}`
}

const findingsResult = `[{"Target":"example:test (alpine 3.23)","Class":"os-pkgs","Type":"alpine","Vulnerabilities":[{"VulnerabilityID":"CVE-2026-0002","PkgName":"zlib","InstalledVersion":"1.0","FixedVersion":"1.1","Severity":"CRITICAL"},{"VulnerabilityID":"CVE-2026-0001","PkgName":"libc","InstalledVersion":"2.0","Severity":"LOW"}]}]`

func TestParseNormalizesAndSortsVulnerabilities(t *testing.T) {
	findings, err := Parse([]byte(envelope(findingsResult)))
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 2 || findings[0].ID != "security.trivy.cve-2026-0001.libc" || findings[1].Severity != model.SeverityCritical {
		t.Fatalf("unexpected findings: %#v", findings)
	}
	if findings[1].Remediation != "Update zlib to 1.1 or a later compatible fixed version." {
		t.Fatal(findings[1])
	}
	if findings[0].Source.Field != "alpine 3.23; class=os-pkgs; type=alpine" {
		t.Fatal(findings[0].Source)
	}
}
func TestParseRejectsUnusableObservations(t *testing.T) {
	cases := map[string]string{
		"null": "null", "empty": "{}", "unrelated": `{"SchemaVersion":2,"unexpected":true}`,
		"truncated": `{"SchemaVersion":2`, "array": "[]", "missing_identity": `{"SchemaVersion":2,"ArtifactType":"container_image","Results":[]}`,
		"unsupported_schema": strings.Replace(envelope("[]"), `"SchemaVersion":2`, `"SchemaVersion":3`, 1),
		"wrong_type":         strings.Replace(envelope("[]"), "container_image", "filesystem", 1),
		"null_result":        envelope(`[null]`), "unknown_result": envelope(`[{}]`),
		"invalid_record":    envelope(`[{"Target":"x","Class":"os-pkgs","Type":"alpine","Vulnerabilities":[{}]}]`),
		"duplicate_key":     strings.Replace(envelope("[]"), `"SchemaVersion":2`, `"SchemaVersion":1,"SchemaVersion":2`, 1),
		"malformed_results": envelope(`{}`),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			findings, err := Parse([]byte(input))
			if err == nil || findings != nil {
				t.Fatalf("unusable scan synthesized findings: %v %#v", err, findings)
			}
		})
	}
}
func TestValidEmptySupportedRepresentations(t *testing.T) {
	for _, results := range []string{"omitted", "null", "[]", `[{"Target":"x","Class":"lang-pkgs","Type":"python-pkg"}]`, `[{"Target":"x","Class":"os-pkgs","Type":"alpine","Vulnerabilities":null}]`} {
		t.Run(results, func(t *testing.T) {
			f, err := Parse([]byte(envelope(results)))
			if err != nil || len(f) != 1 || f[0].Status != model.StatusPass {
				t.Fatalf("valid empty scan: %v %#v", err, f)
			}
		})
	}
}
func TestScanRequiresReliableMatchingSubject(t *testing.T) {
	cases := []struct {
		name, body, id string
		result         model.CommandResult
		wantError      bool
		findings       int
	}{
		{name: "clean", body: envelope("[]"), id: testImageID, findings: 1},
		{name: "vulnerabilities", body: envelope(findingsResult), id: testImageID, findings: 2},
		{name: "subject_mismatch", body: envelope("[]"), id: "sha256:" + strings.Repeat("b", 64), wantError: true},
		{name: "unknown_expected_subject", body: envelope("[]"), wantError: true},
		{name: "reference_mismatch", body: strings.Replace(envelope("[]"), "example:test", "different:test", 1), id: testImageID, wantError: true},
		{name: "truncated_output", body: envelope("[]"), id: testImageID, result: model.CommandResult{Truncated: true}, wantError: true},
		{name: "execution_error", body: envelope("[]"), id: testImageID, result: model.CommandResult{ExitCode: 1, FailureType: model.FailureExit}},
		{name: "cancellation", body: envelope("[]"), id: testImageID, result: model.CommandResult{ExitCode: -1, FailureType: model.FailureCanceled}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			client := New(scanRunnerFunc(func(_ context.Context, req command.Request) model.CommandResult {
				called = true
				r := tt.result
				r.Command = req.Name
				r.Arguments = req.Args
				r.Stdout = tt.body
				return r
			}))
			scan, err := client.ScanImage(context.Background(), "example:test", tt.id)
			if (err != nil) != tt.wantError {
				t.Fatalf("error=%v", err)
			}
			if tt.id == "" && called {
				t.Fatal("scanned without known image subject")
			}
			if len(scan.Findings) != tt.findings {
				t.Fatalf("findings=%#v", scan.Findings)
			}
			if tt.findings == 0 {
				for _, measurement := range scan.Measurements {
					if measurement.Name == "vulnerabilities" || measurement.Name == "known_fix_available" || strings.HasPrefix(measurement.Name, "severity_") {
						t.Fatalf("unusable scan fabricated counts: %v", scan.Measurements)
					}
				}
			}
			if tt.findings > 0 {
				found := false
				for _, m := range scan.Measurements {
					if m.Name == "scanned_image_id" && m.Value == testImageID {
						found = true
					}
				}
				if !found {
					t.Fatal("subject not retained")
				}
			}
		})
	}
}
func TestPackageIdentityCollisionsAndOrder(t *testing.T) {
	raw := []map[string]string{
		{"VulnerabilityID": "CVE-2026-1", "PkgName": "@scope/foo", "InstalledVersion": "1", "Severity": "HIGH"},
		{"VulnerabilityID": "CVE-2026-1", "PkgName": "scope-foo", "InstalledVersion": "1", "Severity": "HIGH"},
		{"VulnerabilityID": "CVE-2026-1", "PkgName": "scope-foo", "InstalledVersion": "2", "Severity": "HIGH"},
	}
	parse := func(v []map[string]string) []model.Finding {
		b, _ := json.Marshal(v)
		f, e := Parse([]byte(envelope(`[{"Target":"x","Class":"lang-pkgs","Type":"npm","Vulnerabilities":` + string(b) + `}]`)))
		if e != nil {
			t.Fatal(e)
		}
		return f
	}
	first := parse(raw)
	raw[0], raw[2] = raw[2], raw[0]
	second := parse(raw)
	if len(first) != 3 || !reflect.DeepEqual(first, second) {
		t.Fatalf("collision/order: %#v %#v", first, second)
	}
	ids := map[string]bool{}
	for _, f := range first {
		if ids[f.ID] {
			t.Fatal("duplicate public ID")
		}
		ids[f.ID] = true
	}
}

type scanRunnerFunc func(context.Context, command.Request) model.CommandResult

func (f scanRunnerFunc) Run(ctx context.Context, r command.Request) model.CommandResult {
	return f(ctx, r)
}

func TestCaseAliasesCannotEraseScanObservations(t *testing.T) {
	base := `{"SchemaVersion":2,"ArtifactName":"example:test","ArtifactType":"container_image","Metadata":{"ImageID":"sha256:` + strings.Repeat("a", 64) + `"},"Results":[{"Target":"image","Class":"os-pkgs","Type":"alpine","Vulnerabilities":[{"VulnerabilityID":"CVE-2026-1","PkgName":"test","InstalledVersion":"1","Severity":"HIGH"}]}]}`
	for _, data := range []string{
		strings.TrimSuffix(base, "}") + `,"results":[]}`,
		strings.Replace(base, `"Vulnerabilities":[`, `"vulnerabilities":[],"Vulnerabilities":[`, 1),
		strings.Replace(base, `"ImageID":`, `"imageid":"", "ImageID":`, 1),
		strings.Replace(base, `"Severity":"HIGH"`, `"Severity":"HIGH","severity":"LOW"`, 1),
		strings.Replace(base, `"Results":`, `"results":`, 1),
	} {
		if findings, err := Parse([]byte(data)); err == nil || findings != nil {
			t.Fatalf("noncanonical alias accepted: %v %v", findings, err)
		}
	}
}
