package kubernetes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

// RedisHeartbeat is a normalized observation. It retains no key or raw payload.
type RedisHeartbeat struct {
	Present   bool
	RedisTime time.Time
	Timestamp time.Time
	TTLMS     int64
}

// Static Redis Lua observes TIME, expiry and the value atomically. STRLEN bounds
// the value before retrieval. It never mutates the key or runs application code.
const heartbeatSnapshotScript = `local t=redis.call('TIME'); local ttl=redis.call('PTTL',KEYS[1]); local r={seconds=t[1],microseconds=t[2],ttl_ms=ttl,present=ttl~=-2}; if ttl~=-2 then if redis.call('STRLEN',KEYS[1])>4096 then r.oversized=true else r.value=redis.call('GET',KEYS[1]) end end; return cjson.encode(r)`

// ObserveRedisHeartbeat uses the owned provider, not an external Redis endpoint.
// All returned command text is scrubbed; callers can report only normalized state.
func (c *Client) ObserveRedisHeartbeat(ctx context.Context, cluster, namespace, key, field string) (RedisHeartbeat, model.CommandResult, error) {
	result := c.runner.Run(ctx, command.Request{Name: "kubectl", Args: []string{"--context", "k3d-" + cluster, "--namespace", namespace, "exec", "deployment/cf-dependency-redis", "--container", "redis", "--", "redis-cli", "-e", "--raw", "EVAL", heartbeatSnapshotScript, "1", key}, Timeout: 5 * time.Second, OutputLimit: 32 * 1024})
	raw := result.Stdout
	result.Arguments = nil
	result.Stdout, result.Stderr = "", ""
	if result.ExitCode != 0 || result.FailureType != model.FailureNone {
		return RedisHeartbeat{}, result, nil
	}
	if result.Truncated {
		return RedisHeartbeat{}, result, errors.New("heartbeat observation exceeded its bound")
	}
	snapshot, err := decodeRedisHeartbeat(raw, field)
	return snapshot, result, err
}

func decodeRedisHeartbeat(raw, field string) (RedisHeartbeat, error) {
	var snapshot struct {
		Seconds      string `json:"seconds"`
		Microseconds string `json:"microseconds"`
		TTLMS        int64  `json:"ttl_ms"`
		Present      bool   `json:"present"`
		Oversized    bool   `json:"oversized"`
		Value        string `json:"value"`
	}
	invalid := func() (RedisHeartbeat, error) {
		return RedisHeartbeat{}, errors.New("invalid bounded heartbeat observation")
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&snapshot) != nil || decoder.Decode(new(any)) != io.EOF || snapshot.Oversized {
		return invalid()
	}
	seconds, e1 := strconv.ParseInt(snapshot.Seconds, 10, 64)
	micros, e2 := strconv.ParseInt(snapshot.Microseconds, 10, 64)
	if e1 != nil || e2 != nil || seconds <= 0 || micros < 0 || micros >= 1_000_000 || snapshot.TTLMS < -2 || snapshot.Present == (snapshot.TTLMS == -2) {
		return invalid()
	}
	result := RedisHeartbeat{Present: snapshot.Present, TTLMS: snapshot.TTLMS, RedisTime: time.Unix(seconds, micros*1000).UTC()}
	if !snapshot.Present {
		return result, nil
	}
	if len(snapshot.Value) > 4096 {
		return invalid()
	}
	value := json.NewDecoder(bytes.NewBufferString(snapshot.Value))
	token, err := value.Token()
	if err != nil || token != json.Delim('{') {
		return invalid()
	}
	seen, timestamp := map[string]bool{}, ""
	for value.More() {
		token, err = value.Token()
		name, ok := token.(string)
		if err != nil || !ok || seen[name] {
			return invalid()
		}
		seen[name] = true
		var rawField json.RawMessage
		if value.Decode(&rawField) != nil {
			return invalid()
		}
		if name == field && json.Unmarshal(rawField, &timestamp) != nil {
			return invalid()
		}
	}
	if token, err = value.Token(); err != nil || token != json.Delim('}') || value.Decode(new(any)) != io.EOF {
		return invalid()
	}
	result.Timestamp, err = time.Parse(time.RFC3339Nano, timestamp)
	if err != nil {
		return invalid()
	}
	if result.Timestamp.After(result.RedisTime.Add(2 * time.Second)) {
		return RedisHeartbeat{}, errors.New("heartbeat producer clock is ahead of Redis; freshness cannot be established")
	}
	return result, nil
}

// ScaleWorker only supports the zero-or-one transition of an isolated worker.
func (c *Client) ScaleWorker(ctx context.Context, cluster, namespace, deployment string, replicas int32) model.CommandResult {
	if replicas != 0 && replicas != 1 {
		return model.CommandResult{ExitCode: 2, FailureType: model.FailureExecution}
	}
	return c.runner.Run(ctx, command.Request{Name: "kubectl", Args: []string{"--context", "k3d-" + cluster, "--namespace", namespace, "scale", "deployment/" + deployment, "--replicas=" + strconv.Itoa(int(replicas))}, Timeout: 15 * time.Second, OutputLimit: 16 * 1024})
}
