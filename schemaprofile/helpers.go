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

// isOutsideProfileKey reports whether k is a keyword outside the profile:
// neither in scope, nor an annotation, nor an x- extension (profile
// normalization step 2). Such keys are retained verbatim by normalization
// and are the outside-profile mark of the position that carries them.
func isOutsideProfileKey(k string) bool {
	if _, ok := inScopeKeywords[k]; ok {
		return false
	}
	if _, ok := annotationKeywords[k]; ok {
		return false
	}
	return !strings.HasPrefix(k, "x-")
}

// outsideProfileKeys returns the outside-profile keys a schema carries at
// its own level, sorted so any diagnostic that names one is deterministic.
func outsideProfileKeys(schema map[string]any) []string {
	var keys []string
	for k := range schema {
		if isOutsideProfileKey(k) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// markedOutsideProfile reports whether a normalized schema is marked
// outside the profile AT this position: it carries an outside-profile
// keyword itself, or a retained allOf (normalization retains an allOf only
// when its siblings or a branch carry an outside-profile keyword; see
// flattenAllOf). The mark is intrinsic to the normalized form — nothing is
// added to the map — so it can never collide with a real keyword or change
// structural identity.
func markedOutsideProfile(schema map[string]any) bool {
	if _, ok := schema["allOf"]; ok {
		return true
	}
	for k := range schema {
		if isOutsideProfileKey(k) {
			return true
		}
	}
	return false
}

// containsOutsideProfile reports whether a normalized schema carries an
// outside-profile keyword anywhere in its subtree (its own level, retained
// allOf branches, properties, additionalProperties, items, or union
// variants). This is the retention test for allOf merging and the
// assertNormalized justification for a retained allOf.
func containsOutsideProfile(schema map[string]any) bool {
	_, _, found := firstOutsideProfile(schema, "")
	return found
}

// firstOutsideProfile locates the first outside-profile keyword in a
// normalized schema's subtree, in a fixed order: the position's own keys
// (sorted), then properties (sorted), additionalProperties, items, and
// oneOf/anyOf variants in authored order, then retained allOf branches in
// authored order (the normalizer visits an allOf's siblings before its
// branches). The returned path uses the normalizer's spelling
// (properties["name"], allOf[0], items, ...), so a comparison-time refusal
// names the position exactly as the former normalization-time refusal did.
func firstOutsideProfile(schema map[string]any, path string) (string, string, bool) {
	if keys := outsideProfileKeys(schema); len(keys) > 0 {
		return pathOrRoot(path), keys[0], true
	}
	if props, ok := asMap(schema["properties"]); ok {
		for _, name := range sortedMapKeys(props) {
			if pm, ok := asMap(props[name]); ok {
				if p, k, found := firstOutsideProfile(pm, ptrJoin(path, fmt.Sprintf("properties[%q]", name))); found {
					return p, k, true
				}
			}
		}
	}
	if ap, ok := asMap(schema["additionalProperties"]); ok {
		if p, k, found := firstOutsideProfile(ap, ptrJoin(path, "additionalProperties")); found {
			return p, k, true
		}
	}
	if items, ok := asMap(schema["items"]); ok {
		if p, k, found := firstOutsideProfile(items, ptrJoin(path, "items")); found {
			return p, k, true
		}
	}
	for _, key := range []string{"oneOf", "anyOf"} {
		variants, ok := asSlice(schema[key])
		if !ok {
			continue
		}
		for i, variant := range variants {
			if vm, ok := asMap(variant); ok {
				if p, k, found := firstOutsideProfile(vm, ptrJoin(path, fmt.Sprintf("%s[%d]", key, i))); found {
					return p, k, true
				}
			}
		}
	}
	if branches, ok := asSlice(schema["allOf"]); ok {
		for i, branch := range branches {
			if bm, ok := asMap(branch); ok {
				if p, k, found := firstOutsideProfile(bm, ptrJoin(path, fmt.Sprintf("allOf[%d]", i))); found {
					return p, k, true
				}
			}
		}
	}
	return "", "", false
}

// applyNullable converts OpenAPI 3.0 "nullable: true" to a JSON Schema type
// union. { "type": "string", "nullable": true } becomes { "type": ["null", "string"] }.
// If type is already an array containing "null", this is a no-op.
// If nullable is absent or false, returns the schema unchanged.
func applyNullable(schema map[string]any) map[string]any {
	nv, ok := schema["nullable"]
	if !ok {
		return schema
	}
	nb, _ := nv.(bool)
	if !nb {
		return schema
	}
	t, hasType := schema["type"]
	if !hasType {
		return schema
	}

	out := cloneMap(schema)
	delete(out, "nullable")

	switch tv := t.(type) {
	case string:
		if tv == "null" {
			out["type"] = []any{"null"}
		} else {
			out["type"] = []any{"null", tv}
		}
	case []any:
		hasNull := false
		for _, v := range tv {
			if s, ok := v.(string); ok && s == "null" {
				hasNull = true
				break
			}
		}
		if !hasNull {
			out["type"] = append([]any{"null"}, tv...)
		}
	}
	return out
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
