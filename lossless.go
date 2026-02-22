package openbindings

import (
	"encoding/json"
	"strings"
)

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
	if len(keys) == 0 {
		return map[string]struct{}{}
	}
	out := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		out[k] = struct{}{}
	}
	return out
}

// marshalLossless merges unknown + extensions with the typed view such that known fields win.
func marshalLossless(unknown, extensions map[string]json.RawMessage, typed any) ([]byte, error) {
	// Start from the lossless fields, then overwrite with the typed view so known fields win.
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
