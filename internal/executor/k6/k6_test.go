package k6

import (
	"context"
	"os"
	"testing"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

type runnerFunc func(context.Context, command.Request) model.CommandResult

func (f runnerFunc) Run(ctx context.Context, request command.Request) model.CommandResult {
	return f(ctx, request)
}

func TestRunParsesNormalizedSummary(t *testing.T) {
	client := New(runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		for i, arg := range request.Args {
			if arg == "--summary-export" {
				_ = os.WriteFile(request.Args[i+1], []byte(`{"metrics":{"http_reqs":{"values":{"count":120,"rate":12.5}},"http_req_failed":{"values":{"rate":0.01}},"http_req_duration":{"values":{"p(50)":4.2,"p(95)":8.4,"p(99)":12.6}}}}`), 0o600)
			}
		}
		return model.CommandResult{Command: request.Name, Arguments: request.Args}
	}))
	summary, result, err := client.Run(context.Background(), t.TempDir(), "http://127.0.0.1:8080/ready", Profile{VirtualUsers: 4, Duration: 1_000_000_000})
	if err != nil || result.ExitCode != 0 || summary.RequestCount != 120 || summary.P95MS != 8.4 {
		t.Fatalf("unexpected result: summary=%#v result=%#v err=%v", summary, result, err)
	}
}

func TestRunRejectsMalformedSummary(t *testing.T) {
	client := New(runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		for i, arg := range request.Args {
			if arg == "--summary-export" {
				_ = os.WriteFile(request.Args[i+1], []byte(`{}`), 0o600)
			}
		}
		return model.CommandResult{}
	}))
	if _, _, err := client.Run(context.Background(), t.TempDir(), "http://127.0.0.1", Profile{VirtualUsers: 1, Duration: 1_000_000_000}); err == nil {
		t.Fatal("expected malformed summary error")
	}
}

func TestParseSummarySupportsCurrentFlatExport(t *testing.T) {
	summary, err := parseSummary([]byte(`{"metrics":{"http_reqs":{"count":193,"rate":191.174},"http_req_failed":{"passes":0,"fails":193,"value":0},"http_req_duration":{"p(50)":10.2,"p(95)":10.5,"p(99)":11.3}}}`))
	if err != nil || summary.RequestCount != 193 || summary.ErrorRate != 0 || summary.P99MS != 11.3 {
		t.Fatalf("unexpected current k6 summary: summary=%#v err=%v", summary, err)
	}
}
