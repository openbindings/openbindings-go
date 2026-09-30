package schemaeval

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// prepared is a bundle made ready for the library, and what that took.
type prepared struct {
	document any
	// sentinels are the formats that stand for what evaluation cannot
	// decide, by name: reaching one on an instance of one of its types
	// gives no verdict.
	sentinels map[string]sentinelFormat
	// bounds are the formats that decide a comparison keyword whose number
	// is beyond the numeric limits, by name.
	bounds map[string]bound
	// compares is whether the library reads the bundle's numbers by more
	// than type and equality: a comparison keyword, or a number in const or
	// enum. Only where it does not is a value's number beyond the numeric
	// limits validated as a stand-in.
	compares bool
}

type sentinelFormat struct {
	// types are the JSON types it stops on; nil for every type.
	types  []string
	reason string
}

// Keywords the library evaluates though JSON Schema 2020-12 does not define
// them, or reads as schemas: dropped, so the library evaluates the bundle as
// strict 2020-12 does. format is an annotation (OBI-T-08), so dropping it
// changes no verdict; the library would otherwise assert "regex" once format
// assertion is on for the sentinels.
var dropped = []string{"format", "dependencies", "definitions", "$recursiveRef", "$recursiveAnchor", "additionalItems"}

var (
	comparisonKeywords = []string{"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf"}
	// countKeywords map each count keyword to whether it is a lower bound,
	// and the type it applies to.
	countKeywords = map[string]struct {
		lower bool
		typ   string
	}{
		"maxLength": {false, "string"}, "minLength": {true, "string"},
		"maxItems": {false, "array"}, "minItems": {true, "array"},
		"maxProperties": {false, "object"}, "minProperties": {true, "object"},
		"maxContains": {false, "array"}, "minContains": {true, "array"},
	}
	schemaMaps   = []string{"$defs", "properties", "patternProperties", "dependentSchemas"}
	schemaSingle = []string{"additionalProperties", "propertyNames", "unevaluatedProperties", "items", "contains", "unevaluatedItems", "not", "if", "then", "else", "contentSchema"}
	schemaArrays = []string{"allOf", "anyOf", "oneOf", "prefixItems"}
	inPlace      = []string{"allOf", "anyOf", "oneOf", "not", "if", "then", "else", "dependentSchemas"}
)

// prepare makes a decoded bundle ready for the library, changing it in
// place.
func prepare(document any) prepared {
	p := prepared{document: document, sentinels: map[string]sentinelFormat{}, bounds: map[string]bound{}}
	// Every schema is found before any changes, so no schema prepare adds
	// is prepared in turn.
	var objects []map[string]any
	forEachSchema(document, func(object map[string]any) { objects = append(objects, object) })
	for _, object := range objects {
		p.prepareSchema(object)
	}
	p.guardPropertyNames()
	return p
}

// forEachSchema calls fn for every schema object a schema holds, itself
// first, at the positions JSON Schema 2020-12 holds schemas.
func forEachSchema(schema any, fn func(object map[string]any)) {
	object, ok := schema.(map[string]any)
	if !ok {
		return
	}
	fn(object)
	for _, keyword := range schemaMaps {
		entries, _ := object[keyword].(map[string]any)
		for _, name := range sortedKeys(entries) {
			forEachSchema(entries[name], fn)
		}
	}
	for _, keyword := range schemaSingle {
		forEachSchema(object[keyword], fn)
	}
	for _, keyword := range schemaArrays {
		entries, _ := object[keyword].([]any)
		for _, entry := range entries {
			forEachSchema(entry, fn)
		}
	}
}

func (p *prepared) prepareSchema(object map[string]any) {
	for _, keyword := range dropped {
		delete(object, keyword)
	}
	for _, keyword := range comparisonKeywords {
		n, isNumber := object[keyword].(json.Number)
		switch {
		case !isNumber:
		case !withinNumericLimits(string(n)):
			delete(object, keyword)
			name := fmt.Sprintf("schemaeval-bound-%d", len(p.bounds))
			p.bounds[name] = bound{keyword, string(n)}
			appendAllOf(object, map[string]any{"format": name})
			p.compares = true
		default:
			p.compares = true
		}
	}
	for _, keyword := range sortedKeys(object) {
		count, isCount := countKeywords[keyword]
		n, isNumber := object[keyword].(json.Number)
		if !isCount || !isNumber {
			continue
		}
		value, past, ok := readCount(string(n))
		switch {
		case !ok:
			// Not a non-negative integer, which core never hands over
			// (OBI-D-10): left for the library.
			continue
		case !past:
			// Spelled as the library reads it, whatever its spelling in
			// the bundle (0e10001, 1.0, 10e-1).
			object[keyword] = json.Number(strconv.Itoa(value))
			continue
		}
		// No string, array, or object has more than math.MaxInt characters,
		// items, or members: an upper bound past it holds, and a lower bound
		// past it fails, for the type it applies to.
		delete(object, keyword)
		switch {
		case !count.lower:
		case keyword == "minContains":
			p.stop(object, []string{"array"}, fmt.Sprintf("minContains %s is beyond this evaluator's integers", n))
		default:
			appendAllOf(object, map[string]any{"if": map[string]any{"type": count.typ}, "then": false})
		}
	}
	for _, keyword := range []string{"const", "enum"} {
		value, present := object[keyword]
		switch {
		case !present:
		case !numbersWithinLimits(value):
			delete(object, keyword)
			p.stop(object, nil, fmt.Sprintf("%s holds a number beyond this evaluator's numeric limits", keyword))
		case holdsNumber(value):
			p.compares = true
		}
	}
}

// stop makes evaluation reaching a schema on an instance of one of types
// give no verdict: a sentinel format, beside the schema's other keywords.
func (p *prepared) stop(object map[string]any, types []string, reason string) {
	name := fmt.Sprintf("schemaeval-sentinel-%d", len(p.sentinels))
	p.sentinels[name] = sentinelFormat{types, reason}
	appendAllOf(object, map[string]any{"format": name})
}

func appendAllOf(object map[string]any, schema map[string]any) {
	entries, _ := object["allOf"].([]any)
	object["allOf"] = append(entries, schema)
}

// guardPropertyNames stops evaluation at a propertyNames subschema that
// applies a $dynamicRef in place: the library checks property names without
// the dynamic scope, so its answer there could differ from 2020-12's.
func (p *prepared) guardPropertyNames() {
	index := indexBundle(p.document)
	forEachSchema(p.document, func(object map[string]any) {
		names, ok := object["propertyNames"].(map[string]any)
		if ok && index.reachesDynamicRef(names, map[uintptr]bool{}) {
			name := fmt.Sprintf("schemaeval-sentinel-%d", len(p.sentinels))
			p.sentinels[name] = sentinelFormat{[]string{"string"}, "a $dynamicRef applies to property names, which the library checks without the dynamic scope"}
			object["propertyNames"] = map[string]any{"format": name}
		}
	})
}

// bundleIndex finds the schemas a bundle's references name: core writes
// every reference as an absolute URI, with a JSON Pointer or a plain name.
type bundleIndex struct {
	resources map[string]map[string]any
	anchors   map[string]map[string]any
}

func indexBundle(document any) bundleIndex {
	index := bundleIndex{resources: map[string]map[string]any{}, anchors: map[string]map[string]any{}}
	var walk func(schema any, base string)
	walk = func(schema any, base string) {
		object, ok := schema.(map[string]any)
		if !ok {
			return
		}
		if id, ok := object["$id"].(string); ok {
			base = strings.TrimSuffix(id, "#")
			index.resources[base] = object
		}
		for _, keyword := range []string{"$anchor", "$dynamicAnchor"} {
			if name, ok := object[keyword].(string); ok {
				index.anchors[base+"#"+name] = object
			}
		}
		for _, keyword := range schemaMaps {
			entries, _ := object[keyword].(map[string]any)
			for _, entry := range entries {
				walk(entry, base)
			}
		}
		for _, keyword := range schemaSingle {
			walk(object[keyword], base)
		}
		for _, keyword := range schemaArrays {
			entries, _ := object[keyword].([]any)
			for _, entry := range entries {
				walk(entry, base)
			}
		}
	}
	walk(document, "")
	return index
}

// target returns the schema a reference names, or nil.
func (b bundleIndex) target(ref string) any {
	base, fragment, _ := strings.Cut(ref, "#")
	if fragment == "" {
		return b.resources[base]
	}
	if !strings.HasPrefix(fragment, "/") {
		return b.anchors[ref]
	}
	var current any = b.resources[base]
	for _, token := range strings.Split(fragment[1:], "/") {
		decoded, err := url.PathUnescape(token)
		if err != nil {
			return nil
		}
		decoded = strings.NewReplacer("~1", "/", "~0", "~").Replace(decoded)
		switch node := current.(type) {
		case map[string]any:
			current = node[decoded]
		case []any:
			i, err := strconv.Atoi(decoded)
			if err != nil || i < 0 || i >= len(node) {
				return nil
			}
			current = node[i]
		default:
			return nil
		}
	}
	return current
}

// reachesDynamicRef reports whether applying a schema in place can reach a
// $dynamicRef: through in-place applicators and references.
func (b bundleIndex) reachesDynamicRef(schema any, visiting map[uintptr]bool) bool {
	object, ok := schema.(map[string]any)
	if !ok {
		return false
	}
	at := reflect.ValueOf(object).Pointer()
	if visiting[at] {
		return false
	}
	visiting[at] = true
	if _, present := object["$dynamicRef"]; present {
		return true
	}
	if ref, ok := object["$ref"].(string); ok && b.reachesDynamicRef(b.target(ref), visiting) {
		return true
	}
	for _, keyword := range inPlace {
		switch value := object[keyword].(type) {
		case []any:
			for _, entry := range value {
				if b.reachesDynamicRef(entry, visiting) {
					return true
				}
			}
		case map[string]any:
			if keyword == "dependentSchemas" {
				for _, entry := range value {
					if b.reachesDynamicRef(entry, visiting) {
						return true
					}
				}
			} else if b.reachesDynamicRef(value, visiting) {
				return true
			}
		}
	}
	return false
}

// readCount reads a count keyword's number exactly, by its digits and power
// of ten, never building a value of the size its exponent names: its value
// when it is a non-negative integer within math.MaxInt, past when it is an
// integer beyond math.MaxInt, and ok false when it is neither a non-negative
// integer nor zero.
func readCount(token string) (value int, past, ok bool) {
	d := decimalOf(token)
	switch {
	case d.significant == "":
		return 0, false, true
	case d.negative || d.power.Sign() < 0:
		// Its significant digits end in a nonzero digit, so a negative
		// power leaves a fraction.
		return 0, false, false
	case !d.power.IsInt64() || int64(len(d.significant))+d.power.Int64() > int64(len(strconv.Itoa(math.MaxInt))):
		return 0, true, true
	}
	parsed, err := strconv.ParseUint(d.significant+strings.Repeat("0", int(d.power.Int64())), 10, 64)
	if err != nil || parsed > math.MaxInt {
		return 0, true, true
	}
	return int(parsed), false, true
}

func numbersWithinLimits(value any) bool {
	_, err := numericLimits(value)
	return err == nil
}

func holdsNumber(value any) bool {
	switch v := value.(type) {
	case json.Number:
		return true
	case []any:
		for _, item := range v {
			if holdsNumber(item) {
				return true
			}
		}
	case map[string]any:
		for _, member := range v {
			if holdsNumber(member) {
				return true
			}
		}
	}
	return false
}

func sortedKeys(object map[string]any) []string {
	out := make([]string, 0, len(object))
	for key := range object {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
