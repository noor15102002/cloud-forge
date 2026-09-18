package doctor

import "regexp"

var versionPattern = regexp.MustCompile(`\b[vV]?([0-9]+\.[0-9]+\.[0-9]+(?:[-+][A-Za-z0-9.-]+)?)\b`)

// ParsedVersion extracts only a semantic version, never arbitrary tool output.
func ParsedVersion(value string) string {
	match := versionPattern.FindStringSubmatch(value)
	if len(match) != 2 {
		return ""
	}
	return match[1]
}
