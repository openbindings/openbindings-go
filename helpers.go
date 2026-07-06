package openbindings

import (
	"encoding/json"
	"strings"
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

// ToStringAnyMap type-asserts v to map[string]any. Returns (nil, false) if v
// is nil or not that type.
func ToStringAnyMap(v any) (map[string]any, bool) {
	if v == nil {
		return nil, false
	}
	m, ok := v.(map[string]any)
	return m, ok
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
