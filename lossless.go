package openbindings

import (
	"bytes"
	"fmt"
	json "github.com/openbindings/openbindings-go/internal/thirdparty/jsoncodec"
	"strings"
)

// objectShape describes one OBI-defined object for decoding: the members this
// model types, which of them are required, where JSON null is itself a value,
// and which members are arrays or maps of strings.
type objectShape struct {
	name        string
	known       map[string]struct{}
	required    []string
	nullable    map[string]bool
	stringLists []string
}

// decodeObject is the first step of decoding every OBI-defined object: its
// members, after refusing what the typed model cannot carry exactly.
//
// The typed model decodes a document only if re-encoding it reproduces every
// member. A JSON null can be carried only where null is itself a value, such
// as an example value or source content; elsewhere a Go zero value or nil
// would re-encode as a different value or as absence. A required string
// member must be present, because its zero value re-encodes as a present
// empty string. Such documents fail decoding here, exactly as a member of
// the wrong JSON type does; ValidateDocument still judges their bytes.
func decodeObject(b []byte, shape objectShape) (map[string]json.RawMessage, error) {
	if isJSONNull(b) {
		return nil, fmt.Errorf("%s: null is not an object", shape.name)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	for _, member := range shape.required {
		if _, ok := raw[member]; !ok {
			return nil, fmt.Errorf("%s: missing required member %q", shape.name, member)
		}
	}
	for member := range shape.known {
		value, ok := raw[member]
		if ok && isJSONNull(value) && !shape.nullable[member] {
			return nil, fmt.Errorf("%s: member %q is null, which this model cannot carry", shape.name, member)
		}
	}
	for _, member := range shape.stringLists {
		if value, ok := raw[member]; ok {
			if err := rejectNullElements(value); err != nil {
				return nil, fmt.Errorf("%s: member %q: %w", shape.name, member, err)
			}
		}
	}
	return raw, nil
}

func isJSONNull(b []byte) bool {
	return bytes.Equal(bytes.TrimSpace(b), []byte("null"))
}

// rejectNullElements refuses a null element in an array or object of
// strings, which a Go string would re-encode as "". A value of another JSON
// type is left to the typed decode to reject.
func rejectNullElements(value json.RawMessage) error {
	switch trimmed := bytes.TrimSpace(value); {
	case len(trimmed) > 0 && trimmed[0] == '[':
		var elements []json.RawMessage
		if err := json.Unmarshal(trimmed, &elements); err != nil {
			return err
		}
		for i, element := range elements {
			if isJSONNull(element) {
				return fmt.Errorf("element %d is null, which this model cannot carry", i)
			}
		}
	case len(trimmed) > 0 && trimmed[0] == '{':
		var entries map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &entries); err != nil {
			return err
		}
		for key, entry := range entries {
			if isJSONNull(entry) {
				return fmt.Errorf("entry %q is null, which this model cannot carry", key)
			}
		}
	}
	return nil
}

// splitLossless separates unknown fields into:
// - extensions: keys starting with "x-"
// - unknown: all other keys not in known
func splitLossless(raw map[string]json.RawMessage, known map[string]struct{}) (extensions, unknown map[string]json.RawMessage) {
	for k, v := range raw {
		if _, ok := known[k]; ok {
			continue
		}
		if strings.HasPrefix(k, "x-") {
			if extensions == nil {
				extensions = map[string]json.RawMessage{}
			}
			extensions[k] = v
			continue
		}
		if unknown == nil {
			unknown = map[string]json.RawMessage{}
		}
		unknown[k] = v
	}
	return extensions, unknown
}

// knownSet builds a map for constant-time known-field checks in lossless unmarshaling.
func knownSet(keys ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		out[k] = struct{}{}
	}
	return out
}

// marshalLossless merges unknown and extension members with the typed view;
// a typed member wins over a colliding lossless one.
func marshalLossless(unknown, extensions map[string]json.RawMessage, typed any) ([]byte, error) {
	out := map[string]json.RawMessage{}
	for k, v := range unknown {
		out[k] = v
	}
	for k, v := range extensions {
		out[k] = v
	}

	knownBytes, err := json.Marshal(typed)
	if err != nil {
		return nil, err
	}
	var known map[string]json.RawMessage
	if err := json.Unmarshal(knownBytes, &known); err != nil {
		return nil, err
	}
	for k, v := range known {
		out[k] = v
	}
	return json.Marshal(out)
}
