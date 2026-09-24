package trivy

import (
	"encoding/json"
	"fmt"
	"strings"
)

// encoding/json otherwise accepts case-insensitive aliases for struct fields.
// Trivy's supported schema uses exact field names. Reject aliases at every
// decoded object so an unknown `results` key cannot overwrite observed Results.
// Unknown fields remain forward-compatible; arbitrary metadata maps are untouched.
func exactKeys(data []byte, fields ...string) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, fmt.Errorf("trivy object has an unsupported shape")
	}
	for key := range object {
		for _, field := range fields {
			if key != field && strings.EqualFold(key, field) {
				return nil, fmt.Errorf("trivy observation contains a noncanonical field alias")
			}
		}
	}
	return object, nil
}

func exactReportShape(data []byte) error {
	root, err := exactKeys(data, "SchemaVersion", "ArtifactName", "ArtifactType", "Metadata", "Results")
	if err != nil {
		return err
	}
	if metadata, ok := root["Metadata"]; ok {
		if _, err := exactKeys(metadata, "ImageID"); err != nil {
			return err
		}
	}
	var results []json.RawMessage
	if raw, ok := root["Results"]; ok {
		if err := json.Unmarshal(raw, &results); err != nil {
			return fmt.Errorf("trivy Results has an unsupported shape")
		}
	}
	for _, raw := range results {
		result, err := exactKeys(raw, "Target", "Class", "Type", "Vulnerabilities")
		if err != nil {
			return err
		}
		var vulnerabilities []json.RawMessage
		if raw, ok := result["Vulnerabilities"]; ok {
			if err := json.Unmarshal(raw, &vulnerabilities); err != nil {
				return fmt.Errorf("trivy Vulnerabilities has an unsupported shape")
			}
		}
		for _, raw := range vulnerabilities {
			if _, err := exactKeys(raw, "VulnerabilityID", "PkgName", "PkgID", "PkgPath", "InstalledVersion", "FixedVersion", "Severity"); err != nil {
				return err
			}
		}
	}
	return nil
}
