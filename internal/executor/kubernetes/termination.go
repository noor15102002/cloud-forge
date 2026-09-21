package kubernetes

import (
	"encoding/json"
	"math"
)

// ContainerTermination is one sanitized Kubernetes status slot, not a count of
// lifecycle events or an inference about the application's root cause. Nil
// numeric fields mean absent or outside the supported diagnostic bounds.
type ContainerTermination struct {
	Reason   string
	ExitCode *int32
	Signal   *int32
}

type rawTermination struct {
	Reason   string `json:"reason"`
	ExitCode *int64 `json:"exitCode"`
	Signal   *int64 `json:"signal"`
}

type terminationSnapshot struct {
	observed bool
	current  *ContainerTermination
	previous *ContainerTermination
}

// The official API type uses zero-valued integers for absent fields. Decode
// presence from the same already-bounded response so missing data cannot become
// a reported successful exit or a reported absence of a signal. Neither raw
// messages nor custom reason strings are retained.
func podTerminationSnapshots(data []byte) ([]terminationSnapshot, error) {
	var list struct {
		Items []struct {
			Status struct {
				ContainerStatuses []struct {
					Name  string `json:"name"`
					State struct {
						Terminated *rawTermination `json:"terminated"`
					} `json:"state"`
					LastTerminationState struct {
						Terminated *rawTermination `json:"terminated"`
					} `json:"lastState"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	result := make([]terminationSnapshot, len(list.Items))
	for index, pod := range list.Items {
		matches := 0
		for _, status := range pod.Status.ContainerStatuses {
			if status.Name != "application" {
				continue
			}
			matches++
			result[index] = terminationSnapshot{observed: true,
				current: safeTermination(status.State.Terminated), previous: safeTermination(status.LastTerminationState.Terminated)}
		}
		if matches != 1 {
			// Duplicate or absent application status cannot identify either slot.
			result[index] = terminationSnapshot{}
		}
	}
	return result, nil
}

func safeTermination(raw *rawTermination) *ContainerTermination {
	if raw == nil {
		return nil
	}
	reason := "unknown"
	switch raw.Reason {
	case "OOMKilled":
		reason = "oom_killed"
	case "Completed":
		reason = "completed"
	case "Error":
		reason = "error"
	case "ContainerCannotRun":
		reason = "cannot_run"
	case "StartError":
		reason = "start_error"
	case "DeadlineExceeded":
		reason = "deadline_exceeded"
	}
	return &ContainerTermination{Reason: reason, ExitCode: boundedTerminationNumber(raw.ExitCode, 255), Signal: boundedTerminationNumber(raw.Signal, 64)}
}

func boundedTerminationNumber(value *int64, maximum int64) *int32 {
	if value == nil || *value < 0 || *value > maximum || *value > math.MaxInt32 {
		return nil
	}
	bounded := int32(*value)
	return &bounded
}
