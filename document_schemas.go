package openbindings

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/openbindings/openbindings-go/internal/jsonpointer"
)

// documentSchemas is what an OBI document holds as schemas (§5.2, §7): the
// schema at every OBI schema position, the target of every same-document
// reference from one, and every schema resource they embed by $id. The rest
// of the document is not a schema, whatever its members are named.
type documentSchemas struct {
	// locations are the JSON Pointers of the schema positions and of the
	// same-document reference targets, sorted.
	locations []string
	// resources maps the absolute URI of every embedded schema resource,
	// without fragment, to where it is.
	resources map[string]schemaResource
	// conflict states why the embedded resources are ambiguous: two
	// locations declare one $id. It is empty when they are not.
	conflict string
}

type schemaResource struct {
	location string
	schema   map[string]any
	// outermost is true when no other embedded resource encloses this one.
	outermost bool
}

// collectDocumentSchemas walks the schema positions of a document's generic
// view, following same-document references from them.
func collectDocumentSchemas(view any) documentSchemas {
	d := documentSchemas{resources: map[string]schemaResource{}}
	visited := map[string]bool{}
	var walk func(node any, tokens []string, base *url.URL)
	visit := func(tokens []string) {
		location := jsonpointer.Format(tokens...)
		if visited[location] {
			return
		}
		visited[location] = true
		d.locations = append(d.locations, location)
		target, base, ok := descend(view, nil, atDocument, tokens)
		if ok {
			walk(target, tokens, base)
		}
	}
	walk = func(node any, tokens []string, base *url.URL) {
		object, ok := node.(map[string]any)
		if !ok {
			return
		}
		if id, declared := resourceID(object, base); declared {
			location := jsonpointer.Format(tokens...)
			key := id.String()
			if existing, seen := d.resources[key]; seen && existing.location != location && d.conflict == "" {
				d.conflict = fmt.Sprintf("the schemas at %q and %q both declare $id %q", existing.location, location, key)
			} else if !seen {
				d.resources[key] = schemaResource{location: location, schema: object, outermost: base == nil}
			}
			base = id
		}
		if ref, ok := object["$ref"].(string); ok && base == nil && strings.HasPrefix(ref, "#") {
			// A same-document fragment outside every resource addresses the
			// document (§7); inside a resource it is the resource's own.
			if refTokens, ok := jsonpointer.Parse(ref[1:]); ok {
				visit(refTokens)
			}
		}
		forEachSubschema(object, func(child any, childTokens ...string) {
			walk(child, append(append([]string(nil), tokens...), childTokens...), base)
		})
	}

	root, _ := view.(map[string]any)
	schemas, _ := root["schemas"].(map[string]any)
	for _, key := range sortedKeys(schemas) {
		visit([]string{"schemas", key})
	}
	operations, _ := root["operations"].(map[string]any)
	for _, key := range sortedKeys(operations) {
		operation, _ := operations[key].(map[string]any)
		for _, position := range []string{"input", "output"} {
			if _, present := operation[position]; present {
				visit([]string{"operations", key, position})
			}
		}
	}
	sort.Strings(d.locations)
	return d
}

// resourceID returns the absolute URI, without fragment, of the resource a
// schema object declares by $id, resolved against base. declared is false
// when the object declares no resource with an absolute URI.
func resourceID(object map[string]any, base *url.URL) (id *url.URL, declared bool) {
	raw, ok := object["$id"].(string)
	if !ok {
		return nil, false
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, false
	}
	if base != nil {
		parsed = base.ResolveReference(parsed)
	}
	if !parsed.IsAbs() {
		return nil, false
	}
	parsed.Fragment, parsed.RawFragment = "", ""
	return parsed, true
}

// forEachSubschema calls fn for every direct subschema of a schema object, as
// JSON Schema 2020-12's applicator keywords define them, with the reference
// tokens that reach it from the object.
func forEachSubschema(object map[string]any, fn func(child any, tokens ...string)) {
	for _, keyword := range sortedKeys(object) {
		value := object[keyword]
		switch {
		case schemaMapKeywords[keyword]:
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

// pathPosition is what the value reached along a reference path is: part of
// the document's structure, a schema, a map or array of schemas, or anything
// else.
type pathPosition int

const (
	atDocument pathPosition = iota
	atSchemasMap
	atOperationsMap
	atOperation
	atSchema
	atSchemaMap
	atSchemaArray
	atOther
)

// descend follows reference tokens from node, a value at the given position,
// and returns the value reached and the base URI in effect there: the $id of
// the innermost schema resource passed on the way, resolved against base.
// Only schemas declare resources, so a member that is not a schema, such as
// an unknown member of the document or a value inside `const`, never does.
// The start node's own $id is not applied; its caller has done so.
func descend(node any, base *url.URL, position pathPosition, tokens []string) (any, *url.URL, bool) {
	for index, token := range tokens {
		if position == atSchema && index > 0 {
			if object, ok := node.(map[string]any); ok {
				if id, declared := resourceID(object, base); declared {
					base = id
				}
			}
		}
		child, ok := jsonpointer.Resolve(node, jsonpointer.Format(token))
		if !ok {
			return nil, nil, false
		}
		node = child
		switch position {
		case atDocument:
			switch token {
			case "schemas":
				position = atSchemasMap
			case "operations":
				position = atOperationsMap
			default:
				position = atOther
			}
		case atSchemasMap, atSchemaMap, atSchemaArray:
			position = atSchema
		case atOperationsMap:
			position = atOperation
		case atOperation:
			if token == "input" || token == "output" {
				position = atSchema
			} else {
				position = atOther
			}
		case atSchema:
			switch {
			case schemaMapKeywords[token]:
				position = atSchemaMap
			case singleSchemaKeywords[token]:
				position = atSchema
			case arraySchemaKeywords[token]:
				position = atSchemaArray
			default:
				position = atOther
			}
		}
	}
	return node, base, true
}

type graphLocality int

const (
	graphWithinDocument graphLocality = iota
	graphUndecided
	graphReachesExternal
)

// schemaGraphLocality decides whether the schema graph statically reachable
// from the schema at a location of the document resolves entirely within the
// document (OBI-D-11's scope).
//
// It follows same-document references from the document root, and absolute
// references into the schema resources the document embeds, fragments
// included. An absolute reference to anything else reaches an external
// resource. A reference this walk cannot follow (a plain-name fragment, a
// pointer that does not resolve, a resource $id two schemas declare) leaves
// the reach undecided rather than guessed. Reaching an external resource
// dominates: such a graph is outside the rule whatever else it holds.
func schemaGraphLocality(view any, tokens []string, schemas documentSchemas) graphLocality {
	result := graphWithinDocument
	undecided := func() {
		if result == graphWithinDocument {
			result = graphUndecided
		}
	}
	if schemas.conflict != "" {
		undecided()
	}
	visited := map[string]bool{}
	var walk func(node any, base *url.URL)
	follow := func(ref string, base *url.URL) {
		if base == nil && strings.HasPrefix(ref, "#") {
			pointer := ref[1:]
			refTokens, ok := jsonpointer.Parse(pointer)
			if !ok {
				undecided()
				return
			}
			if visited["#"+pointer] {
				return
			}
			visited["#"+pointer] = true
			target, targetBase, ok := descend(view, nil, atDocument, refTokens)
			if !ok {
				undecided()
				return
			}
			walk(target, targetBase)
			return
		}
		parsed, err := url.Parse(ref)
		if err != nil {
			undecided()
			return
		}
		if base != nil {
			parsed = base.ResolveReference(parsed)
		}
		if !parsed.IsAbs() {
			undecided()
			return
		}
		fragment := parsed.Fragment
		parsed.Fragment, parsed.RawFragment = "", ""
		resource, embedded := schemas.resources[parsed.String()]
		if !embedded {
			result = graphReachesExternal
			return
		}
		key := parsed.String() + "#" + fragment
		if visited[key] {
			return
		}
		visited[key] = true
		switch {
		case fragment == "":
			walk(resource.schema, parsed)
		case strings.HasPrefix(fragment, "/"):
			fragmentTokens, ok := jsonpointer.Parse(fragment)
			if !ok {
				undecided()
				return
			}
			target, targetBase, ok := descend(resource.schema, parsed, atSchema, fragmentTokens)
			if !ok {
				undecided()
				return
			}
			walk(target, targetBase)
		default:
			undecided() // a plain-name fragment names an anchor
		}
	}
	walk = func(node any, base *url.URL) {
		if result == graphReachesExternal {
			return
		}
		object, ok := node.(map[string]any)
		if !ok {
			return
		}
		if id, declared := resourceID(object, base); declared {
			base = id
		}
		for _, keyword := range []string{"$ref", "$dynamicRef"} {
			if ref, ok := object[keyword].(string); ok {
				follow(ref, base)
			}
		}
		forEachSubschema(object, func(child any, _ ...string) {
			walk(child, base)
		})
	}
	start, base, ok := descend(view, nil, atDocument, tokens)
	if !ok {
		return graphUndecided
	}
	walk(start, base)
	return result
}
