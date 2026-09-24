package verification

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/noor15102002/cloud-forge/internal/executor/kubernetes"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

var sourceShellFailure = regexp.MustCompile(`(?m)^/(?:bin|usr/bin)/(?:sh|bash):[^\n]+: (?:Permission denied|not found)$`)

var registryServerFailure = regexp.MustCompile(`(?i)(status|http|server)[^\n]{0,100}\b5[0-9]{2}\b`)

// Docker does not provide a structured source-vs-registry classification for
// ordinary exits. Preserve structured execution failures first, then recognize
// bounded known source diagnostics; an unknown exit is not application proof.
func isApplicationBuildFailure(result model.CommandResult) bool {
	if result.Truncated || result.ExitCode <= 0 || (result.FailureType != model.FailureExit && result.FailureType != model.FailureNone) {
		return false
	}
	output := strings.ToLower(result.Stdout + " " + result.Stderr)
	if isDockerDaemonFailure(output) || registryServerFailure.MatchString(output) {
		return false
	}
	for _, v := range []string{"no space left on device", "disk quota exceeded", "cannot allocate memory", "out of memory", "temporary failure in name resolution", "no such host", "network is unreachable", "connection refused", "connection reset by peer", "tls handshake timeout", "i/o timeout", "context deadline exceeded", "too many requests"} {
		if strings.Contains(output, v) {
			return false
		}
	}
	if sourceShellFailure.MatchString(result.Stderr) {
		return true
	}
	for _, v := range []string{"dockerfile parse error", "unknown instruction", "invalid instruction", "copy failed", "file not found in build context", "failed to calculate checksum", "did not complete successfully", "executor failed running", "returned a non-zero code"} {
		if strings.Contains(output, v) {
			return true
		}
	}
	return false
}

func applicationIdentity(name, selected, root string) string {
	if strings.TrimSpace(name) != "" {
		return name
	}
	if selected != "" && selected != "." {
		return filepath.ToSlash(selected)
	}
	base := filepath.Base(root)
	if base != "" && base != "." && base != string(filepath.Separator) {
		return base
	}
	return "unknown"
}

func startupFailureSummary(ready, wanted int, restarts int32, timeout time.Duration, pods []kubernetes.PodState) string {
	summary := fmt.Sprintf("Deployment did not become ready within %s. Ready replicas: %d/%d; restarts: %d.", timeout, ready, wanted, restarts)
	var details []string
	for _, m := range podTerminationMeasurements(pods) {
		if strings.HasPrefix(m.Name, "pod_termination_previous_") && m.Value != "0" && m.Name != "pod_termination_previous_count" {
			key := strings.TrimPrefix(m.Name, "pod_termination_previous_")
			details = append(details, strings.ReplaceAll(key, "_", " ")+": "+m.Value)
		}
	}
	sort.Strings(details)
	if len(details) > 8 {
		details = details[:8]
	}
	if len(details) > 0 {
		summary += " Latest previous termination slots: " + strings.Join(details, "; ") + "."
	}
	return summary + " Root cause: not established."
}

func safeProbeMeasurements(prefix string, status int, err error, started bool) []model.Measurement {
	if !started {
		return nil
	}
	var result []model.Measurement
	if status >= 100 && status <= 599 {
		result = append(result, model.Measurement{Name: prefix + "_http_status_" + strconv.Itoa(status), Value: "1", Unit: "requests"})
	}
	if err != nil || status < 200 || status >= 300 {
		result = append(result, model.Measurement{Name: prefix + "_failure_" + probeFailureClass(status, err), Value: "1", Unit: "requests"})
	}
	return result
}

func (o httpObservation) diagnostics() []model.Measurement {
	var result []model.Measurement
	for status, count := range o.HTTPStatuses {
		result = append(result, model.Measurement{Name: "startup_probe_http_status_" + strconv.Itoa(status), Value: strconv.Itoa(count), Unit: "requests"})
	}
	for class, count := range o.FailureClasses {
		result = append(result, model.Measurement{Name: "startup_probe_failure_" + class, Value: strconv.Itoa(count), Unit: "requests"})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}
