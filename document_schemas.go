package openbindings

import (
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/openbindings/openbindings-go/internal/jsonpointer"
	"github.com/openbindings/openbindings-go/internal/schemacompiler"
)

// documentSchemas is what an OBI document holds as schemas (§5.2, §7): the
// schema resources schemas declare with their own $id, the plain names the
// document resource declares, and the identifiers OBI-D-13 compares. Every
// schema the document contains is walked: the schemas at OBI positions (an
// operation's input or output, an entry of schemas, and every subschema the
// 2020-12 meta-schema validates as one, the legacy definitions and the schema
// values of the legacy dependencies included) and the subschemas of the
// resources they declare. The rest of the document is not a schema, whatever
// its members are named, and declares nothing.
type documentSchemas struct {
	// at maps the location of every resource to it.
	at map[string]*schemaResource
	// resources maps the absolute URI of every resource that one schema alone
	// declares, without fragment, to it.
	resources map[string]*schemaResource
	// ambiguous maps an absolute URI that more than one schema declares as its
	// $id to why it names no one resource.
	ambiguous map[string]string
	// anchors maps each plain name the document resource declares to every
	// declaration of it, in document order: one per $anchor and one per
	// $dynamicAnchor, as OBI-D-13 counts them (JSON Schema Core §8.2.2).
	anchors map[string][]anchorDeclaration
	// identifiers maps each identifier OBI-D-13 compares to where every
	// schema declaring it sits, in document order.
	identifiers map[string][]*pathNode
	// dynamicAnchors maps each name a $dynamicAnchor declares, in any
	// resource, to where every schema declaring it sits, in document order: a
	// $dynamicRef naming it may land on any of them at run time.
	dynamicAnchors map[string][]string
	// documentDynamicAnchor is true when the document resource declares a
	// $dynamicAnchor, which the dynamic scope of an evaluation beginning there
	// holds (§7.2).
	documentDynamicAnchor bool
}

// anchorDeclaration is one declaration of a plain name: the schema declaring
// it and the keyword that does.
type anchorDeclaration struct {
	at      *pathNode
	keyword string
}

// schemaResource is a schema that has an $id member, and what it encloses: a
// boundary (§7) whatever the member's value.
type schemaResource struct {
	location string
	schema   map[string]any
	// uri is the absolute URI the resource's $id resolves to, without
	// fragment; nil when it resolves to none, as a relative $id at an OBI
	// position or an $id that is not a string does. Such a resource is still a
	// boundary: what lies within it is its own business (§7), and it can be
	// addressed only from within. An $id empty once its fragment is removed
	// ("" or "#") resolves to its base, a URI the schema enclosing it already
	// has.
	uri *url.URL
	// anchors maps each plain-name anchor the resource declares to where the
	// schema declaring it sits, once per declaration, by $anchor or
	// $dynamicAnchor, in document order, spelled out only when used: JSON
	// Schema leaves a name declared more than once in a resource undefined,
	// even by one schema (Core §8.2.2). A nested resource's anchors are its
	// own.
	anchors map[string][]*pathNode
}

// collectDocumentSchemas walks the schemas a document's generic view contains.
func collectDocumentSchemas(view any) documentSchemas {
	d := documentSchemas{
		at:             map[string]*schemaResource{},
		resources:      map[string]*schemaResource{},
		anchors:        map[string][]anchorDeclaration{},
		identifiers:    map[string][]*pathNode{},
		dynamicAnchors: map[string][]string{},
	}
	claimants := map[string][]*schemaResource{}
	// identifier is the identifier OBI-D-13 compares for the nearest
	// enclosing schema that declares $id, or "" when there is none or it is
	// not compared.
	var walk func(node any, at *pathNode, resource *schemaResource, identifier string)
	walk = func(node any, at *pathNode, resource *schemaResource, identifier string) {
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
			var base *url.URL
			if resource != nil {
				base = resource.uri
			}
			location := at.from(nil)
			resource = &schemaResource{location: location, schema: object, anchors: map[string][]*pathNode{}}
			if isString {
				resource.uri = resolveID(raw, base)
			}
			d.at[location] = resource
			if resource.uri != nil {
				key := resource.uri.String()
				claimants[key] = append(claimants[key], resource)
			}
		}
		for _, keyword := range []string{"$anchor", "$dynamicAnchor"} {
			name, isString := object[keyword].(string)
			if !isString {
				continue
			}
			if resource != nil {
				resource.anchors[name] = append(resource.anchors[name], at)
			} else {
				d.anchors[name] = append(d.anchors[name], anchorDeclaration{at: at, keyword: keyword})
			}
			if keyword == "$dynamicAnchor" {
				d.dynamicAnchors[name] = append(d.dynamicAnchors[name], at.from(nil))
			}
		}
		if _, present := object["$dynamicAnchor"]; present && resource == nil {
			d.documentDynamicAnchor = true
		}
		forEachDescribedSubschema(object, func(child any, tokens ...string) {
			walk(child, at.child(tokens...), resource, identifier)
		})
	}

	root, _ := view.(map[string]any)
	schemas, _ := root["schemas"].(map[string]any)
	for _, key := range sortedKeys(schemas) {
		walk(schemas[key], (*pathNode)(nil).child("schemas", key), nil, "")
	}
	operations, _ := root["operations"].(map[string]any)
	for _, key := range sortedKeys(operations) {
		operation, _ := operations[key].(map[string]any)
		for _, position := range []string{"input", "output"} {
			if schema, present := operation[position]; present {
				walk(schema, (*pathNode)(nil).child("operations", key, position), nil, "")
			}
		}
	}
	for id, resources := range claimants {
		if len(resources) == 1 {
			d.resources[id] = resources[0]
			continue
		}
		var locations []string
		for _, resource := range resources {
			locations = append(locations, resource.location)
		}
		sort.Strings(locations)
		if d.ambiguous == nil {
			d.ambiguous = map[string]string{}
		}
		// The message names a few declarations, so a message per reference
		// does not grow with how many schemas declare the URI.
		named := strings.Join(locations[:min(len(locations), 3)], ", ")
		if more := len(locations) - 3; more > 0 {
			named += fmt.Sprintf(", and %d more", more)
		}
		d.ambiguous[id] = fmt.Sprintf("the schemas at %s all declare it", named)
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

// resourceAt returns the innermost resource holding a location, or nil for the
// document resource: every schema no resource encloses, whose base is the OBI
// document root (§7). A location's resource is found by its prefixes, so a
// location inside a resource belongs to it however it is reached, through
// objects and arrays alike.
func (d documentSchemas) resourceAt(location string) *schemaResource {
	for end := len(location); end >= 0; end = strings.LastIndexByte(location[:end], '/') {
		if resource, ok := d.at[location[:end]]; ok {
			return resource
		}
		if end == 0 {
			break
		}
	}
	return nil
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

// resolveID returns the absolute URI, without fragment, that a declared $id
// resolves to against base, or nil when it resolves to none.
func resolveID(raw string, base *url.URL) *url.URL {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	parsed, resolved := resolveURI(base, parsed)
	if !resolved || !parsed.IsAbs() {
		return nil
	}
	parsed.Fragment, parsed.RawFragment = "", ""
	return parsed
}

// resolveURI resolves ref against base, which may be nil when ref is
// absolute, strictly by RFC 3986 §5.2 (resolveURIReference), in time
// proportional to their length. It reports false when the target does not
// parse as a URL. net/url's own resolution differs from the RFC against a
// base whose path is rootless (urn:x:y), and repeats work for each ".."
// segment.
func resolveURI(base, ref *url.URL) (*url.URL, bool) {
	var from uriParts
	if base != nil {
		from = splitURI(base.String())
	}
	target, err := url.Parse(resolveURIReference(from, splitURI(ref.String())).String())
	return target, err == nil
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

// forEachSubschema calls fn for every direct subschema of a schema object, as
// JSON Schema 2020-12 defines them, with the reference tokens that reach it
// from the object: the applicators' subschemas and the definitions in $defs.
func forEachSubschema(object map[string]any, fn func(child any, tokens ...string)) {
	forEachPosition(object, false, fn)
}

// forEachDescribedSubschema calls fn for every direct subschema of a schema
// object the 2020-12 meta-schema describes: forEachSubschema's, and the
// entries of definitions and dependencies.
func forEachDescribedSubschema(object map[string]any, fn func(child any, tokens ...string)) {
	forEachPosition(object, true, fn)
}

func forEachPosition(object map[string]any, described bool, fn func(child any, tokens ...string)) {
	for _, keyword := range sortedKeys(object) {
		value := object[keyword]
		switch {
		case schemaMapKeywords[keyword], described && describedMapKeywords[keyword]:
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

// builtInMetaSchemas remembers the URIs found to name a meta-schema the
// schema library carries. Only those are remembered: they are few and fixed,
// while the URIs that name none come from documents.
var builtInMetaSchemas sync.Map // string -> struct{}

// isBuiltInMetaSchema reports whether id names a JSON Schema meta-schema the
// schema library carries, which resolves without obtaining anything.
func isBuiltInMetaSchema(id string) bool {
	if !strings.HasPrefix(id, "http://json-schema.org/") && !strings.HasPrefix(id, "https://json-schema.org/") {
		return false
	}
	if _, known := builtInMetaSchemas.Load(id); known {
		return true
	}
	if _, err := schemacompiler.New().Compile(id); err != nil {
		return false
	}
	builtInMetaSchemas.Store(id, struct{}{})
	return true
}
