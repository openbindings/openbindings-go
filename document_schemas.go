package openbindings

import (
	"fmt"
	"slices"

	"github.com/openbindings/openbindings-go/internal/jsonpointer"
)

// documentSchemas is what OBI-D-12 and OBI-D-13 read from an OBI document's
// schemas (§5.2, §7): the plain names the document resource declares and the
// identifiers OBI-D-13 compares. Every schema the document contains is
// walked: the schemas at OBI positions (an operation's input or output, an
// entry of schemas, and every subschema the 2020-12 meta-schema validates as
// one, the legacy definitions and the schema values of the legacy
// dependencies included) and the subschemas of the resources they declare.
// The rest of the document is not a schema, whatever its members are named,
// and declares nothing. Locations are spelled out only when used, so the walk
// takes space linear in the document however deeply its schemas nest.
type documentSchemas struct {
	// anchors maps each plain name the document resource declares to every
	// declaration of it, in document order: one per $anchor and one per
	// $dynamicAnchor, as OBI-D-13 counts them (JSON Schema Core §8.2.2).
	anchors map[string][]anchorDeclaration
	// identifiers maps each identifier OBI-D-13 compares to where every
	// schema declaring it sits, in document order.
	identifiers map[string][]*pathNode
}

// anchorDeclaration is one declaration of a plain name: the schema declaring
// it and the keyword that does.
type anchorDeclaration struct {
	at      *pathNode
	keyword string
}

// collectDocumentSchemas walks the schemas a document's generic view contains.
func collectDocumentSchemas(view any) documentSchemas {
	d := documentSchemas{anchors: map[string][]anchorDeclaration{}, identifiers: map[string][]*pathNode{}}
	// inResource is true within a schema that has an $id member, a boundary
	// (§7) whatever the member's value, whose plain names are its own.
	// identifier is the identifier OBI-D-13 compares for the nearest
	// enclosing schema that declares $id, or "" when there is none or it is
	// not compared.
	var walk func(node any, at *pathNode, inResource bool, identifier string)
	walk = func(node any, at *pathNode, inResource bool, identifier string) {
		object, ok := node.(map[string]any)
		if !ok {
			return
		}
		if value, present := object["$id"]; present {
			// An $id that is not a string is not compared, and neither is a
			// relative $id within it.
			raw, isString := value.(string)
			enclosing := identifier
			if identifier = ""; isString {
				identifier = comparableID(raw, enclosing)
			}
			if identifier != "" {
				d.identifiers[identifier] = append(d.identifiers[identifier], at)
			}
			inResource = true
		}
		if !inResource {
			for _, keyword := range []string{"$anchor", "$dynamicAnchor"} {
				if name, isString := object[keyword].(string); isString {
					d.anchors[name] = append(d.anchors[name], anchorDeclaration{at: at, keyword: keyword})
				}
			}
		}
		forEachDescribedSubschema(object, func(child any, tokens ...string) {
			walk(child, at.child(tokens...), inResource, identifier)
		})
	}

	root, _ := view.(map[string]any)
	schemas, _ := root["schemas"].(map[string]any)
	for _, key := range sortedKeys(schemas) {
		walk(schemas[key], (*pathNode)(nil).child("schemas", key), false, "")
	}
	operations, _ := root["operations"].(map[string]any)
	for _, key := range sortedKeys(operations) {
		operation, _ := operations[key].(map[string]any)
		for _, position := range []string{"input", "output"} {
			if schema, present := operation[position]; present {
				walk(schema, (*pathNode)(nil).child("operations", key, position), false, "")
			}
		}
	}
	return d
}

// comparableID returns the identifier OBI-D-13 compares for an $id, or ""
// when it compares none: a well-formed URI-reference that is an absolute URI,
// or that resolves against the identifier of the nearest enclosing schema that
// declares one, itself compared (enclosing), resolved by RFC 3986 §5.2, which
// removes dot segments, with any empty fragment removed. Nothing else is
// normalized, so spellings a URI library would merge stay distinct.
func comparableID(raw, enclosing string) string {
	wellFormed, absolute := uriReference(raw)
	if !wellFormed || !absolute && enclosing == "" {
		return ""
	}
	var resolved uriParts
	if absolute {
		resolved = resolveURIReference(uriParts{}, splitURI(raw))
	} else {
		resolved = resolveURIReference(splitURI(enclosing), splitURI(raw))
	}
	if resolved.hasFragment && resolved.fragment == "" {
		resolved.hasFragment = false
	}
	return resolved.String()
}

// pathNode is one reference token of a location, linked to the location it
// extends, so a walk records where it is in constant space per step and
// spells a location out only when one is needed.
type pathNode struct {
	parent *pathNode
	token  string
}

// child returns the location tokens reach from n.
func (n *pathNode) child(tokens ...string) *pathNode {
	for _, token := range tokens {
		n = &pathNode{parent: n, token: token}
	}
	return n
}

// from returns the location of n relative to its ancestor base, as a JSON
// Pointer; a nil base is the document root.
func (n *pathNode) from(base *pathNode) string {
	var tokens []string
	for at := n; at != base; at = at.parent {
		tokens = append(tokens, at.token)
	}
	slices.Reverse(tokens)
	return jsonpointer.Format(tokens...)
}

// Keyword tables. Most entries name schema-bearing positions under JSON
// Schema 2020-12. Evaluation is narrower: contentSchema is annotation only,
// and then/else need a sibling if. The pre-2019 spellings definitions and
// dependencies hold schemas the 2020-12 meta-schema validates, so their
// schemas are at OBI positions (§7), though strict 2020-12 evaluates neither
// and the bundle leaves dependencies out.

// schemaMapKeywords hold { name -> schema } maps.
var schemaMapKeywords = map[string]bool{
	"properties":        true,
	"patternProperties": true,
	"$defs":             true,
	"dependentSchemas":  true,
}

// describedMapKeywords hold { name -> schema } maps the meta-schema describes
// but 2020-12 does not evaluate.
var describedMapKeywords = map[string]bool{
	"definitions":  true,
	"dependencies": true,
}

// singleSchemaKeywords hold one schema.
var singleSchemaKeywords = map[string]bool{
	"additionalProperties":  true,
	"propertyNames":         true,
	"unevaluatedProperties": true,
	"items":                 true,
	"contains":              true,
	"unevaluatedItems":      true,
	"not":                   true,
	"if":                    true,
	"then":                  true,
	"else":                  true,
	"contentSchema":         true,
}

// arraySchemaKeywords hold an array of schemas.
var arraySchemaKeywords = map[string]bool{
	"allOf":       true,
	"anyOf":       true,
	"oneOf":       true,
	"prefixItems": true,
}

// forEachDescribedSubschema calls fn for every direct subschema of a schema
// object the 2020-12 meta-schema describes, with the reference tokens that
// reach it from the object: the applicators' subschemas, the definitions in
// $defs, and the entries of definitions and dependencies.
func forEachDescribedSubschema(object map[string]any, fn func(child any, tokens ...string)) {
	for _, keyword := range sortedKeys(object) {
		value := object[keyword]
		switch {
		case schemaMapKeywords[keyword], describedMapKeywords[keyword]:
			entries, _ := value.(map[string]any)
			for _, name := range sortedKeys(entries) {
				fn(entries[name], keyword, name)
			}
		case singleSchemaKeywords[keyword]:
			fn(value, keyword)
		case arraySchemaKeywords[keyword]:
			entries, _ := value.([]any)
			for index, entry := range entries {
				fn(entry, keyword, fmt.Sprint(index))
			}
		}
	}
}

// schemaDepth returns how deeply a schema nests the subschemas the 2020-12
// meta-schema checks, which is what its check's cost grows with: 0 for a
// schema with none. Values that are not subschemas, such as const, enum,
// default, and examples, do not count.
func schemaDepth(schema any) int {
	object, ok := schema.(map[string]any)
	if !ok {
		return 0
	}
	deepest := 0
	forEachDescribedSubschema(object, func(child any, _ ...string) {
		deepest = max(deepest, schemaDepth(child)+1)
	})
	return deepest
}
