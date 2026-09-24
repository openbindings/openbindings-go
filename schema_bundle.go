package openbindings

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
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

// copyGraph is the graph of what the bundle copies: where each schema the
// operation schemas reach is copied (copiedAt), and, as a bundler would, where
// each schema any reference in a copy names is copied, reached or not. The
// library compiles a whole resource whenever any part of it is used, so it
// must find everything the resource references. A problem in one copy is a
// problem of every copy reaching it.
type copyGraph struct {
	id     map[string]int
	copies []copyNode
	// closure holds, per copy, the first problem in sorted order among the
	// copies reachable from it, its own included; comparison the first place
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

// analyze builds the graph of the copies the analyzed schemas need.
func (c *copyGraph) analyze(o *operationSchemas) {
	*c = copyGraph{id: map[string]int{}}
	var queue []string
	for _, node := range o.graph.nodes {
		queue = append(queue, copiedAt(node.location))
	}
	for len(queue) > 0 {
		at := queue[0]
		queue = queue[1:]
		if _, seen := c.id[at]; seen {
			continue
		}
		node := copyNode{location: at, problem: o.copyProblem(at)}
		if o.limitProblem(at) == "" {
			node.comparison = numberComparison(mustResolve(o.view, at), at)
			// A copy meeting a resource limit is never bundled, so what it
			// references is not needed, and reading it would do the work the
			// limit refuses.
			forEachReference(mustResolve(o.view, at), at, func(holder, _, ref string) {
				if target := o.schemas.resolve(ref, holder, o.view); target.origin == inDocument {
					node.to = append(node.to, copiedAt(target.location))
				}
			})
		}
		c.id[at] = len(c.copies)
		c.copies = append(c.copies, node)
		queue = append(queue, node.to...)
	}
	for i := range c.copies {
		for _, at := range c.copies[i].to {
			c.copies[i].edges = append(c.copies[i].edges, c.id[at])
		}
	}
	component, count := components(len(c.copies), func(i int) []int { return c.copies[i].edges })
	successors := func(i int) []int { return c.copies[i].edges }
	problems := gather(component, count, successors, func(i int) string { return c.copies[i].problem })
	comparisons := gather(component, count, successors, func(i int) string { return c.copies[i].comparison })
	c.closure = make([]string, len(c.copies))
	c.comparison = make([]string, len(c.copies))
	for i := range c.copies {
		c.closure[i] = problems[component[i]]
		c.comparison[i] = comparisons[component[i]]
	}
}

// problemOf returns the first problem among the copies a copy reaches.
func (c *copyGraph) problemOf(at string) string {
	return c.closure[c.id[at]]
}

// from returns the copies reachable from those at the given locations.
func (c *copyGraph) from(locations []string) map[string]bool {
	out := map[string]bool{}
	var queue []int
	for _, at := range locations {
		queue = append(queue, c.id[at])
	}
	for len(queue) > 0 {
		i := queue[0]
		queue = queue[1:]
		if out[c.copies[i].location] {
			continue
		}
		out[c.copies[i].location] = true
		queue = append(queue, c.copies[i].edges...)
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

// forEachReference calls fn for every $ref and $dynamicRef string within a
// value, with the location of the object holding it. The values of const and
// enum are data compared as a whole, never schemas, and are skipped.
func forEachReference(value any, at string, fn func(holder, keyword, ref string)) {
	switch value := value.(type) {
	case map[string]any:
		for _, key := range sortedKeys(value) {
			switch member := value[key]; {
			case key == "const" || key == "enum":
			case key == "$ref" || key == "$dynamicRef":
				if ref, ok := member.(string); ok {
					fn(at, key, ref)
				}
			default:
				forEachReference(member, at+jsonpointer.Format(key), fn)
			}
		}
	case []any:
		for i, item := range value {
			forEachReference(item, at+jsonpointer.Format(fmt.Sprint(i)), fn)
		}
	}
}

// copyProblem states why a copied schema cannot be handed to the schema
// library, or returns "": a resource limit it meets (limitProblem); a dialect or vocabulary §5.2 excludes; a pattern Go's
// regexp cannot compile, which cannot be evaluated (the SDK's compilers accept
// every pattern, so format "regex" never asserts, and leave this check to
// core); a resource whose
// base has no hierarchical path (urn:x:y) holding a relative reference, which
// the library resolves differently from RFC 3986; or, for a schema copied from
// outside the schema positions, an identity keyword, which only a schema
// position declares (§7).
func (o *operationSchemas) copyProblem(at string) string {
	if problem := o.limitProblem(at); problem != "" {
		return problem
	}
	value := mustResolve(o.view, at)
	// The library checks what it is given against the meta-schema, but it is
	// given the copy without the keywords strict 2020-12 drops; the document
	// holds them, and they too must be well-formed.
	if problems, err := checkAgainstMetaSchema(value); err != nil {
		return fmt.Sprintf("the schema at %s could not be checked against the 2020-12 meta-schemas: %v", at, err)
	} else if len(problems) > 0 {
		return fmt.Sprintf("the schema at %s is not a well-formed JSON Schema 2020-12 schema: %s: %s", at, at+jsonpointer.Format(problems[0].Location...), problems[0].Message)
	}
	var problems []string
	var walk func(node any, location string)
	walk = func(node any, location string) {
		object, ok := node.(map[string]any)
		if !ok {
			return
		}
		if dialect, present := object["$schema"]; present && dialect != draft202012URI {
			problems = append(problems, fmt.Sprintf("the schema at %s declares $schema %s, not %s", location, describeJSON(dialect), draft202012URI))
		}
		if _, present := object["$vocabulary"]; present {
			problems = append(problems, fmt.Sprintf("the schema at %s declares $vocabulary", location))
		}
		patterns := sortedKeys(asObject(object["patternProperties"]))
		if pattern, ok := object["pattern"].(string); ok {
			patterns = append(patterns, pattern)
		}
		for _, pattern := range patterns {
			if _, err := regexp.Compile(pattern); err != nil {
				problems = append(problems, fmt.Sprintf("the pattern %q at %s cannot be evaluated: Go's regexp does not support it (%v)", pattern, location, err))
			}
		}
		forEachDescribedSubschema(object, func(child any, tokens ...string) {
			walk(child, location+jsonpointer.Format(tokens...))
		})
	}
	walk(value, at)
	for _, resource := range o.resourcesIn[at] {
		if !covers(at, resource.location) || resource.uri == nil {
			continue
		}
		location := resource.location
		if resource.uri.Opaque == "" {
			continue
		}
		if relative := relativeReference(resource.schema, location); relative != "" {
			problems = append(problems, fmt.Sprintf("the schema at %s declares %s, whose path is not hierarchical, and holds the relative reference %s, which the schema library resolves differently from RFC 3986", location, resource.uri, relative))
		}
	}
	if !atSchemaPosition(at) {
		forEachObject(value, at, func(object map[string]any, location string) {
			if keyword := identityKeyword(object); keyword != "" {
				problems = append(problems, fmt.Sprintf("the schema at %s is not at a schema position, and it declares %s, which only a schema position declares; define it in schemas to use it", location, keyword))
			}
		})
	}
	if len(problems) == 0 {
		return ""
	}
	return slices.Min(problems)
}

// limitProblem states a resource limit a copied schema meets, or returns "":
// nesting past schemaDepthLimit, or a number the schema library reads beyond
// the numeric limits of schema evaluation, which the library crashes or
// stalls on (§10.5). The library reads a number that is a keyword's value, or
// in const or enum; one elsewhere, as in default, is only carried. It is
// found once per copy, before the graph is walked into it, so the walk never
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
// tells numbers apart by more than type and equality, or returns "": a
// comparison keyword, or a const or enum holding a number. Only where there
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
// the 2020-12 meta-schema describes within it, in sorted order, with the
// reference tokens that reach it, until fn returns false.
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

// relativeReference returns the first reference or nested $id within a
// resource that is relative and not a bare fragment, or "".
func relativeReference(resource map[string]any, at string) string {
	var found []string
	forEachObject(resource, at, func(object map[string]any, location string) {
		for _, keyword := range []string{"$ref", "$dynamicRef", "$id"} {
			if keyword == "$id" && location == at {
				continue
			}
			value, ok := object[keyword].(string)
			if !ok || strings.HasPrefix(value, "#") {
				continue
			}
			if parsed, err := url.Parse(value); err == nil && !parsed.IsAbs() {
				found = append(found, fmt.Sprintf("%q at %s", value, location))
			}
		}
	})
	if len(found) == 0 {
		return ""
	}
	return slices.Min(found)
}

// forEachObject calls fn for every object within a value, the value included,
// with its location, skipping the values of const and enum.
func forEachObject(value any, at string, fn func(object map[string]any, location string)) {
	switch value := value.(type) {
	case map[string]any:
		fn(value, at)
		for _, key := range sortedKeys(value) {
			if key != "const" && key != "enum" {
				forEachObject(value[key], at+jsonpointer.Format(key), fn)
			}
		}
	case []any:
		for i, item := range value {
			forEachObject(item, at+jsonpointer.Format(fmt.Sprint(i)), fn)
		}
	}
}

// schemaBundle is a bundle and the locations copied into it.
type schemaBundle struct {
	copied   map[string]bool
	document map[string]any
}

// bundle builds the bundle holding the copies at the given locations.
func (o *operationSchemas) bundle(copied []string) schemaBundle {
	b := schemaBundle{copied: map[string]bool{}}
	for _, at := range copied {
		b.copied[at] = true
	}
	defs := make(map[string]any, len(copied))
	for _, at := range copied {
		defs[at] = o.copySchema(mustResolve(o.view, at), at, false, b)
	}
	b.document = map[string]any{"$schema": draft202012URI, "$id": bundleURI, "$defs": defs}
	return b
}

// copySchema copies a value within a copied schema. names is true for the
// object a keyword like properties holds, whose members are names, not
// keywords.
func (o *operationSchemas) copySchema(value any, at string, names bool, b schemaBundle) any {
	switch value := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(value))
		for key, member := range value {
			location := at + jsonpointer.Format(key)
			switch {
			case names:
				out[key] = o.copySchema(member, location, false, b)
			case strictlyExcluded[key]:
			case key == "const" || key == "enum":
				out[key] = member
			case key == "$ref" || key == "$dynamicRef":
				out[key] = o.rewrite(member, at, b)
			case schemaMapKeywords[key] || describedMapKeywords[key]:
				out[key] = o.copySchema(member, location, true, b)
			default:
				out[key] = o.copySchema(member, location, false, b)
			}
		}
		return out
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = o.copySchema(item, at+jsonpointer.Format(fmt.Sprint(i)), false, b)
		}
		return out
	}
	return value
}

// rewrite returns a reference held at a location as the bundle holds it: a
// same-document reference in the document resource points into the bundle,
// and any other is left as written.
func (o *operationSchemas) rewrite(member any, holder string, b schemaBundle) any {
	ref, ok := member.(string)
	if !ok || !strings.HasPrefix(ref, "#") || o.schemas.resourceAt(holder) != nil {
		return member
	}
	target := o.schemas.resolve(ref, holder, o.view)
	if target.origin != inDocument {
		return member
	}
	if address, ok := b.address(target.location); ok {
		return address
	}
	return member
}

// address returns the URI at which the bundle holds a document location: in
// the copy holding it, found by the location's prefixes.
func (b schemaBundle) address(location string) (string, bool) {
	for end := len(location); end > 0; end = strings.LastIndexByte(location[:end], '/') {
		if at := location[:end]; b.copied[at] {
			return bundleURI + "#" + fragment(jsonpointer.Format("$defs", at)+location[end:]), true
		}
	}
	return "", false
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
	for i, node := range o.copies.copies {
		if o.copies.closure[i] == "" {
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

// compileAlone compiles the schema at start from a bundle of its own graph.
func (o *operationSchemas) compileAlone(start string) compiled {
	var reached []string
	for _, at := range o.graph.reachedFrom(start) {
		reached = append(reached, copiedAt(at))
	}
	own := o.bundle(outermost(o.copies.from(reached)))
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

// comparison states where the schema graph from start tells numbers apart by
// more than type and equality, or returns "": a value's stand-ins
// (schemacompiler.Substitute) are then validated as the numbers they stand
// for would be.
func (o *operationSchemas) comparison(start string) string {
	if meta := o.facts(start).metaSchema; meta != "" {
		return "the schema graph reaches the meta-schema " + meta
	}
	return o.copies.comparison[o.copies.id[copiedAt(start)]]
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

// location returns the document location a bundle URI names, or the URI when
// it names none.
func (b schemaBundle) location(uri string) string {
	resource, encoded, _ := strings.Cut(uri, "#")
	if resource != bundleURI {
		return uri
	}
	pointer, err := url.PathUnescape(encoded)
	if err != nil {
		return uri
	}
	tokens, ok := jsonpointer.Parse(pointer)
	if !ok || len(tokens) < 2 || tokens[0] != "$defs" {
		return uri
	}
	return tokens[1] + jsonpointer.Format(tokens[2:]...)
}
