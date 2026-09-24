package openbindings

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/openbindings/openbindings-go/internal/jsonpointer"
)

// schemaDepthLimit bounds how deeply a schema handed to the schema library
// nests subschemas (see schemaDepth). The library's meta-schema checks grow
// faster than linearly with that depth (seconds at a few thousand levels),
// and schemas never nest near this deep, so a deeper one is a resource limit
// met, not evidence about the schema (§10.5).
const schemaDepthLimit = 256

// operationSchemas is one document's operation schemas, as core evaluates
// them: what each reaches (schemaGraph), and the bundle the schema library is
// given to evaluate them (schema_bundle.go).
type operationSchemas struct {
	view    any
	schemas documentSchemas
	// resourceObjects holds the objects that declare the document's
	// resources (objectID).
	resourceObjects map[uintptr]bool
	// resourcesIn holds the resources each OBI schema position holds, and
	// limits the resource limit each copy meets, once found.
	resourcesIn map[string][]*schemaResource
	limits      map[string]string
	graph       schemaGraph
	copies      copyGraph
}

func newOperationSchemas(view any, schemas documentSchemas) *operationSchemas {
	o := &operationSchemas{view: view, schemas: schemas, resourceObjects: map[uintptr]bool{}, resourcesIn: map[string][]*schemaResource{}, limits: map[string]string{}}
	for _, location := range slices.Sorted(maps.Keys(schemas.at)) {
		at := copiedAt(location)
		o.resourcesIn[at] = append(o.resourcesIn[at], schemas.at[location])
		o.resourceObjects[objectID(schemas.at[location].schema)] = true
	}
	return o
}

// graphFacts is what a schema graph holds that decides how it is evaluated.
// Each string holds the first of its kind in sorted order, so no answer
// depends on the order of map iteration.
type graphFacts struct {
	// outside is a resource outside the document the graph reaches, and
	// metaSchema a JSON Schema meta-schema the library carries. Either puts
	// the graph's examples outside OBI-D-11.
	outside, metaSchema string
	// problem states why the graph cannot be evaluated.
	problem string
	// dynamicRef is whether the graph holds a $dynamicRef.
	dynamicRef bool
}

func (f *graphFacts) include(g graphFacts) {
	f.outside = firstOf(f.outside, g.outside)
	f.metaSchema = firstOf(f.metaSchema, g.metaSchema)
	f.problem = firstOf(f.problem, g.problem)
	f.dynamicRef = f.dynamicRef || g.dynamicRef
}

// firstOf returns the first of two strings in sorted order, "" counting as
// none.
func firstOf(a, b string) string {
	if a == "" || (b != "" && b < a) {
		return b
	}
	return a
}

// schemaGraph is the graph the operation schemas reach (§5.2), walked once for
// all of them: every position evaluation can apply, whatever an if would
// select, and the targets of their references, transitively. What each graph
// holds is gathered over the strongly connected components, so a schema many
// operations share is examined once.
//
// The walk follows every position 2020-12 evaluates but $defs (a definition
// belongs to a graph only when a reference reaches it, T16-S-04 of the core
// conformance corpus), with then and else counted whether or not an if selects
// them, and contentSchema (§5.2); $ref; and $dynamicRef, to its static target.
// A dynamic reference may land elsewhere at run time, but only on a
// $dynamicAnchor of a resource the graph enters, which the schema library
// compiles whenever it compiles that resource: one reaching outside the
// document makes the compile fail, so the graph then gets no verdict without
// core finding it. $recursiveRef and dependencies are not 2020-12 keywords and
// are not followed.
type schemaGraph struct {
	id    map[string]int
	nodes []schemaNode
	// reached holds, per schema, the facts of its whole graph.
	reached []graphFacts
}

type schemaNode struct {
	location string
	// root is where the schema library compiles the schema from (rootOf):
	// the schema lies at or below it along its keywords.
	root string
	// Where the schema leads, by location until the graph is linked:
	// in place to what applies to the same value, to what applies to property
	// names, and to what applies to a member or item.
	inPlaceTo, propertyNamesTo, advancingTo []string
	// The same, linked: edges is all of them.
	edges, inPlace, propertyNames []int
	dynamicRef                    bool
	local                         graphFacts
}

// inPlaceKeywords apply their subschemas to the value their schema applies
// to, as $ref and $dynamicRef apply their targets.
var inPlaceKeywords = map[string]bool{
	"allOf": true, "anyOf": true, "oneOf": true, "not": true,
	"if": true, "then": true, "else": true, "dependentSchemas": true,
}

// analyze walks the graphs of the schemas at each start and gathers what they
// hold, which facts then gives for each.
func (o *operationSchemas) analyze(starts []string) {
	g := &o.graph
	*g = schemaGraph{id: map[string]int{}}
	type schemaAt struct{ location, root string }
	var queue []schemaAt
	for _, start := range starts {
		queue = append(queue, schemaAt{start, rootOf(start)})
	}
	for len(queue) > 0 {
		next := queue[0]
		queue = queue[1:]
		if _, seen := g.id[next.location]; seen {
			continue
		}
		node := o.examine(next.location, next.root)
		g.id[node.location] = len(g.nodes)
		g.nodes = append(g.nodes, node)
		for _, to := range slices.Concat(node.inPlaceTo, node.propertyNamesTo, node.advancingTo) {
			// A subschema is compiled with the root of the schema holding it;
			// a reference's target, from its own root, unless the root the
			// graph is in reaches it too.
			root := node.root
			if !reachedByKeywords(root, to) {
				root = rootOf(to)
			}
			queue = append(queue, schemaAt{to, root})
		}
	}
	ids := func(locations []string) []int {
		out := make([]int, len(locations))
		for i, at := range locations {
			out[i] = g.id[at]
		}
		return out
	}
	for i := range g.nodes {
		n := &g.nodes[i]
		n.inPlace, n.propertyNames = ids(n.inPlaceTo), ids(n.propertyNamesTo)
		n.edges = slices.Concat(n.inPlace, n.propertyNames, ids(n.advancingTo))
		n.inPlaceTo, n.propertyNamesTo, n.advancingTo = nil, nil, nil
	}

	// A cycle of in-place edges never advances into the value, so no value
	// can be evaluated against it. The schema library would report one under
	// if or not as an ordinary failure, which would be a wrong verdict.
	inPlace, inPlaceCount := components(len(g.nodes), func(i int) []int { return g.nodes[i].inPlace })
	size := make([]int, inPlaceCount)
	for _, c := range inPlace {
		size[c]++
	}
	for i := range g.nodes {
		if size[inPlace[i]] > 1 || slices.Contains(g.nodes[i].inPlace, i) {
			g.nodes[i].local.problem = firstOf(g.nodes[i].local.problem, "a cycle of references never advances into the value, so no value can be evaluated against it")
		}
	}

	// The schema library checks property names without the dynamic scope, so
	// a $dynamicRef that applies to them in place would get a wrong verdict.
	dynamicHolder := gather(inPlace, inPlaceCount, func(i int) []int { return g.nodes[i].inPlace }, func(i int) string {
		if g.nodes[i].dynamicRef {
			return g.nodes[i].location
		}
		return ""
	})
	for i := range g.nodes {
		for _, child := range g.nodes[i].propertyNames {
			if holder := dynamicHolder[inPlace[child]]; holder != "" {
				g.nodes[i].local.problem = firstOf(g.nodes[i].local.problem, fmt.Sprintf("the $dynamicRef at %s applies to property names, which the schema library checks without the dynamic scope", holder))
			}
		}
	}

	// What the bundle copies for a schema must be fit to hand the library
	// (schema_bundle.go).
	o.copies.analyze(o)
	for i := range g.nodes {
		g.nodes[i].local.problem = firstOf(g.nodes[i].local.problem, o.copies.problemOf(g.nodes[i].root))
	}

	all, count := components(len(g.nodes), func(i int) []int { return g.nodes[i].edges })
	byComponent := make([]graphFacts, count)
	members := make([][]int, count)
	for i, c := range all {
		byComponent[c].include(g.nodes[i].local)
		members[c] = append(members[c], i)
	}
	// components numbers a component after every one it reaches, so each
	// gathers from facts already gathered.
	for c := range count {
		for _, i := range members[c] {
			for _, next := range g.nodes[i].edges {
				if all[next] != c {
					byComponent[c].include(byComponent[all[next]])
				}
			}
		}
	}
	g.reached = make([]graphFacts, len(g.nodes))
	for i, c := range all {
		g.reached[i] = byComponent[c]
	}
}

// facts returns what the graph of the schema at start holds; start must have
// been analyzed.
func (o *operationSchemas) facts(start string) graphFacts {
	f := o.graph.reached[o.graph.id[start]]
	if f.dynamicRef && o.schemas.documentDynamicAnchor {
		f.problem = firstOf(f.problem, "the graph holds a $dynamicRef, and a schema outside every resource declares $dynamicAnchor, which OBI-D-05 excludes")
	}
	return f
}

// graphProblem states why the graph of the schema at start cannot be
// evaluated, or returns "".
func (o *operationSchemas) graphProblem(start string) string {
	f := o.facts(start)
	if f.outside != "" {
		return fmt.Sprintf("the schema graph reaches %s, which the document does not embed", f.outside)
	}
	return f.problem
}

// examine reads one schema's own keywords: where it leads, and what it holds.
// root is the root it is compiled from, whose resource limits it meets.
func (o *operationSchemas) examine(at, root string) schemaNode {
	node := schemaNode{location: at, root: root}
	if problem := o.limitProblem(root); problem != "" {
		node.local.problem = problem
		return node
	}
	value, _ := jsonpointer.Resolve(o.view, at)
	object, isObject := value.(map[string]any)
	if !isObject {
		if _, isBoolean := value.(bool); !isBoolean {
			node.local.problem = fmt.Sprintf("the value at %s is not a schema: got %s", at, jsonTypeName(value))
		}
		return node
	}
	if !atSchemaPosition(at) {
		// Decision 3 lets a reference point anywhere, but only schema
		// positions embed resources and anchors (§7).
		if keyword := identityKeyword(object); keyword != "" {
			node.local.problem = fmt.Sprintf("the schema at %s is not at a schema position, and it declares %s, which only a schema position declares; define it in schemas to use it", at, keyword)
		}
	}
	for _, keyword := range []string{"$ref", "$dynamicRef"} {
		ref, isString := object[keyword].(string)
		if !isString {
			continue
		}
		if keyword == "$dynamicRef" {
			node.dynamicRef, node.local.dynamicRef = true, true
		}
		switch target := o.schemas.resolve(ref, at, o.view); target.origin {
		case inDocument:
			node.inPlaceTo = append(node.inPlaceTo, target.location)
		case inMetaSchema:
			node.local.metaSchema = firstOf(node.local.metaSchema, target.uri)
		case outside:
			node.local.outside = firstOf(node.local.outside, target.uri)
		default:
			node.local.problem = firstOf(node.local.problem, fmt.Sprintf("the %s %q at %s %s", keyword, ref, at, target.why))
		}
	}
	forEachSubschema(object, func(_ any, tokens ...string) {
		if tokens[0] == "$defs" {
			return
		}
		child := at + jsonpointer.Format(tokens...)
		switch {
		case inPlaceKeywords[tokens[0]]:
			node.inPlaceTo = append(node.inPlaceTo, child)
		case tokens[0] == "propertyNames":
			node.propertyNamesTo = append(node.propertyNamesTo, child)
		default:
			node.advancingTo = append(node.advancingTo, child)
		}
	})
	return node
}

// components returns the strongly connected component of each of n nodes
// (Tarjan's algorithm, with an explicit stack, since reference chains can be
// long), and how many there are. A component is numbered after every component
// it reaches.
func components(n int, successors func(int) []int) ([]int, int) {
	index, low := make([]int, n), make([]int, n)
	onStack := make([]bool, n)
	component := make([]int, n)
	for i := range index {
		index[i] = -1
	}
	var stack []int
	next, count := 0, 0
	type frame struct{ node, edge int }
	for root := range n {
		if index[root] >= 0 {
			continue
		}
		calls := []frame{{node: root}}
		index[root], low[root] = next, next
		next++
		stack = append(stack, root)
		onStack[root] = true
		for len(calls) > 0 {
			f := &calls[len(calls)-1]
			if edges := successors(f.node); f.edge < len(edges) {
				w := edges[f.edge]
				f.edge++
				switch {
				case index[w] < 0:
					index[w], low[w] = next, next
					next++
					stack = append(stack, w)
					onStack[w] = true
					calls = append(calls, frame{node: w})
				case onStack[w]:
					low[f.node] = min(low[f.node], index[w])
				}
				continue
			}
			v := f.node
			calls = calls[:len(calls)-1]
			if len(calls) > 0 {
				parent := calls[len(calls)-1].node
				low[parent] = min(low[parent], low[v])
			}
			if low[v] == index[v] {
				for {
					w := stack[len(stack)-1]
					stack = stack[:len(stack)-1]
					onStack[w] = false
					component[w] = count
					if w == v {
						break
					}
				}
				count++
			}
		}
	}
	return component, count
}

// gather returns, per component, the first in sorted order of value over the
// nodes reachable from the component's, its own included, "" counting as none.
func gather(component []int, count int, successors func(int) []int, value func(int) string) []string {
	out := make([]string, count)
	members := make([][]int, count)
	for i, c := range component {
		out[c] = firstOf(out[c], value(i))
		members[c] = append(members[c], i)
	}
	for c := range count {
		for _, i := range members[c] {
			for _, next := range successors(i) {
				if component[next] != c {
					out[c] = firstOf(out[c], out[component[next]])
				}
			}
		}
	}
	return out
}

// atSchemaPosition reports whether a location is a schema position: an OBI
// schema position, or reached from one through the positions 2020-12
// evaluates.
func atSchemaPosition(location string) bool {
	tokens, _ := jsonpointer.Parse(location)
	var i int
	switch {
	case len(tokens) >= 2 && tokens[0] == "schemas":
		i = 2
	case len(tokens) >= 3 && tokens[0] == "operations" && (tokens[2] == "input" || tokens[2] == "output"):
		i = 3
	default:
		return false
	}
	return keywordPath(tokens[i:], false)
}

// keywordPath reports whether reference tokens lead from a schema to a
// subschema, each step a keyword holding one schema, or a keyword holding a
// map or an array of them and then an entry. described admits the entries of
// definitions and dependencies, which the 2020-12 meta-schema describes but
// 2020-12 does not evaluate.
func keywordPath(tokens []string, described bool) bool {
	i := 0
	for i < len(tokens) {
		switch keyword := tokens[i]; {
		case schemaMapKeywords[keyword], arraySchemaKeywords[keyword], described && describedMapKeywords[keyword]:
			i += 2
		case singleSchemaKeywords[keyword]:
			i++
		default:
			return false
		}
	}
	return i == len(tokens)
}

// reachedByKeywords reports whether a location lies at or below from along
// the subschema positions the 2020-12 meta-schema describes.
func reachedByKeywords(from, location string) bool {
	if !covers(from, location) {
		return false
	}
	tokens, ok := jsonpointer.Parse(location[len(from):])
	return ok && keywordPath(tokens, true)
}

// rootOf returns the root the schema library compiles the schema at a
// location from: the copy holding it (copiedAt), when the copy's keywords
// reach it, or the location itself, as for a schema a reference names in an
// annotation, which the library compiles as a schema of its own.
func rootOf(location string) string {
	if copy := copiedAt(location); reachedByKeywords(copy, location) {
		return copy
	}
	return location
}

// identityKeyword returns the first keyword by which a schema object declares
// a resource or an anchor, or "".
func identityKeyword(object map[string]any) string {
	if _, declares := declaredID(object); declares {
		return "$id"
	}
	for _, keyword := range []string{"$anchor", "$dynamicAnchor"} {
		if _, present := object[keyword]; present {
			return keyword
		}
	}
	return ""
}

// mustResolve returns the value at a location known to exist.
func mustResolve(view any, location string) any {
	value, _ := jsonpointer.Resolve(view, location)
	return value
}

// covers reports whether location lies at or below within.
func covers(within, location string) bool {
	return location == within || strings.HasPrefix(location, within+"/")
}
