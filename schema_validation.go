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
// Used by ValidateInterface() to enforce OBI-D-02 (the document validates against
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

// validateAgainstOBISchema reports OBI-D-02 violations: the document does not
// validate against openbindings.schema.json. The Interface is round-tripped
// through JSON to obtain a generic value (map[string]any/[]any/scalars) that
// the schema validator accepts.
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

// validateExamplesAgainstOpSchemas reports OBI-D-15 violations: every example's
// provided input/output must validate against its operation's input/output
// schema, when the respective schema is specified. The operation's schema may
// reference document-level schemas via `#/schemas/<name>`; those are rewritten
// to `#/$defs/<name>` and a synthetic compound schema is compiled with the
// document's schemas map exposed under $defs for $ref resolution.
func validateExamplesAgainstOpSchemas(errs *[]string, i Interface) {
	if len(i.Operations) == 0 {
		return
	}
	defs := buildSchemaDefs(i.Schemas)
	for opKey, op := range i.Operations {
		if len(op.Examples) == 0 {
			continue
		}
		var inputSchema, outputSchema *jsonschema.Schema
		if op.Input != nil {
			compiled, err := compileExampleSchema(op.Input, defs)
			if err != nil {
				*errs = append(*errs, fmt.Sprintf("operations[%q].input: cannot compile schema: %v (OBI-D-15)", opKey, err))
			} else {
				inputSchema = compiled
			}
		}
		if op.Output != nil {
			compiled, err := compileExampleSchema(op.Output, defs)
			if err != nil {
				*errs = append(*errs, fmt.Sprintf("operations[%q].output: cannot compile schema: %v (OBI-D-15)", opKey, err))
			} else {
				outputSchema = compiled
			}
		}
		for exKey, ex := range op.Examples {
			if ex.Input != nil && inputSchema != nil {
				if verr := inputSchema.Validate(ex.Input); verr != nil {
					for _, line := range splitSchemaError(verr) {
						*errs = append(*errs, fmt.Sprintf("operations[%q].examples[%q].input: %s (OBI-D-15)", opKey, exKey, line))
					}
				}
			}
			if ex.Output != nil && outputSchema != nil {
				if verr := outputSchema.Validate(ex.Output); verr != nil {
					for _, line := range splitSchemaError(verr) {
						*errs = append(*errs, fmt.Sprintf("operations[%q].examples[%q].output: %s (OBI-D-15)", opKey, exKey, line))
					}
				}
			}
		}
	}
}

// buildSchemaDefs deep-copies the document's schemas map and rewrites any
// `$ref: "#/schemas/X"` to `$ref: "#/$defs/X"` so cross-schema references
// resolve correctly inside the compound schema we compile per example.
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

// compileExampleSchema builds a compound JSON Schema rooted at the operation's
// input/output schema, with the document's schemas map exposed under $defs
// (and `#/schemas/X` $refs rewritten to `#/$defs/X`), then compiles it.
func compileExampleSchema(opSchema JSONSchema, defs map[string]any) (*jsonschema.Schema, error) {
	root := deepCopyJSON(map[string]any(opSchema))
	rootMap, ok := root.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("operation schema must be a JSON object")
	}
	rewriteSchemaRefs(rootMap)
	if len(defs) > 0 {
		// Don't clobber an existing $defs the user authored.
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

// rewriteSchemaRefs walks the JSON value and rewrites any string `$ref` value
// matching `#/schemas/<name>` to `#/$defs/<name>` in place. Returns the input
// unchanged for convenience.
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

// deepCopyJSON makes a deep copy of a JSON-shaped value (map[string]any /
// []any / strings / numbers / bools / nil). Typed slices like []string are
// normalized to []any so the result is always valid for JSON Schema validators.
// Returns the input unchanged for scalar values.
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

// splitSchemaError flattens a jsonschema.ValidationError into one line per
// nested cause. Returns at least one line for every non-nil error.
func splitSchemaError(err error) []string {
	if err == nil {
		return nil
	}
	if ve, ok := err.(*jsonschema.ValidationError); ok {
		return flattenValidationError(ve, "")
	}
	return []string{err.Error()}
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
