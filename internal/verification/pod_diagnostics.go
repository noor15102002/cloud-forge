package verification

import (
	"sort"
	"strconv"

	"github.com/noor15102002/cloud-forge/internal/executor/kubernetes"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

// podTerminationMeasurements describes only the latest successful PodList
// snapshot supplied by the caller. Current and previous Kubernetes slots can
// coexist and are counted separately; they are never accumulated across polls.
// A nil slice means no observation, while an observed empty list is explicit.
func podTerminationMeasurements(pods []kubernetes.PodState) []model.Measurement {
	if pods == nil {
		return nil
	}
	counts := map[string]int{
		"pod_termination_snapshot_pods":              len(pods),
		"pod_termination_application_status_known":   0,
		"pod_termination_application_status_unknown": 0,
		"pod_termination_current_count":              0,
		"pod_termination_previous_count":             0,
	}
	for _, pod := range pods {
		if !pod.ApplicationStatusObserved {
			counts["pod_termination_application_status_unknown"]++
			continue
		}
		counts["pod_termination_application_status_known"]++
		for _, slot := range []struct {
			name  string
			state *kubernetes.ContainerTermination
		}{{"current", pod.CurrentTermination}, {"previous", pod.PreviousTermination}} {
			if slot.state == nil {
				continue
			}
			prefix := "pod_termination_" + slot.name
			counts[prefix+"_count"]++
			reason := slot.state.Reason
			switch reason {
			case "oom_killed", "completed", "error", "cannot_run", "start_error", "deadline_exceeded":
			default:
				reason = "unknown"
			}
			counts[prefix+"_reason_"+reason]++
			counts[prefix+"_exit_code_"+terminationNumberLabel(slot.state.ExitCode, 255)]++
			counts[prefix+"_signal_"+terminationNumberLabel(slot.state.Signal, 64)]++
		}
	}
	measurements := []model.Measurement{{Name: "pod_termination_snapshot_scope", Value: "latest_successful_pod_observation_not_event_totals"}}
	for name, count := range counts {
		measurements = append(measurements, model.Measurement{Name: name, Value: strconv.Itoa(count), Unit: "pods"})
	}
	sort.Slice(measurements, func(i, j int) bool { return measurements[i].Name < measurements[j].Name })
	return measurements
}

func terminationNumberLabel(value *int32, maximum int32) string {
	if value == nil || *value < 0 || *value > maximum {
		return "unknown"
	}
	return strconv.FormatInt(int64(*value), 10)
}
