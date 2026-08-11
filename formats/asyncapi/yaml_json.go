package asyncapi

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// decodeYAMLObject narrows YAML into AsyncAPI's JSON-compatible object model.
func decodeYAMLObject(data []byte) (map[string]any, error) {
	var decoded any
	if err := yaml.Unmarshal(data, &decoded); err != nil {
		protected, sentinel, changed := protectBlockScalarLeadingTabs(data)
		if !changed {
			return nil, err
		}
		if retryErr := yaml.Unmarshal(protected, &decoded); retryErr != nil {
			return nil, err
		}
		decoded = restoreProtectedYAMLTabs(decoded, sentinel)
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

// yaml.v3 rejects a tab when it is the first content character of a block
// scalar, even after the scalar's required space indentation. YAML 1.2 permits
// that tab because it is content, not indentation. Retry by protecting only
// that narrow construct; tabs used as indentation remain parser errors.
func protectBlockScalarLeadingTabs(data []byte) ([]byte, string, bool) {
	sentinel := "__OPENBINDINGS_YAML_BLOCK_TAB__"
	for bytes.Contains(data, []byte(sentinel)) {
		sentinel += "_"
	}
	lines := bytes.SplitAfter(data, []byte("\n"))
	activeIndent := -1
	changed := false
	for index, line := range lines {
		body := bytes.TrimSuffix(line, []byte("\n"))
		body = bytes.TrimSuffix(body, []byte("\r"))
		spaces := leadingSpaces(body)
		blank := len(bytes.TrimSpace(body)) == 0

		if activeIndent >= 0 {
			if !blank && spaces <= activeIndent {
				activeIndent = -1
			} else if spaces > activeIndent {
				content := body[spaces:]
				tabs := 0
				for tabs < len(content) && content[tabs] == '\t' {
					tabs++
				}
				if tabs > 0 {
					replacement := bytes.Repeat([]byte(sentinel), tabs)
					protectedBody := make([]byte, 0, len(body)+len(replacement)-tabs)
					protectedBody = append(protectedBody, body[:spaces]...)
					protectedBody = append(protectedBody, replacement...)
					protectedBody = append(protectedBody, content[tabs:]...)
					ending := line[len(body):]
					lines[index] = append(protectedBody, ending...)
					changed = true
				}
				continue
			}
		}

		if isBlockScalarHeader(body) {
			activeIndent = spaces
		}
	}
	return bytes.Join(lines, nil), sentinel, changed
}

func leadingSpaces(line []byte) int {
	count := 0
	for count < len(line) && line[count] == ' ' {
		count++
	}
	return count
}

func isBlockScalarHeader(line []byte) bool {
	trimmed := strings.TrimSpace(string(line))
	if comment := strings.Index(trimmed, " #"); comment >= 0 {
		trimmed = strings.TrimSpace(trimmed[:comment])
	}
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return false
	}
	indicator := fields[len(fields)-1]
	if indicator == "" || (indicator[0] != '|' && indicator[0] != '>') {
		return false
	}
	modifiers := indicator[1:]
	if len(modifiers) > 2 {
		return false
	}
	seenDigit, seenChomp := false, false
	for _, modifier := range modifiers {
		switch {
		case modifier >= '1' && modifier <= '9' && !seenDigit:
			seenDigit = true
		case (modifier == '+' || modifier == '-') && !seenChomp:
			seenChomp = true
		default:
			return false
		}
	}
	if len(fields) == 1 {
		return true
	}
	prefix := strings.TrimSpace(strings.TrimSuffix(trimmed, indicator))
	return strings.HasSuffix(prefix, ":") || strings.HasSuffix(prefix, "-") || strings.HasSuffix(prefix, "?")
}

func restoreProtectedYAMLTabs(value any, sentinel string) any {
	switch typed := value.(type) {
	case string:
		return strings.ReplaceAll(typed, sentinel, "\t")
	case []any:
		for index, child := range typed {
			typed[index] = restoreProtectedYAMLTabs(child, sentinel)
		}
		return typed
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			restoredKey := strings.ReplaceAll(key, sentinel, "\t")
			out[restoredKey] = restoreProtectedYAMLTabs(child, sentinel)
		}
		return out
	case map[any]any:
		out := make(map[any]any, len(typed))
		for key, child := range typed {
			restoredKey := restoreProtectedYAMLTabs(key, sentinel)
			out[restoredKey] = restoreProtectedYAMLTabs(child, sentinel)
		}
		return out
	default:
		return value
	}
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
