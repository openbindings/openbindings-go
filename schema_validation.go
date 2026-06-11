package openbindings

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// openbindingsSchemaJSON is the OBI document schema (openbindings.schema.json),
// embedded at build time. Synced from the spec repo via scripts/sync-schema.sh.
//
//go:embed openbindings.schema.json
var openbindingsSchemaJSON []byte

// compiledOBISchema is the embedded OBI document schema, compiled once at init.
// Used by Validate() to enforce OBI-D-02 (the document validates against
// openbindings.schema.json).
var compiledOBISchema *jsonschema.Schema

func init() {
	var doc any
	if err := json.Unmarshal(openbindingsSchemaJSON, &doc); err != nil {
		panic(fmt.Sprintf("openbindings: embedded openbindings.schema.json is not valid JSON: %v", err))
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("openbindings:///schema", doc); err != nil {
		panic(fmt.Sprintf("openbindings: cannot register OBI schema: %v", err))
	}
	s, err := c.Compile("openbindings:///schema")
	if err != nil {
		panic(fmt.Sprintf("openbindings: cannot compile OBI schema: %v", err))
	}
	compiledOBISchema = s
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

// validateExamplesAgainstOpSchemas reports OBI-D-12 violations: every
// example's provided input/output (including an explicit JSON null) must
// validate against its operation's input/output schema, when the respective
// schema is specified.
//
// Verification is capability-relative (cf. the spec's §8 / OBI-D-14
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
				*errs = append(*errs, fmt.Sprintf("operations[%q].input: cannot compile schema: %v (OBI-D-12)", opKey, err))
			} else {
				inputSchema = compiled
			}
		}
		if op.Output != nil && !defsExternal && !schemaHasExternalRef(op.Output) {
			compiled, err := compileExampleSchema(op.Output, defs)
			if err != nil {
				*errs = append(*errs, fmt.Sprintf("operations[%q].output: cannot compile schema: %v (OBI-D-12)", opKey, err))
			} else {
				outputSchema = compiled
			}
		}
		for exKey, ex := range op.Examples {
			if ex.HasInput() && inputSchema != nil {
				if verr := inputSchema.Validate(ex.Input); verr != nil {
					for _, line := range splitSchemaError(verr) {
						*errs = append(*errs, fmt.Sprintf("operations[%q].examples[%q].input: %s (OBI-D-12)", opKey, exKey, line))
					}
				}
			}
			if ex.HasOutput() && outputSchema != nil {
				if verr := outputSchema.Validate(ex.Output); verr != nil {
					for _, line := range splitSchemaError(verr) {
						*errs = append(*errs, fmt.Sprintf("operations[%q].examples[%q].output: %s (OBI-D-12)", opKey, exKey, line))
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
	case JSONSchema:
		return schemaHasExternalRef(map[string]any(t))
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
		defs[name] = rewriteSchemaRefs(deepCopyJSON(map[string]any(sch)))
	}
	return defs
}

// compileExampleSchema builds a compound JSON Schema rooted at the
// operation's input/output schema, with the document's schemas map
// exposed under $defs, then compiles it.
func compileExampleSchema(opSchema JSONSchema, defs map[string]any) (*jsonschema.Schema, error) {
	root := deepCopyJSON(map[string]any(opSchema))
	rootMap, ok := root.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("operation schema must be a JSON object")
	}
	rewriteSchemaRefs(rootMap)
	if len(defs) > 0 {
		if existing, has := rootMap["$defs"]; has {
			if existingMap, isMap := existing.(map[string]any); isMap {
				for k, v := range defs {
					if _, present := existingMap[k]; !present {
						existingMap[k] = v
					}
				}
			}
		} else {
			rootMap["$defs"] = defs
		}
	}
	c := jsonschema.NewCompiler()
	const url = "openbindings:///example-schema"
	if err := c.AddResource(url, rootMap); err != nil {
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
			Message: fmt.Sprintf("%v", ve.ErrorKind),
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
	return loc + fmt.Sprintf("%v", ve.ErrorKind)
}
