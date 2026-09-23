package openbindings

import (
	"fmt"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"sync"
	"unsafe"

	"github.com/openbindings/openbindings-go/internal/jsonpointer"
	"github.com/openbindings/openbindings-go/internal/schemacompiler"
)

// documentSchemas is what an OBI document holds as schemas (§5.2, §7): the
// schema at every OBI schema position with its subschemas, and every schema
// resource they embed by $id. The rest of the document is not a schema,
// whatever its members are named: a location outside the positions becomes
// one only when a reference reaches it, and declares no resource the document
// embeds.
type documentSchemas struct {
	// resources maps the absolute URI of every embedded schema resource,
	// without fragment, to where it is.
	resources map[string]schemaResource
	// ambiguous maps an absolute URI that more than one schema declares as its
	// $id to why it names no one resource. Such a URI is in no graph's reach.
	ambiguous map[string]string
	// shadowed holds embedded resources whose $id is a JSON Schema
	// meta-schema's URI. The document embeds them (§7), but the schema
	// backend resolves that URI to the meta-schema it carries, so it cannot
	// evaluate them.
	shadowed map[string]bool
	// dynamicAnchors maps a $dynamicAnchor name to every schema declaring it,
	// the places a $dynamicRef naming it can land.
	dynamicAnchors map[string][]anchorSite
}

type schemaResource struct {
	location string
	schema   map[string]any
	// parent is the base in effect where the resource sits, against which its
	// own $id resolves; nil at an OBI schema position.
	parent *url.URL
}

// anchorSite is a schema declaring a dynamic anchor, with the base in effect
// where it sits, before its own $id.
type anchorSite struct {
	schema map[string]any
	parent *url.URL
}

// collectDocumentSchemas walks the schema positions of a document's generic
// view and every subschema of them.
func collectDocumentSchemas(view any) documentSchemas {
	d := documentSchemas{resources: map[string]schemaResource{}}
	claims := map[string][]string{}
	var walk func(node any, tokens []string, base *url.URL)
	walk = func(node any, tokens []string, base *url.URL) {
		object, ok := node.(map[string]any)
		if !ok {
			return
		}
		if name, ok := object["$dynamicAnchor"].(string); ok {
			if d.dynamicAnchors == nil {
				d.dynamicAnchors = map[string][]anchorSite{}
			}
			d.dynamicAnchors[name] = append(d.dynamicAnchors[name], anchorSite{schema: object, parent: base})
		}
		if id, declared := resourceID(object, base); declared {
			location := jsonpointer.Format(tokens...)
			key := id.String()
			claims[key] = append(claims[key], location)
			d.resources[key] = schemaResource{location: location, schema: object, parent: base}
			base = id
		}
		forEachSubschema(object, func(child any, childTokens ...string) {
			walk(child, append(append([]string(nil), tokens...), childTokens...), base)
		})
	}

	root, _ := view.(map[string]any)
	schemas, _ := root["schemas"].(map[string]any)
	for _, key := range sortedKeys(schemas) {
		walk(schemas[key], []string{"schemas", key}, nil)
	}
	operations, _ := root["operations"].(map[string]any)
	for _, key := range sortedKeys(operations) {
		operation, _ := operations[key].(map[string]any)
		for _, position := range []string{"input", "output"} {
			if schema, present := operation[position]; present {
				walk(schema, []string{"operations", key, position}, nil)
			}
		}
	}
	for id, locations := range claims {
		switch {
		case len(locations) > 1:
			sort.Strings(locations)
			if d.ambiguous == nil {
				d.ambiguous = map[string]string{}
			}
			d.ambiguous[id] = fmt.Sprintf("the schemas at %s all declare it", strings.Join(locations, ", "))
			delete(d.resources, id)
		case isBuiltInMetaSchema(id):
			if d.shadowed == nil {
				d.shadowed = map[string]bool{}
			}
			d.shadowed[id] = true
		}
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
		if !definitions && unappliedKeywords[keyword] {
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

// unappliedKeywords hold schemas that evaluation applies only when a
// reference reaches them: the definitions in $defs (T16-S-04 of the core
// conformance corpus) and definitions, and the entries of dependencies, which
// the 2020-12 meta-schema still describes but 2020-12 no longer evaluates.
var unappliedKeywords = map[string]bool{"$defs": true, "definitions": true, "dependencies": true}

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
// reaches it (§5.2, OBI-T-16), and a $dynamicRef may land on any schema
// declaring its anchor. Each field is empty when the walk found none.
type schemaGraph struct {
	// external is a resource the graph reaches that the document does not
	// embed and that is not available locally.
	external string
	// builtIn is a JSON Schema meta-schema the graph reaches. It is outside
	// the document but always available: the SDK carries it.
	builtIn string
	// unresolved states why the graph cannot be evaluated as a whole: a
	// reference that does not resolve or names no one schema, a relative $id
	// with no base, or a cycle of references that never advances into the
	// value.
	unresolved string
	// illFormed states a schema in the graph that §5.2 excludes: one the
	// 2020-12 meta-schemas refuse, a $schema other than 2020-12, or a
	// $vocabulary.
	illFormed string
}

// complete reports whether the graph is available and well-formed as far as
// a static walk can tell: it reaches nothing outside the document but a
// built-in meta-schema, it can be evaluated, and every schema in it is
// well-formed.
func (g schemaGraph) complete() bool {
	return g.external == "" && g.unresolved == "" && g.illFormed == ""
}

// problem states why the graph is not complete.
func (g schemaGraph) problem() string {
	switch {
	case g.external != "":
		return fmt.Sprintf("the schema graph reaches %s, which the document does not embed", g.external)
	case g.unresolved != "":
		return "the schema graph cannot be evaluated: " + g.unresolved
	case g.illFormed != "":
		return "the schema graph is not well-formed: " + g.illFormed
	}
	return ""
}

// inPlaceKeywords apply their subschemas to the value being evaluated
// itself, rather than to a member or item of it. A cycle through these and
// references alone never advances into the value.
var inPlaceKeywords = map[string]bool{
	"allOf": true, "anyOf": true, "oneOf": true, "not": true,
	"if": true, "then": true, "else": true, "dependentSchemas": true,
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
	illFormed := func(format string, args ...any) {
		if graph.illFormed == "" {
			graph.illFormed = fmt.Sprintf(format, args...)
		}
	}
	// inPlace holds, for every schema object walked, the schemas it applies
	// to the same value: in-place applicators and reference targets.
	inPlace := map[unsafe.Pointer][]unsafe.Pointer{}
	walked := map[unsafe.Pointer]bool{}
	followed := map[string][]any{}

	var walk func(node any, base *url.URL)
	// enter walks a schema the graph reaches as a whole, the schema it starts
	// from or a reference's target, after checking it and every subschema in
	// it well-formed (§5.2).
	enter := func(node any, base *url.URL) {
		switch node.(type) {
		case bool:
			return
		case map[string]any:
			if err := compiledMetaSchema.Validate(node); err != nil {
				if problems, mismatch := schemacompiler.Outcome(err); mismatch {
					illFormed("a schema in it does not validate against the 2020-12 meta-schemas: %s", problems[0].Line())
				} else {
					unresolved("a schema in it could not be checked against the meta-schemas: %v", err)
				}
			}
			checkDialect(node, illFormed)
			walk(node, base)
		default:
			illFormed("a reference reaches a %s, which is not a schema", jsonTypeName(node))
		}
	}
	// follow resolves a reference against base and walks what it reaches,
	// returning the schemas it can land on.
	follow := func(ref string, base *url.URL, dynamic bool) []any {
		parsed, err := url.Parse(ref)
		if err != nil {
			unresolved("%q is not a URI reference", ref)
			return nil
		}
		if base == nil && strings.HasPrefix(ref, "#") {
			// A same-document fragment resolves from the document root. URI
			// semantics decode it before it is read as a JSON Pointer (RFC
			// 6901 §6); a percent-encoded one is OBI-D-05's violation, not an
			// unresolvable reference.
			key := "#" + parsed.Fragment
			if targets, seen := followed[key]; seen {
				return targets
			}
			followed[key] = nil
			refTokens, ok := jsonpointer.Parse(parsed.Fragment)
			if !ok {
				unresolved("%q is not a JSON Pointer fragment", ref)
				return nil
			}
			target, targetBase, ok := descend(view, nil, atDocument, refTokens)
			if !ok {
				unresolved("%q does not resolve within the document", ref)
				return nil
			}
			followed[key] = []any{target}
			enter(target, targetBase)
			return followed[key]
		}
		if base != nil {
			parsed = base.ResolveReference(parsed)
		}
		if !parsed.IsAbs() {
			unresolved("%q has no base to resolve against", ref)
			return nil
		}
		fragment := parsed.Fragment
		parsed.Fragment, parsed.RawFragment = "", ""
		id := parsed.String()
		key := id + "#" + fragment
		if targets, seen := followed[key]; seen {
			return targets
		}
		followed[key] = nil
		var targets []any
		switch resource, embedded := schemas.resources[id]; {
		case schemas.ambiguous[id] != "":
			unresolved("%s names no one embedded schema: %s", id, schemas.ambiguous[id])
		case schemas.shadowed[id]:
			unresolved("the schema backend resolves %s to the meta-schema it carries, so it cannot evaluate the schema the document embeds as it", id)
		case !embedded && isBuiltInMetaSchema(id):
			if graph.builtIn == "" {
				graph.builtIn = id
			}
		case !embedded:
			if graph.external == "" {
				graph.external = id
			}
		default:
			switch target, targetBase, resolution := resolveInResource(resource, parsed, fragment); resolution {
			case resolved:
				targets = append(targets, target)
				enter(target, targetBase)
			case ambiguousAnchor:
				unresolved("%q names an anchor more than one schema in %s declares", ref, id)
			default:
				unresolved("%q does not resolve within the resource %s", ref, id)
			}
		}
		if dynamic && fragment != "" && !strings.HasPrefix(fragment, "/") {
			// A $dynamicRef can land on any schema in the dynamic scope that
			// declares its anchor dynamically.
			for _, site := range schemas.dynamicAnchors[fragment] {
				targets = append(targets, site.schema)
				enter(site.schema, site.parent)
			}
		}
		followed[key] = targets
		return targets
	}
	walk = func(node any, base *url.URL) {
		object, ok := node.(map[string]any)
		if !ok {
			return
		}
		self := objectIdentity(object)
		if walked[self] {
			return
		}
		walked[self] = true
		if raw, isString := object["$id"].(string); isString {
			id, declared := resourceID(object, base)
			switch {
			case !declared:
				unresolved("$id %q resolves to no absolute URI", raw)
			case schemas.ambiguous[id.String()] != "":
				unresolved("%s names no one embedded schema: %s", id, schemas.ambiguous[id.String()])
			default:
				base = id
			}
		}
		for _, keyword := range []string{"$ref", "$dynamicRef"} {
			if ref, ok := object[keyword].(string); ok {
				for _, target := range follow(ref, base, keyword == "$dynamicRef") {
					if targetObject, ok := target.(map[string]any); ok {
						inPlace[self] = append(inPlace[self], objectIdentity(targetObject))
					}
				}
			}
		}
		forEachAppliedSubschema(object, func(child any, tokens ...string) {
			if childObject, ok := child.(map[string]any); ok && inPlaceKeywords[tokens[0]] {
				inPlace[self] = append(inPlace[self], objectIdentity(childObject))
			}
			walk(child, base)
		})
	}
	start, base, ok := descend(view, nil, atDocument, tokens)
	if !ok {
		unresolved("%q is not a location in the document", jsonpointer.Format(tokens...))
		return graph
	}
	enter(start, base)
	if hasCycle(inPlace) {
		unresolved("a cycle of references never advances into the value, so no value can be evaluated against it")
	}
	return graph
}

// checkDialect reports a schema, or any subschema of it, that §5.2's dialect
// constraints exclude: a $schema other than 2020-12, or a $vocabulary.
func checkDialect(node any, illFormed func(format string, args ...any)) {
	object, ok := node.(map[string]any)
	if !ok {
		return
	}
	if dialect, present := object["$schema"]; present && dialect != draft202012URI {
		illFormed("a schema declares $schema %s, not %s", describeJSON(dialect), draft202012URI)
	}
	if _, present := object["$vocabulary"]; present {
		illFormed("a schema declares $vocabulary")
	}
	forEachSubschema(object, func(child any, _ ...string) {
		checkDialect(child, illFormed)
	})
}

// hasCycle reports whether a directed graph has a cycle.
func hasCycle(edges map[unsafe.Pointer][]unsafe.Pointer) bool {
	const (
		unvisited = iota
		active
		done
	)
	state := map[unsafe.Pointer]int{}
	var visit func(node unsafe.Pointer) bool
	visit = func(node unsafe.Pointer) bool {
		switch state[node] {
		case active:
			return true
		case done:
			return false
		}
		state[node] = active
		for _, next := range edges[node] {
			if visit(next) {
				return true
			}
		}
		state[node] = done
		return false
	}
	for node := range edges {
		if visit(node) {
			return true
		}
	}
	return false
}

// objectIdentity is the identity of a decoded JSON object: two references
// reach the same schema exactly when they reach the same map.
func objectIdentity(object map[string]any) unsafe.Pointer {
	return reflect.ValueOf(object).UnsafePointer()
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

// resolution is what a fragment resolves to within a resource.
type resolution int

const (
	resolved resolution = iota
	missing
	ambiguousAnchor
)

// resolveInResource resolves a fragment within an embedded resource whose
// absolute URI is id: the resource itself, a JSON Pointer from it, or a
// plain-name anchor it declares. It returns the schema reached and the base
// in effect where it sits, before that schema's own $id, which the walk
// applies.
func resolveInResource(resource schemaResource, id *url.URL, fragment string) (any, *url.URL, resolution) {
	switch {
	case fragment == "":
		return resource.schema, resource.parent, resolved
	case strings.HasPrefix(fragment, "/"):
		tokens, ok := jsonpointer.Parse(fragment)
		if !ok {
			return nil, nil, missing
		}
		target, base, ok := descend(resource.schema, id, atSchema, tokens)
		if !ok {
			return nil, nil, missing
		}
		if object, isObject := target.(map[string]any); isObject && objectIdentity(object) == objectIdentity(resource.schema) {
			base = resource.parent
		}
		return target, base, resolved
	default:
		found := findAnchor(resource.schema, fragment)
		switch {
		case len(found) == 0:
			return nil, nil, missing
		case len(found) > 1:
			return nil, nil, ambiguousAnchor
		case objectIdentity(found[0]) == objectIdentity(resource.schema):
			return found[0], resource.parent, resolved
		default:
			return found[0], id, resolved
		}
	}
}

// findAnchor returns every schema in a resource that declares a plain-name
// anchor, by $anchor or $dynamicAnchor. The search does not enter a nested
// resource, whose anchors are its own.
func findAnchor(resource map[string]any, name string) []map[string]any {
	var found []map[string]any
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
	return found
}
