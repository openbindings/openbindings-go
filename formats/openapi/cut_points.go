package openapi

import (
	"sort"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// Which nodes become cut points.
//
// A cyclic schema is emitted using the dialect's own recursion mechanism: a
// cycle participant is hoisted into the operation schema's `$defs` and every
// occurrence becomes a same-document `$ref` to it (OBI-D-16). WHICH nodes get
// hoisted is synthesis surface, exactly as their `$defs` keys are:
// The registered OpenAPI family siblings' §10 places deterministic generation of OBI documents
// from OpenAPI artifacts — "operation-key derivation, output-schema selection,
// schema translation" — outside the specification, and the binding-specification
// authoring doctrine states that a family specification does not define a
// synthesis naming convention. Nothing in Core, in the OpenAPI specification, or
// in anything either incorporates decides it. It is therefore this
// implementation's convention, and its hard obligation is that the Go and
// TypeScript engines hoist the same nodes and reference them by the same keys.
//
// The convention, twinned in openapi-client/typescript/src/util.ts
// (decycleSchema):
//
//   - The question is asked about the graph the engine is ABOUT TO EMIT: per
//     operation direction, after read/write projection. A cycle that projection
//     removes is not a cycle there, and its participants are ordinary shared
//     subschemas that inline. This is what makes the answer a property of the
//     emitted contract rather than of the source document's component table.
//
//   - A cut point is a node the artifact ADDRESSES — the target of a `$ref` —
//     that participates in a reference cycle in that graph. Every such node is
//     hoisted; nothing else is. Being declared under `components.schemas` is not
//     part of the question: a cycle closing through
//     `#/components/schemas/Wrapper/properties/inner` is a cycle, and leaving it
//     as a literal artifact-space `$ref` would emit a schema whose reference
//     does not resolve in the emitted document.
//
//   - Every participant is hoisted, not a minimum feedback set. Choosing one
//     member of a cycle to cut at would need a preference among equals that no
//     authority supplies and the artifact does not express; hoisting all of them
//     is a function of the cycle alone.
//
// Go reaches that graph through the reference registry, which is the form its
// pipeline carries schemas in; TypeScript reaches it through the dereferenced
// object graph. The two representations answer the same question: a cycle in
// the object graph exists only where a reference was substituted, so its
// reference-target members are exactly this registry graph's cycle members.

// refGraphCycles returns the refs that can reach themselves in `registry`.
//
// The node set is closed over reference targets rather than limited to registry
// roots: `#/components/schemas/Envelope/properties/id` is a schema position the
// artifact addresses, and a cycle through it is a cycle. resolveRegistryRef
// resolves both forms.
func refGraphCycles(registry map[string]any) map[string]bool {
	successors := make(map[string][]string, len(registry))

	var collect func(node any, position schemaTraversalPosition, out *[]string)
	collect = func(node any, position schemaTraversalPosition, out *[]string) {
		if position == schemaDataPosition {
			return
		}
		switch v := node.(type) {
		case map[string]any:
			if position == schemaObjectPosition {
				if ref, ok := v["$ref"].(string); ok {
					*out = append(*out, ref)
				}
			}
			keys := make([]string, 0, len(v))
			for k := range v {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				childPosition := schemaDataPosition
				switch position {
				case schemaObjectPosition:
					childPosition = schemaChildPosition(k)
				case schemaMapPosition:
					childPosition = schemaObjectPosition
				}
				collect(v[k], childPosition, out)
			}
		case []any:
			childPosition := schemaDataPosition
			if position == schemaArrayPosition {
				childPosition = schemaObjectPosition
			}
			for _, item := range v {
				collect(item, childPosition, out)
			}
		}
	}

	pending := make([]string, 0, len(registry))
	for ref := range registry {
		pending = append(pending, ref)
	}
	for len(pending) > 0 {
		ref := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if _, done := successors[ref]; done {
			continue
		}
		node, found := resolveRegistryRef(ref, registry)
		if !found {
			// An address this registry cannot resolve reaches nothing; it is a
			// dead end in the graph, not an error to raise here.
			successors[ref] = nil
			continue
		}
		var out []string
		collect(node, schemaObjectPosition, &out)
		successors[ref] = out
		pending = append(pending, out...)
	}

	cyclic := make(map[string]bool)
	for ref := range successors {
		visited := map[string]bool{}
		work := append([]string(nil), successors[ref]...)
		for len(work) > 0 {
			current := work[len(work)-1]
			work = work[:len(work)-1]
			if current == ref {
				cyclic[ref] = true
				break
			}
			if visited[current] {
				continue
			}
			visited[current] = true
			work = append(work, successors[current]...)
		}
	}
	return cyclic
}

// directionGraph is the reference graph for one operation direction: the
// registry as that direction will emit it, and the cut points that graph needs.
type directionGraph struct {
	registry map[string]any
	cyclic   map[string]bool
	// readdressed names the component roots whose declaration of a cut point
	// below them was rewritten into an explicit reference; declaredForm serves
	// that rewritten form so the declaration site emits a reference rather than
	// one inline copy of a definition that is also in `$defs`.
	readdressed map[string]bool
}

// declaredForm materializes a referenced schema for an operation-root builder.
//
// The builders resolve a reference eagerly, which is right for an ordinary
// shared component — the emitted contract inlines it — but wrong for a node the
// decycler is about to hoist: the reference is the only record that this
// position and the `$defs` entry are the same node, and resolving it emits one
// inline copy of a definition that is also in `$defs`. Where the target is a cut
// point, or a component this direction readdressed a cut point inside, the
// reference is preserved and inlineRefs decides its emitted form. Everything
// else keeps the marshaled form.
func (g *directionGraph) declaredForm(ref *openapi3.SchemaRef, schemaOverlays *rawSchemaOverlayCollector) map[string]any {
	if g.hoists(ref) {
		return map[string]any{"$ref": ref.Ref}
	}
	return g.rootForm(ref, schemaOverlays)
}

// rootForm is declaredForm at a position that is the emitted schema's own ROOT,
// where a cut point is not hoisted in either engine: a root consisting of
// nothing but a reference into its own `$defs` states the contract less
// directly than the definition itself, and the TypeScript decycler never hoists
// the node it was handed. Readdressing still applies, so a cut point DECLARED
// inside that component is a reference from here too.
func (g *directionGraph) rootForm(ref *openapi3.SchemaRef, schemaOverlays *rawSchemaOverlayCollector) map[string]any {
	if g != nil && ref != nil && ref.Ref != "" && g.readdressed[ref.Ref] {
		if node, ok := g.registry[ref.Ref].(map[string]any); ok {
			out := make(map[string]any, len(node))
			for key, value := range node {
				out[key] = value
			}
			return out
		}
	}
	return schemaRefToMap(ref, schemaOverlays)
}

// hoists reports whether this direction will hoist the referenced node into
// `$defs`, so an operation-root builder must keep the artifact's reference to it
// rather than resolving it.
func (g *directionGraph) hoists(ref *openapi3.SchemaRef) bool {
	if g == nil || ref == nil || ref.Ref == "" || !g.cyclic[ref.Ref] {
		return false
	}
	_, found := resolveRegistryRef(ref.Ref, g.registry)
	return found
}

// newDirectionGraphs projects the reference registry once per direction and
// computes each direction's cut points from its own projected graph.
//
// readOnly and writeOnly annotations are preserved in both directions, so the
// two projections share one registry and the cycle computation runs once.
func newDirectionGraphs(registry map[string]any) (request directionGraph, response directionGraph) {
	requestRegistry, requestOmitted := projectRefRegistry(registry, openAPIRequestSchema)
	responseRegistry, responseOmitted := projectRefRegistry(registry, openAPIResponseSchema)
	if !requestOmitted && !responseOmitted {
		// The two projections are the same graph, so one cycle computation answers
		// for both.
		graph := newDirectionGraph(requestRegistry)
		return graph, graph
	}
	return newDirectionGraph(requestRegistry), newDirectionGraph(responseRegistry)
}

func newDirectionGraph(registry map[string]any) directionGraph {
	cyclic := refGraphCycles(registry)
	addressed, readdressed := addressCutPointDeclarations(registry, cyclic)
	return directionGraph{registry: addressed, cyclic: cyclic, readdressed: readdressed}
}

// projectRefRegistry clones every registry entry at the directional synthesis
// boundary. readOnly and writeOnly are annotations, not deletion instructions,
// so the second result remains false and both directions share the same graph.
func projectRefRegistry(registry map[string]any, direction openAPISchemaDirection) (map[string]any, bool) {
	projected := make(map[string]any, len(registry))
	omitted := false
	for ref, node := range registry {
		schema, ok := node.(map[string]any)
		if !ok {
			projected[ref] = node
			continue
		}
		_ = direction
		projector := openAPISchemaProjector{}
		out, ok := projector.project(schema, false).(map[string]any)
		if !ok {
			projected[ref] = node
			continue
		}
		projected[ref] = out
	}
	return projected, omitted
}

// addressCutPointDeclarations registers each cut point that sits BELOW a
// component root as a registry entry of its own, and replaces its declaration
// site with an explicit reference to it.
//
// The decycler emits a `$ref` to a hoisted definition wherever it meets a
// reference to that definition. A cut point at a position such as
// `#/components/schemas/Wrapper/properties/inner` is written inline by the
// artifact and reached by reference only on the back edge, so without this the
// declaration site would emit one inline copy of a definition that is also in
// `$defs`. The TypeScript engine has no such asymmetry — a dereferenced graph
// has one node per position, reached the same way from everywhere — and this
// makes the reference form explicit so both engines emit the same shape.
//
// The registry is left untouched when no cut point sits below a root, which is
// every document in both corpora.
func addressCutPointDeclarations(registry map[string]any, cyclic map[string]bool) (map[string]any, map[string]bool) {
	below := make([]string, 0)
	for ref := range cyclic {
		if _, isRoot := registry[ref]; !isRoot {
			below = append(below, ref)
		}
	}
	if len(below) == 0 {
		return registry, nil
	}
	sort.Strings(below)

	readdressed := map[string]bool{}
	out := make(map[string]any, len(registry)+len(below))
	for ref, node := range registry {
		out[ref] = node
	}
	// Capture every cut point's own declaration from the UNMODIFIED registry
	// first: one cut point may be declared inside another.
	for _, ref := range below {
		if node, found := resolveRegistryRef(ref, registry); found {
			out[ref] = node
		}
	}
	for _, ref := range below {
		if _, captured := out[ref]; !captured {
			continue
		}
		owner, path := longestRegisteredPrefix(out, ref)
		if owner == "" || len(path) == 0 {
			continue
		}
		if replaced, ok := replaceAtPointerPath(out[owner], path, map[string]any{"$ref": ref}); ok {
			out[owner] = replaced
			readdressed[owner] = true
		}
	}
	return out, readdressed
}

// longestRegisteredPrefix splits a reference into the longest prefix that is
// itself a registry entry and the remaining RFC 6901 tokens below it.
func longestRegisteredPrefix(registry map[string]any, ref string) (string, []string) {
	segments := strings.Split(ref, "/")
	for i := len(segments) - 1; i >= 1; i-- {
		prefix := strings.Join(segments[:i], "/")
		if _, found := registry[prefix]; found {
			tokens := make([]string, 0, len(segments)-i)
			for _, encoded := range segments[i:] {
				tokens = append(tokens, unescapeJSONPointerSegment(encoded))
			}
			return prefix, tokens
		}
	}
	return "", nil
}

// replaceAtPointerPath returns a copy of `node` with the value at `path`
// replaced. Only the containers along the path are copied; every sibling stays
// shared with the caller's registry.
func replaceAtPointerPath(node any, path []string, value any) (any, bool) {
	if len(path) == 0 {
		return value, true
	}
	switch container := node.(type) {
	case map[string]any:
		child, found := container[path[0]]
		if !found {
			return node, false
		}
		replaced, ok := replaceAtPointerPath(child, path[1:], value)
		if !ok {
			return node, false
		}
		out := make(map[string]any, len(container))
		for k, v := range container {
			out[k] = v
		}
		out[path[0]] = replaced
		return out, true
	case []any:
		index, err := strconv.Atoi(path[0])
		if err != nil || index < 0 || index >= len(container) {
			return node, false
		}
		replaced, ok := replaceAtPointerPath(container[index], path[1:], value)
		if !ok {
			return node, false
		}
		out := make([]any, len(container))
		copy(out, container)
		out[index] = replaced
		return out, true
	default:
		return node, false
	}
}

// registryOrNil is the graph's registry, or nil for a nil graph. Callers that
// only need reference resolution accept both.
func (g *directionGraph) registryOrNil() map[string]any {
	if g == nil {
		return nil
	}
	return g.registry
}

func (g *directionGraph) isCyclic(ref string) bool {
	return g != nil && g.cyclic[ref]
}

// parameterForm is declaredForm for a parameter's schema. A Parameter Object
// description is merged into the emitted parameter schema, and the merged
// schema is a different schema from the component it was merged from — so it is
// not the component's cut point, and the reference is resolved. The component
// still cuts wherever it is referenced inside.
func (g *directionGraph) parameterForm(ref *openapi3.SchemaRef, merged bool, schemaOverlays *rawSchemaOverlayCollector) map[string]any {
	if merged {
		return g.rootForm(ref, schemaOverlays)
	}
	return g.declaredForm(ref, schemaOverlays)
}
