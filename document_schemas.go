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
	// claimants holds, for each ambiguous URI, every resource declaring it.
	claimants map[string][]schemaResource
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
	// node is where the resource sits, and anchors maps each plain-name
	// anchor the resource declares, by $anchor or $dynamicAnchor, to where
	// every schema declaring it sits, in document order; anchorLocation
	// spells one out. A nested resource's anchors are its own.
	node    *pathNode
	anchors map[string][]*pathNode
}

// anchorLocation returns where a schema declaring one of the resource's
// anchors sits, as a JSON Pointer from the resource.
func (r schemaResource) anchorLocation(anchor *pathNode) string {
	return anchor.from(r.node)
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

// collectDocumentSchemas walks the schema positions of a document's generic
// view and every subschema of them.
func collectDocumentSchemas(view any) documentSchemas {
	d := documentSchemas{resources: map[string]schemaResource{}}
	claimants := map[string][]schemaResource{}
	// anchors indexes the anchors of the resource the walk is in, whose root
	// is at the node from; nil outside a resource, or within a nested $id
	// that declares none (its anchors are not the outer resource's).
	var walk func(node any, at *pathNode, base *url.URL, anchors map[string][]*pathNode)
	walk = func(node any, at *pathNode, base *url.URL, anchors map[string][]*pathNode) {
		object, ok := node.(map[string]any)
		if !ok {
			return
		}
		if id, declared := resourceID(object, base); declared {
			key := id.String()
			anchors = map[string][]*pathNode{}
			resource := schemaResource{location: at.from(nil), schema: object, parent: base, node: at, anchors: anchors}
			claimants[key] = append(claimants[key], resource)
			d.resources[key] = resource
			base = id
		} else if _, declares := declaredID(object); declares {
			anchors = nil
		}
		if anchors != nil {
			for _, name := range anchorNames(object) {
				anchors[name] = append(anchors[name], at)
			}
		}
		forEachSubschema(object, func(child any, tokens ...string) {
			walk(child, at.child(tokens...), base, anchors)
		})
	}

	root, _ := view.(map[string]any)
	schemas, _ := root["schemas"].(map[string]any)
	for _, key := range sortedKeys(schemas) {
		walk(schemas[key], (*pathNode)(nil).child("schemas", key), nil, nil)
	}
	operations, _ := root["operations"].(map[string]any)
	for _, key := range sortedKeys(operations) {
		operation, _ := operations[key].(map[string]any)
		for _, position := range []string{"input", "output"} {
			if schema, present := operation[position]; present {
				walk(schema, (*pathNode)(nil).child("operations", key, position), nil, nil)
			}
		}
	}
	for id, resources := range claimants {
		switch {
		case len(resources) > 1:
			var locations []string
			for _, resource := range resources {
				locations = append(locations, resource.location)
			}
			sort.Strings(locations)
			if d.ambiguous == nil {
				d.ambiguous, d.claimants = map[string]string{}, map[string][]schemaResource{}
			}
			d.ambiguous[id] = fmt.Sprintf("the schemas at %s all declare it", strings.Join(locations, ", "))
			d.claimants[id] = resources
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
	raw, ok := declaredID(object)
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

// declaredID returns the $id a schema object declares a resource by, its
// fragment removed, as the schema library reads it: an $id that is empty
// once its fragment is removed ("" or "#") declares nothing.
func declaredID(object map[string]any) (string, bool) {
	raw, _ := object["$id"].(string)
	id, _, _ := strings.Cut(raw, "#")
	return id, id != ""
}

// resolveURI resolves ref against base as the schema library does for every
// $id and $ref, which follows RFC 3986 §5.2: dot segments are removed even
// from an absolute reference, so https://example.com/x/../a names
// https://example.com/a. base may be nil when ref is absolute. One exception
// departs from RFC 3986: a relative reference against an opaque base (a
// urn:, say) keeps the base's opaque part, as the library keeps it, where
// RFC 3986 would replace it.
func resolveURI(base, ref *url.URL) *url.URL {
	if base == nil {
		base = &url.URL{}
	}
	target := base.ResolveReference(ref)
	if !ref.IsAbs() && base.Opaque != "" {
		target.Opaque = base.Opaque
	}
	return target
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

// schemaDepth returns how deeply a schema nests subschemas, as JSON Schema
// 2020-12 defines them: 0 for a schema with none. Values that are not
// subschemas, such as const, enum, default, and examples, do not count.
func schemaDepth(schema any) int {
	object, ok := schema.(map[string]any)
	if !ok {
		return 0
	}
	deepest := 0
	forEachSubschema(object, func(child any, _ ...string) {
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
