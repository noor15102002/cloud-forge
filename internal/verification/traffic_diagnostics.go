package verification

import (
	"context"
	"errors"
	"io"
	"net"
	"sort"
	"strconv"
	"syscall"

	"github.com/noor15102002/cloud-forge/pkg/model"
)

// readinessProbeError carries only a fixed category. Raw response contents,
// expected values, endpoint URLs and transport messages never reach evidence.
type readinessProbeError struct{ class string }

func (*readinessProbeError) Error() string { return "readiness acceptance did not match" }

func probeFailureClass(status int, err error) string {
	var readiness *readinessProbeError
	if errors.As(err, &readiness) {
		return readiness.class
	}
	var network net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &network) && network.Timeout():
		return "timeout"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection_refused"
	case errors.Is(err, syscall.ECONNRESET):
		return "connection_reset"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "connection_closed"
	case err != nil:
		return "transport_error"
	case status >= 100 && status <= 599:
		return "http_status"
	default:
		return "invalid_http_status"
	}
}

func semanticFailureClass(check readinessCheck) string {
	if !check.Transport {
		if check.FailureClass != "" {
			return check.FailureClass
		}
		return "transport_error"
	}
	switch check.Reason {
	case "status_mismatch":
		return "http_status"
	case "body_unreadable":
		return "body_unreadable"
	case "body_limit":
		return "body_limit"
	case "invalid_json":
		return "invalid_json"
	default:
		return "semantic_mismatch"
	}
}

// diagnostics is bounded by the 500 legal HTTP statuses and eleven fixed
// failure classes. It retains counts, never per-request data or response text.
// Dynamic names use only those fixed categories and validated status numbers.
func (t trafficObservation) diagnostics() []model.Measurement {
	if t.FailureClasses == nil {
		return nil // No traffic sampler was started.
	}
	result := []model.Measurement{
		{Name: "probe_timeout_ms", Value: strconv.FormatInt(httpTimeout.Milliseconds(), 10), Unit: "ms"},
		{Name: "probe_poll_interval_ms", Value: strconv.FormatInt(t.PollMS, 10), Unit: "ms"},
		{Name: "downtime_window_censored", Value: strconv.FormatBool(t.WindowCensored)},
	}
	for status, count := range t.HTTPStatuses {
		result = append(result, model.Measurement{Name: "probe_http_status_" + strconv.Itoa(status), Value: strconv.Itoa(count), Unit: "requests"})
	}
	for class, count := range t.FailureClasses {
		result = append(result, model.Measurement{Name: "probe_failure_" + class, Value: strconv.Itoa(count), Unit: "requests"})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func trafficPrerequisiteBlocked(id, title string, traffic trafficObservation) recoveryOutcome {
	result := lifecycleBlocked(id, title, "The fresh Service/readiness probe failed before mutation; the experiment was not started.")
	result.Evidence.Measurements = append([]model.Measurement{
		{Name: "prerequisite_request_count", Value: strconv.Itoa(traffic.Requests), Unit: "requests"},
		{Name: "prerequisite_failed_requests", Value: strconv.Itoa(traffic.Failures), Unit: "requests"},
	}, traffic.diagnostics()...)
	return result
}
