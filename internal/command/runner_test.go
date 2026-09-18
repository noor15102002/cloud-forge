package command

import (
	"context"
	"testing"
	"time"

	"github.com/noor15102002/cloud-forge/pkg/model"
)

func TestExecRunnerClassifiesFailuresAndBoundsOutput(t *testing.T) {
	runner := ExecRunner{}
	missing := runner.Run(context.Background(), Request{Name: "cloudforge-command-that-does-not-exist"})
	if missing.FailureType != model.FailureNotFound {
		t.Fatalf("expected not_found, got %#v", missing)
	}

	bounded := runner.Run(context.Background(), Request{Name: "sh", Args: []string{"-c", "printf 123456789"}, OutputLimit: 4})
	if bounded.Stdout != "1234" || !bounded.Truncated {
		t.Fatalf("expected bounded output, got %#v", bounded)
	}

	timedOut := runner.Run(context.Background(), Request{Name: "sh", Args: []string{"-c", "sleep 1"}, Timeout: 10 * time.Millisecond})
	if timedOut.FailureType != model.FailureTimeout {
		t.Fatalf("expected timeout, got %#v", timedOut)
	}
}
