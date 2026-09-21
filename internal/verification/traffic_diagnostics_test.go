package verification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/noor15102002/cloud-forge/pkg/model"
)

func TestProbeFailureClassesAreBoundedAndSanitized(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		err    error
		class  string
	}{
		{"timeout", 0, &url.Error{Op: "Get", URL: "http://PRIVATE_TOKEN/", Err: context.DeadlineExceeded}, "timeout"},
		{"refused", 0, &net.OpError{Op: "dial", Net: "tcp", Err: fmt.Errorf("PRIVATE_TOKEN: %w", syscall.ECONNREFUSED)}, "connection_refused"},
		{"reset", 0, fmt.Errorf("PRIVATE_TOKEN: %w", syscall.ECONNRESET), "connection_reset"},
		{"unknown", 0, errors.New("PRIVATE_TOKEN"), "transport_error"},
		{"status", 503, nil, "http_status"},
		{"invalid-status", 1234567, nil, "invalid_http_status"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := probeFailureClass(test.status, test.err); got != test.class {
				t.Fatalf("class=%s want=%s", got, test.class)
			}
		})
	}
}

func TestTrafficDiagnosticsPreserveCountsWithoutCanceledRequests(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service := fixedService(successRunner())
	index := 0
	service.probe = func(ctx context.Context, _ string) (int, error) {
		index++
		switch index {
		case 1:
			return 503, nil
		case 2:
			return 0, &url.Error{URL: "http://PRIVATE_TOKEN/", Err: syscall.ECONNRESET}
		case 3:
			return 200, &readinessProbeError{class: "semantic_mismatch"}
		case 4:
			return 200, nil
		default:
			<-ctx.Done()
			return 0, ctx.Err()
		}
	}
	sampled := make(chan trafficSample, 4)
	done := make(chan trafficObservation, 1)
	go func() { done <- service.collectTraffic(ctx, "http://PRIVATE_TOKEN/", sampled, nil) }()
	for range 4 {
		<-sampled
	}
	cancel()
	observed := <-done
	if observed.Requests != 4 || observed.Failures != 3 || observed.WindowCensored {
		t.Fatalf("canceled sample changed observations: %+v", observed)
	}
	measurements := observed.diagnostics()
	for name, value := range map[string]string{
		"probe_http_status_503": "1", "probe_http_status_200": "2",
		"probe_failure_http_status": "1", "probe_failure_connection_reset": "1", "probe_failure_semantic_mismatch": "1",
		"probe_timeout_ms": "2000", "downtime_window_censored": "false",
	} {
		if measurementValue(measurements, name) != value {
			t.Errorf("%s=%q want %q", name, measurementValue(measurements, name), value)
		}
	}
	if !reflect.DeepEqual(measurements, observed.diagnostics()) {
		t.Fatal("diagnostic measurement ordering is nondeterministic")
	}
	data, _ := json.Marshal(measurements)
	if strings.Contains(string(data), "PRIVATE_TOKEN") || strings.Contains(string(data), "http://") {
		t.Fatal("raw transport data leaked")
	}
}

func TestTrafficMarksUnrecoveredWindowCensored(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	service := fixedService(successRunner())
	service.probe = func(context.Context, string) (int, error) { return 503, nil }
	sampled := make(chan trafficSample, 1)
	done := make(chan trafficObservation, 1)
	go func() { done <- service.collectTraffic(ctx, "http://unused", sampled, nil) }()
	<-sampled
	cancel()
	observed := <-done
	if !observed.WindowCensored || measurementValue(observed.diagnostics(), "downtime_window_censored") != "true" {
		t.Fatal("unobserved recovery presented as a closed outage window")
	}
}

func TestRealHTTPAndSemanticFailuresHaveDistinctSafeCounts(t *testing.T) {
	for _, scenario := range []struct {
		name  string
		class string
	}{
		{"timeout", "timeout"}, {"closed", "connection_closed"}, {"status", "http_status"},
		{"semantic", "semantic_mismatch"}, {"json", "invalid_json"}, {"body-limit", "body_limit"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch scenario.name {
				case "timeout":
					<-r.Context().Done()
				case "closed":
					conn, _, err := w.(http.Hijacker).Hijack()
					if err == nil {
						_ = conn.Close()
					}
				case "status":
					w.WriteHeader(503)
				case "semantic":
					_, _ = w.Write([]byte(`{"status":"PRIVATE_TOKEN"}`))
				case "json":
					_, _ = w.Write([]byte(`PRIVATE_TOKEN`))
				case "body-limit":
					_, _ = w.Write([]byte(strings.Repeat("PRIVATE_TOKEN", readinessBodyLimit)))
				}
			}))
			defer server.Close()
			client := directHTTPClient()
			if scenario.name == "timeout" {
				client.Timeout = 15 * time.Millisecond
			}
			checker := newReadinessChecker(client, model.ReadinessAcceptance{Status: 200, JSON: map[string]string{"status": "ready"}})
			status, err := checker.probe(context.Background(), server.URL)
			if err == nil || probeFailureClass(status, err) != scenario.class {
				t.Fatalf("wrong semantic/transport boundary: status=%d class=%s err=%v", status, probeFailureClass(status, err), err)
			}
			if strings.Contains(err.Error(), "PRIVATE_TOKEN") || strings.Contains(err.Error(), server.URL) {
				t.Fatal("raw response/transport data escaped fixed classification")
			}
		})
	}
}
