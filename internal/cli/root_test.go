package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/noor15102002/cloud-forge/pkg/model"
)

func TestAnalyzeJSONContract(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"analyze", filepath.Join("..", "..", "testdata", "healthy-node"), "--format", "json"}, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	var result model.AnalysisResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.SchemaVersion != model.SchemaVersion || !result.Supported {
		t.Fatalf("unexpected contract: %#v", result)
	}
}

func TestUnsupportedApplicationExitCode(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"analyze", t.TempDir(), "--format", "json"}, &stdout, &stderr)
	if code != 1 || stderr.Len() != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestInvalidFormatExitCode(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"version", "--format", "yaml"}, &stdout, &stderr)
	if code != 2 || stdout.Len() != 0 || stderr.Len() == 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestVerboseDiagnosticsStayOnStderr(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"--verbose", "analyze", filepath.Join("..", "..", "testdata", "healthy-python"), "--format", "json"}, &stdout, &stderr)
	if code != 0 || !bytes.Contains(stderr.Bytes(), []byte("analyzing repository")) {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	var result model.AnalysisResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("verbose logging corrupted JSON output: %v", err)
	}
}
