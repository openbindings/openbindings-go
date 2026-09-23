package openbindings

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

	// Memoized per location: the graph walked from it, what the positions
	// under it reference, the resource-limit check of its value, and the
	// resource holding it.
	graphs     map[string]schemaGraph
	references map[string]localReferences
	checked    map[string]error
	resources  map[string]string
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
		graphs:     map[string]schemaGraph{},
		references: map[string]localReferences{},
		checked:    map[string]error{},
		resources:  map[string]string{},
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
//
// The graph is walked before the library is given it (see graph), so what it
// reaches is known however far the library compiles: the library skips a
// position no evaluation can apply, such as a then with no if, which §5.2
// still counts.
func (o *operationContracts) compile(key, position string) (compiled *CompiledSchema, outside string, err error) {
	target := jsonpointer.Format("operations", key, position)
	g := o.graph(target)
	switch {
	case g.external:
		return nil, g.outside, fmt.Errorf("the schema graph reaches %s, which the document does not embed", g.outside)
	case g.problem != "":
		return nil, g.outside, errors.New(g.problem)
	}
	if err := o.check(g); err != nil {
		return nil, g.outside, err
	}
	if o.compiler == nil {
		if err := o.prepare(); err != nil {
			return nil, g.outside, err
		}
	}
	o.loader.external = ""
	schema, err := o.compiler.Compile(o.documentURL + "#" + fragment(target))
	if err != nil {
		if invalid := (*jsonschema.SchemaValidationError)(nil); errors.As(err, &invalid) {
			err = fmt.Errorf("the schema at %s is not a well-formed JSON Schema 2020-12 schema: %w", o.documentLocation(invalid.URL), invalid.Err)
		}
		return nil, cmp.Or(g.outside, o.loader.external), err
	}
	metaSchema, problem := o.inspect(schema)
	outside = cmp.Or(g.outside, metaSchema)
	if problem != "" {
		return nil, outside, errors.New(problem)
	}
	return &CompiledSchema{backend: schema}, outside, nil
}

// prepare registers the document with a new compiler and compiles every
// embedded resource the resource limits admit, in order of $id.
func (o *operationContracts) prepare() error {
	o.documentURL = newDocumentURL(o.container)
	o.loader = &documentLoader{contracts: o}
	c := schemacompiler.New()
	c.UseLoader(o.loader)
	if err := c.AddResource(o.documentURL, o.container); err != nil {
		return err
	}
	for _, id := range slices.Sorted(maps.Keys(o.schemas.resources)) {
		location := o.schemas.resources[id].location
		if o.check(o.graph(location)) == nil {
			// A resource that does not compile is not learned; a graph that
			// reaches it reports why.
			_, _ = c.Compile(o.documentURL + "#" + fragment(location))
		}
	}
	o.compiler = c
	return nil
}

// check reports why the schema library must not be given a graph: a value it
// is handed holds a number beyond the numeric limits of schema evaluation, or
// nests deeper than schemaDepthLimit. Both are resource limits met before the
// library does the work (§10.5). Each entry is checked whole, since the
// library checks a value it reaches against the meta-schemas whole.
func (o *operationContracts) check(g schemaGraph) error {
	for _, location := range g.entries {
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

// schemaGraph is what a walk of the schema graph statically reachable from
// one location finds (§5.2): every position evaluation can apply, whatever an
// if would select, and the targets of their references, transitively. A
// definition in $defs belongs to the graph only when a reference reaches it
// (T16-S-04 of the core conformance corpus).
type schemaGraph struct {
	// entries are the locations the library is handed as schemas: the start,
	// every reference target, and the embedded resource around each, sorted.
	entries []string
	// outside is the first resource outside the document the graph reaches,
	// in sorted order. external is true unless it is a JSON Schema
	// meta-schema the library carries.
	outside  string
	external bool
	// problem states the first reference, in sorted order, that names no
	// schema the document holds.
	problem string
}

// graph walks the schema graph statically reachable from a location of the
// container. References resolve as §7 and JSON Schema 2020-12 resolve them: a
// same-document fragment from the document root, or from the embedded
// resource holding the reference, and an absolute reference by the $id of a
// resource the document embeds.
func (o *operationContracts) graph(start string) schemaGraph {
	if g, ok := o.graphs[start]; ok {
		return g
	}
	entries := map[string]bool{}
	var metaSchemas, externals, problems []string
	queue := []string{start}
	for len(queue) > 0 {
		entry := queue[0]
		queue = queue[1:]
		if entries[entry] {
			continue
		}
		entries[entry] = true
		if resource := o.resourceAt(entry); resource != "" {
			entries[o.schemas.resources[resource].location] = true
		}
		local := o.referencesUnder(entry)
		metaSchemas = append(metaSchemas, local.metaSchemas...)
		externals = append(externals, local.externals...)
		problems = append(problems, local.problems...)
		queue = append(queue, local.targets...)
	}
	g := schemaGraph{entries: slices.Sorted(maps.Keys(entries))}
	switch {
	case len(externals) > 0:
		g.outside, g.external = slices.Min(externals), true
	case len(metaSchemas) > 0:
		g.outside = slices.Min(metaSchemas)
	}
	if len(problems) > 0 {
		g.problem = slices.Min(problems)
	}
	o.graphs[start] = g
	return g
}

// localReferences is what the references at the positions under one location
// resolve to, not following them.
type localReferences struct {
	targets                          []string
	metaSchemas, externals, problems []string
}

// referencesUnder walks the positions under a location that evaluation can
// apply (every subschema but a definition, which applies only through a
// reference) and resolves the references there.
func (o *operationContracts) referencesUnder(location string) localReferences {
	if local, ok := o.references[location]; ok {
		return local
	}
	var local localReferences
	var walk func(location string)
	walk = func(location string) {
		node, _ := jsonpointer.Resolve(o.container, location)
		object, ok := node.(map[string]any)
		if !ok {
			return
		}
		for _, keyword := range []string{"$ref", "$dynamicRef"} {
			ref, isString := object[keyword].(string)
			if !isString {
				continue
			}
			switch target, outside, external, problem := o.resolve(ref, location); {
			case problem != "":
				local.problems = append(local.problems, fmt.Sprintf("the %s %q at %s %s", keyword, ref, location, problem))
			case external:
				local.externals = append(local.externals, outside)
			case outside != "":
				local.metaSchemas = append(local.metaSchemas, outside)
			default:
				local.targets = append(local.targets, target)
			}
		}
		forEachSubschema(object, func(_ any, tokens ...string) {
			if tokens[0] != "$defs" && tokens[0] != "definitions" {
				walk(location + jsonpointer.Format(tokens...))
			}
		})
	}
	walk(location)
	o.references[location] = local
	return local
}

// resolve resolves a reference held at a location: to the location it names
// in the container, to a resource outside the document (external unless the
// library carries it as a meta-schema), or to why it names no schema the
// document holds.
func (o *operationContracts) resolve(ref, at string) (target, outside string, external bool, problem string) {
	parsed, err := url.Parse(ref)
	if err != nil {
		return "", "", false, "is not a URI reference"
	}
	id, fragment := o.resourceAt(at), parsed.Fragment
	if !strings.HasPrefix(ref, "#") {
		if !parsed.IsAbs() {
			if id == "" {
				return "", "", false, "is relative, with no base to resolve against"
			}
			base, _ := url.Parse(id)
			parsed = base.ResolveReference(parsed)
		}
		fragment = parsed.Fragment
		parsed.Fragment, parsed.RawFragment = "", ""
		id = parsed.String()
	}
	if id == "" {
		// The document root, which declares no anchors (§7).
		if fragment != "" && !strings.HasPrefix(fragment, "/") {
			return "", "", false, "is a plain-name fragment, which no schema at an OBI position declares"
		}
		if _, ok := jsonpointer.Resolve(o.container, fragment); !ok {
			return "", "", false, "does not resolve within the document"
		}
		return fragment, "", false, ""
	}
	if why := o.schemas.ambiguous[id]; why != "" {
		return "", "", false, fmt.Sprintf("names no one embedded schema: %s", why)
	}
	resource, embedded := o.schemas.resources[id]
	if !embedded {
		return "", id, !isBuiltInMetaSchema(id), ""
	}
	within := fragment
	if fragment != "" && !strings.HasPrefix(fragment, "/") {
		switch anchors := anchorLocations(resource.schema, fragment); len(anchors) {
		case 1:
			within = anchors[0]
		case 0:
			return "", "", false, fmt.Sprintf("names an anchor %s does not declare", id)
		default:
			return "", "", false, fmt.Sprintf("names an anchor more than one schema in %s declares", id)
		}
	}
	target = resource.location + within
	if _, ok := jsonpointer.Resolve(o.container, target); !ok {
		return "", "", false, fmt.Sprintf("does not resolve within %s", id)
	}
	return target, "", false, ""
}

// resourceAt returns the $id of the innermost resource the document embeds
// that holds a location, or "" for none: the base of a same-document fragment
// held there.
func (o *operationContracts) resourceAt(location string) string {
	if id, ok := o.resources[location]; ok {
		return id
	}
	innermost, depth := "", -1
	for id, resource := range o.schemas.resources {
		if (location == resource.location || strings.HasPrefix(location, resource.location+"/")) && len(resource.location) > depth {
			innermost, depth = id, len(resource.location)
		}
	}
	o.resources[location] = innermost
	return innermost
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
			problems = append(problems, fmt.Sprintf("the schema library reached %s, which the document does not hold", o.documentLocation(s.Location)))
			return
		}
		if object, ok := node.(map[string]any); ok {
			if dialect, present := object["$schema"]; present && dialect != draft202012URI {
				problems = append(problems, fmt.Sprintf("the schema at %s declares $schema %s, not %s", o.documentLocation(s.Location), describeJSON(dialect), draft202012URI))
			}
			if _, present := object["$vocabulary"]; present {
				problems = append(problems, fmt.Sprintf("the schema at %s declares $vocabulary", o.documentLocation(s.Location)))
			}
		}
		patterns := []jsonschema.Regexp{s.Pattern}
		for pattern := range s.PatternProperties {
			patterns = append(patterns, pattern)
		}
		for _, pattern := range patterns {
			if uncompiled, ok := pattern.(schemacompiler.UncompiledPattern); ok {
				problems = append(problems, fmt.Sprintf("the pattern %q at %s cannot be evaluated: Go's regexp does not support it (%v)", uncompiled.Source, o.documentLocation(s.Location), uncompiled.Cause))
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

// newDocumentURL returns the base URI the schema library is given for the
// OBI document, derived from the document's content: the same document gets
// the same base on every run, and no URI a document declares can equal it,
// since that would take a document holding a hash of itself. No reference
// resolves against the URI a document was fetched from (§7).
func newDocumentURL(container map[string]any) string {
	encoded, _ := json.Marshal(container) // decoded JSON always encodes
	sum := sha256.Sum256(encoded)
	return "urn:openbindings:document:" + hex.EncodeToString(sum[:16])
}

// documentLocation states where a location the schema library reports lies:
// a JSON Pointer into the document, or the URI of a resource outside it.
func (o *operationContracts) documentLocation(location string) string {
	resource, encoded, _ := strings.Cut(location, "#")
	pointer, err := url.PathUnescape(encoded)
	if err != nil {
		return location
	}
	switch embedded, isEmbedded := o.schemas.resources[resource]; {
	case resource == o.documentURL:
		return pointer
	case isEmbedded:
		return embedded.location + pointer
	}
	return location
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
		if err := l.contracts.check(l.contracts.graph(resource.location)); err != nil {
			return nil, err
		}
		return withID(resource.schema, url), nil
	}
	if l.external == "" {
		l.external = url
	}
	return nil, schemacompiler.RefuseExternal(url)
}
