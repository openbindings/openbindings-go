package openbindings

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

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
//
// The meta-schema validator's work grows faster than linearly with a
// schema's depth, so a schema holding a number beyond the numeric limits of
// schema evaluation, or nesting subschemas deeper than schemaDepthLimit, meets
// a resource limit and leaves the rule inconclusive there (§10.5), as it does
// for an operation's schema graph.
func validateSchemaWellFormedness(c *ruleChecks, prefix string, schema any, knownValid map[string]bool) {
	switch v := schema.(type) {
	case bool:
		// Boolean schemas are always well-formed.
	case map[string]any:
		key := metaSchemaCacheKey(v)
		if key != "" && knownValid[key] {
			return
		}
		if at, err := schemacompiler.NumericLimit(v); err != nil {
			c.inconclusive("OBI-D-17", prefix+at, fmt.Sprintf("could not be checked against the 2020-12 meta-schemas: %v", err))
			return
		}
		if schemaDepth(v) > schemaDepthLimit {
			c.inconclusive("OBI-D-17", prefix, fmt.Sprintf("could not be checked against the 2020-12 meta-schemas: it nests subschemas deeper than %d levels", schemaDepthLimit))
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

// numericMembers are the members of an OBI document the document schema does
// numeric work on, as reference tokens where "*" is every entry of a map: a
// binding's preference, held to its integer range (§5.3), and the items of an
// operation's aliases and of a dependency's bindingSpecs, which uniqueItems
// compares as numbers when they are numbers.
// TestDocumentSchema_NumericWorkIsOnNumericMembers holds this list to the
// embedded schema.
var numericMembers = [][]string{
	{"bindings", "*", "preference"},
	{"operations", "*", "aliases"},
	{"dependencies", "*", "bindingSpecs"},
}

// validateAgainstOBISchema records OBI-D-02 evidence: whether the document's
// generic view validates against openbindings.schema.json.
//
// The schema library is not handed a number beyond the numeric limits of
// schema evaluation where the document schema does numeric work
// (numericMembers). A member holding one is set aside, and the rest of the
// document is still checked: a preference is decided here exactly, and an
// array is left inconclusive.
func validateAgainstOBISchema(c *ruleChecks, view any) {
	var setAside [][]string
	for _, pattern := range numericMembers {
		for _, member := range membersAt(view, pattern, nil) {
			at, err := schemacompiler.NumericLimit(member.value)
			if err == nil {
				continue
			}
			path := jsonpointer.Format(member.tokens...)
			if member.tokens[len(member.tokens)-1] == "preference" {
				inRange := false
				if number, isNumber := member.value.(json.Number); isNumber {
					_, inRange = preferenceValue(string(number))
				}
				if !inRange {
					c.violated("OBI-D-02", path, fmt.Sprintf("does not validate against the document schema: a preference is an integer from -%d through %d", maxPreference, maxPreference))
				}
			} else {
				c.inconclusive("OBI-D-02", path, fmt.Sprintf("could not be checked against the document schema: it holds, at %q, %v", at, err))
			}
			setAside = append(setAside, member.tokens)
		}
	}
	if verr := compiledOBISchema.Validate(withoutMembers(view, setAside)); verr != nil {
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

// member is a member of a document's generic view and where it is.
type member struct {
	tokens []string
	value  any
}

// membersAt returns the members of view that pattern names, where "*" is
// every entry of an object, in sorted order. at is the reference tokens of
// view itself.
func membersAt(view any, pattern, at []string) []member {
	if len(pattern) == 0 {
		return []member{{tokens: at, value: view}}
	}
	object, _ := view.(map[string]any)
	names := []string{pattern[0]}
	if pattern[0] == "*" {
		names = sortedKeys(object)
	}
	var out []member
	for _, name := range names {
		if value, present := object[name]; present {
			out = append(out, membersAt(value, pattern[1:], append(slices.Clip(at), name))...)
		}
	}
	return out
}

// withoutMembers returns view without the members at each of paths, copying
// each object on the way to them once, so view itself is not changed and the
// work is linear in the objects copied.
func withoutMembers(view any, paths [][]string) any {
	if len(paths) == 0 {
		return view
	}
	object, _ := view.(map[string]any)
	removed := map[string]bool{}
	within := map[string][][]string{}
	for _, tokens := range paths {
		if len(tokens) == 1 {
			removed[tokens[0]] = true
		} else {
			within[tokens[0]] = append(within[tokens[0]], tokens[1:])
		}
	}
	copied := make(map[string]any, len(object))
	for name, value := range object {
		switch {
		case removed[name]:
		case within[name] != nil:
			copied[name] = withoutMembers(value, within[name])
		default:
			copied[name] = value
		}
	}
	return copied
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
	o := newOperationSchemas(view, schemas)
	type position struct {
		path, name string
		examples   map[string]any
		provided   []string
	}
	var candidates []position
	var paths []string
	for _, opKey := range sortedKeys(operations) {
		operation, _ := operations[opKey].(map[string]any)
		examples, _ := operation["examples"].(map[string]any)
		for _, name := range []string{"input", "output"} {
			if _, specified := operation[name]; !specified {
				continue
			}
			var provided []string
			for _, exampleKey := range sortedKeys(examples) {
				if example, ok := examples[exampleKey].(map[string]any); ok && hasKey(example, name) {
					provided = append(provided, exampleKey)
				}
			}
			if len(provided) == 0 {
				continue
			}
			path := jsonpointer.Format("operations", opKey, name)
			candidates = append(candidates, position{path: path, name: name, examples: examples, provided: provided})
			paths = append(paths, path)
		}
	}
	o.analyze(paths)
	var checked []position
	var starts []string
	for _, p := range candidates {
		if f := o.facts(p.path); f.outside != "" || f.metaSchema != "" {
			// The graph reaches outside the document.
			continue
		}
		if problem := o.graphProblem(p.path); problem != "" {
			c.inconclusive("OBI-D-11", p.path, "the schema graph could not be evaluated, so its examples were not checked: "+problem)
			continue
		}
		checked = append(checked, p)
		starts = append(starts, p.path)
	}
	results := o.compile(starts)
	for _, p := range checked {
		result := results[p.path]
		if result.err != nil {
			c.inconclusive("OBI-D-11", p.path, fmt.Sprintf("the schema graph could not be evaluated, so its examples were not checked: %v", result.err))
			continue
		}
		for _, exampleKey := range p.provided {
			examplePath := p.path[:strings.LastIndexByte(p.path, '/')] + jsonpointer.Format("examples", exampleKey, p.name)
			var mismatch *SchemaValidationError
			switch err := result.schema.Validate(p.examples[exampleKey].(map[string]any)[p.name]); {
			case err == nil:
			case errors.As(err, &mismatch):
				for _, problem := range mismatch.Problems {
					c.violated("OBI-D-11", examplePath+problem.Path, "does not validate against the operation's "+p.name+" schema: "+problem.Message)
				}
			default:
				c.inconclusive("OBI-D-11", examplePath, fmt.Sprintf("this example could not be checked: %v", err))
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
// would exercise that part of it. The schema library is given the schemas the
// graph uses as a JSON Schema 2020-12 bundle, never the OBI document itself,
// and evaluates strictly as 2020-12: dependencies, $recursiveRef, and
// $recursiveAnchor constrain nothing. A JSON Schema meta-schema is outside the
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
	key, target, ok := ResolveOperation(i, operation)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrOperationNotFound, operation)
	}
	var schema JSONSchema
	switch position {
	case "input":
		schema = target.Input
	case "output":
		schema = target.Output
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
	o := newOperationSchemas(view, collectDocumentSchemas(view))
	path := jsonpointer.Format("operations", key, position)
	o.analyze([]string{path})
	if problem := o.graphProblem(path); problem != "" {
		return nil, &SchemaGraphUnavailableError{Cause: errors.New(problem)}
	}
	result := o.compileAlone(path)
	if result.err != nil {
		return nil, &SchemaGraphUnavailableError{Cause: result.err}
	}
	return result.schema, nil
}

// ValidateOperationInput validates a value against an operation's input
// schema, with the complete OBI document as the resolution root (OBI-T-16).
// It compiles the document's schemas on every call; to validate many values,
// compile once with CompileOperationSchema and use CompiledSchema.Validate.
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
