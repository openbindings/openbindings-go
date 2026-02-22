package schemaprofile

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/openbindings/openbindings-go/canonicaljson"
)

// Fetcher optionally provides bytes for external $ref targets.
// The SDK does not provide an HTTP/filesystem implementation; tools own IO.
type Fetcher interface {
	Fetch(u *url.URL) ([]byte, error)
}

// Normalizer normalizes schemas deterministically per the OpenBindings Schema Compatibility Profile (v0.1).
// It also provides directional compatibility checks (InputCompatible / OutputCompatible).
//
// A Normalizer is not safe for concurrent use. Create a separate instance for each goroutine,
// or protect access with external synchronization.
type Normalizer struct {
	// Root is the containing document against which JSON Pointer fragments (e.g. "#/schemas/Foo")
	// are resolved. In OpenBindings, this is typically the full interface document decoded as JSON.
	Root any

	// Base is an optional base URL used to resolve relative $ref references.
	// If Base is nil and a relative reference is encountered, normalization fails.
	Base *url.URL

	// Fetch is optional. If nil, external $ref resolution is not supported.
	Fetch Fetcher

	// refStack tracks $ref resolution to detect cycles within a single call.
	// It is created fresh on each public method invocation.
	refStack map[string]bool
}

// Normalize returns a normalized copy of schema per the v0.1 profile.
func (n *Normalizer) Normalize(schema map[string]any) (map[string]any, error) {
	if n == nil {
		return nil, errors.New("schemaprofile: nil normalizer")
	}
	n.refStack = map[string]bool{}
	return n.normalizeAt(schema, "")
}

// InputCompatible reports whether candidate can stand in for target as an input schema.
func (n *Normalizer) InputCompatible(target, candidate map[string]any) (bool, error) {
	if n == nil {
		return false, errors.New("schemaprofile: nil normalizer")
	}
	n.refStack = map[string]bool{}
	ti, err := n.normalizeAt(target, "")
	if err != nil {
		return false, err
	}
	tc, err := n.normalizeAt(candidate, "")
	if err != nil {
		return false, err
	}
	return inputCompatible(ti, tc)
}

// OutputCompatible reports whether candidate can stand in for target as an output/payload schema.
func (n *Normalizer) OutputCompatible(target, candidate map[string]any) (bool, error) {
	if n == nil {
		return false, errors.New("schemaprofile: nil normalizer")
	}
	n.refStack = map[string]bool{}
	ti, err := n.normalizeAt(target, "")
	if err != nil {
		return false, err
	}
	tc, err := n.normalizeAt(candidate, "")
	if err != nil {
		return false, err
	}
	return outputCompatible(ti, tc)
}

// CanonicalString returns the RFC 8785 (JCS) canonical JSON string of v.
func CanonicalString(v any) (string, error) {
	b, err := canonicaljson.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// OutsideProfileError indicates the schema uses a keyword outside the v0.1 profile.
type OutsideProfileError struct {
	Path    string
	Keyword string
}

func (e *OutsideProfileError) Error() string {
	if e == nil {
		return "outside profile"
	}
	if e.Path == "" {
		return fmt.Sprintf("outside profile: keyword %q", e.Keyword)
	}
	return fmt.Sprintf("outside profile at %s: keyword %q", e.Path, e.Keyword)
}

// RefError indicates a $ref resolution problem.
type RefError struct {
	Path string
	Ref  string
	Err  error
}

func (e *RefError) Error() string {
	if e == nil {
		return "ref error"
	}
	if e.Path == "" {
		return fmt.Sprintf("$ref %q: %v", e.Ref, e.Err)
	}
	return fmt.Sprintf("%s.$ref %q: %v", e.Path, e.Ref, e.Err)
}

func (e *RefError) Unwrap() error { return e.Err }

// SchemaError indicates an irreconcilable schema conflict (e.g., empty type intersection in allOf).
type SchemaError struct {
	Path    string
	Message string
}

func (e *SchemaError) Error() string {
	if e == nil {
		return "schema error"
	}
	if e.Path == "" {
		return fmt.Sprintf("schema error: %s", e.Message)
	}
	return fmt.Sprintf("schema error at %s: %s", e.Path, e.Message)
}

var (
	// supported keywords (profile v0.1)
	inScopeKeywords = map[string]struct{}{
		"$ref":                 {},
		"$defs":                {},
		"allOf":                {},
		"type":                 {},
		"enum":                 {},
		"const":                {},
		"properties":           {},
		"required":             {},
		"additionalProperties": {},
		"items":                {},
		"oneOf":                {},
		"anyOf":                {},
		"minimum":              {},
		"maximum":              {},
		"exclusiveMinimum":     {},
		"exclusiveMaximum":     {},
		"minLength":            {},
		"maxLength":            {},
		"minItems":             {},
		"maxItems":             {},
	}

	// annotation-only keywords (ignored for compatibility decisions, stripped during normalization)
	annotationKeywords = map[string]struct{}{
		"title":       {},
		"description": {},
		"examples":    {},
		"default":     {},
		"deprecated":  {},
		"readOnly":    {},
		"writeOnly":   {},
		"$schema":     {}, // allowed if JSON Schema 2020-12; stripped for comparison
	}
)

func (n *Normalizer) normalizeAt(schema map[string]any, path string) (map[string]any, error) {
	if schema == nil {
		// treat nil as Top: return empty object
		return map[string]any{}, nil
	}
	if err := assertProfileKeywords(schema, path); err != nil {
		return nil, err
	}

	// Inline $ref for comparison.
	if ref, ok := schema["$ref"].(string); ok && strings.TrimSpace(ref) != "" {
		resolved, cleanup, err := n.resolveRef(ref, path)
		if err != nil {
			return nil, err
		}
		// Keep the ref on the stack during normalization to detect cycles.
		defer cleanup()
		rm, ok := asMap(resolved)
		if !ok {
			return nil, &RefError{Path: path, Ref: ref, Err: errors.New("resolved $ref is not an object")}
		}
		// The profile defines evaluation equivalent to inlining. We normalize the resolved schema.
		return n.normalizeAt(rm, path)
	}

	// Strip annotation-only keywords and $defs from the output.
	out := make(map[string]any, len(schema))
	for k, v := range schema {
		if _, isAnnotation := annotationKeywords[k]; isAnnotation {
			continue
		}
		if k == "$defs" {
			continue // $defs are only needed for $ref resolution; after inlining they're dead weight
		}
		out[k] = v
	}

	// Flatten allOf before anything else.
	if allOf, ok := out["allOf"]; ok {
		merged, err := n.flattenAllOf(allOf, path)
		if err != nil {
			return nil, err
		}
		// Replace out with the merged result and re-normalize.
		return n.normalizeAt(merged, path)
	}

	// Normalize type (absent type is unconstrained per spec — do NOT infer).
	if _, ok := out["type"]; ok {
		types, err := normalizeType(out["type"])
		if err != nil {
			return nil, fmt.Errorf("%s.type: %w", pathOrRoot(path), err)
		}
		out["type"] = types
	}

	// Normalize required.
	if v, ok := out["required"]; ok {
		req, err := normalizeStringSet(v)
		if err != nil {
			return nil, fmt.Errorf("%s.required: %w", pathOrRoot(path), err)
		}
		out["required"] = req
	}

	// Recurse into nested schemas.
	if props, ok := out["properties"]; ok {
		propsMap, ok := asMap(props)
		if !ok {
			return nil, fmt.Errorf("%s.properties: must be object", pathOrRoot(path))
		}
		nm := make(map[string]any, len(propsMap))
		for k, v := range propsMap {
			vm, ok := asMap(v)
			if !ok {
				return nil, fmt.Errorf("%s.properties[%q]: must be object", pathOrRoot(path), k)
			}
			nv, err := n.normalizeAt(vm, ptrJoin(path, fmt.Sprintf("properties[%q]", k)))
			if err != nil {
				return nil, err
			}
			nm[k] = nv
		}
		out["properties"] = nm
	}

	if ap, ok := out["additionalProperties"]; ok {
		switch x := ap.(type) {
		case bool:
			out["additionalProperties"] = x
		case map[string]any:
			nv, err := n.normalizeAt(x, ptrJoin(path, "additionalProperties"))
			if err != nil {
				return nil, err
			}
			out["additionalProperties"] = nv
		default:
			return nil, fmt.Errorf("%s.additionalProperties: must be boolean or object", pathOrRoot(path))
		}
	}

	if items, ok := out["items"]; ok {
		im, ok := asMap(items)
		if !ok {
			return nil, fmt.Errorf("%s.items: must be object", pathOrRoot(path))
		}
		nv, err := n.normalizeAt(im, ptrJoin(path, "items"))
		if err != nil {
			return nil, err
		}
		out["items"] = nv
	}

	for _, k := range []string{"oneOf", "anyOf"} {
		if u, ok := out[k]; ok {
			arr, ok := asSlice(u)
			if !ok {
				return nil, fmt.Errorf("%s.%s: must be array", pathOrRoot(path), k)
			}
			var variants []map[string]any
			for idx, item := range arr {
				m, ok := asMap(item)
				if !ok {
					return nil, fmt.Errorf("%s.%s[%d]: must be object", pathOrRoot(path), k, idx)
				}
				nv, err := n.normalizeAt(m, ptrJoin(path, fmt.Sprintf("%s[%d]", k, idx)))
				if err != nil {
					return nil, err
				}
				variants = append(variants, nv)
			}

			type scored struct {
				canon string
				v     map[string]any
			}
			sc := make([]scored, 0, len(variants))
			for _, v := range variants {
				c, err := CanonicalString(v)
				if err != nil {
					return nil, fmt.Errorf("%s.%s: canonicalize variant: %w", pathOrRoot(path), k, err)
				}
				sc = append(sc, scored{canon: c, v: v})
			}
			sort.Slice(sc, func(i, j int) bool { return sc[i].canon < sc[j].canon })
			outArr := make([]any, 0, len(sc))
			for _, s := range sc {
				outArr = append(outArr, s.v)
			}
			out[k] = outArr
		}
	}

	return out, nil
}

// resolveRef resolves a $ref and returns the resolved value plus a cleanup function.
// The cleanup function MUST be called when the caller is done normalizing the resolved schema,
// to remove the ref from the cycle-detection stack. This ensures that recursive $refs
// within the resolved schema are properly detected as cycles.
func (n *Normalizer) resolveRef(ref string, path string) (any, func(), error) {
	noop := func() {}
	u, err := url.Parse(ref)
	if err != nil {
		return nil, noop, &RefError{Path: pathOrRoot(path), Ref: ref, Err: err}
	}

	// Resolve against base if needed.
	if !u.IsAbs() && u.Path != "" {
		if n.Base == nil {
			return nil, noop, &RefError{Path: pathOrRoot(path), Ref: ref, Err: errors.New("relative $ref with no base")}
		}
		u = n.Base.ResolveReference(u)
	}

	key := u.String()

	// Cycle detection: if this ref is already being resolved on the current stack, it's a cycle.
	if n.refStack[key] {
		return nil, noop, &RefError{Path: pathOrRoot(path), Ref: ref, Err: errors.New("cycle detected")}
	}

	n.refStack[key] = true
	cleanup := func() { delete(n.refStack, key) }

	var doc any
	switch {
	case u.Scheme == "" && u.Path == "" && (u.Fragment != "" || strings.HasPrefix(ref, "#")):
		// Fragment-only ref: resolve against root.
		doc = n.Root
	default:
		// External document (optional).
		if n.Fetch == nil {
			cleanup()
			return nil, noop, &RefError{Path: pathOrRoot(path), Ref: ref, Err: errors.New("external $ref unsupported (no fetcher)")}
		}
		fetched, err := n.Fetch.Fetch(u)
		if err != nil {
			cleanup()
			return nil, noop, &RefError{Path: pathOrRoot(path), Ref: ref, Err: err}
		}
		doc, err = decodeJSON(fetched)
		if err != nil {
			cleanup()
			return nil, noop, &RefError{Path: pathOrRoot(path), Ref: ref, Err: err}
		}
	}

	v, err := resolveJSONPointer(doc, u.Fragment)
	if err != nil {
		cleanup()
		return nil, noop, &RefError{Path: pathOrRoot(path), Ref: ref, Err: err}
	}

	return v, cleanup, nil
}
