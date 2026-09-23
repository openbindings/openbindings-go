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
// schema at every OBI schema position, the target of every same-document
// reference from one, and every schema resource they embed by $id. The rest
// of the document is not a schema, whatever its members are named.
type documentSchemas struct {
	// resources maps the absolute URI of every embedded schema resource,
	// without fragment, to where it is.
	resources map[string]schemaResource
	// ambiguous maps an absolute URI that some schema declares as its $id but
	// that names no one resource to why: more than one schema declares it, or
	// it is a URI the SDK itself resolves (the document's own, or a built-in
	// meta-schema's). Such a URI is in no graph's reach.
	ambiguous map[string]string
}

type schemaResource struct {
	location string
	schema   map[string]any
}

// collectDocumentSchemas walks the schema positions of a document's generic
// view, following same-document references from them.
func collectDocumentSchemas(view any) documentSchemas {
	d := documentSchemas{resources: map[string]schemaResource{}}
	claims := map[string][]string{}
	visited := map[string]bool{}
	var walk func(node any, tokens []string, base *url.URL)
	visit := func(tokens []string) {
		location := jsonpointer.Format(tokens...)
		if visited[location] {
			return
		}
		visited[location] = true
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
			if !slices.Contains(claims[key], location) {
				claims[key] = append(claims[key], location)
				d.resources[key] = schemaResource{location: location, schema: object}
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
	for id, locations := range claims {
		sort.Strings(locations)
		why := ""
		switch {
		case len(locations) > 1:
			why = fmt.Sprintf("the schemas at %s all declare it", strings.Join(locations, ", "))
		case id == documentURL:
			why = "it is the URI the SDK gives the document itself"
		case strings.HasPrefix(id, "http://json-schema.org/") || strings.HasPrefix(id, "https://json-schema.org/"):
			why = "it is a JSON Schema meta-schema's URI, which resolves to the built-in meta-schema"
		default:
			continue
		}
		if d.ambiguous == nil {
			d.ambiguous = map[string]string{}
		}
		d.ambiguous[id] = why
		delete(d.resources, id)
	}
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
// JSON Schema 2020-12 defines them, with the reference tokens that reach it
// from the object: the applicators' subschemas and the definitions in $defs.
func forEachSubschema(object map[string]any, fn func(child any, tokens ...string)) {
	forEachSubschemaOf(object, true, fn)
}

// forEachAppliedSubschema calls fn for every direct subschema that
// evaluating the object applies: every subschema but a definition, which
// applies only through a reference to it.
func forEachAppliedSubschema(object map[string]any, fn func(child any, tokens ...string)) {
	forEachSubschemaOf(object, false, fn)
}

func forEachSubschemaOf(object map[string]any, definitions bool, fn func(child any, tokens ...string)) {
	for _, keyword := range sortedKeys(object) {
		value := object[keyword]
		if !definitions && definitionKeywords[keyword] {
			continue
		}
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

// definitionKeywords hold schemas that apply only when a reference reaches
// them (T16-S-04 of the core conformance corpus).
var definitionKeywords = map[string]bool{"$defs": true, "definitions": true}

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

// schemaGraph is what a static walk of the schema graph reachable from one
// schema of a document established. The graph is what evaluating the schema
// applies: its applied subschemas and the targets of its references,
// transitively; a definition in $defs belongs to it only when a reference
// reaches it (§5.2, OBI-T-16). Each field is empty when the walk found none.
type schemaGraph struct {
	// external is a resource the graph reaches that the document does not
	// embed and that is not available locally.
	external string
	// builtIn is a JSON Schema meta-schema the graph reaches. It is outside
	// the document but always available: the SDK carries it.
	builtIn string
	// unresolved states a reference the walk could not follow: a pointer or
	// anchor that does not resolve, or a $id two schemas declare.
	unresolved string
	// illFormed states a schema in the graph that §5.2's constraints exclude:
	// a $schema other than 2020-12, or a $vocabulary.
	illFormed string
}

// complete reports whether the graph is available and well-formed as far as
// a static walk can tell: it reaches nothing outside the document, every
// reference resolves, and every schema keeps §5.2's dialect constraints.
func (g schemaGraph) complete() bool {
	return g.external == "" && g.unresolved == "" && g.illFormed == ""
}

// problem states why the graph is not complete.
func (g schemaGraph) problem() string {
	switch {
	case g.external != "":
		return fmt.Sprintf("the schema graph reaches %s, which the document does not embed", g.external)
	case g.unresolved != "":
		return "the schema graph is not fully resolvable: " + g.unresolved
	case g.illFormed != "":
		return "the schema graph is not well-formed: " + g.illFormed
	}
	return ""
}

// analyzeSchemaGraph walks the schema graph reachable from the schema at a
// location of the document. It follows same-document references from the
// document root, and absolute references into the schema resources the
// document embeds, with a JSON Pointer or plain-name fragment.
func analyzeSchemaGraph(view any, tokens []string, schemas documentSchemas) schemaGraph {
	var graph schemaGraph
	unresolved := func(format string, args ...any) {
		if graph.unresolved == "" {
			graph.unresolved = fmt.Sprintf(format, args...)
		}
	}
	visited := map[string]bool{}
	var walk func(node any, base *url.URL)
	// enter walks a schema the graph reaches as a whole, the schema it starts
	// from or a reference's target, after checking it well-formed against the
	// 2020-12 meta-schemas (§5.2).
	enter := func(node any, base *url.URL) {
		if base != nil && schemas.ambiguous[base.String()] != "" {
			unresolved("a schema in the graph lies inside %s, which names no one embedded schema: %s", base, schemas.ambiguous[base.String()])
			return
		}
		switch node.(type) {
		case bool:
		case map[string]any:
			if err := compiledMetaSchema.Validate(node); err != nil && graph.illFormed == "" {
				if problems, mismatch := schemacompiler.Outcome(err); mismatch {
					graph.illFormed = "a schema in it does not validate against the 2020-12 meta-schemas: " + problems[0].Line()
				} else {
					unresolved("a schema in it could not be checked against the meta-schemas: %v", err)
				}
			}
		default:
			if graph.illFormed == "" {
				graph.illFormed = fmt.Sprintf("a reference reaches a %s, which is not a schema", jsonTypeName(node))
			}
		}
		walk(node, base)
	}
	follow := func(ref string, base *url.URL) {
		if base == nil && strings.HasPrefix(ref, "#") {
			// URI semantics decode the fragment before it is read as a JSON
			// Pointer (RFC 6901 §6); a percent-encoded one is OBI-D-05's
			// violation, not an unresolvable reference.
			parsed, err := url.Parse(ref)
			if err != nil {
				unresolved("%q is not a URI reference", ref)
				return
			}
			pointer := parsed.Fragment
			refTokens, ok := jsonpointer.Parse(pointer)
			if !ok {
				unresolved("%q is not a JSON Pointer fragment", ref)
				return
			}
			if visited["#"+pointer] {
				return
			}
			visited["#"+pointer] = true
			target, targetBase, ok := descend(view, nil, atDocument, refTokens)
			if !ok {
				unresolved("%q does not resolve within the document", ref)
				return
			}
			enter(target, targetBase)
			return
		}
		parsed, err := url.Parse(ref)
		if err != nil {
			unresolved("%q is not a URI reference", ref)
			return
		}
		if base != nil {
			parsed = base.ResolveReference(parsed)
		}
		if !parsed.IsAbs() {
			unresolved("%q has no base to resolve against", ref)
			return
		}
		fragment := parsed.Fragment
		parsed.Fragment, parsed.RawFragment = "", ""
		id := parsed.String()
		if why := schemas.ambiguous[id]; why != "" {
			unresolved("%s names no one embedded schema: %s", id, why)
			return
		}
		resource, embedded := schemas.resources[id]
		if !embedded {
			switch {
			case isBuiltInMetaSchema(id):
				if graph.builtIn == "" {
					graph.builtIn = id
				}
			case graph.external == "":
				graph.external = id
			}
			return
		}
		if visited[id+"#"+fragment] {
			return
		}
		visited[id+"#"+fragment] = true
		target, targetBase, ok := resolveInResource(resource.schema, parsed, fragment)
		if !ok {
			unresolved("%q does not resolve within the resource %s", ref, id)
			return
		}
		enter(target, targetBase)
	}
	walk = func(node any, base *url.URL) {
		object, ok := node.(map[string]any)
		if !ok {
			return
		}
		if id, declared := resourceID(object, base); declared {
			if why := schemas.ambiguous[id.String()]; why != "" {
				unresolved("%s names no one embedded schema: %s", id, why)
			}
			base = id
		}
		if dialect, present := object["$schema"]; present && dialect != draft202012URI && graph.illFormed == "" {
			graph.illFormed = fmt.Sprintf("a schema declares $schema %v, not %s", dialect, draft202012URI)
		}
		if _, present := object["$vocabulary"]; present && graph.illFormed == "" {
			graph.illFormed = "a schema declares $vocabulary"
		}
		for _, keyword := range []string{"$ref", "$dynamicRef"} {
			if ref, ok := object[keyword].(string); ok {
				follow(ref, base)
			}
		}
		forEachAppliedSubschema(object, func(child any, _ ...string) {
			walk(child, base)
		})
	}
	start, base, ok := descend(view, nil, atDocument, tokens)
	if !ok {
		unresolved("%q is not a location in the document", jsonpointer.Format(tokens...))
		return graph
	}
	enter(start, base)
	return graph
}

// builtInMetaSchemas remembers which URIs name a meta-schema the schema
// backend carries.
var builtInMetaSchemas sync.Map // string -> bool

// isBuiltInMetaSchema reports whether id names a JSON Schema meta-schema the
// SDK carries, which resolves without obtaining anything.
func isBuiltInMetaSchema(id string) bool {
	if !strings.HasPrefix(id, "http://json-schema.org/") && !strings.HasPrefix(id, "https://json-schema.org/") {
		return false
	}
	if known, ok := builtInMetaSchemas.Load(id); ok {
		return known.(bool)
	}
	_, err := schemacompiler.New().Compile(id)
	builtInMetaSchemas.Store(id, err == nil)
	return err == nil
}

// resolveInResource resolves a fragment within an embedded resource whose
// absolute URI is base: the resource itself, a JSON Pointer from it, or a
// plain-name anchor it declares.
func resolveInResource(resource map[string]any, base *url.URL, fragment string) (any, *url.URL, bool) {
	switch {
	case fragment == "":
		return resource, base, true
	case strings.HasPrefix(fragment, "/"):
		tokens, ok := jsonpointer.Parse(fragment)
		if !ok {
			return nil, nil, false
		}
		return descend(resource, base, atSchema, tokens)
	default:
		target, ok := findAnchor(resource, fragment)
		return target, base, ok
	}
}

// findAnchor returns the schema in a resource that declares a plain-name
// anchor, by $anchor or $dynamicAnchor. The search does not enter a nested
// resource, whose anchors are its own. ok is false when no schema, or more
// than one, declares it.
func findAnchor(resource map[string]any, name string) (target any, ok bool) {
	var found []any
	var search func(node any, root bool)
	search = func(node any, root bool) {
		object, isObject := node.(map[string]any)
		if !isObject {
			return
		}
		if _, nested := object["$id"].(string); nested && !root {
			return
		}
		if object["$anchor"] == name || object["$dynamicAnchor"] == name {
			found = append(found, object)
		}
		forEachSubschema(object, func(child any, _ ...string) {
			search(child, false)
		})
	}
	search(resource, true)
	if len(found) != 1 {
		return nil, false
	}
	return found[0], true
}
