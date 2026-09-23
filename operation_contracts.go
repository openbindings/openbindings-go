package openbindings

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
	"sync"

	"github.com/openbindings/openbindings-go/internal/jsonpointer"
	"github.com/openbindings/openbindings-go/internal/schemacompiler"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// schemaDepthLimit bounds the nesting depth of a value handed to the schema
// library to compile. The library's compile-time meta-schema checks grow
// faster than linearly with depth (seconds at a few thousand levels), and
// schemas never nest near this deep, so a deeper one is a resource limit met,
// not evidence about the schema (§10.5).
const schemaDepthLimit = 256

// operationContracts is what the schema library is given of one document to
// evaluate its operations' schemas, built once and shared by every operation
// of the document.
//
// A same-document fragment is a JSON Pointer from the OBI document root,
// which is not a schema (§7). The library reads the members of whatever it is
// given as a root as schema keywords, so it is given the document without the
// root members named like a JSON Schema 2020-12 keyword (the OBI's own
// dependencies and description among them): every other member stays at the
// location it holds, so a reference means what its author wrote. A reference
// into a removed member does not resolve. The resources the document embeds
// by $id are compiled first, so a location inside one has that resource's
// base however a reference reaches it, and the loader serves them by $id.
type operationContracts struct {
	schemas     documentSchemas
	container   map[string]any
	documentURL string
	loader      *documentLoader
	compiler    *jsonschema.Compiler

	// references memoizes, per location, the locations the references inside
	// the value there name; checked memoizes the resource-limit check of a
	// location.
	references map[string][]string
	checked    map[string]error
}

func newOperationContracts(view any, schemas documentSchemas) *operationContracts {
	root, _ := view.(map[string]any)
	container := map[string]any{}
	for name, value := range root {
		if !schemaKeywords()[name] {
			container[name] = value
		}
	}
	return &operationContracts{
		schemas:    schemas,
		container:  container,
		references: map[string][]string{},
		checked:    map[string]error{},
	}
}

// schemaKeywords are the keywords of JSON Schema 2020-12, read from the
// meta-schema the library carries: the properties of the meta-schema and of
// the vocabulary meta-schemas it applies, deprecated keywords included.
var schemaKeywords = sync.OnceValue(func() map[string]bool {
	keywords := map[string]bool{}
	seen := map[*jsonschema.Schema]bool{}
	var collect func(s *jsonschema.Schema)
	collect = func(s *jsonschema.Schema) {
		if s == nil || seen[s] {
			return
		}
		seen[s] = true
		for name := range s.Properties {
			keywords[name] = true
		}
		collect(s.Ref)
		for _, member := range s.AllOf {
			collect(member)
		}
	}
	collect(compiledMetaSchema)
	return keywords
})

// compile compiles the schema at an operation's input or output, given its
// canonical key. outside names a resource outside the document the graph
// reaches: an external one, which the SDK does not obtain, so err reports the
// graph unavailable; or a JSON Schema meta-schema, which the schema library
// carries.
func (o *operationContracts) compile(key, position string) (compiled *CompiledSchema, outside string, err error) {
	target := jsonpointer.Format("operations", key, position)
	if err := o.check(target); err != nil {
		return nil, "", err
	}
	if o.compiler == nil {
		if err := o.prepare(); err != nil {
			return nil, "", err
		}
	}
	o.loader.external = ""
	schema, err := o.compiler.Compile(o.documentURL + "#" + fragment(target))
	if err != nil {
		return nil, o.loader.external, err
	}
	metaSchema, problem := o.inspect(schema)
	if problem != "" {
		return nil, metaSchema, errors.New(problem)
	}
	return &CompiledSchema{backend: schema}, metaSchema, nil
}

// prepare registers the document with a new compiler and compiles every
// embedded resource the resource limits admit, in order of $id.
func (o *operationContracts) prepare() error {
	o.documentURL = newDocumentURL()
	o.loader = &documentLoader{contracts: o}
	c := schemacompiler.New()
	c.UseLoader(o.loader)
	if err := c.AddResource(o.documentURL, o.container); err != nil {
		return err
	}
	for _, id := range slices.Sorted(maps.Keys(o.schemas.resources)) {
		location := o.schemas.resources[id].location
		if o.check(location) == nil {
			// A resource that does not compile is not learned; a graph that
			// reaches it reports why.
			_, _ = c.Compile(o.documentURL + "#" + fragment(location))
		}
	}
	o.compiler = c
	return nil
}

// check reports why the schema library must not be given the graph from a
// location: a value the library can reach from it holds a number beyond the
// numeric limits of schema evaluation, or nests deeper than schemaDepthLimit.
// Both are resource limits met before the library does the work (§10.5).
func (o *operationContracts) check(start string) error {
	for _, location := range o.reachable(start) {
		err, done := o.checked[location]
		if !done {
			value, _ := jsonpointer.Resolve(o.container, location)
			if at, limit := schemacompiler.NumericLimit(value); limit != nil {
				err = fmt.Errorf("the schema graph holds, at %s, %w", location+at, limit)
			} else if schemacompiler.Depth(value) > schemaDepthLimit {
				err = fmt.Errorf("the schema graph nests deeper than %d levels at %s", schemaDepthLimit, location)
			}
			o.checked[location] = err
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// reachable returns, in order of discovery, the locations of the values the
// schema library can reach when it compiles the schema at start: a superset,
// since every reference inside a reached value is followed, whether or not
// the library applies the keyword holding it. A same-document fragment names
// a location from the document root; an absolute reference to a resource the
// document embeds names that resource; a relative reference, which only a
// resource's base resolves, names every embedded resource. A location inside
// an embedded resource brings the whole resource, whose references resolve
// against its own base.
func (o *operationContracts) reachable(start string) []string {
	var locations []string
	seen := map[string]bool{}
	queue := []string{start}
	for len(queue) > 0 {
		location := queue[0]
		queue = queue[1:]
		if seen[location] {
			continue
		}
		seen[location] = true
		if _, ok := jsonpointer.Resolve(o.container, location); !ok {
			continue
		}
		locations = append(locations, location)
		queue = append(queue, o.referencedFrom(location)...)
		for _, id := range slices.Sorted(maps.Keys(o.schemas.resources)) {
			if resource := o.schemas.resources[id].location; strings.HasPrefix(location, resource+"/") {
				queue = append(queue, resource)
			}
		}
	}
	return locations
}

// referencedFrom returns the locations the $ref and $dynamicRef strings
// anywhere in the value at location name.
func (o *operationContracts) referencedFrom(location string) []string {
	if targets, ok := o.references[location]; ok {
		return targets
	}
	var targets []string
	value, _ := jsonpointer.Resolve(o.container, location)
	var walk func(node any)
	walk = func(node any) {
		switch node := node.(type) {
		case []any:
			for _, item := range node {
				walk(item)
			}
		case map[string]any:
			for _, key := range sortedKeys(node) {
				if ref, isString := node[key].(string); isString && (key == "$ref" || key == "$dynamicRef") {
					targets = append(targets, o.referenced(ref)...)
				}
				walk(node[key])
			}
		}
	}
	walk(value)
	o.references[location] = targets
	return targets
}

// referenced returns the locations a reference can name in the document.
func (o *operationContracts) referenced(ref string) []string {
	parsed, err := url.Parse(ref)
	if err != nil {
		return nil
	}
	switch {
	case strings.HasPrefix(ref, "#"):
		if parsed.Fragment == "" || strings.HasPrefix(parsed.Fragment, "/") {
			return []string{parsed.Fragment}
		}
		return nil // a plain-name anchor lies in the resource that holds it
	case parsed.IsAbs():
		parsed.Fragment, parsed.RawFragment = "", ""
		if resource, embedded := o.schemas.resources[parsed.String()]; embedded {
			return []string{resource.location}
		}
		return nil // outside the document
	default:
		var all []string
		for _, id := range slices.Sorted(maps.Keys(o.schemas.resources)) {
			all = append(all, o.schemas.resources[id].location)
		}
		return all
	}
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

// inspect walks a compiled schema graph for what the schema library does not
// judge itself, and returns a JSON Schema meta-schema the graph reaches and
// why the graph cannot be evaluated as the document holds it:
//   - a reached location the document does not hold;
//   - a schema §5.2's dialect constraints exclude: a $schema other than
//     2020-12, or a $vocabulary;
//   - a pattern Go's regexp cannot compile, which cannot be evaluated;
//   - a reference to a resource the document embeds under a meta-schema's
//     URI that the library resolved to the meta-schema instead;
//   - a cycle of references and in-place applicators that never advances
//     into the value, which the library reports only where evaluation meets
//     it, and then as a failed subschema a not or an anyOf can absorb.
//
// The walk visits the whole graph and reports the first meta-schema and the
// first problem in sorted order, so the answer does not depend on the order
// of Go map iteration.
func (o *operationContracts) inspect(root *jsonschema.Schema) (metaSchema, problem string) {
	var metaSchemas, problems []string
	seen := map[*jsonschema.Schema]bool{}
	inPlace := map[*jsonschema.Schema][]*jsonschema.Schema{}
	var visit func(s *jsonschema.Schema)
	visit = func(s *jsonschema.Schema) {
		if s == nil || seen[s] {
			return
		}
		seen[s] = true
		inPlace[s] = inPlaceSubschemas(s)
		resource, encoded, _ := strings.Cut(s.Location, "#")
		pointer, err := url.PathUnescape(encoded)
		if err != nil {
			problems = append(problems, fmt.Sprintf("the schema library reached %s, which is not a location", s.Location))
			return
		}
		var node any
		var held bool
		switch embedded, isEmbedded := o.schemas.resources[resource]; {
		case resource == o.documentURL:
			node, held = jsonpointer.Resolve(o.container, pointer)
		case o.schemas.shadowed[resource]:
			problems = append(problems, fmt.Sprintf("the schema library resolves %s to the JSON Schema meta-schema it carries, not to the schema the document embeds under that URI", resource))
			return
		case isEmbedded:
			node, held = jsonpointer.Resolve(embedded.schema, pointer)
		default:
			// A meta-schema: the loader obtains nothing else.
			metaSchemas = append(metaSchemas, resource)
			return
		}
		if !held {
			problems = append(problems, fmt.Sprintf("the schema library reached %s, which the document does not hold", s.Location))
			return
		}
		if object, ok := node.(map[string]any); ok {
			if dialect, present := object["$schema"]; present && dialect != draft202012URI {
				problems = append(problems, fmt.Sprintf("the schema at %s declares $schema %s, not %s", s.Location, describeJSON(dialect), draft202012URI))
			}
			if _, present := object["$vocabulary"]; present {
				problems = append(problems, fmt.Sprintf("the schema at %s declares $vocabulary", s.Location))
			}
		}
		patterns := []jsonschema.Regexp{s.Pattern}
		for pattern := range s.PatternProperties {
			patterns = append(patterns, pattern)
		}
		for _, pattern := range patterns {
			if uncompiled, ok := pattern.(schemacompiler.UncompiledPattern); ok {
				problems = append(problems, fmt.Sprintf("the pattern %q at %s cannot be evaluated: Go's regexp does not support it (%v)", uncompiled.Source, s.Location, uncompiled.Cause))
			}
		}
		for _, child := range subschemas(s) {
			visit(child)
		}
	}
	visit(root)
	if hasCycle(inPlace) {
		problems = append(problems, "a cycle of references never advances into the value, so no value can be evaluated against it")
	}
	if len(metaSchemas) > 0 {
		metaSchema = slices.Min(metaSchemas)
	}
	if len(problems) > 0 {
		problem = slices.Min(problems)
	}
	return metaSchema, problem
}

// inPlaceSubschemas returns the schemas a compiled schema applies to the
// value it evaluates itself, rather than to a member or item of it.
func inPlaceSubschemas(s *jsonschema.Schema) []*jsonschema.Schema {
	out := []*jsonschema.Schema{s.Ref, s.RecursiveRef, s.Not, s.If, s.Then, s.Else}
	if s.DynamicRef != nil {
		out = append(out, s.DynamicRef.Ref)
	}
	out = append(out, s.AllOf...)
	out = append(out, s.AnyOf...)
	out = append(out, s.OneOf...)
	for _, child := range s.DependentSchemas {
		out = append(out, child)
	}
	return out
}

// hasCycle reports whether a directed graph has a cycle.
func hasCycle(edges map[*jsonschema.Schema][]*jsonschema.Schema) bool {
	const (
		unvisited = iota
		active
		done
	)
	state := map[*jsonschema.Schema]int{}
	var visit func(node *jsonschema.Schema) bool
	visit = func(node *jsonschema.Schema) bool {
		switch state[node] {
		case active:
			return true
		case done:
			return false
		}
		state[node] = active
		for _, next := range edges[node] {
			if next != nil && visit(next) {
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

// subschemas returns the schemas a compiled schema applies or references.
func subschemas(s *jsonschema.Schema) []*jsonschema.Schema {
	out := []*jsonschema.Schema{s.Ref, s.RecursiveRef, s.Not, s.If, s.Then, s.Else,
		s.PropertyNames, s.UnevaluatedProperties, s.Contains, s.Items2020,
		s.UnevaluatedItems, s.ContentSchema}
	if s.DynamicRef != nil {
		out = append(out, s.DynamicRef.Ref)
	}
	out = append(out, s.AllOf...)
	out = append(out, s.AnyOf...)
	out = append(out, s.OneOf...)
	out = append(out, s.PrefixItems...)
	for _, child := range s.Properties {
		out = append(out, child)
	}
	for _, child := range s.PatternProperties {
		out = append(out, child)
	}
	for _, child := range s.DependentSchemas {
		out = append(out, child)
	}
	for _, value := range s.Dependencies {
		if child, ok := value.(*jsonschema.Schema); ok {
			out = append(out, child)
		}
	}
	for _, value := range []any{s.AdditionalProperties, s.AdditionalItems, s.Items} {
		switch child := value.(type) {
		case *jsonschema.Schema:
			out = append(out, child)
		case []*jsonschema.Schema:
			out = append(out, child...)
		}
	}
	return out
}

// newDocumentURL returns the base URI a compilation gives the OBI document:
// unique to that compilation, so no URI a document declares can collide with
// it. No reference resolves against the URI a document was fetched from (§7).
func newDocumentURL() string {
	var id [16]byte
	_, _ = rand.Read(id[:])
	return "urn:openbindings:document:" + hex.EncodeToString(id[:])
}

// withID returns a shallow copy of a resource registered by its absolute URI,
// whose $id is that URI. A nested resource's $id may be relative to the
// resource enclosing it, and would otherwise resolve against its own URI.
func withID(resource map[string]any, id string) map[string]any {
	copied := make(map[string]any, len(resource))
	for keyword, value := range resource {
		copied[keyword] = value
	}
	copied["$id"] = id
	return copied
}

// documentLoader gives the schema library the resources the document embeds
// by $id, wherever they are referenced from, and declines every other
// resource, as every SDK compiler does. external is the first it declined
// since it was last reset.
type documentLoader struct {
	contracts *operationContracts
	external  string
}

func (l *documentLoader) Load(url string) (any, error) {
	schemas := l.contracts.schemas
	if why := schemas.ambiguous[url]; why != "" {
		return nil, fmt.Errorf("%s names no one embedded schema: %s", url, why)
	}
	if resource, embedded := schemas.resources[url]; embedded {
		if err := l.contracts.check(resource.location); err != nil {
			return nil, err
		}
		return withID(resource.schema, url), nil
	}
	if l.external == "" {
		l.external = url
	}
	return nil, schemacompiler.RefuseExternal(url)
}
