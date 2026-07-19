package openbindings

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// kindPrinter renders jsonschema/v6 ErrorKind values. The kinds implement
// LocalizedString(*message.Printer), not String(), so formatting them with
// %v would print the raw struct (e.g. `&{[customer]}` for a missing
// required property) — and that text flows into ValidationFailure.Message,
// a wire-crossing details payload consumers render per-field.
var kindPrinter = message.NewPrinter(language.English)

func kindString(k jsonschema.ErrorKind) string {
	return k.LocalizedString(kindPrinter)
}

// openbindingsSchemaJSON is the OBI document schema (openbindings.schema.json),
// embedded at build time. Synced from the spec repo via scripts/sync-schema.sh.
//
//go:embed openbindings.schema.json
var openbindingsSchemaJSON []byte

// compiledOBISchema is the embedded OBI document schema, compiled once at init.
// Used by Validate() to enforce OBI-D-02 (the document validates against
// openbindings.schema.json).
var compiledOBISchema *jsonschema.Schema

// compiledMetaSchema is the JSON Schema 2020-12 meta-schema, compiled once at
// init from the validator library's locally embedded copy (never fetched from
// the network, per OBI-D-17's verification note). Used by Validate() to
// enforce OBI-D-17 (every schema in the document is well-formed).
var compiledMetaSchema *jsonschema.Schema

func init() {
	var doc any
	if err := json.Unmarshal(openbindingsSchemaJSON, &doc); err != nil {
		panic(fmt.Sprintf("openbindings: embedded openbindings.schema.json is not valid JSON: %v", err))
	}
	c := jsonschema.NewCompiler()
	c.UseRegexpEngine(ECMARegexpEngine)
	if err := c.AddResource("openbindings:///schema", doc); err != nil {
		panic(fmt.Sprintf("openbindings: cannot register OBI schema: %v", err))
	}
	s, err := c.Compile("openbindings:///schema")
	if err != nil {
		panic(fmt.Sprintf("openbindings: cannot compile OBI schema: %v", err))
	}
	compiledOBISchema = s

	mc := jsonschema.NewCompiler()
	mc.UseRegexpEngine(ECMARegexpEngine)
	meta, err := mc.Compile(draft202012URI)
	if err != nil {
		panic(fmt.Sprintf("openbindings: cannot compile embedded 2020-12 meta-schema: %v", err))
	}
	compiledMetaSchema = meta
}

// validateSchemaWellFormedness reports OBI-D-17 violations at one schema
// position: the value must be a JSON Schema 2020-12 schema in object or
// boolean form, and the object form must validate against the 2020-12
// meta-schemas (which cover subschemas recursively). The check is
// deliberately narrow, mirroring §5.2: unknown keywords, unparseable
// `pattern` values, and unresolvable `$ref` targets all pass — they surface
// when the schema is used, not here.
func validateSchemaWellFormedness(errs *[]string, prefix string, schema JSONSchema) {
	switch v := schema.(type) {
	case bool:
		// Boolean schemas are always well-formed.
	case map[string]any:
		if verr := compiledMetaSchema.Validate(any(v)); verr != nil {
			for _, line := range splitSchemaError(verr) {
				*errs = append(*errs, fmt.Sprintf("%s: not a well-formed JSON Schema 2020-12 schema: %s (OBI-D-17)", prefix, line))
			}
		}
	default:
		*errs = append(*errs, fmt.Sprintf("%s: a schema is a JSON Schema 2020-12 object or boolean; got %s (OBI-D-17)", prefix, jsonTypeName(v)))
	}
}

// validateAgainstOBISchema reports OBI-D-02 violations: the document does
// not validate against openbindings.schema.json. The Interface is
// round-tripped through JSON to obtain a generic value
// (map[string]any/[]any/scalars) that the schema validator accepts.
func validateAgainstOBISchema(errs *[]string, i Interface) {
	data, err := json.Marshal(i)
	if err != nil {
		*errs = append(*errs, fmt.Sprintf("schema validation: cannot marshal document: %v (OBI-D-02)", err))
		return
	}
	var doc any
	if err := json.Unmarshal(data, &doc); err != nil {
		*errs = append(*errs, fmt.Sprintf("schema validation: cannot re-parse document: %v (OBI-D-02)", err))
		return
	}
	if verr := compiledOBISchema.Validate(doc); verr != nil {
		for _, line := range splitSchemaError(verr) {
			*errs = append(*errs, fmt.Sprintf("schema validation: %s (OBI-D-02)", line))
		}
	}
}

// validateExamplesAgainstOpSchemas reports OBI-D-11 violations: every
// example's provided input/output (including an explicit JSON null) must
// validate against its operation's input/output schema, when the respective
// schema is specified.
//
// Verification is capability-relative (cf. the spec's §8 / OBI-D-13
// discussion): when a schema's $refs point outside the document, this
// validator cannot resolve them and abstains from example validation for
// that operation rather than failing the document.
func validateExamplesAgainstOpSchemas(errs *[]string, i Interface) {
	if len(i.Operations) == 0 {
		return
	}
	defs := buildSchemaDefs(i.Schemas)
	// If any document schema carries an external $ref, the compound schema
	// space is not fully resolvable locally; abstain across the board.
	defsExternal := schemaHasExternalRef(defs)
	for opKey, op := range i.Operations {
		if len(op.Examples) == 0 {
			continue
		}
		var inputSchema, outputSchema *jsonschema.Schema
		if op.Input != nil && !defsExternal && !schemaHasExternalRef(op.Input) {
			compiled, err := compileExampleSchema(op.Input, defs)
			if err != nil {
				*errs = append(*errs, fmt.Sprintf("operations[%q].input: cannot compile schema: %v (OBI-D-11)", opKey, err))
			} else {
				inputSchema = compiled
			}
		}
		if op.Output != nil && !defsExternal && !schemaHasExternalRef(op.Output) {
			compiled, err := compileExampleSchema(op.Output, defs)
			if err != nil {
				*errs = append(*errs, fmt.Sprintf("operations[%q].output: cannot compile schema: %v (OBI-D-11)", opKey, err))
			} else {
				outputSchema = compiled
			}
		}
		for exKey, ex := range op.Examples {
			if ex.HasInput() && inputSchema != nil {
				if verr := inputSchema.Validate(ex.Input); verr != nil {
					for _, line := range splitSchemaError(verr) {
						*errs = append(*errs, fmt.Sprintf("operations[%q].examples[%q].input: %s (OBI-D-11)", opKey, exKey, line))
					}
				}
			}
			if ex.HasOutput() && outputSchema != nil {
				if verr := outputSchema.Validate(ex.Output); verr != nil {
					for _, line := range splitSchemaError(verr) {
						*errs = append(*errs, fmt.Sprintf("operations[%q].examples[%q].output: %s (OBI-D-11)", opKey, exKey, line))
					}
				}
			}
		}
	}
}

// schemaHasExternalRef reports whether any $ref in the schema tree points
// outside the document (i.e., does not start with "#"). Such references are
// unresolvable without fetching external resources, so document validation
// abstains from example checks against them.
func schemaHasExternalRef(v any) bool {
	switch t := v.(type) {
	case map[string]any:
		if ref, ok := t["$ref"].(string); ok && !strings.HasPrefix(ref, "#") {
			return true
		}
		for _, child := range t {
			if schemaHasExternalRef(child) {
				return true
			}
		}
	case []any:
		for _, child := range t {
			if schemaHasExternalRef(child) {
				return true
			}
		}
	}
	return false
}

// buildSchemaDefs deep-copies the document's schemas map and rewrites
// `$ref: "#/schemas/X"` → `$ref: "#/$defs/X"` so cross-schema refs
// resolve inside the compound schema we compile per example.
func buildSchemaDefs(schemas map[string]JSONSchema) map[string]any {
	if len(schemas) == 0 {
		return nil
	}
	defs := make(map[string]any, len(schemas))
	for name, sch := range schemas {
		switch v := sch.(type) {
		case map[string]any:
			defs[name] = rewriteSchemaRefs(deepCopyJSON(v))
		default:
			// Boolean schemas carry no refs to rewrite; anything else is
			// malformed (OBI-D-17's concern) and surfaces at compile time.
			defs[name] = v
		}
	}
	return defs
}

// compileExampleSchema builds a compound JSON Schema rooted at the
// operation's input/output schema, with the document's schemas map
// exposed under $defs, then compiles it.
func compileExampleSchema(opSchema JSONSchema, defs map[string]any) (*jsonschema.Schema, error) {
	var root any
	switch v := opSchema.(type) {
	case map[string]any:
		copied := deepCopyJSON(v)
		rootMap := copied.(map[string]any)
		rewriteSchemaRefs(rootMap)
		if len(defs) > 0 {
			if existing, has := rootMap["$defs"]; has {
				if existingMap, isMap := existing.(map[string]any); isMap {
					for k, dv := range defs {
						if _, present := existingMap[k]; !present {
							existingMap[k] = dv
						}
					}
				}
			} else {
				rootMap["$defs"] = defs
			}
		}
		root = rootMap
	case bool:
		// Boolean schemas reference nothing; compile the boolean directly.
		root = v
	default:
		return nil, fmt.Errorf("operation schema must be a JSON Schema object or boolean")
	}
	c := jsonschema.NewCompiler()
	c.UseRegexpEngine(ECMARegexpEngine)
	const url = "openbindings:///example-schema"
	if err := c.AddResource(url, root); err != nil {
		return nil, err
	}
	return c.Compile(url)
}

func rewriteSchemaRefs(v any) any {
	switch t := v.(type) {
	case map[string]any:
		if ref, ok := t["$ref"].(string); ok && strings.HasPrefix(ref, "#/schemas/") {
			t["$ref"] = "#/$defs/" + strings.TrimPrefix(ref, "#/schemas/")
		}
		for _, child := range t {
			rewriteSchemaRefs(child)
		}
	case []any:
		for _, child := range t {
			rewriteSchemaRefs(child)
		}
	}
	return v
}

func deepCopyJSON(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, child := range t {
			out[k] = deepCopyJSON(child)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, child := range t {
			out[i] = deepCopyJSON(child)
		}
		return out
	case []string:
		out := make([]any, len(t))
		for i, s := range t {
			out[i] = s
		}
		return out
	default:
		return v
	}
}

func splitSchemaError(err error) []string {
	if err == nil {
		return nil
	}
	if ve, ok := err.(*jsonschema.ValidationError); ok {
		return flattenValidationError(ve, "")
	}
	return []string{err.Error()}
}

// collectValidationFailures returns structured per-leaf failures. The
// shape mirrors the TS SDK's ValidationFailure so consumers can render
// per-field diagnostics from either runtime without parsing strings.
func collectValidationFailures(err error) []ValidationFailure {
	if err == nil {
		return nil
	}
	ve, ok := err.(*jsonschema.ValidationError)
	if !ok {
		return []ValidationFailure{{Message: err.Error()}}
	}
	return flattenValidationFailures(ve)
}

func flattenValidationFailures(ve *jsonschema.ValidationError) []ValidationFailure {
	if ve == nil {
		return nil
	}
	if len(ve.Causes) == 0 {
		path := ""
		if len(ve.InstanceLocation) > 0 {
			path = "/" + strings.Join(ve.InstanceLocation, "/")
		}
		return []ValidationFailure{{
			Path:    path,
			Message: kindString(ve.ErrorKind),
		}}
	}
	var out []ValidationFailure
	for _, c := range ve.Causes {
		out = append(out, flattenValidationFailures(c)...)
	}
	return out
}

func flattenValidationError(ve *jsonschema.ValidationError, prefix string) []string {
	if ve == nil {
		return nil
	}
	var out []string
	if len(ve.Causes) == 0 {
		out = append(out, fmt.Sprintf("%s%s", prefix, summarizeValidationError(ve)))
		return out
	}
	for _, c := range ve.Causes {
		out = append(out, flattenValidationError(c, prefix)...)
	}
	return out
}

func summarizeValidationError(ve *jsonschema.ValidationError) string {
	loc := ""
	if len(ve.InstanceLocation) > 0 {
		loc = "/" + strings.Join(ve.InstanceLocation, "/") + ": "
	}
	return loc + kindString(ve.ErrorKind)
}

// ValidateAgainstSchema validates a value against an operation-level JSON
// Schema, resolving #/schemas/ references against the interface's named
// schema pool. This is the same compilation and validation the operation
// invoker applies to outputs under OBI-T-16, exported so tools can enforce
// or test wire conformance on values they carry themselves.
func ValidateAgainstSchema(value any, schema JSONSchema, schemas map[string]JSONSchema) error {
	compiled, err := compileExampleSchema(schema, buildSchemaDefs(schemas))
	if err != nil {
		return fmt.Errorf("openbindings: schema compilation failed: %w", err)
	}
	if verr := compiled.Validate(value); verr != nil {
		// The library's Error() leads with the compiler's internal resource
		// URI; the flattened per-leaf lines are the readable form. The raw
		// error stays reachable through Unwrap for structured consumers.
		return &schemaValidationError{lines: splitSchemaError(verr), cause: verr}
	}
	return nil
}

// schemaValidationError renders a validation failure as its per-leaf lines
// (the shape splitSchemaError produces) instead of the underlying library's
// resource-URI-prefixed dump.
type schemaValidationError struct {
	lines []string
	cause error
}

func (e *schemaValidationError) Error() string { return strings.Join(e.lines, "; ") }
func (e *schemaValidationError) Unwrap() error { return e.cause }
