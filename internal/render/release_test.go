package render

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/noor15102002/cloud-forge/internal/regression"
	"github.com/noor15102002/cloud-forge/pkg/model"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	goldhtml "github.com/yuin/goldmark/renderer/html"
	"golang.org/x/net/html"
)

func renderedMarkdown(t *testing.T, markdown string) *html.Node {
	t.Helper()
	var output bytes.Buffer
	parser := goldmark.New(goldmark.WithExtensions(extension.GFM), goldmark.WithRendererOptions(goldhtml.WithUnsafe()))
	if err := parser.Convert([]byte(markdown), &output); err != nil {
		t.Fatal(err)
	}
	document, err := html.Parse(&output)
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func nodesNamed(node *html.Node, tag string) []*html.Node {
	var found []*html.Node
	if node.Type == html.ElementNode && node.Data == tag {
		found = append(found, node)
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		found = append(found, nodesNamed(child, tag)...)
	}
	return found
}

func nodeText(node *html.Node) string {
	if node.Type == html.TextNode {
		return node.Data
	}
	var result strings.Builder
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		result.WriteString(nodeText(child))
	}
	return result.String()
}

func TestMarkdownEscapingRendersOriginalTextOnce(t *testing.T) {
	for _, input := range []string{
		`worker's "ready" — ready | yes & <unknown>`,
		`@team [link](https://example.test) #123 _ready_ *yes* ` + "`code`" + ` \ path`,
		`www.example.test someone@example.test https://example.test`,
		`WWW.example.test WwW.example.test HTTPS://example.test`,
		`literal &#39; &amp; and <script>alert("x")</script>`,
	} {
		document := renderedMarkdown(t, markdownText(input))
		paragraphs := nodesNamed(document, "p")
		if len(paragraphs) != 1 || nodeText(paragraphs[0]) != input {
			t.Fatalf("text did not survive Markdown→HTML: input=%q output=%q markdown=%q", input, nodeText(document), markdownText(input))
		}
		for _, tag := range []string{"a", "script", "em", "strong", "code"} {
			if len(nodesNamed(document, tag)) > 0 {
				t.Fatalf("untrusted text became active %s markup", tag)
			}
		}
	}
	if got := markdownText(`A worker's "ready" response: received.`); got != `A worker's "ready" response: received.` {
		t.Fatalf("ordinary prose was unnecessarily escaped: %q", got)
	}
}

func TestVerificationMarkdownTableCellsAndGitHubMarker(t *testing.T) {
	value := `worker's "ready" — yes | no & <unknown>`
	run := model.VerificationRun{SchemaVersion: model.VerificationSchemaVersion, Status: model.StatusFail, Application: value, Evidence: []model.Evidence{{ExperimentID: "rolling-deployment", Title: value, Status: model.StatusFail, Summary: value}}}
	var output bytes.Buffer
	if err := VerificationMarkdown(&output, run); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(output.String(), "<!-- cloudforge-verification-report:"+model.VerificationSchemaVersion+" -->\n") {
		t.Fatal("GitHub reporter marker missing")
	}
	document := renderedMarkdown(t, output.String())
	rows := nodesNamed(document, "tr")
	if len(rows) != 2 {
		t.Fatalf("unexpected table rows: %d", len(rows))
	}
	cells := nodesNamed(rows[1], "td")
	if len(cells) != 4 || nodeText(cells[0]) != value || nodeText(cells[3]) != value {
		t.Fatalf("escaped values broke table columns or text: %q", nodeText(rows[1]))
	}
}

func TestTerminalFailureCategoriesAreDecisiveBoundedAndSafe(t *testing.T) {
	run := model.VerificationRun{Status: model.StatusFail, Evidence: []model.Evidence{{ExperimentID: "rolling-deployment", Status: model.StatusFail, Measurements: []model.Measurement{
		{Name: "request_count", Value: "14"}, {Name: "failed_requests", Value: "1"},
		{Name: "probe_failure_connection_closed", Value: "1"}, {Name: "probe_http_status_429", Value: "531"},
		{Name: "final_probe_failure_timeout", Value: "1"}, {Name: "startup_probe_failure_semantic_mismatch", Value: "1"},
		{Name: "probe_failure_private-secret", Value: "123"}, {Name: "probe_http_status_999", Value: "123"},
		// #nosec G101 -- synthetic credential sentinel verifies that raw transport text never reaches terminal diagnostics.
		{Name: "probe_failure_transport_error", Value: "https://user:private-secret@example.test/"},
	}}}}
	before, _ := json.Marshal(run)
	var output bytes.Buffer
	if err := VerificationText(&output, run); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"Failed requests: 1 / 14", "connection_closed: 1", "HTTP 429: 531", "Final timeout: 1", "Startup semantic_mismatch: 1"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("missing decisive category %q: %s", expected, output.String())
		}
	}
	for _, secret := range []string{"private-secret", "https://", "999"} {
		if strings.Contains(output.String(), secret) {
			t.Fatalf("unsafe diagnostic exposed: %s", output.String())
		}
	}
	after, _ := json.Marshal(run)
	if !bytes.Equal(before, after) {
		t.Fatal("rendering mutated evidence")
	}
	for status := 400; status < 450; status++ {
		run.Evidence[0].Measurements = append(run.Evidence[0].Measurements, model.Measurement{Name: fmt.Sprintf("probe_http_status_%d", status), Value: "1"})
	}
	output.Reset()
	if err := VerificationText(&output, run); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "more categories") || strings.Count(output.String(), "    HTTP ") > terminalFailureReasonLimit {
		t.Fatalf("terminal reasons were not bounded: %s", output.String())
	}
}

func TestScanSummaryUsesRetainedCountsAndPrioritizesBoundedFindings(t *testing.T) {
	run := model.VerificationRun{Status: model.StatusWarn, Evidence: []model.Evidence{{ExperimentID: "container-scan", Status: model.StatusWarn, Measurements: []model.Measurement{
		{Name: "vulnerabilities", Value: "235"}, {Name: "severity_critical", Value: "0"}, {Name: "severity_high", Value: "52"},
		{Name: "severity_medium", Value: "82"}, {Name: "severity_low", Value: "99"}, {Name: "severity_info", Value: "2"},
		{Name: "known_fix_available", Value: "8"}, {Name: "scanned_image_reference", Value: "cloudforge:test-a"},
		{Name: "scanned_image_id", Value: "sha256:abc"}, {Name: "scan_schema_version", Value: "2"}, {Name: "scan_scope", Value: "image_a"},
	}}}}
	for index := 0; index < 235; index++ {
		run.Findings = append(run.Findings, model.Finding{ID: fmt.Sprintf("security.%03d", index), Category: "security", Status: model.StatusWarn, Severity: model.SeverityLow, Summary: "Trivy reported a finding"})
	}
	run.Findings[234].Severity = model.SeverityHigh
	before := append([]model.Finding(nil), run.Findings...)
	var terminal, markdown bytes.Buffer
	if err := VerificationText(&terminal, run); err != nil {
		t.Fatal(err)
	}
	if err := VerificationMarkdown(&markdown, run); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"Container scan: WARN", "Findings: 235", "Critical: 0", "High: 52", "Medium: 82", "Low: 99", "Unknown/Info: 2", "Known fix available: 8", "Trivy reported 235 normalized vulnerability findings", "Scan scope: image_a", "Scanned image: cloudforge:test-a"} {
		if !strings.Contains(terminal.String(), expected) {
			t.Fatalf("missing scan summary %q: %s", expected, terminal.String())
		}
	}
	if strings.Count(terminal.String(), "Trivy reported a finding") != terminalFindingLimit || !strings.Contains(terminal.String(), "WARN/HIGH security.234") || !strings.Contains(terminal.String(), "225 more actionable") {
		t.Fatalf("terminal findings not bounded/prioritized: %s", terminal.String())
	}
	if strings.Count(markdown.String(), "Trivy reported a finding") != markdownFindingLimit || !strings.Contains(markdown.String(), "security.234") {
		t.Fatal("Markdown findings not bounded/prioritized")
	}
	if !reflect.DeepEqual(before, run.Findings) {
		t.Fatal("priority ordering mutated canonical findings")
	}
}

func TestHistoricalScanDoesNotGainMissingMetadata(t *testing.T) {
	for _, status := range []model.Status{model.StatusPass, model.StatusError} {
		run := model.VerificationRun{Status: status, Evidence: []model.Evidence{{ExperimentID: "container-scan", Status: status}}}
		var terminal, markdown bytes.Buffer
		_ = VerificationText(&terminal, run)
		_ = VerificationMarkdown(&markdown, run)
		for _, forbidden := range []string{"Findings: 0", "Known fix available", "Trivy reported", "Image ID", "Scan scope"} {
			if strings.Contains(terminal.String()+markdown.String(), forbidden) {
				t.Fatalf("missing historical scan evidence synthesized: %q", forbidden)
			}
		}
	}
}

func TestNumericalComparisonOutputIsAdvisoryWithoutRegradingHistoricalData(t *testing.T) {
	run := reportFixture()
	before, _ := json.Marshal(run.Comparison)
	var terminal, markdown bytes.Buffer
	_ = VerificationText(&terminal, run)
	_ = VerificationMarkdown(&markdown, run)
	if !strings.Contains(terminal.String(), "2 advisory numerical changes") || !strings.Contains(terminal.String(), "ADVISORY load-profile") || !strings.Contains(markdown.String(), "experimental and advisory") {
		t.Fatal("numerical policy not explicit in both rendered formats")
	}
	after, _ := json.Marshal(run.Comparison)
	if !bytes.Equal(before, after) {
		t.Fatal("historical comparison was rewritten")
	}
}

func TestLargeSchemaValidReportProducesBoundedRenderedGitHubComment(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required for the GitHub reporter integration; its standalone tests run in CI")
	}
	run := model.VerificationRun{
		SchemaVersion: model.VerificationSchemaVersion, RunID: "reporter-integration", Status: model.StatusError,
		Application: `worker's "ready" — sample`, StartedAt: "2026-09-18T12:00:00Z",
		Producer:    &model.BuildIdentity{Version: "test", Commit: strings.Repeat("a", 40)},
		Environment: model.VerificationEnvironment{Backend: "k3d"},
		Evidence: []model.Evidence{
			{ExperimentID: "original-failure", Title: "Application's original failure", Status: model.StatusFail, Summary: "connection_closed: 1 / 14"},
			{ExperimentID: "restoration", Title: "Baseline restored", Status: model.StatusPass, Summary: "Restoration completed"},
		},
	}
	for index := 0; index < 300; index++ {
		run.Evidence = append(run.Evidence, model.Evidence{ExperimentID: fmt.Sprintf("observation-%03d", index), Title: strings.Repeat("é", 300), Status: model.StatusError, Summary: strings.Repeat("界", 300)})
	}
	for index := range run.Evidence {
		run.Evidence[index].Execution = &model.ExperimentExecution{Executed: true}
	}
	var jsonOutput, markdown bytes.Buffer
	if err := JSON(&jsonOutput, run); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "verification.json")
	if err := os.WriteFile(path, jsonOutput.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	validated, err := regression.Load(path)
	if err != nil {
		t.Fatalf("large test report did not satisfy the supported schema: %v", err)
	}
	if err := VerificationMarkdown(&markdown, validated); err != nil {
		t.Fatal(err)
	}
	if markdown.Len() <= 60000 {
		t.Fatal("fixture did not exercise large-report fallback")
	}
	reporter, err := filepath.Abs(filepath.Join("..", "..", ".github", "actions", "report", "comment.cjs"))
	if err != nil {
		t.Fatal(err)
	}
	// #nosec G204 -- runs the fixed local reporter with Node discovered on PATH;
	// report contents are stdin data, never executable code or command arguments.
	command := exec.Command(node, "-e", `const fs = require('node:fs'); const reporter = require(process.argv[1]); process.stdout.write(reporter.commentBody(fs.readFileSync(0, 'utf8'), 'https://github.com/owner/repo/actions/runs/123'));`, reporter)
	command.Stdin = &markdown
	compact, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("large report fallback failed: %v %s", err, compact)
	}
	if len(compact) > 60000 || !bytes.Contains(compact, []byte("Application's original failure")) || !bytes.Contains(compact, []byte("**Status:** ERROR")) {
		t.Fatalf("compact comment lost verdict/earlier failure or exceeded limit: %s", compact)
	}
	document := renderedMarkdown(t, string(compact))
	if !strings.Contains(nodeText(document), run.Application) || !strings.Contains(nodeText(document), "Application's original failure") {
		t.Fatal("published comment did not render application identity and original failure correctly")
	}
	for _, row := range nodesNamed(document, "tr") {
		if cells := nodesNamed(row, "td"); len(cells) != 0 && len(cells) != 4 {
			t.Fatalf("compact report broke table structure: %d cells", len(cells))
		}
	}
}
