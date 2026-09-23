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
	// anchors maps each plain-name anchor the resource declares, by $anchor
	// or $dynamicAnchor, to the location of every schema declaring it, as a
	// JSON Pointer from the resource, in document order. A nested resource's
	// anchors are its own.
	anchors map[string][]string
}

// collectDocumentSchemas walks the schema positions of a document's generic
// view and every subschema of them.
func collectDocumentSchemas(view any) documentSchemas {
	d := documentSchemas{resources: map[string]schemaResource{}}
	claims := map[string][]string{}
	var path []string
	// anchors indexes the anchors of the resource the walk is in, whose root
	// is at path[:anchorsFrom]; nil outside a resource, or within a nested
	// $id that declares none (its anchors are not the outer resource's).
	var walk func(node any, base *url.URL, anchors map[string][]string, anchorsFrom int)
	walk = func(node any, base *url.URL, anchors map[string][]string, anchorsFrom int) {
		object, ok := node.(map[string]any)
		if !ok {
			return
		}
		if id, declared := resourceID(object, base); declared {
			location := jsonpointer.Format(path...)
			key := id.String()
			claims[key] = append(claims[key], location)
			anchors, anchorsFrom = map[string][]string{}, len(path)
			d.resources[key] = schemaResource{location: location, schema: object, parent: base, anchors: anchors}
			base = id
		} else if _, nested := object["$id"].(string); nested {
			anchors = nil
		}
		if anchors != nil {
			for _, name := range anchorNames(object) {
				anchors[name] = append(anchors[name], jsonpointer.Format(path[anchorsFrom:]...))
			}
		}
		forEachSubschema(object, func(child any, tokens ...string) {
			path = append(path, tokens...)
			walk(child, base, anchors, anchorsFrom)
			path = path[:len(path)-len(tokens)]
		})
	}

	root, _ := view.(map[string]any)
	schemas, _ := root["schemas"].(map[string]any)
	for _, key := range sortedKeys(schemas) {
		path = []string{"schemas", key}
		walk(schemas[key], nil, nil, 0)
	}
	operations, _ := root["operations"].(map[string]any)
	for _, key := range sortedKeys(operations) {
		operation, _ := operations[key].(map[string]any)
		for _, position := range []string{"input", "output"} {
			if schema, present := operation[position]; present {
				path = []string{"operations", key, position}
				walk(schema, nil, nil, 0)
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

// anchorNames returns the plain-name anchors a schema object declares, by
// $anchor or $dynamicAnchor, each once.
func anchorNames(object map[string]any) []string {
	var names []string
	for _, keyword := range []string{"$anchor", "$dynamicAnchor"} {
		if name, ok := object[keyword].(string); ok && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return names
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
	parsed = resolveURI(base, parsed)
	if !parsed.IsAbs() {
		return nil, false
	}
	parsed.Fragment, parsed.RawFragment = "", ""
	return parsed, true
}

// resolveURI resolves ref against base as RFC 3986 §5.2 does, and as the
// schema library does for every $id and $ref: dot segments are removed even
// from an absolute reference, so https://example.com/x/../a names
// https://example.com/a. base may be nil when ref is absolute. A relative
// reference against an opaque base (a urn:, say) keeps the base's opaque
// part, as the library keeps it.
func resolveURI(base, ref *url.URL) *url.URL {
	if base == nil {
		base = &url.URL{}
	}
	resolved := base.ResolveReference(ref)
	if !ref.IsAbs() && base.Opaque != "" {
		resolved.Opaque = base.Opaque
	}
	return resolved
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
		switch found := resource.anchors[fragment]; {
		case len(found) == 0:
			return missing
		case len(found) > 1:
			return ambiguousAnchor
		default:
			return resolved
		}
	}
}
