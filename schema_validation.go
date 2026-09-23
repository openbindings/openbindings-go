package openbindings

import (
	_ "embed"
	"errors"
	"fmt"
	"sort"
	"strings"

	json "github.com/openbindings/openbindings-go/internal/thirdparty/jsoncodec"

	"github.com/openbindings/openbindings-go/internal/jsonpointer"
	"github.com/openbindings/openbindings-go/internal/schemacompiler"
	"github.com/openbindings/openbindings-go/internal/thirdparty/jsonschema"
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
		if verr := compiledMetaSchema.Validate(any(v)); verr != nil {
			for _, problem := range schemacompiler.Problems(verr) {
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
	if verr := compiledOBISchema.Validate(view); verr != nil {
		for _, problem := range schemacompiler.Problems(verr) {
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
// never as non-conformance). A graph whose reach this validator cannot
// establish, or a schema it cannot compile, leaves the rule inconclusive there:
// neither is evidence that the examples conform. Operations and examples that
// are not objects are left to the rules that judge their shape.
func checkExamples(c *ruleChecks, view any, operations map[string]any) {
	var schemas *documentSchemas
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
			if schemas == nil {
				collected := collectDocumentSchemas(view)
				schemas = &collected
			}
			tokens := []string{"operations", opKey, position}
			path := jsonpointer.Format(tokens...)
			switch schemaGraphLocality(view, tokens, *schemas) {
			case graphReachesExternal:
				continue
			case graphUndecided:
				c.inconclusive("OBI-D-11", path, "the reach of this schema's graph could not be established, so its examples were not checked")
				continue
			}
			compiled, err := compileDocumentSchema(view, *schemas, tokens...)
			if err != nil {
				c.inconclusive("OBI-D-11", path, fmt.Sprintf("the schema could not be compiled, so its examples were not checked: %v", err))
				continue
			}
			for _, exampleKey := range provided {
				value := examples[exampleKey].(map[string]any)[position]
				if verr := compiled.Validate(value); verr != nil {
					examplePath := jsonpointer.Format("operations", opKey, "examples", exampleKey, position)
					for _, problem := range schemacompiler.Problems(verr) {
						c.violated("OBI-D-11", examplePath+jsonpointer.Format(problem.Location...), "does not validate against the operation's "+position+" schema: "+problem.Message)
					}
				}
			}
		}
	}
}

// CompileOperationSchema compiles an operation's input or output schema,
// addressed by its canonical key, with the complete OBI document as the
// resolution root of same-document references (§7, OBI-D-16, OBI-T-16).
// Only the schemas the document holds are schemas: an unknown document member
// never acts as a schema keyword or declares a resource, and a reference to a
// resource the document does not embed is unavailable. format is an
// annotation, never an assertion, whatever dialect a reached schema declares.
//
// It returns a *SchemaGraphUnavailableError when the governing graph cannot
// be compiled completely, and another error when there is nothing to compile:
// no interface, no such operation, or no schema at that position.
func CompileOperationSchema(i *Interface, operationName, position string) (*CompiledSchema, error) {
	if i == nil {
		return nil, errors.New("openbindings: interface is nil")
	}
	operation, ok := i.Operations[operationName]
	if !ok {
		return nil, fmt.Errorf("openbindings: operation %q is not defined", operationName)
	}
	var schema JSONSchema
	switch position {
	case "input":
		schema = operation.Input
	case "output":
		schema = operation.Output
	default:
		return nil, fmt.Errorf("openbindings: unknown operation schema position %q", position)
	}
	if schema == nil {
		return nil, fmt.Errorf("openbindings: operation %q specifies no %s schema", operationName, position)
	}
	view, err := documentView(*i)
	if err != nil {
		return nil, err
	}
	return compileOperationSchemaInView(view, operationName, position)
}

func compileOperationSchemaInView(view any, operationName, position string) (*CompiledSchema, error) {
	compiled, err := compileDocumentSchema(view, collectDocumentSchemas(view), "operations", operationName, position)
	if err != nil {
		return nil, &SchemaGraphUnavailableError{Cause: err}
	}
	return compiled, nil
}

// documentURL is the base URI of an OBI document during compilation. No
// reference resolves against the URI a document was fetched from (§7).
const documentURL = "openbindings:///document"

// compileDocumentSchema compiles the schema at a location of a document's
// generic view, given as JSON Pointer reference tokens.
func compileDocumentSchema(view any, schemas documentSchemas, tokens ...string) (*CompiledSchema, error) {
	if schemas.conflict != "" {
		return nil, errors.New(schemas.conflict)
	}
	c := schemacompiler.New()
	c.NeverAssertFormat() // §5.2, OBI-T-16
	ids := make([]string, 0, len(schemas.resources))
	for id := range schemas.resources {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		// A resource is resolved by its $id wherever it is referenced from;
		// the compiler finds the resources nested in it itself.
		if resource := schemas.resources[id]; resource.outermost {
			if err := c.AddResource(id, resource.schema); err != nil {
				return nil, fmt.Errorf("register embedded schema resource %q: %w", id, err)
			}
		}
	}
	if err := c.AddContainer(documentURL, view, schemas.locations...); err != nil {
		return nil, err
	}
	compiled, err := c.Compile(documentURL + "#" + jsonpointer.Format(tokens...))
	if err != nil {
		return nil, err
	}
	return &CompiledSchema{backend: compiled}, nil
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

// SchemaGraphUnavailableError reports that a value verdict could not be
// reached because the governing schema's complete statically reachable graph
// was not available, well-formed, and evaluable. It is distinct from an
// instance mismatch, as required by OBI-T-16.
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

// SchemaValidationError is an established instance mismatch. Use errors.As to
// distinguish it from an unavailable schema graph. Problems holds one line per
// failed constraint, as "location: message" with the location a JSON Pointer
// into the value; Cause is the validator's own error, when there is one.
type SchemaValidationError struct {
	Problems []string
	Cause    error
}

func (e *SchemaValidationError) Error() string { return strings.Join(e.Problems, "; ") }
func (e *SchemaValidationError) Unwrap() error { return e.Cause }

// schemaValidationError projects a backend validation error onto the SDK's
// outcome: a *SchemaValidationError for an established mismatch, and a
// *SchemaGraphUnavailableError for anything else, such as a capability
// refusal during evaluation.
func schemaValidationError(err error) error {
	if err == nil {
		return nil
	}
	var mismatch *jsonschema.ValidationError
	if !errors.As(err, &mismatch) {
		return &SchemaGraphUnavailableError{Cause: err}
	}
	problems := schemacompiler.Problems(mismatch)
	lines := make([]string, len(problems))
	for i, problem := range problems {
		lines[i] = problem.Line()
	}
	return &SchemaValidationError{Problems: lines, Cause: err}
}
