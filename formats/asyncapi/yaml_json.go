package asyncapi

import (
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// decodeYAMLObject narrows YAML into AsyncAPI's JSON-compatible object model.
func decodeYAMLObject(data []byte) (map[string]any, error) {
	var decoded any
	if err := yaml.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}
	normalized, err := normalizeYAMLJSONValue(decoded)
	if err != nil {
		return nil, err
	}
	root, ok := normalized.(map[string]any)
	if !ok || root == nil {
		return nil, fmt.Errorf("document root is not an object")
	}
	return root, nil
}

func normalizeYAMLJSONValue(value any) (any, error) {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			normalized, err := normalizeYAMLJSONValue(child)
			if err != nil {
				return nil, err
			}
			out[key] = normalized
		}
		return out, nil
	case map[any]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			text, ok := yamlJSONKey(key)
			if !ok {
				return nil, fmt.Errorf("mapping key %T is not representable as a JSON object property", key)
			}
			if _, duplicate := out[text]; duplicate {
				return nil, fmt.Errorf("mapping keys collide at JSON property %q", text)
			}
			normalized, err := normalizeYAMLJSONValue(child)
			if err != nil {
				return nil, err
			}
			out[text] = normalized
		}
		return out, nil
	case []any:
		out := make([]any, len(typed))
		for index, child := range typed {
			normalized, err := normalizeYAMLJSONValue(child)
			if err != nil {
				return nil, err
			}
			out[index] = normalized
		}
		return out, nil
	case time.Time:
		return typed.Format(time.RFC3339Nano), nil
	default:
		return value, nil
	}
}

func yamlJSONKey(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, true
	case bool, int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64, float32, float64:
		return fmt.Sprint(typed), true
	default:
		return "", false
	}
}
