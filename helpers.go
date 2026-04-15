package openbindings

import (
	"encoding/json"
	"strings"
	"time"
)

// ContentToBytes converts a content value (string, []byte, or JSON-marshalable) to raw bytes.
func ContentToBytes(content any) ([]byte, error) {
	switch c := content.(type) {
	case string:
		return []byte(c), nil
	case []byte:
		return c, nil
	default:
		return json.Marshal(c)
	}
}

// FailedOutput builds an ExecuteOutput for a pre-request failure.
func FailedOutput(start time.Time, code, message string) *ExecuteOutput {
	return &ExecuteOutput{
		Status:     1,
		DurationMs: time.Since(start).Milliseconds(),
		Error: &ExecuteError{
			Code:    code,
			Message: message,
		},
	}
}

// ToStringAnyMap type-asserts v to map[string]any. Returns (nil, false) if v
// is nil or not that type.
func ToStringAnyMap(v any) (map[string]any, bool) {
	if v == nil {
		return nil, false
	}
	m, ok := v.(map[string]any)
	return m, ok
}

// MaybeJSON returns true if the trimmed string looks like a JSON object or array.
func MaybeJSON(s string) bool {
	s = strings.TrimSpace(s)
	return (strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}")) ||
		(strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]"))
}

// DetectFormatVersion extracts a normalized major.minor version from a full
// version string (e.g. "3.1.0" → "3.1").
func DetectFormatVersion(version string) string {
	parts := strings.Split(version, ".")
	if len(parts) >= 2 {
		return parts[0] + "." + parts[1]
	}
	return version
}
