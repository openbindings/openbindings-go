package schemaprofile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

func assertProfileKeywords(schema map[string]any, path string) error {
	for k := range schema {
		if _, ok := inScopeKeywords[k]; ok {
			continue
		}
		if _, ok := annotationKeywords[k]; ok {
			continue
		}
		return &OutsideProfileError{Path: pathOrRoot(path), Keyword: k}
	}
	return nil
}

func pathOrRoot(path string) string {
	if path == "" {
		return "<root>"
	}
	return path
}

func normalizeType(v any) ([]any, error) {
	switch x := v.(type) {
	case string:
		if strings.TrimSpace(x) == "" {
			return nil, errors.New("must not be empty")
		}
		return []any{x}, nil
	case []any:
		// unique, sorted
		set := map[string]struct{}{}
		for _, it := range x {
			s, ok := it.(string)
			if !ok || strings.TrimSpace(s) == "" {
				return nil, errors.New("must be array of non-empty strings")
			}
			set[s] = struct{}{}
		}
		out := make([]any, 0, len(set))
		for s := range set {
			out = append(out, s)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].(string) < out[j].(string) })
		return out, nil
	default:
		return nil, errors.New("must be string or array of strings")
	}
}

func normalizeStringSet(v any) ([]any, error) {
	arr, ok := asSlice(v)
	if !ok {
		return nil, errors.New("must be array")
	}
	set := map[string]struct{}{}
	for _, it := range arr {
		s, ok := it.(string)
		if !ok || strings.TrimSpace(s) == "" {
			return nil, errors.New("must contain only non-empty strings")
		}
		set[s] = struct{}{}
	}
	out := make([]any, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].(string) < out[j].(string) })
	return out, nil
}

func resolveJSONPointer(doc any, fragment string) (any, error) {
	// fragment is the part after '#'. JSON Pointer starts with '/'.
	if fragment == "" {
		return doc, nil
	}
	if !strings.HasPrefix(fragment, "/") {
		return nil, errors.New("unsupported fragment (must be JSON Pointer)")
	}
	toks := strings.Split(fragment, "/")[1:]
	cur := doc
	for _, tok := range toks {
		tok = strings.ReplaceAll(tok, "~1", "/")
		tok = strings.ReplaceAll(tok, "~0", "~")
		switch x := cur.(type) {
		case map[string]any:
			nxt, ok := x[tok]
			if !ok {
				return nil, fmt.Errorf("pointer not found: %q", tok)
			}
			cur = nxt
		case []any:
			if tok == "-" {
				return nil, errors.New("pointer '-' is not valid for array lookup")
			}
			idx, err := strconv.Atoi(tok)
			if err != nil || idx < 0 || idx >= len(x) {
				return nil, fmt.Errorf("array index out of range: %q", tok)
			}
			cur = x[idx]
		default:
			return nil, errors.New("pointer traversed non-container")
		}
	}
	return cur, nil
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func asMap(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}

func asSlice(v any) ([]any, bool) {
	s, ok := v.([]any)
	return s, ok
}

func ptrJoin(prefix, next string) string {
	if prefix == "" {
		return next
	}
	if next == "" {
		return prefix
	}
	if strings.HasPrefix(next, "[") || strings.HasPrefix(next, ".") {
		return prefix + next
	}
	return prefix + "." + next
}

// decodeJSON is used for fetched documents (UseNumber to preserve numeric intent).
func decodeJSON(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	var extra any
	if err := dec.Decode(&extra); err == nil {
		return nil, errors.New("invalid JSON: trailing data")
	}
	return v, nil
}

// toFloat64 converts a JSON numeric value to float64. It handles the types
// produced by encoding/json (float64, json.Number) and Go integer types.
// Returns 0 for unrecognised types; callers rely on this for absent/nil values
// which are guarded by hasKey checks before reaching this function.
func toFloat64(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case json.Number:
		f, _ := x.Float64()
		return f
	default:
		return 0
	}
}
