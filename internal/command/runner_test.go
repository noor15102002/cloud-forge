package command

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"slices"
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

func TestExecRunnerEnvironmentInheritanceIsExplicit(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLOUDFORGE_COMMAND_TEST_AMBIENT", "not-for-child")
	run := func(clearEnvironment bool, env []string) []string {
		t.Helper()
		result := (ExecRunner{}).Run(context.Background(), Request{
			Name: executable, Args: []string{"-test.run=^TestCommandEnvironmentHelper$", "--", "--command-env-helper"},
			ClearEnv: clearEnvironment, Env: env,
		})
		if result.FailureType != model.FailureNone {
			t.Fatalf("helper failed: %s", result.FailureType)
		}
		var actual []string
		if err := json.Unmarshal([]byte(result.Stdout), &actual); err != nil {
			t.Fatal(err)
		}
		return actual
	}
	if inherited := run(false, []string{"CLOUDFORGE_COMMAND_TEST_AMBIENT=explicit"}); !slices.Contains(inherited, "CLOUDFORGE_COMMAND_TEST_AMBIENT=explicit") || slices.Contains(inherited, "CLOUDFORGE_COMMAND_TEST_AMBIENT=not-for-child") {
		t.Fatal("default execution did not retain explicit override semantics")
	}
	if controlled := run(true, []string{"EXPLICIT_ONLY=value"}); !reflect.DeepEqual(controlled, []string{"EXPLICIT_ONLY=value"}) {
		t.Fatal("controlled execution inherited ambient environment")
	}
	if empty := run(true, nil); len(empty) != 0 {
		t.Fatal("explicit empty environment inherited ambient environment")
	}
}

func TestCommandEnvironmentHelper(_ *testing.T) {
	if !slices.Contains(os.Args, "--command-env-helper") {
		return
	}
	data, err := json.Marshal(os.Environ())
	if err != nil {
		os.Exit(8)
	}
	fmt.Println(string(data))
	os.Exit(0)
}
