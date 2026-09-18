// Package regression loads explicit verification baselines and compares them
// with current runtime evidence without depending on an artifact provider.
package regression

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/noor15102002/cloud-forge/pkg/model"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

const maxBaselineBytes = 4 << 20

//go:embed verification.v1alpha1.schema.json
var verificationSchema []byte

type direction int

type measurementPolicy struct {
	direction         direction
	relativeTolerance float64
}

const (
	lowerIsBetter direction = iota
	higherIsBetter
)

var measurementPolicies = map[string]measurementPolicy{
	"container_restarts":          {direction: lowerIsBetter},
	"downtime_ms":                 {direction: lowerIsBetter},
	"dropped_requests":            {direction: lowerIsBetter},
	"error_rate":                  {direction: lowerIsBetter},
	"failed_requests":             {direction: lowerIsBetter},
	"latency_p50_ms":              {direction: lowerIsBetter, relativeTolerance: 0.10},
	"latency_p95_ms":              {direction: lowerIsBetter, relativeTolerance: 0.10},
	"latency_p99_ms":              {direction: lowerIsBetter, relativeTolerance: 0.10},
	"readiness_duration_ms":       {direction: lowerIsBetter, relativeTolerance: 0.10},
	"replacement_duration_ms":     {direction: lowerIsBetter, relativeTolerance: 0.10},
	"rollout_duration_ms":         {direction: lowerIsBetter, relativeTolerance: 0.10},
	"scale_up_duration_ms":        {direction: lowerIsBetter, relativeTolerance: 0.10},
	"startup_duration_ms":         {direction: lowerIsBetter, relativeTolerance: 0.10},
	"termination_duration_ms":     {direction: lowerIsBetter, relativeTolerance: 0.10},
	"version_b_build_duration_ms": {direction: lowerIsBetter, relativeTolerance: 0.10},
	"vulnerabilities":             {direction: lowerIsBetter},
	"request_count":               {direction: higherIsBetter, relativeTolerance: 0.10},
	"throughput_rps":              {direction: higherIsBetter, relativeTolerance: 0.10},
}

// Load reads one bounded, strict, version-compatible verification report.
func Load(path string) (model.VerificationRun, error) {
	file, err := os.Open(path) // #nosec G304 -- the caller explicitly supplies the baseline path.
	if err != nil {
		return model.VerificationRun{}, fmt.Errorf("open baseline %q: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return model.VerificationRun{}, fmt.Errorf("inspect baseline %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return model.VerificationRun{}, fmt.Errorf("baseline %q is not a regular file", path)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBaselineBytes+1))
	if err != nil {
		return model.VerificationRun{}, fmt.Errorf("read baseline %q: %w", path, err)
	}
	if len(data) > maxBaselineBytes {
		return model.VerificationRun{}, fmt.Errorf("baseline %q exceeds the %d-byte limit", path, maxBaselineBytes)
	}
	var identity struct {
		SchemaVersion string `json:"schema_version"`
	}
	if err := json.Unmarshal(data, &identity); err != nil {
		return model.VerificationRun{}, fmt.Errorf("decode baseline %q: %w", path, err)
	}
	if identity.SchemaVersion != model.SchemaVersion {
		return model.VerificationRun{}, fmt.Errorf("baseline schema version %q is unsupported; expected %q", identity.SchemaVersion, model.SchemaVersion)
	}
	if err := validateSchema(data); err != nil {
		return model.VerificationRun{}, fmt.Errorf("baseline %q does not satisfy the %s schema: %w", path, model.SchemaVersion, err)
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var baseline model.VerificationRun
	if err := decoder.Decode(&baseline); err != nil {
		return model.VerificationRun{}, fmt.Errorf("decode baseline %q: %w", path, err)
	}
	if err := requireJSONEnd(decoder); err != nil {
		return model.VerificationRun{}, fmt.Errorf("decode baseline %q: %w", path, err)
	}
	if baseline.RunID == "" || baseline.Evidence == nil {
		return model.VerificationRun{}, errors.New("baseline is missing required run_id or evidence fields")
	}
	if !validStatus(baseline.Status) {
		return model.VerificationRun{}, fmt.Errorf("baseline has invalid status %q", baseline.Status)
	}
	if err := validateEvidence(baseline.Evidence); err != nil {
		return model.VerificationRun{}, fmt.Errorf("baseline is invalid: %w", err)
	}
	return baseline, nil
}

func validateSchema(data []byte) error {
	var schemaDocument any
	if err := json.Unmarshal(verificationSchema, &schemaDocument); err != nil {
		return fmt.Errorf("load embedded schema: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err := compiler.AddResource("verification.v1alpha1.schema.json", schemaDocument); err != nil {
		return fmt.Errorf("load embedded schema: %w", err)
	}
	compiled, err := compiler.Compile("verification.v1alpha1.schema.json")
	if err != nil {
		return fmt.Errorf("compile embedded schema: %w", err)
	}
	var document any
	if err := json.Unmarshal(data, &document); err != nil {
		return err
	}
	return compiled.Validate(document)
}

func requireJSONEnd(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

func validateEvidence(evidence []model.Evidence) error {
	seen := make(map[string]struct{}, len(evidence))
	for _, item := range evidence {
		if item.ExperimentID == "" {
			return errors.New("evidence contains an empty experiment_id")
		}
		if !validStatus(item.Status) {
			return fmt.Errorf("experiment %q has invalid status %q", item.ExperimentID, item.Status)
		}
		if _, exists := seen[item.ExperimentID]; exists {
			return fmt.Errorf("evidence contains duplicate experiment_id %q", item.ExperimentID)
		}
		seen[item.ExperimentID] = struct{}{}
		measurements := make(map[string]struct{}, len(item.Measurements))
		for _, measurement := range item.Measurements {
			if measurement.Name == "" {
				return fmt.Errorf("experiment %q contains an empty measurement name", item.ExperimentID)
			}
			if _, exists := measurements[measurement.Name]; exists {
				return fmt.Errorf("experiment %q contains duplicate measurement %q", item.ExperimentID, measurement.Name)
			}
			measurements[measurement.Name] = struct{}{}
		}
	}
	return nil
}

// Compare returns deterministic relative changes. It never alters the current
// run status or findings, which remain absolute observations of that run.
func Compare(current, baseline model.VerificationRun) model.BaselineComparison {
	comparison := model.BaselineComparison{BaselineRunID: baseline.RunID, Status: model.StatusPass}
	if current.Application != "" && baseline.Application != "" && current.Application != baseline.Application {
		comparison.Status = model.StatusWarn
		comparison.Unavailable = append(comparison.Unavailable, unavailable("application", model.ComparisonStatus, "", fmt.Sprintf("baseline application %q does not match current application %q", baseline.Application, current.Application)))
		return comparison
	}
	currentByID := evidenceByID(current.Evidence)
	baselineByID := evidenceByID(baseline.Evidence)
	ids := unionKeys(currentByID, baselineByID)
	for _, id := range ids {
		currentEvidence, hasCurrent := currentByID[id]
		baselineEvidence, hasBaseline := baselineByID[id]
		switch {
		case !hasCurrent:
			comparison.Unavailable = append(comparison.Unavailable, unavailable(id, model.ComparisonStatus, "", "current run does not contain this baseline experiment"))
		case !hasBaseline:
			comparison.Unavailable = append(comparison.Unavailable, unavailable(id, model.ComparisonStatus, "", "baseline does not contain this current experiment"))
		default:
			compareStatus(&comparison, currentEvidence, baselineEvidence)
			compareMeasurements(&comparison, currentEvidence, baselineEvidence)
		}
	}
	if len(comparison.Regressions) > 0 {
		comparison.Status = model.StatusFail
	} else if len(comparison.Unavailable) > 0 {
		comparison.Status = model.StatusWarn
	}
	return comparison
}

func compareStatus(comparison *model.BaselineComparison, current, baseline model.Evidence) {
	if current.Status == baseline.Status {
		if current.Status == model.StatusSkipped {
			comparison.Unavailable = append(comparison.Unavailable, unavailable(current.ExperimentID, model.ComparisonStatus, "", "both baseline and current experiments were skipped"))
		}
		return
	}
	currentRank, currentComparable := statusRank(current.Status)
	baselineRank, baselineComparable := statusRank(baseline.Status)
	if !currentComparable || !baselineComparable {
		reason := fmt.Sprintf("status comparison is unavailable for baseline %s and current %s", baseline.Status, current.Status)
		comparison.Unavailable = append(comparison.Unavailable, unavailable(current.ExperimentID, model.ComparisonStatus, "", reason))
		return
	}
	change := model.ComparisonChange{
		ExperimentID: current.ExperimentID,
		Kind:         model.ComparisonStatus,
		Baseline:     string(baseline.Status),
		Current:      string(current.Status),
		Summary:      fmt.Sprintf("status changed from %s to %s", baseline.Status, current.Status),
	}
	if currentRank > baselineRank {
		comparison.Regressions = append(comparison.Regressions, change)
	} else {
		comparison.Improvements = append(comparison.Improvements, change)
	}
}

func compareMeasurements(comparison *model.BaselineComparison, current, baseline model.Evidence) {
	currentByName := measurementByName(current.Measurements)
	baselineByName := measurementByName(baseline.Measurements)
	for _, name := range unionKeys(currentByName, baselineByName) {
		policy, comparable := measurementPolicies[name]
		if !comparable {
			continue
		}
		currentMeasurement, hasCurrent := currentByName[name]
		baselineMeasurement, hasBaseline := baselineByName[name]
		switch {
		case !hasCurrent:
			comparison.Unavailable = append(comparison.Unavailable, unavailable(current.ExperimentID, model.ComparisonMeasurement, name, "current run does not contain this baseline measurement"))
			continue
		case !hasBaseline:
			comparison.Unavailable = append(comparison.Unavailable, unavailable(current.ExperimentID, model.ComparisonMeasurement, name, "baseline does not contain this current measurement"))
			continue
		case currentMeasurement.Unit != baselineMeasurement.Unit:
			comparison.Unavailable = append(comparison.Unavailable, unavailable(current.ExperimentID, model.ComparisonMeasurement, name, fmt.Sprintf("unit changed from %q to %q", baselineMeasurement.Unit, currentMeasurement.Unit)))
			continue
		}
		currentValue, currentErr := strconv.ParseFloat(currentMeasurement.Value, 64)
		baselineValue, baselineErr := strconv.ParseFloat(baselineMeasurement.Value, 64)
		if currentErr != nil || baselineErr != nil || math.IsNaN(currentValue) || math.IsNaN(baselineValue) || math.IsInf(currentValue, 0) || math.IsInf(baselineValue, 0) {
			comparison.Unavailable = append(comparison.Unavailable, unavailable(current.ExperimentID, model.ComparisonMeasurement, name, "baseline or current value is not numeric"))
			continue
		}
		if currentValue == baselineValue {
			continue
		}
		if withinTolerance(currentValue, baselineValue, policy.relativeTolerance) {
			continue
		}
		change := model.ComparisonChange{
			ExperimentID: current.ExperimentID,
			Kind:         model.ComparisonMeasurement,
			Measurement:  name,
			Baseline:     baselineMeasurement.Value,
			Current:      currentMeasurement.Value,
			Unit:         currentMeasurement.Unit,
			Summary:      fmt.Sprintf("%s changed from %s to %s", name, formatMeasurement(baselineMeasurement.Value, baselineMeasurement.Unit), formatMeasurement(currentMeasurement.Value, currentMeasurement.Unit)),
		}
		regressed := policy.direction == lowerIsBetter && currentValue > baselineValue || policy.direction == higherIsBetter && currentValue < baselineValue
		if regressed {
			comparison.Regressions = append(comparison.Regressions, change)
		} else {
			comparison.Improvements = append(comparison.Improvements, change)
		}
	}
}

func statusRank(status model.Status) (int, bool) {
	switch status {
	case model.StatusPass:
		return 0, true
	case model.StatusWarn:
		return 1, true
	case model.StatusFail:
		return 2, true
	case model.StatusError:
		return 3, true
	default:
		return 0, false
	}
}

func validStatus(status model.Status) bool {
	if status == model.StatusSkipped {
		return true
	}
	_, valid := statusRank(status)
	return valid
}

func withinTolerance(current, baseline, relativeTolerance float64) bool {
	if relativeTolerance == 0 || baseline == 0 {
		return false
	}
	return math.Abs(current-baseline)/math.Abs(baseline) <= relativeTolerance
}

func unavailable(experimentID string, kind model.ComparisonKind, measurement, reason string) model.ComparisonUnavailable {
	return model.ComparisonUnavailable{ExperimentID: experimentID, Kind: kind, Measurement: measurement, Reason: reason}
}

func evidenceByID(evidence []model.Evidence) map[string]model.Evidence {
	result := make(map[string]model.Evidence, len(evidence))
	for _, item := range evidence {
		result[item.ExperimentID] = item
	}
	return result
}

func measurementByName(measurements []model.Measurement) map[string]model.Measurement {
	result := make(map[string]model.Measurement, len(measurements))
	for _, item := range measurements {
		result[item.Name] = item
	}
	return result
}

func unionKeys[T any](left, right map[string]T) []string {
	seen := make(map[string]struct{}, len(left)+len(right))
	for key := range left {
		seen[key] = struct{}{}
	}
	for key := range right {
		seen[key] = struct{}{}
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// ComparableMeasurements lists the normalized numeric measurements with an
// explicit quality direction. Unknown and configuration metrics are retained
// in reports but are not assigned a regression meaning.
func ComparableMeasurements() []string {
	result := make([]string, 0, len(measurementPolicies))
	for name := range measurementPolicies {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func formatMeasurement(value, unit string) string {
	return strings.TrimSpace(value + " " + unit)
}
