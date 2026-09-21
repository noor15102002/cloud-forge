package kubernetes

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

func heartbeatDocument(value string, ttl int64) string {
	encoded, _ := json.Marshal(map[string]any{"seconds": "1893456000", "microseconds": "123000", "ttl_ms": ttl, "present": ttl != -2, "value": value})
	return string(encoded)
}

func TestRedisHeartbeatUsesBoundedAtomicObservationAndOmitsPrivateContent(t *testing.T) {
	client := New(runnerFunc(func(_ context.Context, request command.Request) model.CommandResult {
		args := strings.Join(request.Args, " ")
		if !strings.Contains(args, "--context k3d-run --namespace cloudforge exec deployment/cf-dependency-redis --container redis -- redis-cli -e --raw EVAL") || request.Timeout != 5*time.Second || request.OutputLimit != 32*1024 || !strings.Contains(args, "STRLEN") || strings.Contains(args, "'DEL'") {
			t.Fatal("unbounded, unscoped or mutating Redis observation")
		}
		return model.CommandResult{Command: request.Name, Arguments: request.Args, Stdout: heartbeatDocument(`{"pid":1,"at":"2030-01-01T00:00:00.100Z","arbitrary":"private-payload"}`, 3000), Stderr: "private-stderr"}
	}))
	observation, result, err := client.ObserveRedisHeartbeat(context.Background(), "run", "cloudforge", "private-key", "at")
	if err != nil || !observation.Present || observation.TTLMS != 3000 || observation.RedisTime.Sub(observation.Timestamp) != 23*time.Millisecond {
		t.Fatalf("incorrect observation: %#v %v", observation, err)
	}
	encoded, _ := json.Marshal([]any{observation, result})
	if strings.Contains(string(encoded), "private-") || result.Stdout != "" || result.Stderr != "" || len(result.Arguments) != 0 {
		t.Fatal("heartbeat key, payload or command escaped the observation")
	}
}

func TestRedisHeartbeatMissingIsDifferentFromUnreadableOrMalformed(t *testing.T) {
	cases := []struct {
		name, value string
		ttl         int64
		bad         bool
	}{
		{"missing", "", -2, false},
		{"persistent", `{"at":"2030-01-01T00:00:00Z"}`, -1, false},
		{"stale-valid", `{"at":"2029-12-31T23:00:00Z"}`, 3000, false},
		{"unbounded-expiry", `{"at":"2030-01-01T00:00:00Z"}`, 65000, false},
		{"malformed", "private-malformed", 3000, true},
		{"null", "null", 3000, true},
		{"missing-field", `{"pid":1}`, 3000, true},
		{"wrong-field-type", `{"at":123}`, 3000, true},
		{"duplicate-field", `{"at":"2030-01-01T00:00:00Z","at":"2030-01-01T00:00:01Z"}`, 3000, true},
		{"trailing-value", `{"at":"2030-01-01T00:00:00Z"} {}`, 3000, true},
		{"future-clock", `{"at":"2030-01-01T00:00:04Z"}`, 3000, true},
		{"oversized", strings.Repeat("x", 4097), 3000, true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			observation, err := decodeRedisHeartbeat(heartbeatDocument(test.value, test.ttl), "at")
			if (err != nil) != test.bad {
				t.Fatalf("error=%v", err)
			}
			if err != nil && strings.Contains(err.Error(), "private-") {
				t.Fatal("parser exposed payload")
			}
			if !test.bad && observation.Present != (test.ttl != -2) {
				t.Fatal("presence changed")
			}
		})
	}
	for _, failure := range []model.CommandResult{{ExitCode: 1, FailureType: model.FailureExit}, {ExitCode: -1, FailureType: model.FailureCanceled}, {ExitCode: -1, FailureType: model.FailureTimeout}, {Truncated: true}} {
		client := New(runnerFunc(func(context.Context, command.Request) model.CommandResult {
			r := failure
			r.Stdout = "private-payload"
			r.Stderr = "private-error"
			return r
		}))
		state, result, err := client.ObserveRedisHeartbeat(context.Background(), "run", "cloudforge", "private-key", "at")
		if state.Present || (result.FailureType == model.FailureNone && err == nil) || result.Stdout != "" || result.Stderr != "" {
			t.Fatal("observer failure became reliable absence or leaked content")
		}
	}
}
