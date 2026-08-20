package openbindings

import (
	"encoding/json"
	"errors"
	"strings"
)

// IsHTTPURL reports whether s starts with http:// or https://.
func IsHTTPURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// ContentToBytes converts a source content value (raw JSON, presence-aware —
// see Source.Content) to artifact bytes: a JSON string yields its text
// verbatim (an embedded artifact rides as a JSON string), any other present
// value (object, array, number, boolean, null) yields its raw JSON bytes
// unchanged. Absent content (nil) is an error — presence is the caller's
// question, answered before decoding.
func ContentToBytes(content json.RawMessage) ([]byte, error) {
	if content == nil {
		return nil, errors.New("openbindings: source content is absent")
	}
	var s string
	if err := json.Unmarshal(content, &s); err == nil {
		return []byte(s), nil
	}
	return []byte(content), nil
}

// TextContent renders artifact text as an embedded content value (a JSON
// string) — the inverse of ContentToBytes' string lane. Formats and tools
// that embed an artifact's text into Source.Content use this so the carrier
// stays raw JSON end to end.
func TextContent(text string) json.RawMessage {
	b, err := json.Marshal(text)
	if err != nil {
		// Marshaling a Go string cannot fail (invalid UTF-8 is coerced).
		panic("openbindings: marshal string content: " + err.Error())
	}
	return b
}

// ContentKind names the JSON kind of a raw content value for diagnostics:
// "absent" (nil), "null", "string", "number", "boolean", "object", "array",
// or "invalid JSON". Families use it to say what a refused content actually
// was.
func ContentKind(content json.RawMessage) string {
	if content == nil {
		return "absent"
	}
	for _, b := range content {
		switch b {
		case ' ', '\t', '\n', '\r':
			continue
		case '{':
			return "object"
		case '[':
			return "array"
		case '"':
			return "string"
		case 't', 'f':
			return "boolean"
		case 'n':
			return "null"
		default:
			if b == '-' || (b >= '0' && b <= '9') {
				return "number"
			}
			return "invalid JSON"
		}
	}
	return "invalid JSON"
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
