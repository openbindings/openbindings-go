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
// §7): every schema at a schema position that declares its own $id. A schema
// position is an OBI schema position (an operation's input or output, or an
// entry of schemas) or a subschema of one, as JSON Schema 2020-12 evaluates
// subschemas (forEachSubschema). The rest of the document is not a schema,
// whatever its members are named, and declares no resource.
type documentSchemas struct {
	// at maps the location of every resource to it.
	at map[string]*schemaResource
	// resources maps the absolute URI of every resource that one schema alone
	// declares, without fragment, to it.
	resources map[string]*schemaResource
	// ambiguous maps an absolute URI that more than one schema declares as its
	// $id to why it names no one resource, and claimants to those schemas.
	ambiguous map[string]string
	claimants map[string][]*schemaResource
	// documentDynamicAnchor is true when a schema outside every resource
	// declares $dynamicAnchor, which OBI-D-05 excludes.
	documentDynamicAnchor bool
}

type schemaResource struct {
	location string
	schema   map[string]any
	// uri is the absolute URI the resource's $id resolves to, without
	// fragment; nil when it resolves to none, as a relative $id at an OBI
	// position does. Such a resource is still a boundary: what lies within it
	// is its own business (§7), and it can be addressed only from within.
	uri *url.URL
	// anchors maps each plain-name anchor the resource declares, by $anchor or
	// $dynamicAnchor, to where every schema declaring it sits, in document
	// order, spelled out only when used. A nested resource's anchors are its
	// own.
	anchors map[string][]*pathNode
}

// collectDocumentSchemas walks the schema positions of a document's generic
// view.
func collectDocumentSchemas(view any) documentSchemas {
	d := documentSchemas{at: map[string]*schemaResource{}, resources: map[string]*schemaResource{}}
	claimants := map[string][]*schemaResource{}
	var walk func(node any, at *pathNode, resource *schemaResource)
	walk = func(node any, at *pathNode, resource *schemaResource) {
		object, ok := node.(map[string]any)
		if !ok {
			return
		}
		if raw, declares := declaredID(object); declares {
			var base *url.URL
			if resource != nil {
				base = resource.uri
			}
			location := at.from(nil)
			resource = &schemaResource{location: location, schema: object, uri: resolveID(raw, base), anchors: map[string][]*pathNode{}}
			d.at[location] = resource
			if resource.uri != nil {
				key := resource.uri.String()
				claimants[key] = append(claimants[key], resource)
			}
		}
		names := anchorNames(object)
		switch {
		case resource != nil:
			for _, name := range names {
				resource.anchors[name] = append(resource.anchors[name], at)
			}
		case object["$dynamicAnchor"] != nil:
			d.documentDynamicAnchor = true
		}
		forEachSubschema(object, func(child any, tokens ...string) {
			walk(child, at.child(tokens...), resource)
		})
	}

	root, _ := view.(map[string]any)
	schemas, _ := root["schemas"].(map[string]any)
	for _, key := range sortedKeys(schemas) {
		walk(schemas[key], (*pathNode)(nil).child("schemas", key), nil)
	}
	operations, _ := root["operations"].(map[string]any)
	for _, key := range sortedKeys(operations) {
		operation, _ := operations[key].(map[string]any)
		for _, position := range []string{"input", "output"} {
			if schema, present := operation[position]; present {
				walk(schema, (*pathNode)(nil).child("operations", key, position), nil)
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
			d.ambiguous, d.claimants = map[string]string{}, map[string][]*schemaResource{}
		}
		d.ambiguous[id] = fmt.Sprintf("the schemas at %s all declare it", strings.Join(locations, ", "))
		d.claimants[id] = resources
	}
	return d
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

// anchorNames returns the plain-name anchors a schema object declares, by
// $anchor or $dynamicAnchor, each once: one schema declaring a name both ways
// declares it at one location.
func anchorNames(object map[string]any) []string {
	var names []string
	for _, keyword := range []string{"$anchor", "$dynamicAnchor"} {
		if name, ok := object[keyword].(string); ok && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return names
}

// declaredID returns the $id a schema object declares a resource by, its
// fragment removed: an $id that is empty once its fragment is removed ("" or
// "#") declares nothing, as JSON Schema libraries read it.
func declaredID(object map[string]any) (string, bool) {
	raw, _ := object["$id"].(string)
	id, _, _ := strings.Cut(raw, "#")
	return id, id != ""
}

// resolveID returns the absolute URI, without fragment, that a declared $id
// resolves to against base, or nil when it resolves to none.
func resolveID(raw string, base *url.URL) *url.URL {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	parsed = resolveURI(base, parsed)
	if !parsed.IsAbs() {
		return nil
	}
	parsed.Fragment, parsed.RawFragment = "", ""
	return parsed
}

// resolveURI resolves ref against base by RFC 3986 §5.2. base may be nil when
// ref is absolute. net/url follows the RFC except against a base whose path is
// rootless (urn:x:y), where it would give urn:///b for b; the RFC merges the
// paths, giving urn:b.
func resolveURI(base, ref *url.URL) *url.URL {
	if base == nil {
		base = &url.URL{}
	}
	if base.Opaque == "" || ref.Scheme != "" || ref.Host != "" || ref.User != nil || ref.Path == "" || strings.HasPrefix(ref.Path, "/") {
		return base.ResolveReference(ref)
	}
	directory := base.Opaque[:strings.LastIndexByte(base.Opaque, '/')+1]
	return &url.URL{
		Scheme:      base.Scheme,
		Opaque:      removeDotSegments(directory + ref.Path),
		RawQuery:    ref.RawQuery,
		Fragment:    ref.Fragment,
		RawFragment: ref.RawFragment,
	}
}

// removeDotSegments removes the "." and ".." segments of a path (RFC 3986
// §5.2.4).
func removeDotSegments(path string) string {
	var out []string
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		last := i == len(segments)-1
		switch segment {
		case ".":
			if last {
				out = append(out, "")
			}
		case "..":
			if len(out) > 0 {
				out = out[:len(out)-1]
			}
			if last {
				out = append(out, "")
			}
		default:
			out = append(out, segment)
		}
	}
	return strings.Join(out, "/")
}

// Keyword tables. JSON Schema 2020-12 evaluates the subschemas at these
// positions; definitions and dependencies are the pre-2019 spellings the
// 2020-12 meta-schema still describes, and checks the shape of, but 2020-12
// neither evaluates them nor finds resources or anchors in them.

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
