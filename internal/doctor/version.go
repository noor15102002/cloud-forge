package doctor

import "github.com/noor15102002/cloud-forge/internal/runtimepolicy"

// ParsedVersion extracts only a semantic version, never arbitrary tool output.
func ParsedVersion(value string) string { return runtimepolicy.ParsedVersion(value) }
