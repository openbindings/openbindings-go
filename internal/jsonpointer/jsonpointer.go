// Package jsonpointer formats, parses, and resolves RFC 6901 JSON Pointers
// over generic JSON values.
package jsonpointer

import (
	"strconv"
	"strings"
)

// Format builds an RFC 6901 JSON Pointer from unescaped reference
// tokens. No tokens is the empty pointer, which addresses the whole value.
func Format(tokens ...string) string {
	var b strings.Builder
	for _, token := range tokens {
		b.WriteByte('/')
		b.WriteString(pointerTokenEscaper.Replace(token))
	}
	return b.String()
}

var (
	pointerTokenEscaper   = strings.NewReplacer("~", "~0", "/", "~1")
	pointerTokenUnescaper = strings.NewReplacer("~1", "/", "~0", "~")
)

// Parse splits an RFC 6901 JSON Pointer into its unescaped
// reference tokens. It reports false for a string that is not a JSON Pointer:
// one that is neither empty nor starts with "/", or that has a "~" not
// followed by "0" or "1".
func Parse(pointer string) ([]string, bool) {
	if pointer == "" {
		return nil, true
	}
	if pointer[0] != '/' {
		return nil, false
	}
	for i := 0; i < len(pointer); i++ {
		if pointer[i] == '~' && (i+1 == len(pointer) || (pointer[i+1] != '0' && pointer[i+1] != '1')) {
			return nil, false
		}
	}
	tokens := strings.Split(pointer[1:], "/")
	for i, token := range tokens {
		tokens[i] = pointerTokenUnescaper.Replace(token)
	}
	return tokens, true
}

// Resolve evaluates an RFC 6901 JSON Pointer against a generic JSON
// value, returning the addressed value. It reports false when the pointer is
// malformed or addresses no location.
func Resolve(value any, pointer string) (any, bool) {
	tokens, ok := Parse(pointer)
	if !ok {
		return nil, false
	}
	current := value
	for _, token := range tokens {
		switch node := current.(type) {
		case map[string]any:
			child, ok := node[token]
			if !ok {
				return nil, false
			}
			current = child
		case []any:
			// An array index is "0" or a digit string without a leading zero.
			if token == "" || (len(token) > 1 && token[0] == '0') || strings.TrimLeft(token, "0123456789") != "" {
				return nil, false
			}
			index, err := strconv.Atoi(token)
			if err != nil || index >= len(node) {
				return nil, false
			}
			current = node[index]
		default:
			return nil, false
		}
	}
	return current, true
}
