package verification

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/noor15102002/cloud-forge/internal/executor/kubernetes"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

func terminationValue(value int32) *int32 { return &value }

func terminationMeasurementValues(measurements []model.Measurement) map[string]string {
	values := map[string]string{}
	for _, measurement := range measurements {
		values[measurement.Name] = measurement.Value
	}
	return values
}

func TestPodTerminationMeasurementsSeparateSlotsAndSortSnapshotCounts(t *testing.T) {
	pods := []kubernetes.PodState{
		{ApplicationStatusObserved: true, CurrentTermination: &kubernetes.ContainerTermination{Reason: "error", ExitCode: terminationValue(1), Signal: terminationValue(0)}, PreviousTermination: &kubernetes.ContainerTermination{Reason: "oom_killed", ExitCode: terminationValue(137), Signal: terminationValue(9)}},
		{ApplicationStatusObserved: true, PreviousTermination: &kubernetes.ContainerTermination{Reason: "oom_killed", ExitCode: terminationValue(137), Signal: terminationValue(9)}},
		{ApplicationStatusObserved: true},
		{},
	}
	measurements := podTerminationMeasurements(pods)
	values := terminationMeasurementValues(measurements)
	for name, want := range map[string]string{
		"pod_termination_snapshot_pods":              "4",
		"pod_termination_application_status_known":   "3",
		"pod_termination_application_status_unknown": "1",
		"pod_termination_current_count":              "1",
		"pod_termination_previous_count":             "2",
		"pod_termination_current_reason_error":       "1",
		"pod_termination_current_exit_code_1":        "1",
		"pod_termination_current_signal_0":           "1",
		"pod_termination_previous_reason_oom_killed": "2",
		"pod_termination_previous_exit_code_137":     "2",
		"pod_termination_previous_signal_9":          "2",
		"pod_termination_snapshot_scope":             "latest_successful_pod_observation_not_event_totals",
	} {
		if values[name] != want {
			t.Fatalf("%s = %q, want %q", name, values[name], want)
		}
	}
	if !sort.SliceIsSorted(measurements, func(i, j int) bool { return measurements[i].Name < measurements[j].Name }) {
		t.Fatal("measurements are not sorted")
	}
	for _, measurement := range measurements {
		if measurement.Name != "pod_termination_snapshot_scope" && measurement.Unit != "pods" {
			t.Fatalf("slot counts imply cumulative events: %+v", measurement)
		}
	}
	if !reflect.DeepEqual(measurements, podTerminationMeasurements([]kubernetes.PodState{pods[3], pods[2], pods[1], pods[0]})) || !reflect.DeepEqual(measurements, podTerminationMeasurements(pods)) {
		t.Fatal("snapshot is nondeterministic or accumulates previous polls")
	}
}

func TestPodTerminationMeasurementsExplicitUnknownAndMissingObservation(t *testing.T) {
	if podTerminationMeasurements(nil) != nil {
		t.Fatal("missing observation became zero terminations")
	}
	empty := terminationMeasurementValues(podTerminationMeasurements([]kubernetes.PodState{}))
	if empty["pod_termination_snapshot_pods"] != "0" || empty["pod_termination_current_count"] != "0" || empty["pod_termination_previous_count"] != "0" {
		t.Fatalf("observed empty snapshot omitted: %+v", empty)
	}
	measurements := podTerminationMeasurements([]kubernetes.PodState{{ApplicationStatusObserved: true, CurrentTermination: &kubernetes.ContainerTermination{Reason: "private-custom-reason", ExitCode: terminationValue(256), Signal: terminationValue(65)}, PreviousTermination: &kubernetes.ContainerTermination{}}})
	values := terminationMeasurementValues(measurements)
	for _, slot := range []string{"current", "previous"} {
		for _, field := range []string{"reason", "exit_code", "signal"} {
			if values["pod_termination_"+slot+"_"+field+"_unknown"] != "1" {
				t.Fatalf("unknown %s/%s omitted: %+v", slot, field, measurements)
			}
		}
	}
	body, _ := json.Marshal(measurements)
	if strings.Contains(string(body), "private-custom-reason") || strings.Contains(string(body), "256") || strings.Contains(string(body), "65") {
		t.Fatalf("unbounded measurement labels retained: %s", body)
	}
}
