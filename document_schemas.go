package openbindings

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/openbindings/openbindings-go/internal/jsonpointer"
	"github.com/openbindings/openbindings-go/internal/schemacompiler"
)

// documentSchemas is what an OBI document holds as schema resources (§5.2,
// §7): every schema at an OBI schema position, or a subschema of one, that
// declares its own $id. The rest of the document is not a schema, whatever
// its members are named, and declares no resource.
type documentSchemas struct {
	// resources maps the absolute URI of every embedded schema resource,
	// without fragment, to where it is.
	resources map[string]schemaResource
	// ambiguous maps an absolute URI that more than one schema declares as its
	// $id to why it names no one resource. Such a URI is in no graph's reach.
	ambiguous map[string]string
	// shadowed holds embedded resources whose $id is a JSON Schema
	// meta-schema's URI. The document embeds them (§7), but the schema
	// library resolves that URI to the meta-schema it carries, so it cannot
	// evaluate them.
	shadowed map[string]bool
}

type schemaResource struct {
	location string
	schema   map[string]any
	// parent is the base in effect where the resource sits, against which its
	// own $id resolves; nil at an OBI schema position.
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

// resolution is what a fragment resolves to within a resource.
type resolution int

const (
	resolved resolution = iota
	missing
	ambiguousAnchor
)

// resolveInResource resolves a fragment within an embedded resource: the
// resource itself, a JSON Pointer from it, or a plain-name anchor it declares
// (OBI-D-16).
func resolveInResource(resource schemaResource, fragment string) resolution {
	switch {
	case fragment == "":
		return resolved
	case strings.HasPrefix(fragment, "/"):
		tokens, ok := jsonpointer.Parse(fragment)
		if !ok {
			return missing
		}
		if _, ok := jsonpointer.Resolve(resource.schema, jsonpointer.Format(tokens...)); !ok {
			return missing
		}
		return resolved
	default:
		switch found := findAnchor(resource.schema, fragment); {
		case len(found) == 0:
			return missing
		case len(found) > 1:
			return ambiguousAnchor
		default:
			return resolved
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
