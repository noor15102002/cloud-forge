package verification

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"sync"

	"github.com/noor15102002/cloud-forge/pkg/model"
)

const readinessBodyLimit = 64 << 10

type readinessCheck struct {
	HTTPStatus    int
	Transport     bool
	StatusMatches bool
	Matches       map[string]bool
	Reason        string
	Success       bool
}
type readinessChecker struct {
	client     *http.Client
	acceptance model.ReadinessAcceptance
	mu         sync.Mutex
	last       readinessCheck
}

func newReadinessChecker(client *http.Client, acceptance model.ReadinessAcceptance) *readinessChecker {
	return &readinessChecker{client: client, acceptance: acceptance}
}
func (c *readinessChecker) probe(ctx context.Context, url string) (int, error) {
	check := c.check(ctx, url)
	c.mu.Lock()
	if ctx.Err() == nil || !c.last.Transport {
		c.last = check
	}
	c.mu.Unlock()
	if !check.Success {
		return check.HTTPStatus, errors.New("readiness acceptance did not match")
	}
	return check.HTTPStatus, nil
}
func (c *readinessChecker) check(ctx context.Context, url string) readinessCheck {
	result := readinessCheck{Matches: map[string]bool{}, Reason: "transport_unavailable"}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return result
	}
	response, err := c.client.Do(request)
	if err != nil {
		return result
	}
	defer func() { _ = response.Body.Close() }()
	result.Transport = true
	result.HTTPStatus = response.StatusCode
	result.StatusMatches = response.StatusCode == c.acceptance.Status
	if !result.StatusMatches {
		result.Reason = "status_mismatch"
		return result
	}
	if len(c.acceptance.JSON) == 0 {
		result.Success = true
		result.Reason = "matched"
		return result
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, readinessBodyLimit+1))
	if err != nil {
		result.Reason = "body_unreadable"
		return result
	}
	if len(body) > readinessBodyLimit {
		result.Reason = "body_limit"
		return result
	}
	object, err := readinessObject(body)
	if err != nil {
		result.Reason = "invalid_json"
		return result
	}
	result.Success = true
	for key, expected := range c.acceptance.JSON {
		var actual string
		raw, ok := object[key]
		matches := ok && json.Unmarshal(raw, &actual) == nil && actual == expected
		result.Matches[key] = matches
		result.Success = result.Success && matches
	}
	if result.Success {
		result.Reason = "matched"
	} else {
		result.Reason = "assertion_mismatch"
	}
	return result
}
func readinessObject(body []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return nil, errors.New("expected object")
	}
	object := map[string]json.RawMessage{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, errors.New("invalid key")
		}
		if _, exists := object[key]; exists {
			return nil, errors.New("duplicate key")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		object[key] = value
	}
	if _, err = decoder.Token(); err != nil {
		return nil, err
	}
	var extra any
	if !errors.Is(decoder.Decode(&extra), io.EOF) {
		return nil, errors.New("trailing JSON")
	}
	return object, nil
}
func (c *readinessChecker) evidence(duration int64) model.Evidence {
	c.mu.Lock()
	last := c.last
	c.mu.Unlock()
	status := model.StatusFail
	if last.Success {
		status = model.StatusPass
	}
	measurements := []model.Measurement{{Name: "http_transport_available", Value: strconv.FormatBool(last.Transport)}, {Name: "http_status", Value: strconv.Itoa(last.HTTPStatus)}, {Name: "expected_status_matched", Value: strconv.FormatBool(last.StatusMatches)}}
	keys := make([]string, 0, len(c.acceptance.JSON))
	for key := range c.acceptance.JSON {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		measurements = append(measurements, model.Measurement{Name: "json_" + key + "_matched", Value: strconv.FormatBool(last.Matches[key])})
	}
	return model.Evidence{ExperimentID: "semantic-readiness", Title: "Configured HTTP readiness acceptance", Status: status, Summary: "Readiness acceptance: " + last.Reason + "; response values omitted.", DurationMS: duration, Measurements: measurements}
}
