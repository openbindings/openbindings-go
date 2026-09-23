package openbindings

import (
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	json "github.com/openbindings/openbindings-go/internal/thirdparty/jsoncodec"

	"github.com/openbindings/openbindings-go/internal/jsonpointer"
	"github.com/openbindings/openbindings-go/internal/schemacompiler"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// openbindingsSchemaJSON is the OBI document schema (openbindings.schema.json),
// embedded at build time. Synced from the spec repo via scripts/sync-schema.sh.
//
//go:embed openbindings.schema.json
var openbindingsSchemaJSON []byte

// compiledOBISchema is the embedded OBI document schema, compiled once at
// init, for OBI-D-02 (the document validates against openbindings.schema.json).
var compiledOBISchema *jsonschema.Schema

// compiledMetaSchema is the JSON Schema 2020-12 meta-schema, compiled once at
// init from the validator library's locally embedded copy (never fetched from
// the network, per OBI-D-17's validation note), for OBI-D-17 (every schema in
// the document is well-formed).
var compiledMetaSchema *jsonschema.Schema

func init() {
	var doc any
	if err := json.Unmarshal(openbindingsSchemaJSON, &doc); err != nil {
		panic(fmt.Sprintf("openbindings: embedded openbindings.schema.json is not valid JSON: %v", err))
	}
	c := schemacompiler.New()
	if err := c.AddResource("openbindings:///schema", doc); err != nil {
		panic(fmt.Sprintf("openbindings: cannot register OBI schema: %v", err))
	}
	s, err := c.Compile("openbindings:///schema")
	if err != nil {
		panic(fmt.Sprintf("openbindings: cannot compile OBI schema: %v", err))
	}
	compiledOBISchema = s

	meta, err := schemacompiler.New().Compile(draft202012URI)
	if err != nil {
		panic(fmt.Sprintf("openbindings: cannot compile embedded 2020-12 meta-schema: %v", err))
	}
	compiledMetaSchema = meta
}

// validateSchemaWellFormedness records OBI-D-17 violations at one schema
// position: the value must be a JSON Schema 2020-12 schema in object or
// boolean form, and the object form must validate against the 2020-12
// meta-schemas (which cover subschemas recursively). The check is
// deliberately narrow, mirroring §5.2: unknown keywords, unparseable
// `pattern` values, and unresolvable `$ref` targets all pass — they surface
// when the schema is used, not here. knownValid remembers schemas already
// found well-formed, by their encoding.
func validateSchemaWellFormedness(c *ruleChecks, prefix string, schema any, knownValid map[string]bool) {
	switch v := schema.(type) {
	case bool:
		// Boolean schemas are always well-formed.
	case map[string]any:
		key := metaSchemaCacheKey(v)
		if key != "" && knownValid[key] {
			return
		}
		if err := schemacompiler.NumericLimit(v); err != nil {
			c.inconclusive("OBI-D-17", prefix, fmt.Sprintf("could not be checked against the 2020-12 meta-schemas: %v", err))
			return
		}
		if verr := compiledMetaSchema.Validate(any(v)); verr != nil {
			problems, mismatch := schemacompiler.Outcome(verr)
			if !mismatch {
				c.inconclusive("OBI-D-17", prefix, fmt.Sprintf("could not be checked against the 2020-12 meta-schemas: %v", verr))
				return
			}
			for _, problem := range problems {
				c.violated("OBI-D-17", prefix+jsonpointer.Format(problem.Location...), "not a well-formed JSON Schema 2020-12 schema: "+problem.Message)
			}
		} else if key != "" {
			knownValid[key] = true
		}
	default:
		c.violated("OBI-D-17", prefix, fmt.Sprintf("a schema is a JSON Schema 2020-12 object or boolean; got %s", jsonTypeName(v)))
	}
}

// validateAgainstOBISchema records OBI-D-02 evidence: whether the document's
// generic view validates against openbindings.schema.json.
func validateAgainstOBISchema(c *ruleChecks, view any) {
	if err := schemacompiler.NumericLimit(view); err != nil {
		c.inconclusive("OBI-D-02", "", fmt.Sprintf("could not be checked against the document schema: %v", err))
		return
	}
	if verr := compiledOBISchema.Validate(view); verr != nil {
		problems, mismatch := schemacompiler.Outcome(verr)
		if !mismatch {
			// An exceeded resource limit is not evidence of violation (§10.5).
			c.inconclusive("OBI-D-02", "", fmt.Sprintf("could not be checked against the document schema: %v", verr))
			return
		}
		for _, problem := range problems {
			c.violated("OBI-D-02", jsonpointer.Format(problem.Location...), "does not validate against the document schema: "+problem.Message)
		}
	}
}

func metaSchemaCacheKey(schema map[string]any) string {
	data, err := json.Marshal(schema)
	if err != nil {
		return ""
	}
	return string(data)
}

// checkExamples records OBI-D-11 evidence: every provided example value
// (including an explicit JSON null) must validate against its operation's
// corresponding schema, where that schema is specified and the schema graph
// statically reachable from it resolves entirely within the document.
//
// The rule's scope is decided per schema position. A graph that reaches an
// external resource puts that position's examples outside the rule, so they
// are neither checked nor reported (§5.1 lets a tool check them as evidence,
// never as non-conformance). A graph that cannot be evaluated (an
// unresolvable reference, an ill-formed schema, or a failure to compile or
// evaluate it) leaves the rule inconclusive there: none of that is evidence
// that the examples conform. Operations and examples that are not objects
// hold no example values.
func checkExamples(c *ruleChecks, view any, operations map[string]any, schemas documentSchemas) {
	contracts := newOperationContracts(view, schemas)
	for _, opKey := range sortedKeys(operations) {
		operation, _ := operations[opKey].(map[string]any)
		examples, _ := operation["examples"].(map[string]any)
		for _, position := range []string{"input", "output"} {
			if _, specified := operation[position]; !specified {
				continue
			}
			var provided []string
			for _, exampleKey := range sortedKeys(examples) {
				if example, ok := examples[exampleKey].(map[string]any); ok && hasKey(example, position) {
					provided = append(provided, exampleKey)
				}
			}
			if len(provided) == 0 {
				continue
			}
			path := jsonpointer.Format("operations", opKey, position)
			compiled, outside, err := contracts.compile(opKey, position)
			if outside != "" {
				// The graph reaches outside the document.
				continue
			}
			if err != nil {
				c.inconclusive("OBI-D-11", path, fmt.Sprintf("the schema graph could not be evaluated, so its examples were not checked: %v", err))
				continue
			}
			for _, exampleKey := range provided {
				examplePath := jsonpointer.Format("operations", opKey, "examples", exampleKey, position)
				var mismatch *SchemaValidationError
				switch err := compiled.Validate(examples[exampleKey].(map[string]any)[position]); {
				case err == nil:
				case errors.As(err, &mismatch):
					for _, problem := range mismatch.Problems {
						c.violated("OBI-D-11", examplePath+problem.Path, "does not validate against the operation's "+position+" schema: "+problem.Message)
					}
				default:
					c.inconclusive("OBI-D-11", examplePath, fmt.Sprintf("the schema could not be evaluated, so this example was not checked: %v", err))
				}
			}
		}
	}
}

// CompileOperationSchema compiles an operation's input or output schema, for
// validating values against the operation's contract (OBI-T-16). The
// operation is named by any of its identifiers, its key or an alias
// (OBI-T-12). The OBI document is the resolution root of same-document
// references (§7), and only the schemas the document holds are schemas: an
// unknown document member never acts as a schema keyword or declares a
// resource.
//
// The schema graph statically reachable from the operation's schema must be
// complete: a graph that reaches a resource the document does not embed, has
// a reference that does not resolve, or holds a schema that is not
// well-formed yields a *SchemaGraphUnavailableError, even where no value
// would exercise that part of it. A JSON Schema meta-schema is outside the
// document but available: the schema library carries it. A document is
// interpreted only under a supported version: one declaring a well-formed
// version outside the supported set returns a *VersionRefusalError (OBI-T-04), and one declaring
// no valid version returns an error (OBI-D-12). Any other error means nothing
// was compiled: there is no interface, the name resolves to no one operation
// (wrapping ErrOperationNotFound), the operation specifies no schema at that
// position, or the interface cannot be encoded.
func CompileOperationSchema(i *Interface, operation, position string) (*CompiledSchema, error) {
	if i == nil {
		return nil, errors.New("openbindings: interface is nil")
	}
	if refusal := versionRefusalOf(i.OpenBindings); refusal != nil {
		return nil, refusal
	}
	if !IsValidSemver(i.OpenBindings) {
		return nil, fmt.Errorf("openbindings: the document declares no valid version (%q is not SemVer 2.0.0, OBI-D-12), so it is not interpreted", i.OpenBindings)
	}
	key, resolved, ok := ResolveOperation(i, operation)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrOperationNotFound, operation)
	}
	var schema JSONSchema
	switch position {
	case "input":
		schema = resolved.Input
	case "output":
		schema = resolved.Output
	default:
		return nil, fmt.Errorf("openbindings: unknown operation schema position %q", position)
	}
	if schema == nil {
		return nil, fmt.Errorf("openbindings: operation %q specifies no %s schema", key, position)
	}
	view, err := documentView(*i)
	if err != nil {
		return nil, err
	}
	compiled, _, err := newOperationContracts(view, collectDocumentSchemas(view)).compile(key, position)
	if err != nil {
		return nil, &SchemaGraphUnavailableError{Cause: err}
	}
	return compiled, nil
}

// operationContracts is what the schema library is given of one document to
// evaluate its operations' schemas. A same-document fragment is a JSON
// Pointer from the OBI document root, which is not a schema (§7), so the
// library is given the document without the root members that would act as
// schema keywords there: its maps (schemas, operations, and the rest) and its
// extensions, each at the location it holds, so every reference means what
// its author wrote. The document's scalar members are never schemas, and an
// unknown member is ignored (OBI-T-02), so it never acts as a schema keyword
// or declares a resource.
type operationContracts struct {
	schemas   documentSchemas
	container map[string]any
}

// documentMaps are the OBI root members that hold the document's content.
var documentMaps = map[string]bool{
	"schemas": true, "operations": true, "dependencies": true,
	"sources": true, "bindings": true, "transforms": true,
}

func newOperationContracts(view any, schemas documentSchemas) operationContracts {
	root, _ := view.(map[string]any)
	container := map[string]any{}
	for name, value := range root {
		if documentMaps[name] || strings.HasPrefix(name, "x-") {
			container[name] = value
		}
	}
	return operationContracts{schemas: schemas, container: container}
}

// compile compiles the schema at an operation's input or output, given its
// canonical key. outside names a resource outside the document the graph
// reaches: an external one, which the SDK does not obtain, so err reports the
// graph unavailable; or a JSON Schema meta-schema, which the schema library
// carries.
func (o operationContracts) compile(key, position string) (compiled *CompiledSchema, outside string, err error) {
	if err := schemacompiler.NumericLimit(o.container); err != nil {
		return nil, "", fmt.Errorf("the document holds %w", err)
	}
	documentURL := newDocumentURL()
	loader := &documentLoader{schemas: o.schemas}
	c := schemacompiler.New()
	c.UseLoader(loader)
	if err := c.AddResource(documentURL, o.container); err != nil {
		return nil, "", err
	}
	// The library learns that a location declares a resource when it compiles
	// that location. Compiling each embedded resource first gives a location
	// inside one that resource's base however a reference reaches it (§7). A
	// resource that does not compile is not learned; a graph that reaches it
	// reports why.
	for _, id := range slices.Sorted(maps.Keys(o.schemas.resources)) {
		_, _ = c.Compile(documentURL + "#" + o.schemas.resources[id].location)
	}
	loader.external = ""
	schema, err := c.Compile(documentURL + "#" + jsonpointer.Format("operations", key, position))
	if err != nil {
		return nil, loader.external, err
	}
	metaSchema, problem := o.inspect(schema, documentURL)
	if problem != "" {
		return nil, metaSchema, errors.New(problem)
	}
	return &CompiledSchema{backend: schema}, metaSchema, nil
}

// inspect walks a compiled schema graph for what the schema library does not
// judge itself. It returns a JSON Schema meta-schema the graph reaches, and
// states why the graph cannot be evaluated as the document holds it: a
// schema §5.2's dialect constraints exclude (a $schema other than 2020-12, or
// a $vocabulary); a reference to a resource the document embeds under a
// meta-schema's URI, which the library resolves to the meta-schema instead;
// or a cycle of references and in-place applicators that never advances into
// the value, which the library reports only where evaluation meets it, and
// then as a failed subschema a not or an anyOf can absorb.
func (o operationContracts) inspect(root *jsonschema.Schema, documentURL string) (metaSchema, problem string) {
	seen := map[*jsonschema.Schema]bool{}
	inPlace := map[*jsonschema.Schema][]*jsonschema.Schema{}
	var visit func(s *jsonschema.Schema)
	visit = func(s *jsonschema.Schema) {
		if s == nil || seen[s] || problem != "" {
			return
		}
		seen[s] = true
		inPlace[s] = inPlaceSubschemas(s)
		resource, pointer, _ := strings.Cut(s.Location, "#")
		var node any
		switch embedded, isEmbedded := o.schemas.resources[resource]; {
		case resource == documentURL:
			node, _ = jsonpointer.Resolve(o.container, pointer)
		case o.schemas.shadowed[resource]:
			problem = fmt.Sprintf("the schema library resolves %s to the JSON Schema meta-schema it carries, not to the schema the document embeds under that URI", resource)
			return
		case isEmbedded:
			node, _ = jsonpointer.Resolve(embedded.schema, pointer)
		default:
			// A meta-schema: the loader obtains nothing else.
			if metaSchema == "" {
				metaSchema = resource
			}
			return
		}
		if object, ok := node.(map[string]any); ok {
			if dialect, present := object["$schema"]; present && dialect != draft202012URI {
				problem = fmt.Sprintf("the schema at %s declares $schema %s, not %s", s.Location, describeJSON(dialect), draft202012URI)
				return
			}
			if _, present := object["$vocabulary"]; present {
				problem = fmt.Sprintf("the schema at %s declares $vocabulary", s.Location)
				return
			}
		}
		for _, child := range subschemas(s) {
			visit(child)
		}
	}
	visit(root)
	if problem == "" && hasCycle(inPlace) {
		problem = "a cycle of references never advances into the value, so no value can be evaluated against it"
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
// resource, as every SDK compiler does. external is the first it declined.
type documentLoader struct {
	schemas  documentSchemas
	external string
}

func (l *documentLoader) Load(url string) (any, error) {
	if why := l.schemas.ambiguous[url]; why != "" {
		return nil, fmt.Errorf("%s names no one embedded schema: %s", url, why)
	}
	if resource, embedded := l.schemas.resources[url]; embedded {
		return withID(resource.schema, url), nil
	}
	if l.external == "" {
		l.external = url
	}
	return nil, schemacompiler.RefuseExternal(url)
}

// ValidateOperationInput validates a value against an operation's input
// schema, with the complete OBI document as the resolution root (OBI-T-16).
//
// A nil error means the value validates. A *SchemaValidationError is an
// established mismatch; a *SchemaGraphUnavailableError means the schema's
// graph could not be fully resolved, so no verdict was reached. Any other
// error means nothing was validated: see CompileOperationSchema.
func ValidateOperationInput(value any, iface *Interface, operationName string) error {
	compiled, err := CompileOperationSchema(iface, operationName, "input")
	if err != nil {
		return err
	}
	return compiled.Validate(value)
}

// ValidateOperationOutput validates a value against an operation's output
// schema, as ValidateOperationInput does against its input schema.
func ValidateOperationOutput(value any, iface *Interface, operationName string) error {
	compiled, err := CompileOperationSchema(iface, operationName, "output")
	if err != nil {
		return err
	}
	return compiled.Validate(value)
}

// SchemaGraphUnavailableError reports that no verdict was reached because the
// governing schema's complete statically reachable graph was not available,
// well-formed, and evaluable. It is distinct from a mismatch, as OBI-T-16
// requires of validation against an operation's contract.
//
// Callers can use errors.As rather than parsing diagnostic text. Cause remains
// available through errors.Unwrap for validator-specific diagnostics.
type SchemaGraphUnavailableError struct {
	Cause error
}

func (e *SchemaGraphUnavailableError) Error() string {
	if e == nil || e.Cause == nil {
		return "openbindings: schema graph unavailable"
	}
	return fmt.Sprintf("openbindings: schema graph unavailable: %v", e.Cause)
}

func (e *SchemaGraphUnavailableError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// SchemaValidationError is an established mismatch between a value and a
// schema. Use errors.As to distinguish it from a *SchemaGraphUnavailableError,
// where no verdict was reached; OBI-T-16 requires the two outcomes to stay
// distinct when a value is validated against an operation's contract. Cause
// is the validator's own error.
type SchemaValidationError struct {
	Problems []SchemaProblem
	Cause    error
}

// SchemaProblem is one failed constraint of a mismatch. Path locates it in the
// validated value as an RFC 6901 JSON Pointer; the empty pointer is the whole
// value.
type SchemaProblem struct {
	Path    string
	Message string
}

func (e *SchemaValidationError) Error() string {
	if e == nil || len(e.Problems) == 0 {
		return "openbindings: the value does not validate against the schema"
	}
	lines := make([]string, len(e.Problems))
	for i, problem := range e.Problems {
		if problem.Path == "" {
			lines[i] = problem.Message
		} else {
			lines[i] = problem.Path + ": " + problem.Message
		}
	}
	return strings.Join(lines, "; ")
}

func (e *SchemaValidationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// schemaValidationError projects a backend validation error onto the SDK's
// outcomes: a *SchemaValidationError for an established mismatch, and a
// *SchemaGraphUnavailableError for anything that reached no verdict.
func schemaValidationError(err error) error {
	if err == nil {
		return nil
	}
	problems, mismatch := schemacompiler.Outcome(err)
	if !mismatch {
		return &SchemaGraphUnavailableError{Cause: err}
	}
	out := &SchemaValidationError{Problems: make([]SchemaProblem, len(problems)), Cause: err}
	for i, problem := range problems {
		out.Problems[i] = SchemaProblem{Path: jsonpointer.Format(problem.Location...), Message: problem.Message}
	}
	return out
}
