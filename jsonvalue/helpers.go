package jsonvalue

import (
	"encoding/json"
)

// TextContent encodes text as a JSON string for Source.Content. A binding
// specification decides whether text is an accepted source representation.
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
