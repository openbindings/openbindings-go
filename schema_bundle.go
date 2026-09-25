package openbindings

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/openbindings/openbindings-go/internal/jsonpointer"
	"github.com/openbindings/openbindings-go/internal/schemacompiler"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// The schema library is given an operation's schemas as one bundle: a JSON
// Schema 2020-12 document holding a copy of each schema the operation uses
// under $defs, keyed by its document location (JSON Schema 2020-12 §9.3, and
// what OpenAPI bundlers do). The OBI document itself is not a schema, and its
// references may point anywhere in it (§7), so it is never handed over whole.
//
// A copy keeps what the document holds, with three changes:
//   - A same-document reference in the document resource, which is a JSON
//     Pointer from the OBI document root, points into the bundle instead. A
//     reference inside a resource that declares $id is left as written, and
//     the library resolves it by that $id, as it would any schema's.
//   - dependencies, $recursiveRef, and $recursiveAnchor are dropped: 2020-12
//     does not evaluate them, though the library still would.
//   - Nothing else changes, so the library meets exactly the schemas the
//     document holds.
//
// The bundle's own URI is under .invalid (RFC 2606), so it names no real
// resource.
const bundleURI = "https://openbindings.invalid/document"

// strictlyExcluded are keywords the library evaluates in a 2020-12 schema
// though 2020-12 does not define them.
var strictlyExcluded = map[string]bool{"dependencies": true, "$recursiveRef": true, "$recursiveAnchor": true}

// copiedAt returns the location a schema is copied at: the OBI schema position
// holding it, whole, or the schema itself when it lies elsewhere in the
// document (decision 3).
func copiedAt(location string) string {
	tokens, _ := jsonpointer.Parse(location)
	switch {
	case len(tokens) >= 2 && tokens[0] == "schemas":
		return jsonpointer.Format(tokens[:2]...)
	case len(tokens) >= 3 && tokens[0] == "operations" && (tokens[2] == "input" || tokens[2] == "output"):
		return jsonpointer.Format(tokens[:3]...)
	}
	return location
}

// copyGraph is the graph of what the schema library compiles from the bundle,
// by root: where it compiles a schema from (rootOf). A root is a copy
// (copiedAt), or a location a reference names that no copy's keywords reach,
// such as a value in an annotation, which is no schema position and so a root
// with a problem (rootProblem); the schemas
// a root holds are what its keywords reach, and everything else in it is data
// the library only carries. As a bundler would, the graph follows every
// reference those schemas hold, reached by the operation schemas or not, since
// the library compiles a whole resource whenever any part of it is used, and
// must find everything the resource references. A root within a copy leads to
// the copy too, which the bundle must hold. A problem of one root is a problem
// of every root reaching it.
type copyGraph struct {
	id    map[string]int
	nodes []copyNode
	// closure holds, per root, the first problem in sorted order among the
	// roots reachable from it, its own included; comparison the first place
	// among them where a schema compares numbers (see numberComparison).
	closure    []string
	comparison []string
}

type copyNode struct {
	location   string
	to         []string
	edges      []int
	problem    string
	comparison string
}

// analyze builds the graph of the roots the analyzed schemas need.
func (c *copyGraph) analyze(o *operationSchemas) {
	*c = copyGraph{id: map[string]int{}}
	var queue []string
	for _, node := range o.graph.nodes {
		queue = append(queue, node.root)
	}
	for len(queue) > 0 {
		root := queue[0]
		queue = queue[1:]
		if _, seen := c.id[root]; seen {
			continue
		}
		node := copyNode{location: root, problem: o.rootProblem(root)}
		if copy := copiedAt(root); copy != root {
			node.to = append(node.to, copy)
		}
		if o.limitProblem(root) == "" {
			// A root meeting a resource limit is never compiled, so what it
			// references is not needed, and reading it would do the work the
			// limit refuses.
			value := mustResolve(o.view, root)
			node.comparison = numberComparison(value, root)
			forEachSchemaReference(value, root, func(holder, ref string) {
				if target := o.schemas.resolve(ref, holder, o.view); target.origin == inDocument {
					node.to = append(node.to, rootOf(target.location))
				}
			})
		}
		c.id[root] = len(c.nodes)
		c.nodes = append(c.nodes, node)
		queue = append(queue, node.to...)
	}
	// A root the bundle holds within a copy is compiled as that copy
	// carries it, which copySchema decides: the copy holding it at a schema
	// position, or any copy outside the schema positions that covers it.
	var outside copyTrie
	for _, node := range c.nodes {
		if at := node.location; copiedAt(at) == at && !atSchemaPosition(at) {
			outside.add(at)
		}
	}
	for i := range c.nodes {
		root := c.nodes[i].location
		if copy := copiedAt(root); copy != root {
			c.nodes[i].problem = firstOf(c.nodes[i].problem, o.carriageProblem(copy, root))
		}
		outside.forEachHolder(root, func(holder string, _ []string) bool {
			if holder != root {
				c.nodes[i].problem = firstOf(c.nodes[i].problem, o.carriageProblem(holder, root))
			}
			return true
		})
	}
	for i := range c.nodes {
		for _, at := range c.nodes[i].to {
			c.nodes[i].edges = append(c.nodes[i].edges, c.id[at])
		}
	}
	successors := func(i int) []int { return c.nodes[i].edges }
	component, count := components(len(c.nodes), successors)
	problems := gather(component, count, successors, func(i int) string { return c.nodes[i].problem })
	comparisons := gather(component, count, successors, func(i int) string { return c.nodes[i].comparison })
	c.closure = make([]string, len(c.nodes))
	c.comparison = make([]string, len(c.nodes))
	for i := range c.nodes {
		c.closure[i] = problems[component[i]]
		c.comparison[i] = comparisons[component[i]]
	}
}

// problemOf returns the first problem among the roots a root reaches.
func (c *copyGraph) problemOf(root string) string {
	return c.closure[c.id[root]]
}

// copiesFrom returns the copies holding the roots reachable from a root.
func (c *copyGraph) copiesFrom(root string) map[string]bool {
	out := map[string]bool{}
	seen := map[int]bool{c.id[root]: true}
	queue := []int{c.id[root]}
	for len(queue) > 0 {
		i := queue[0]
		queue = queue[1:]
		out[copiedAt(c.nodes[i].location)] = true
		for _, next := range c.nodes[i].edges {
			if !seen[next] {
				seen[next] = true
				queue = append(queue, next)
			}
		}
	}
	return out
}

// outermost returns the locations of a set that no other location of the set
// holds, sorted.
func outermost(set map[string]bool) []string {
	var out []string
	for _, at := range slices.Sorted(maps.Keys(set)) {
		if len(out) == 0 || !covers(out[len(out)-1], at) {
			out = append(out, at)
		}
	}
	return out
}

// carriageProblem states why a copy at holder cannot give the library the
// schema at root, which lies within it, as a schema, or returns "": the copy
// leaves out a keyword strict 2020-12 drops, and carries the value of const
// and enum as written, so a schema within the one is absent and a schema
// within the other keeps same-document references that point nowhere in the
// bundle.
func (o *operationSchemas) carriageProblem(holder, root string) string {
	tokens, _ := jsonpointer.Parse(root[len(holder):])
	names := false
	for i, token := range tokens {
		switch {
		case names:
			names = false
		case strictlyExcluded[token]:
			return fmt.Sprintf("the schema at %s lies within %s, which the bundle leaves out, as strict 2020-12 does not evaluate it", root, holder+jsonpointer.Format(tokens[:i+1]...))
		case token == "const" || token == "enum":
			within := holder + jsonpointer.Format(tokens[:i+1]...)
			inResource := o.schemas.resourceAt(root) != nil
			problem := ""
			walkSchemaObjects(mustResolve(o.view, root), func(object map[string]any, _ []string) bool {
				for keyword := range object {
					if strictlyExcluded[keyword] {
						problem = fmt.Sprintf("the schema at %s lies within the %s value at %s, which the bundle carries as written, so it keeps %s, which strict 2020-12 does not evaluate but the schema library would", root, token, within, keyword)
						return false
					}
				}
				for _, keyword := range []string{"$ref", "$dynamicRef"} {
					if ref, ok := object[keyword].(string); ok && strings.HasPrefix(ref, "#") && !inResource {
						problem = fmt.Sprintf("the schema at %s lies within the %s value at %s, which the bundle carries as written, so its same-document references cannot point into the bundle", root, token, within)
						return false
					}
				}
				return true
			})
			return problem
		case schemaMapKeywords[token] || describedMapKeywords[token]:
			names = true
		}
	}
	return ""
}

// forEachSchemaReference calls fn for every $ref and $dynamicRef the schemas
// of a root hold, with the location of the schema holding it. A member
// that is not a schema is data, whatever it holds.
func forEachSchemaReference(root any, at string, fn func(holder, ref string)) {
	walkSchemaObjects(root, func(object map[string]any, path []string) bool {
		for _, keyword := range []string{"$ref", "$dynamicRef"} {
			if ref, ok := object[keyword].(string); ok {
				fn(at+jsonpointer.Format(path...), ref)
			}
		}
		return true
	})
}

// rootProblem states why the schema library cannot be given what a root
// holds, or returns "": a resource limit it meets (limitProblem); a schema
// that is not well-formed; a dialect or vocabulary §5.2 excludes; a pattern
// Go's regexp cannot compile, which cannot be evaluated (the SDK's compilers
// accept every pattern, so format "regex" never asserts, and leave this
// check to core); a resource whose base has no hierarchical path (urn:x:y)
// holding a relative reference, which the library resolves differently from
// RFC 3986; or, for a root outside the schema positions, an identity keyword,
// which only a schema position declares (§7).
func (o *operationSchemas) rootProblem(at string) string {
	// A schema $ref reaches only a schema the document model places
	// (OBI-D-12); JSON Schema 2020-12 leaves any other target undefined
	// (§9.4.2), so nothing elsewhere is evaluated as a schema.
	if !atSchemaPosition(at) {
		return fmt.Sprintf("%s is not a schema position; a schema $ref reaches only a schema the document model places (OBI-D-12)", describeLocation(at))
	}
	if problem := o.limitProblem(at); problem != "" {
		return problem
	}
	value := mustResolve(o.view, at)
	// The library checks what it is given against the meta-schema, but it is
	// given each copy without the keywords strict 2020-12 drops; the document
	// holds them as schemas, and they too must be well-formed.
	if problems, err := checkAgainstMetaSchema(value); err != nil {
		return fmt.Sprintf("the schema at %s could not be checked against the 2020-12 meta-schemas: %v", at, err)
	} else if len(problems) > 0 {
		return fmt.Sprintf("the schema at %s is not a well-formed JSON Schema 2020-12 schema: %s: %s", at, at+jsonpointer.Format(problems[0].Location...), problems[0].Message)
	}
	var problems []string
	walkSchemaObjects(value, func(object map[string]any, path []string) bool {
		location := func() string { return at + jsonpointer.Format(path...) }
		if dialect, present := object["$schema"]; present && dialect != draft202012URI {
			problems = append(problems, fmt.Sprintf("the schema at %s declares $schema %s, not %s", location(), describeJSON(dialect), draft202012URI))
		}
		if _, present := object["$vocabulary"]; present {
			problems = append(problems, fmt.Sprintf("the schema at %s declares $vocabulary", location()))
		}
		patterns := sortedKeys(asObject(object["patternProperties"]))
		if pattern, ok := object["pattern"].(string); ok {
			patterns = append(patterns, pattern)
		}
		for _, pattern := range patterns {
			if _, err := regexp.Compile(pattern); err != nil {
				problems = append(problems, fmt.Sprintf("the pattern %q at %s cannot be evaluated: Go's regexp does not support it (%v)", pattern, location(), err))
			}
		}
		return true
	})
	resources := o.resourcesIn[at]
	if resource := o.schemas.resourceAt(at); resource != nil && resource.location != at {
		// A root within a resource resolves its references against it.
		resources = append(slices.Clip(resources), &schemaResource{location: at, uri: resource.uri, schema: asObject(value)})
	}
	for _, resource := range resources {
		if resource.uri == nil || resource.uri.Opaque == "" {
			continue
		}
		if relative := relativeReference(resource.schema, resource.location); relative != "" {
			problems = append(problems, fmt.Sprintf("the schema at %s declares %s, whose path is not hierarchical, and holds the relative reference %s, which the schema library resolves differently from RFC 3986", resource.location, resource.uri, relative))
		}
	}
	if len(problems) == 0 {
		return ""
	}
	return slices.Min(problems)
}

// limitProblem states a resource limit a root meets, or returns "":
// nesting past schemaDepthLimit, or a number the schema library reads beyond
// the numeric limits of schema evaluation, which the library crashes or
// stalls on (§10.5). The library reads a number that is a keyword's value, or
// in const or enum; one elsewhere, as in default, is only carried. It is
// found once per root, before the graph is walked into it, so the walk never
// does work that grows with a depth the limit refuses.
func (o *operationSchemas) limitProblem(at string) string {
	if problem, found := o.limits[at]; found {
		return problem
	}
	value := mustResolve(o.view, at)
	problem := ""
	if schemaDepth(value) > schemaDepthLimit {
		problem = fmt.Sprintf("the schema graph nests subschemas deeper than %d levels at %s", schemaDepthLimit, at)
	} else if where, err := numberRead(value); err != nil {
		problem = fmt.Sprintf("the schema graph holds, at %s, %v", at+where, err)
	}
	o.limits[at] = problem
	return problem
}

// comparisonKeywords compare a value's number with the schema's by more than
// equality; countKeywords bound a count. The schema library reads the value
// of each as a number.
var (
	comparisonKeywords = map[string]bool{"minimum": true, "maximum": true, "exclusiveMinimum": true, "exclusiveMaximum": true, "multipleOf": true}
	countKeywords      = map[string]bool{"maxLength": true, "minLength": true, "maxItems": true, "minItems": true, "maxContains": true, "minContains": true, "maxProperties": true, "minProperties": true}
)

// numberRead returns where, in a schema, the first number the schema library
// reads lies beyond the numeric limits of schema evaluation, with the limits'
// error, or a nil error when none does: the value of a comparison or count
// keyword, or a number in const or enum, which the library compares with a
// value's.
func numberRead(schema any) (string, error) {
	var location string
	var err error
	walkSchemaObjects(schema, func(object map[string]any, path []string) bool {
		for _, keyword := range sortedKeys(object) {
			if !comparisonKeywords[keyword] && !countKeywords[keyword] && keyword != "const" && keyword != "enum" {
				continue
			}
			if where, limit := schemacompiler.NumericLimit(object[keyword]); limit != nil {
				location, err = jsonpointer.Format(append(slices.Clip(path), keyword)...)+where, limit
				return false
			}
		}
		return true
	})
	return location, err
}

// numberComparison states the first place in a schema, at location at, that
// compares a number by order or divisibility (a comparison keyword) or holds
// one in const or enum, or returns "". Only where there
// is none is a value's stand-in (schemacompiler.Substitute) validated as the
// number it stands for would be.
func numberComparison(schema any, at string) string {
	found := ""
	walkSchemaObjects(schema, func(object map[string]any, path []string) bool {
		for _, keyword := range sortedKeys(object) {
			if comparisonKeywords[keyword] || (keyword == "const" || keyword == "enum") && holdsNumber(object[keyword]) {
				found = fmt.Sprintf("the schema at %s compares numbers with %s", at+jsonpointer.Format(path...), keyword)
				return false
			}
		}
		return true
	})
	return found
}

// walkSchemaObjects calls fn for a schema object and each subschema object
// a copy of it carries (the positions the 2020-12 meta-schema describes, but
// for the keywords strict 2020-12 drops), in sorted order, with the reference
// tokens that reach it, until fn returns false.
func walkSchemaObjects(schema any, fn func(object map[string]any, path []string) bool) {
	var path []string
	more := true
	var walk func(node any)
	walk = func(node any) {
		object, ok := node.(map[string]any)
		if !ok || !more {
			return
		}
		if more = fn(object, path); !more {
			return
		}
		forEachDescribedSubschema(object, func(child any, tokens ...string) {
			if strictlyExcluded[tokens[0]] {
				return
			}
			path = append(path, tokens...)
			walk(child)
			path = path[:len(path)-len(tokens)]
		})
	}
	walk(schema)
}

func holdsNumber(value any) bool {
	switch value := value.(type) {
	case json.Number:
		return true
	case []any:
		return slices.ContainsFunc(value, holdsNumber)
	case map[string]any:
		for _, member := range value {
			if holdsNumber(member) {
				return true
			}
		}
	}
	return false
}

func asObject(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

// relativeReference returns the first reference or nested $id the schemas of
// a resource hold that is relative and not a bare fragment, or "".
func relativeReference(resource map[string]any, at string) string {
	var found []string
	walkSchemaObjects(resource, func(object map[string]any, path []string) bool {
		for _, keyword := range []string{"$ref", "$dynamicRef", "$id"} {
			if keyword == "$id" && len(path) == 0 {
				continue
			}
			value, ok := object[keyword].(string)
			if !ok || strings.HasPrefix(value, "#") {
				continue
			}
			if parsed, err := url.Parse(value); err == nil && !parsed.IsAbs() {
				found = append(found, fmt.Sprintf("%q at %s", value, at+jsonpointer.Format(path...)))
			}
		}
		return true
	})
	if len(found) == 0 {
		return ""
	}
	return slices.Min(found)
}

// schemaBundle is a bundle and the locations copied into it.
type schemaBundle struct {
	copied   copyTrie
	document map[string]any
}

// copyTrie holds copy locations by their reference tokens, so the copies
// holding a location are found in time linear in it, however deep it lies.
type copyTrie struct {
	children map[string]*copyTrie
	// copy is the location of the copy ending here, or "".
	copy string
}

func (t *copyTrie) add(location string) {
	tokens, _ := jsonpointer.Parse(location)
	for _, token := range tokens {
		if t.children == nil {
			t.children = map[string]*copyTrie{}
		}
		next := t.children[token]
		if next == nil {
			next = &copyTrie{}
			t.children[token] = next
		}
		t = next
	}
	t.copy = location
}

// forEachHolder calls fn for each copy at or above a location, outermost
// first, with the reference tokens from it to the location, until fn returns
// false.
func (t *copyTrie) forEachHolder(location string, fn func(copy string, rest []string) bool) {
	tokens, _ := jsonpointer.Parse(location)
	for i := 0; t != nil; i++ {
		if t.copy != "" && !fn(t.copy, tokens[i:]) {
			return
		}
		if i == len(tokens) {
			return
		}
		t = t.children[tokens[i]]
	}
}

// bundle builds the bundle holding the copies at the given locations.
func (o *operationSchemas) bundle(copied []string) schemaBundle {
	var b schemaBundle
	for _, at := range copied {
		b.copied.add(at)
	}
	defs := make(map[string]any, len(copied))
	for _, at := range copied {
		defs[at] = o.copySchema(mustResolve(o.view, at), false, false, b)
	}
	b.document = map[string]any{"$schema": draft202012URI, "$id": bundleURI, "$defs": defs}
	return b
}

// copySchema copies a value within a copied schema. names is true for the
// object a keyword like properties holds, whose members are names, not
// keywords; inResource for a value within a schema resource, whose
// references the library resolves by its $id.
func (o *operationSchemas) copySchema(value any, names, inResource bool, b schemaBundle) any {
	switch value := value.(type) {
	case map[string]any:
		inResource = inResource || o.resourceObjects[objectID(value)]
		out := make(map[string]any, len(value))
		for key, member := range value {
			switch {
			case names:
				out[key] = o.copySchema(member, false, inResource, b)
			case strictlyExcluded[key]:
			case key == "const" || key == "enum":
				out[key] = member
			case key == "$ref" || key == "$dynamicRef":
				out[key] = o.rewrite(member, inResource, b)
			case schemaMapKeywords[key] || describedMapKeywords[key]:
				out[key] = o.copySchema(member, true, inResource, b)
			default:
				out[key] = o.copySchema(member, false, inResource, b)
			}
		}
		return out
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = o.copySchema(item, false, inResource, b)
		}
		return out
	}
	return value
}

// rewrite returns a reference as the bundle holds it: a same-document
// reference outside every schema resource, which §7 reads from the document
// root, points into the bundle, and any other is left as written.
func (o *operationSchemas) rewrite(member any, inResource bool, b schemaBundle) any {
	ref, ok := member.(string)
	if !ok || !strings.HasPrefix(ref, "#") || inResource {
		return member
	}
	target := o.schemas.resolve(ref, "", o.view)
	if target.origin != inDocument {
		return member
	}
	if address, ok := b.address(target.location); ok {
		return address
	}
	return member
}

// objectID identifies an object of the generic view by its map, which
// decoding never shares between locations.
func objectID(object map[string]any) uintptr {
	return reflect.ValueOf(object).Pointer()
}

// address returns the URI at which the bundle holds a document location: in
// the copy holding it. The bundle holds no copy within another.
func (b schemaBundle) address(location string) (string, bool) {
	address, found := "", false
	b.copied.forEachHolder(location, func(copy string, rest []string) bool {
		address, found = bundleURI+"#"+fragment(jsonpointer.Format("$defs", copy)+jsonpointer.Format(rest...)), true
		return false
	})
	return address, found
}

// fragment writes a JSON Pointer as a URI fragment the way the schema library
// reads one: each reference token percent-encoded, so a token holding % or a
// space names itself.
func fragment(pointer string) string {
	tokens := strings.Split(pointer, "/")
	for i, token := range tokens {
		tokens[i] = url.PathEscape(token)
	}
	return strings.Join(tokens, "/")
}

// compiled is the outcome of compiling one schema.
type compiled struct {
	schema *CompiledSchema
	err    error
}

// compile compiles the schemas at each start, analyzed and found evaluable.
// They are compiled from one bundle and one compiler, so a schema several
// graphs share is compiled once. The bundle holds every copy whose own graph
// of copies is fit to hand the library; a start that does not compile from it
// is compiled again from a bundle of its own graph, so another graph's schemas
// never cost it its verdict.
func (o *operationSchemas) compile(starts []string) map[string]compiled {
	out := map[string]compiled{}
	fit := map[string]bool{}
	for i, node := range o.copies.nodes {
		if o.copies.closure[i] == "" && copiedAt(node.location) == node.location {
			fit[node.location] = true
		}
	}
	shared := o.bundle(outermost(fit))
	c := schemacompiler.New()
	sharedErr := c.AddResource(bundleURI, shared.document)
	for _, start := range starts {
		if sharedErr == nil {
			if address, ok := shared.address(start); ok {
				if schema, err := c.Compile(address); err == nil {
					out[start] = compiled{schema: &CompiledSchema{backend: schema, comparison: o.comparison(start)}}
					continue
				}
			}
		}
		out[start] = o.compileAlone(start)
	}
	return out
}

// compileAlone compiles the schema at start from a bundle of the copies its
// root reaches.
func (o *operationSchemas) compileAlone(start string) compiled {
	own := o.bundle(outermost(o.copies.copiesFrom(rootOf(start))))
	c := schemacompiler.New()
	if err := c.AddResource(bundleURI, own.document); err != nil {
		return compiled{err: own.describe(err)}
	}
	address, _ := own.address(start)
	schema, err := c.Compile(address)
	if err != nil {
		return compiled{err: own.describe(err)}
	}
	return compiled{schema: &CompiledSchema{backend: schema, comparison: o.comparison(start)}}
}

// comparison states where what the library compiles for start compares a
// number by order or divisibility or holds one in const or enum, or where
// the schema graph reaches a meta-schema, which core does not examine; or it
// returns "". Only where there is none is a value's stand-in
// (schemacompiler.Substitute) validated as the number it stands for would be.
func (o *operationSchemas) comparison(start string) string {
	if meta := o.facts(start).metaSchema; meta != "" {
		return "the schema graph reaches the meta-schema " + meta
	}
	return o.copies.comparison[o.copies.id[rootOf(start)]]
}

// describe restates an error the schema library reports for a bundle in the
// document's terms.
func (b schemaBundle) describe(err error) error {
	var invalid *jsonschema.SchemaValidationError
	if errors.As(err, &invalid) {
		// The library checks the whole bundle at once; each problem is located
		// in it, under the copy it lies in.
		problems, mismatch := schemacompiler.Outcome(invalid.Err)
		if !mismatch || len(problems) == 0 {
			return fmt.Errorf("a schema the graph uses is not a well-formed JSON Schema 2020-12 schema: %w", invalid.Err)
		}
		var texts []string
		copied := ""
		for _, problem := range problems {
			at, within := b.copiedLocation(problem.Location)
			if copied == "" {
				copied = within
			}
			texts = append(texts, at+": "+problem.Message)
		}
		return fmt.Errorf("the schema at %s is not a well-formed JSON Schema 2020-12 schema: %s", copied, strings.Join(texts, "; "))
	}
	var load *jsonschema.LoadURLError
	if errors.As(err, &load) {
		return fmt.Errorf("a schema the graph uses references %s, which the document does not embed", load.URL)
	}
	return fmt.Errorf("the schema library could not compile the schema graph: %w", err)
}

// copiedLocation returns the document location of a location within the
// bundle document, given as reference tokens, and the copy holding it.
func (b schemaBundle) copiedLocation(tokens []string) (location, copied string) {
	if len(tokens) < 2 || tokens[0] != "$defs" {
		return jsonpointer.Format(tokens...), ""
	}
	return tokens[1] + jsonpointer.Format(tokens[2:]...), tokens[1]
}
