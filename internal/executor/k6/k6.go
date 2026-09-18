// Package k6 runs bounded HTTP load profiles and normalizes their summary.
package k6

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

// Profile is a bounded load profile.
type Profile struct {
	VirtualUsers int
	Duration     time.Duration
}

// Summary contains the stable metrics CloudForge exposes.
type Summary struct {
	RequestCount  int64
	ThroughputRPS float64
	ErrorRate     float64
	P50MS         float64
	P95MS         float64
	P99MS         float64
}

// Client invokes k6 through the shared command runner.
type Client struct{ runner command.Runner }

// New creates a k6 adapter.
func New(runner command.Runner) *Client { return &Client{runner: runner} }

// Run executes one generated, bounded script and reads k6's summary export.
func (c *Client) Run(ctx context.Context, workspace, url string, profile Profile) (Summary, model.CommandResult, error) {
	if profile.VirtualUsers < 1 || profile.VirtualUsers > 64 {
		return Summary{}, model.CommandResult{}, fmt.Errorf("virtual users must be between 1 and 64")
	}
	if profile.Duration < time.Second || profile.Duration > time.Minute {
		return Summary{}, model.CommandResult{}, fmt.Errorf("duration must be between 1s and 1m")
	}
	scriptPath := filepath.Join(workspace, "load.js")
	summaryPath := filepath.Join(workspace, "k6-summary.json")
	script := "import http from 'k6/http';\nimport { check } from 'k6';\nhttp.setResponseCallback(http.expectedStatuses({ min: 200, max: 299 }));\n\nexport default function () {\n  const response = http.get(" + strconv.Quote(url) + ", { redirects: 0, timeout: '2s' });\n  check(response, { 'status is successful': (value) => value.status >= 200 && value.status < 300 });\n}\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
		return Summary{}, model.CommandResult{}, fmt.Errorf("write k6 script: %w", err)
	}
	result := c.runner.Run(ctx, command.Request{
		Name: "k6", Args: []string{"run", "--quiet", "--no-color", "--vus", strconv.Itoa(profile.VirtualUsers), "--duration", profile.Duration.String(), "--summary-trend-stats", "p(50),p(95),p(99)", "--summary-export", summaryPath, scriptPath},
		Timeout: profile.Duration + 30*time.Second, OutputLimit: 128 * 1024,
	})
	if result.FailureType != model.FailureNone || result.ExitCode != 0 {
		return Summary{}, result, nil
	}
	// #nosec G304 -- summaryPath is a fixed filename under CloudForge's private temporary workspace.
	data, err := os.ReadFile(summaryPath)
	if err != nil {
		return Summary{}, result, fmt.Errorf("read k6 summary: %w", err)
	}
	summary, err := parseSummary(data)
	if err != nil {
		return Summary{}, result, err
	}
	return summary, result, nil
}

type exportedSummary struct {
	Metrics map[string]json.RawMessage `json:"metrics"`
}

func parseSummary(data []byte) (Summary, error) {
	var exported exportedSummary
	if err := json.Unmarshal(data, &exported); err != nil {
		return Summary{}, fmt.Errorf("decode k6 summary: %w", err)
	}
	value := func(metric string, fields ...string) (float64, error) {
		raw, ok := exported.Metrics[metric]
		if !ok {
			return 0, fmt.Errorf("decode k6 summary: missing metric %s", metric)
		}
		var item map[string]json.RawMessage
		if err := json.Unmarshal(raw, &item); err != nil {
			return 0, fmt.Errorf("decode k6 summary metric %s: %w", metric, err)
		}
		if nested, ok := item["values"]; ok {
			if err := json.Unmarshal(nested, &item); err != nil {
				return 0, fmt.Errorf("decode k6 summary metric %s values: %w", metric, err)
			}
		}
		for _, field := range fields {
			if rawValue, exists := item[field]; exists {
				var result float64
				if err := json.Unmarshal(rawValue, &result); err != nil {
					return 0, fmt.Errorf("decode k6 summary metric %s field %s: %w", metric, field, err)
				}
				return result, nil
			}
		}
		return 0, fmt.Errorf("decode k6 summary: missing %s.%s", metric, fields[0])
	}
	count, err := value("http_reqs", "count")
	if err != nil {
		return Summary{}, err
	}
	rate, err := value("http_reqs", "rate")
	if err != nil {
		return Summary{}, err
	}
	failures, err := value("http_req_failed", "rate", "value")
	if err != nil {
		return Summary{}, err
	}
	p50, err := value("http_req_duration", "p(50)")
	if err != nil {
		return Summary{}, err
	}
	p95, err := value("http_req_duration", "p(95)")
	if err != nil {
		return Summary{}, err
	}
	p99, err := value("http_req_duration", "p(99)")
	if err != nil {
		return Summary{}, err
	}
	return Summary{RequestCount: int64(count), ThroughputRPS: rate, ErrorRate: failures, P50MS: p50, P95MS: p95, P99MS: p99}, nil
}
