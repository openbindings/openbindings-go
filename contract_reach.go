package openbindings

import (
	"fmt"
	"slices"
	"strings"

	"github.com/openbindings/openbindings-go/internal/jsonpointer"
)

// schemaKey is a schema in the space: a location in a schema document.
type schemaKey struct {
	doc      *schemaDoc
	location string
}

// contractReach is what evaluation of one value contract can reach (4.1 of
// the design): the entry and, transitively, every schema a reached schema
// applies, the targets of its references, and for a $dynamicRef that looks a
// name up dynamically every schema declaring the name as a $dynamicAnchor in a
// reached resource, and the scope wrapper for the name.
type contractReach struct {
	space *schemaSpace
	// entry is the value contract's schema.
	entry schemaKey
	// inDocument is whether the entry lies in the document resource, whose
	// $dynamicAnchors are outermost in its evaluations' dynamic scope (§7.2).
	inDocument bool
	reached    map[schemaKey]bool
	queue      []schemaKey
	// resources are the resources holding a reached schema.
	resources map[*docResource]bool
	// lookups holds, per name a reached $dynamicRef looks up dynamically, the
	// schemas holding such a $dynamicRef; wrappers the location of the
	// document-resource declaration a scope wrapper resolves to, per name.
	lookups  map[string][]schemaKey
	wrappers map[string]string
	// inPlace holds the edges along which evaluation applies a schema to the
	// value its source applies to.
	inPlace map[schemaKey][]schemaKey
	// failures are what the reached schemas' references and scope refuse.
	failures []located
}

// located is a failure and where it lies, as a URI-reference.
type located struct {
	failure
	location string
}

// inPlaceKeywords apply their subschemas to the value their schema applies
// to; advancingKeywords to a member, an item, or a property name. Neither
// lists $defs, definitions, dependencies, or contentSchema, whose schemas
// evaluation applies only through a reference.
var (
	inPlaceKeywords   = []string{"allOf", "anyOf", "oneOf", "not", "if", "then", "else", "dependentSchemas"}
	advancingKeywords = []string{"properties", "patternProperties", "additionalProperties", "propertyNames", "items", "prefixItems", "contains", "unevaluatedItems", "unevaluatedProperties"}
)

// reach walks what evaluation of the value contract at entry can reach.
func (s *schemaSpace) reach(entry string) *contractReach {
	r := &contractReach{
		space:     s,
		reached:   map[schemaKey]bool{},
		resources: map[*docResource]bool{},
		lookups:   map[string][]schemaKey{},
		wrappers:  map[string]string{},
		inPlace:   map[schemaKey][]schemaKey{},
	}
	start := schemaKey{s.obi, entry}
	r.entry = start
	resource, isSchema := s.obi.schemas[entry]
	if !isSchema {
		r.fail(start, failure{undefinedResult, "the operation's schema there is not a JSON Schema 2020-12 object or boolean (OBI-10)"})
		return r
	}
	r.inDocument = resource.document
	r.enqueue(start)
	for len(r.queue) > 0 {
		next := r.queue[0]
		r.queue = r.queue[1:]
		r.visit(next)
	}
	r.findCycles()
	return r
}

func (r *contractReach) enqueue(key schemaKey) {
	if !r.reached[key] {
		r.reached[key] = true
		r.queue = append(r.queue, key)
	}
}

func (r *contractReach) fail(at schemaKey, f failure) {
	r.failures = append(r.failures, located{f, locationOf(at.doc, at.location)})
}

func (r *contractReach) edge(from, to schemaKey) {
	r.inPlace[from] = append(r.inPlace[from], to)
	r.enqueue(to)
}

// visit reads one reached schema's keywords.
func (r *contractReach) visit(at schemaKey) {
	resource := at.doc.schemas[at.location]
	if !r.resources[resource] {
		r.resources[resource] = true
		// A newly reached resource adds its declarations of every name
		// already looked up dynamically.
		for name, holders := range r.lookups {
			for _, declared := range resource.dynamicAnchors[name] {
				for _, holder := range holders {
					r.edge(holder, schemaKey{at.doc, declared})
				}
			}
		}
	}
	value, _ := jsonpointer.Resolve(at.doc.value, at.location)
	object, isObject := value.(map[string]any)
	if !isObject {
		return
	}
	child := func(tokens ...string) schemaKey {
		return schemaKey{at.doc, at.location + jsonpointer.Format(tokens...)}
	}
	_, hasIf := object["if"]
	for _, keyword := range inPlaceKeywords {
		if (keyword == "then" || keyword == "else") && !hasIf {
			continue
		}
		forEachChildOf(object, keyword, func(tokens ...string) {
			if _, ok := at.doc.schemas[child(tokens...).location]; ok {
				r.edge(at, child(tokens...))
			}
		})
	}
	for _, keyword := range advancingKeywords {
		forEachChildOf(object, keyword, func(tokens ...string) {
			if _, ok := at.doc.schemas[child(tokens...).location]; ok {
				r.enqueue(child(tokens...))
			}
		})
	}
	if ref, ok := object["$ref"].(string); ok {
		if target, f := r.space.resolve(ref, resource); f != nil {
			r.fail(at, *f)
		} else {
			r.edge(at, schemaKey{target.doc, target.location})
		}
	}
	if ref, ok := object["$dynamicRef"].(string); ok {
		target, f := r.space.resolve(ref, resource)
		if f != nil {
			r.fail(at, *f)
			return
		}
		r.edge(at, schemaKey{target.doc, target.location})
		if target.name != "" && declaresDynamicAnchor(target, target.name) {
			r.lookUp(at, target.name)
		}
	}
}

// lookUp records a $dynamicRef at holder that looks a name up in the dynamic
// scope: it may land on any schema declaring the name as a $dynamicAnchor in
// a reached resource, or on the scope wrapper for the name.
func (r *contractReach) lookUp(holder schemaKey, name string) {
	first := r.lookups[name] == nil
	r.lookups[name] = append(r.lookups[name], holder)
	for resource := range r.resources {
		for _, declared := range resource.dynamicAnchors[name] {
			r.edge(holder, schemaKey{resource.doc, declared})
		}
	}
	if !first || !r.inDocument {
		return
	}
	// The document resource is outermost in the dynamic scope (§7.2), so the
	// lookup consults its plain name first, and the name captures the
	// $dynamicRef when its declaration there is a $dynamicAnchor (JSON
	// Schema Core §8.2.3.2). Its plain names are OBI-12's (§7.2, §7.3):
	// one it declares more than once, by either keyword, leaves the capture
	// undefined (Core §8.2.2), and one past core's index is its limit.
	switch declarations := r.space.document.anchors[name]; {
	case len(declarations) > 1:
		r.fail(holder, failure{undefinedResult, fmt.Sprintf("its $dynamicRef looks up %q, which the document resource declares more than once, which JSON Schema leaves undefined (Core §8.2.2)", name)})
	case len(declarations) == 1 && declarations[0].keyword == "$dynamicAnchor":
		location := declarations[0].at.from(nil)
		if _, indexed := r.space.obi.schemas[location]; !indexed {
			r.fail(holder, *r.space.obi.unindexed(location, fmt.Sprintf("its $dynamicRef looks up %q, which the schema at %s declares", name, location)))
			return
		}
		r.wrappers[name] = location
	}
}

// declaresDynamicAnchor reports whether the schema a reference names declares
// the name as a $dynamicAnchor, which makes a $dynamicRef naming it look the
// name up dynamically (JSON Schema Core §8.2.3.2).
func declaresDynamicAnchor(target schemaTarget, name string) bool {
	value, _ := jsonpointer.Resolve(target.doc.value, target.location)
	object, _ := value.(map[string]any)
	declared, _ := object["$dynamicAnchor"].(string)
	return declared == name
}

// findCycles refuses a reached cycle of in-place applications, which never
// advances into the value. Where every evaluation must enter it (certainLoop),
// the result is undefined (OBI-T-07, §7.4); otherwise an evaluator may never
// enter it for some values, and the refusal is conservative: core does not
// decide which values' evaluations do.
func (r *contractReach) findCycles() {
	if at, loops := r.certainLoop(); loops {
		r.fail(at, failure{undefinedResult, "every evaluation applies a cycle of schemas in place without advancing into the value, which JSON Schema leaves undefined (§7.4)"})
		return
	}
	keys := slices.SortedFunc(func(yield func(schemaKey) bool) {
		for key := range r.reached {
			if !yield(key) {
				return
			}
		}
	}, compareKeys)
	index := make(map[schemaKey]int, len(keys))
	for i, key := range keys {
		index[key] = i
	}
	successors := func(i int) []int {
		var out []int
		for _, to := range r.inPlace[keys[i]] {
			out = append(out, index[to])
		}
		return out
	}
	component, count := components(len(keys), successors)
	size := make([]int, count)
	for _, c := range component {
		size[c]++
	}
	for i, key := range keys {
		if size[component[i]] > 1 || slices.Contains(successors(i), i) {
			r.fail(key, failure{conservativePolicy, "it applies a cycle of schemas in place, which never advances into the value, so no value can be evaluated against it"})
			return
		}
	}
}

// evaluatingKeywords are the 2020-12 keywords that affect a schema's result.
// Any other keyword a schema holds (identifiers, annotations, $defs, and
// keywords 2020-12 does not define) evaluates nothing.
var evaluatingKeywords = []string{
	"$ref", "$dynamicRef", "allOf", "anyOf", "oneOf", "not", "if", "then", "else", "dependentSchemas",
	"prefixItems", "items", "contains", "properties", "patternProperties", "additionalProperties", "propertyNames",
	"unevaluatedItems", "unevaluatedProperties", "type", "enum", "const", "multipleOf", "maximum", "exclusiveMaximum",
	"minimum", "exclusiveMinimum", "maxLength", "minLength", "pattern", "maxItems", "minItems", "uniqueItems",
	"maxContains", "minContains", "maxProperties", "minProperties", "required", "dependentRequired",
}

// certainLoop reports whether every evaluation of the entry loops: whether,
// from the entry, the schemas that evaluate nothing but one step onward (a
// lone $ref, a lone allOf, anyOf, or oneOf of one subschema, or a lone not)
// lead back to a schema already passed. It returns that schema.
func (r *contractReach) certainLoop() (schemaKey, bool) {
	passed := map[schemaKey]bool{}
	for at := r.entry; ; {
		if passed[at] {
			return at, true
		}
		passed[at] = true
		next, only := r.soleStep(at)
		if !only {
			return schemaKey{}, false
		}
		at = next
	}
}

// soleStep returns the one schema a schema's evaluation applies in place,
// when it evaluates nothing else.
func (r *contractReach) soleStep(at schemaKey) (schemaKey, bool) {
	value, _ := jsonpointer.Resolve(at.doc.value, at.location)
	object, isObject := value.(map[string]any)
	if !isObject {
		return schemaKey{}, false
	}
	var keyword string
	for _, candidate := range evaluatingKeywords {
		if _, present := object[candidate]; present {
			if keyword != "" {
				return schemaKey{}, false
			}
			keyword = candidate
		}
	}
	switch keyword {
	case "$ref", "not":
	case "allOf", "anyOf", "oneOf":
		if subschemas, _ := object[keyword].([]any); len(subschemas) != 1 {
			return schemaKey{}, false
		}
	default:
		return schemaKey{}, false
	}
	if steps := r.inPlace[at]; len(steps) == 1 {
		return steps[0], true
	}
	return schemaKey{}, false
}

func compareKeys(a, b schemaKey) int {
	if a.doc != b.doc {
		return compareDocs(a.doc, b.doc)
	}
	return strings.Compare(a.location, b.location)
}

// compareDocs orders the OBI document first, then supplied resources and
// meta-schemas by URI.
func compareDocs(a, b *schemaDoc) int {
	if a.kind != b.kind {
		return int(a.kind) - int(b.kind)
	}
	return strings.Compare(a.uri, b.uri)
}

// forEachChildOf calls fn with the reference tokens of each subschema one
// keyword holds: the keyword itself, or it and a member name or index.
func forEachChildOf(object map[string]any, keyword string, fn func(tokens ...string)) {
	switch value := object[keyword].(type) {
	case map[string]any:
		if schemaMapKeywords[keyword] {
			for _, name := range sortedKeys(value) {
				fn(keyword, name)
			}
			return
		}
		fn(keyword)
	case []any:
		if arraySchemaKeywords[keyword] {
			for i := range value {
				fn(keyword, fmt.Sprint(i))
			}
		}
	case bool:
		fn(keyword)
	}
}

// components returns the strongly connected component of each of n nodes
// (Tarjan's algorithm, with an explicit stack, since reference chains can be
// long), and how many there are.
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
