package openbindings

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestInterfaceValidateInterface_RequiresOpenBindingsAndOperations(t *testing.T) {
	i := Interface{}
	err := i.ValidateInterface()
	if err == nil {
		t.Fatalf("expected error")
	}
	if _, ok := err.(*ValidationError); !ok {
		t.Fatalf("expected ValidationError, got %T", err)
	}
}

func TestParseDocumentRejectsDuplicateObjectKeys(t *testing.T) {
	cases := []struct {
		name string
		doc  string
	}{
		{
			name: "top-level duplicate",
			doc:  `{"openbindings":"0.2.0","operations":{},"operations":{}}`,
		},
		{
			name: "nested duplicate",
			doc:  `{"openbindings":"0.2.0","operations":{"op":{"input":{"type":"string","type":"number"}}}}`,
		},
		{
			name: "escaped duplicate",
			doc:  `{"openbindings":"0.2.0","operations":{"op":{"input":{"a":1,"\u0061":2}}}}`,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseDocument([]byte(tt.doc)); err == nil {
				t.Fatal("expected duplicate-key parse error")
			}
		})
	}
}

func TestInterfaceValidateInterface_RefusesHigherMajorVersion_OBI_T_04(t *testing.T) {
	// OBI-T-04: refuse to load when document's major version exceeds MaxTested.
	i := Interface{
		OpenBindings: "1.0.0",
		Operations:   map[string]Operation{},
	}
	err := i.ValidateInterface()
	if err == nil {
		t.Fatalf("expected error for higher-major version")
	}
	if !containsProblem(err, `openbindings: "1.0.0" exceeds this SDK's MaxTestedVersion "0.2.0" (OBI-T-04)`) {
		t.Fatalf("expected OBI-T-04 problem, got %v", err)
	}
}

func TestInterfaceValidateInterface_RefusesPre1HigherMinor_OBI_T_04(t *testing.T) {
	// OBI-T-04: while MaxTested is pre-1.0, refuse strictly higher minor too.
	i := Interface{
		OpenBindings: "0.99.0",
		Operations:   map[string]Operation{},
	}
	err := i.ValidateInterface()
	if err == nil {
		t.Fatalf("expected error for pre-1.0 higher-minor version")
	}
	if !containsProblem(err, `openbindings: "0.99.0" exceeds this SDK's MaxTestedVersion "0.2.0" (OBI-T-04)`) {
		t.Fatalf("expected OBI-T-04 problem, got %v", err)
	}
}

func TestInterfaceValidateInterface_RefusesInvalidSemver_OBI_D_16(t *testing.T) {
	i := Interface{
		OpenBindings: "0.1",
		Operations:   map[string]Operation{},
	}
	err := i.ValidateInterface()
	if err == nil {
		t.Fatalf("expected error for invalid semver")
	}
	if !containsProblem(err, `openbindings: "0.1" is not a valid SemVer 2.0.0 string (OBI-D-16)`) {
		t.Fatalf("expected OBI-D-16 problem, got %v", err)
	}
}

func TestInterfaceValidateInterface_UnknownTopLevelFields_StrictMode(t *testing.T) {
	i := Interface{
		OpenBindings: "0.1.0",
		Operations:   map[string]Operation{},
		LosslessFields: LosslessFields{
			Unknown: map[string]json.RawMessage{
				"unknownField": json.RawMessage(`{"value":"unknownFieldValue"}`),
			},
		},
	}
	if err := i.ValidateInterface(WithRejectUnknownTypedFields()); err == nil {
		t.Fatalf("expected error")
	}
}

func TestInterfaceValidateInterface_UnknownFields_StrictMode_CatchesNestedTypedObjects(t *testing.T) {
	i := Interface{
		OpenBindings: "0.1.0",
		Roles: map[string]string{
			"io.example@1.0": "https://example.com/interface.json",
		},
		Operations: map[string]Operation{
			"op": {
				Satisfies: []Satisfies{
					{
						Role:      "io.example@1.0",
						Operation: "op",
						LosslessFields: LosslessFields{
							Unknown: map[string]json.RawMessage{
								"unknownField": json.RawMessage(`{"value":"unknownFieldValue"}`),
							},
						},
					},
				},
			},
		},
		Sources: map[string]Source{
			"src": {
				Format:   "openapi@3.1",
				Location: "./api.json",
				LosslessFields: LosslessFields{
					Unknown: map[string]json.RawMessage{
						"unknownField": json.RawMessage(`{"value":"unknownFieldValue"}`),
					},
				},
			},
		},
		Bindings: map[string]BindingEntry{
			"op.src": {
				Operation: "op",
				Source:    "src",
				LosslessFields: LosslessFields{
					Unknown: map[string]json.RawMessage{
						"unknownField": json.RawMessage(`{"value":"unknownFieldValue"}`),
					},
				},
			},
		},
	}
	if err := i.ValidateInterface(WithRejectUnknownTypedFields()); err == nil {
		t.Fatalf("expected error")
	}
}

func TestInterfaceValidateInterface_StrictMode_CatchesOperationExampleUnknownFields(t *testing.T) {
	i := Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"op": {
				Examples: map[string]OperationExample{
					"ex1": {
						Description: "test",
						LosslessFields: LosslessFields{
							Unknown: map[string]json.RawMessage{
								"unknownField": json.RawMessage(`"bad"`),
							},
						},
					},
				},
			},
		},
	}
	if err := i.ValidateInterface(WithRejectUnknownTypedFields()); err == nil {
		t.Fatalf("expected error for unknown field in example")
	}
}

func TestInterfaceValidateInterface_StrictMode_CatchesInlineTransformUnknownFields(t *testing.T) {
	i := Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"op": {},
		},
		Sources: map[string]Source{
			"api": {Format: "openapi@3.1", Location: "./api.json"},
		},
		Bindings: map[string]BindingEntry{
			"op.api": {
				Operation: "op",
				Source:    "api",
				LosslessFields: LosslessFields{
					Unknown: map[string]json.RawMessage{
						"unknownBindingField": json.RawMessage(`"bad"`),
					},
				},
			},
		},
	}
	if err := i.ValidateInterface(WithRejectUnknownTypedFields()); err == nil {
		t.Fatalf("expected error for unknown field on binding entry")
	}
}

func TestInterfaceValidateInterface_AliasesMustBeUniqueAcrossOperations(t *testing.T) {
	i := Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"a": {Aliases: []string{"shared"}},
			"b": {Aliases: []string{"shared"}},
		},
	}
	if err := i.ValidateInterface(); err == nil {
		t.Fatalf("expected error")
	}
}

func TestInterfaceValidateInterface_SatisfiesFieldsMustBeNonEmpty(t *testing.T) {
	i := Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"a": {Satisfies: []Satisfies{{Role: "", Operation: ""}}},
		},
	}
	if err := i.ValidateInterface(); err == nil {
		t.Fatalf("expected error")
	}
}

func TestInterfaceValidateInterface_SatisfiesRoleMustExistInRoles(t *testing.T) {
	i := Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"a": {
				Satisfies: []Satisfies{
					{Role: "nonexistent", Operation: "a"},
				},
			},
		},
	}
	err := i.ValidateInterface()
	if err == nil {
		t.Fatalf("expected error for dangling satisfies reference")
	}
	if !containsProblem(err, `operations["a"].satisfies[0].role: references unknown role "nonexistent" (OBI-D-13)`) {
		t.Fatalf("expected role reference error, got %v", err)
	}
}

func TestInterfaceValidateInterface_SatisfiesRoleValidWhenRoleExists(t *testing.T) {
	i := Interface{
		OpenBindings: "0.1.0",
		Roles: map[string]string{
			"taskmanager": "https://example.com/taskmanager/v1.json",
		},
		Operations: map[string]Operation{
			"tasks.create": {
				Satisfies: []Satisfies{
					{Role: "taskmanager", Operation: "tasks.create"},
				},
			},
		},
	}
	if err := i.ValidateInterface(); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestInterfaceValidateInterface_OpenBindingsVersionErrorMessageIsStable(t *testing.T) {
	i := Interface{
		OpenBindings: "0.1",
		Operations:   map[string]Operation{},
	}
	err := i.ValidateInterface()
	if err == nil {
		t.Fatalf("expected error")
	}
	if err.Error() == "" || err.Error() == "invalid interface" {
		t.Fatalf("expected detailed error, got %q", err.Error())
	}
	if want := `openbindings: "0.1" is not a valid SemVer 2.0.0 string (OBI-D-16)`; !containsProblem(err, want) {
		t.Fatalf("expected problem %q, got %q", want, err.Error())
	}
}

func containsProblem(err error, want string) bool {
	ve, ok := err.(*ValidationError)
	if !ok {
		return false
	}
	for _, p := range ve.Problems {
		if p == want {
			return true
		}
	}
	return false
}

func TestInterfaceValidateInterface_SourceMustHaveLocationOrContent(t *testing.T) {
	i := Interface{
		OpenBindings: "0.1.0",
		Operations:   map[string]Operation{},
		Sources: map[string]Source{
			"empty": {Format: "openapi@3.1"},
		},
	}
	err := i.ValidateInterface()
	if err == nil {
		t.Fatalf("expected error")
	}
	if !containsProblem(err, "sources[\"empty\"]: must have location or content") {
		t.Fatalf("expected location/content error, got %v", err)
	}
}

func TestInterfaceValidateInterface_SourceAcceptsBothLocationAndContent(t *testing.T) {
	i := Interface{
		OpenBindings: "0.1.0",
		Operations:   map[string]Operation{},
		Sources: map[string]Source{
			"both": {
				Format:   "openapi@3.1",
				Location: "./api.json",
				Content:  map[string]any{"openapi": "3.1.0"},
			},
		},
	}
	err := i.ValidateInterface()
	if err != nil {
		t.Fatalf("expected no error for source with both location and content, got %v", err)
	}
}

func TestInterfaceValidateInterface_TransformExpressionMustBeNonEmpty(t *testing.T) {
	// Per v0.2 spec §6.5, transforms are JSONata 2.0 expression strings.
	// An empty string is not a valid expression.
	i := Interface{
		OpenBindings: "0.1.0",
		Operations:   map[string]Operation{},
		Transforms: map[string]Transform{
			"empty": "",
		},
	}
	err := i.ValidateInterface()
	if err == nil {
		t.Fatalf("expected error")
	}
	if !containsProblem(err, `transforms["empty"]: must be a non-empty JSONata expression`) {
		t.Fatalf("expected transform empty-expression error, got %v", err)
	}
}

func TestInterfaceValidateInterface_BindingTransformRefMustExist(t *testing.T) {
	i := Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"op": {},
		},
		Sources: map[string]Source{
			"api": {Format: "openapi@3.1", Location: "./api.json"},
		},
		Bindings: map[string]BindingEntry{
			"op.api": {
				Operation: "op",
				Source:    "api",
				InputTransform: &TransformOrRef{
					Ref: "#/transforms/nonexistent",
				},
			},
		},
	}
	err := i.ValidateInterface()
	if err == nil {
		t.Fatalf("expected error")
	}
	if !containsProblem(err, `bindings["op.api"].inputTransform.$ref: references unknown transform "nonexistent" (OBI-D-12)`) {
		t.Fatalf("expected transform ref error, got %v", err)
	}
}

func TestInterfaceValidateInterface_OperationRefMustExist(t *testing.T) {
	i := Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"op": {},
		},
		Sources: map[string]Source{
			"api": {Format: "openapi@3.1", Location: "./api.json"},
		},
		Bindings: map[string]BindingEntry{
			"nonexistent.api": {
				Operation: "nonexistent",
				Source:    "api",
			},
		},
	}
	err := i.ValidateInterface()
	if err == nil {
		t.Fatalf("expected error")
	}
	if !containsProblem(err, `bindings["nonexistent.api"].operation: references unknown operation "nonexistent" (OBI-D-09)`) {
		t.Fatalf("expected operation ref error, got %v", err)
	}
}

func TestInterfaceValidateInterface_SourceRefMustExist(t *testing.T) {
	i := Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"op": {},
		},
		Bindings: map[string]BindingEntry{
			"op.nonexistent": {
				Operation: "op",
				Source:    "nonexistent",
			},
		},
	}
	err := i.ValidateInterface()
	if err == nil {
		t.Fatalf("expected error")
	}
	if !containsProblem(err, `bindings["op.nonexistent"].source: references unknown source "nonexistent" (OBI-D-10)`) {
		t.Fatalf("expected source ref error, got %v", err)
	}
}

func TestInterfaceValidateInterface_InlineTransformMustBeNonEmpty(t *testing.T) {
	i := Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"op": {},
		},
		Sources: map[string]Source{
			"api": {Format: "openapi@3.1", Location: "./api.json"},
		},
		Bindings: map[string]BindingEntry{
			"op.api": {
				Operation:       "op",
				Source:          "api",
				OutputTransform: &TransformOrRef{Inline: ""},
			},
		},
	}
	err := i.ValidateInterface()
	if err == nil {
		t.Fatalf("expected error")
	}
	if !containsProblem(err, `bindings["op.api"].outputTransform: must be a non-empty JSONata expression`) {
		t.Fatalf("expected empty-inline-transform error, got %v", err)
	}
}

func TestInterfaceValidateInterface_SecurityRefMustExist(t *testing.T) {
	i := Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"op": {},
		},
		Sources: map[string]Source{
			"api": {Format: "openapi@3.1", Location: "./api.json"},
		},
		Bindings: map[string]BindingEntry{
			"op.api": {
				Operation: "op",
				Source:    "api",
				Security:  "nonexistent",
			},
		},
	}
	err := i.ValidateInterface()
	if err == nil {
		t.Fatalf("expected error")
	}
	if !containsProblem(err, `bindings["op.api"].security: references unknown security "nonexistent" (OBI-D-11)`) {
		t.Fatalf("expected security ref error, got %v", err)
	}
}

func TestInterfaceValidateInterface_SecurityRefValid(t *testing.T) {
	i := Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"op": {},
		},
		Sources: map[string]Source{
			"api": {Format: "openapi@3.1", Location: "./api.json"},
		},
		Security: map[string][]SecurityMethod{
			"default": {{Type: "bearer"}},
		},
		Bindings: map[string]BindingEntry{
			"op.api": {
				Operation: "op",
				Source:    "api",
				Security:  "default",
			},
		},
	}
	if err := i.ValidateInterface(); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestInterfaceValidateInterface_ValidInterfaceWithTransforms(t *testing.T) {
	i := Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"pay": {},
		},
		Transforms: map[string]Transform{
			"toApi": "{ amount: total * 100 }",
		},
		Sources: map[string]Source{
			"stripe": {Format: "openapi@3.1", Location: "./stripe.json"},
		},
		Bindings: map[string]BindingEntry{
			"pay.stripe": {
				Operation:      "pay",
				Source:         "stripe",
				InputTransform: &TransformOrRef{Ref: "#/transforms/toApi"},
			},
		},
	}
	if err := i.ValidateInterface(); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

// containsProblemSubstring reports whether the error is a *ValidationError with
// at least one Problem containing substr.
func containsProblemSubstring(err error, substr string) bool {
	ve, ok := err.(*ValidationError)
	if !ok {
		return false
	}
	for _, p := range ve.Problems {
		if strings.Contains(p, substr) {
			return true
		}
	}
	return false
}

// newInterfaceWithExamples builds a minimal valid Interface with one operation
// that has the given input/output schemas and the given examples map.
func newInterfaceWithExamples(inputSchema, outputSchema JSONSchema, examples map[string]OperationExample) Interface {
	return Interface{
		OpenBindings: "0.2.0",
		Operations: map[string]Operation{
			"greet": {
				Input:    inputSchema,
				Output:   outputSchema,
				Examples: examples,
			},
		},
	}
}

func TestInterfaceValidateInterface_ExampleValidation_ValidExamplePasses(t *testing.T) {
	i := newInterfaceWithExamples(
		JSONSchema{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}, "required": []any{"name"}},
		JSONSchema{"type": "object", "properties": map[string]any{"greeting": map[string]any{"type": "string"}}},
		map[string]OperationExample{
			"basic": {
				Input:  map[string]any{"name": "Alice"},
				Output: map[string]any{"greeting": "Hello, Alice!"},
			},
		},
	)
	if err := i.ValidateInterface(WithExampleValidation()); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestInterfaceValidateInterface_ExampleValidation_InvalidInputFails(t *testing.T) {
	i := newInterfaceWithExamples(
		JSONSchema{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}, "required": []any{"name"}},
		nil, // no output schema
		map[string]OperationExample{
			"bad": {
				// Missing required "name" field.
				Input: map[string]any{"wrong": 42},
			},
		},
	)
	err := i.ValidateInterface(WithExampleValidation())
	if err == nil {
		t.Fatalf("expected error for invalid example input")
	}
	if !containsProblemSubstring(err, `operations["greet"].examples["bad"].input:`) {
		t.Fatalf("expected OBI-D-15 input problem, got %v", err)
	}
	if !containsProblemSubstring(err, "OBI-D-15") {
		t.Fatalf("expected OBI-D-15 tag, got %v", err)
	}
}

func TestInterfaceValidateInterface_ExampleValidation_InvalidOutputFails(t *testing.T) {
	i := newInterfaceWithExamples(
		nil, // no input schema
		JSONSchema{"type": "object", "properties": map[string]any{"count": map[string]any{"type": "integer"}}, "additionalProperties": false},
		map[string]OperationExample{
			"bad": {
				// "count" should be integer, not string; extra field also present.
				Output: map[string]any{"count": "not-a-number", "extra": true},
			},
		},
	)
	err := i.ValidateInterface(WithExampleValidation())
	if err == nil {
		t.Fatalf("expected error for invalid example output")
	}
	if !containsProblemSubstring(err, `operations["greet"].examples["bad"].output:`) {
		t.Fatalf("expected OBI-D-15 output problem, got %v", err)
	}
	if !containsProblemSubstring(err, "OBI-D-15") {
		t.Fatalf("expected OBI-D-15 tag, got %v", err)
	}
}

func TestInterfaceValidateInterface_ExampleValidation_SkippedByDefault(t *testing.T) {
	// Invalid example input, but WithExampleValidation is NOT passed.
	i := newInterfaceWithExamples(
		JSONSchema{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}, "required": []any{"name"}},
		nil,
		map[string]OperationExample{
			"bad": {
				Input: map[string]any{"wrong": 42}, // missing required "name"
			},
		},
	)
	if err := i.ValidateInterface(); err != nil {
		t.Fatalf("expected no error without WithExampleValidation, got %v", err)
	}
}

func TestInterfaceValidateInterface_ExampleValidation_NoSchemasSkipsGracefully(t *testing.T) {
	// Operation has examples but no input/output schemas at all.
	i := newInterfaceWithExamples(
		nil,
		nil,
		map[string]OperationExample{
			"ex1": {
				Input:  map[string]any{"anything": true},
				Output: "arbitrary",
			},
		},
	)
	if err := i.ValidateInterface(WithExampleValidation()); err != nil {
		t.Fatalf("expected no error when schemas are absent, got %v", err)
	}
}

func TestInterfaceValidateInterface_ExampleValidation_NoExamplesSkipsGracefully(t *testing.T) {
	// Operation has schemas but no examples.
	i := newInterfaceWithExamples(
		JSONSchema{"type": "object"},
		JSONSchema{"type": "object"},
		nil,
	)
	if err := i.ValidateInterface(WithExampleValidation()); err != nil {
		t.Fatalf("expected no error when examples are absent, got %v", err)
	}
}

func TestInterfaceValidateInterface_ExampleValidation_ExampleWithoutInputOrOutput(t *testing.T) {
	// Example has neither input nor output -- should be skipped, not crash.
	i := newInterfaceWithExamples(
		JSONSchema{"type": "object"},
		JSONSchema{"type": "object"},
		map[string]OperationExample{
			"empty": {Description: "an example with no data"},
		},
	)
	if err := i.ValidateInterface(WithExampleValidation()); err != nil {
		t.Fatalf("expected no error for example without input/output, got %v", err)
	}
}

func TestInterfaceValidateInterface_ExampleValidation_WithSchemaRef(t *testing.T) {
	// Operation input uses $ref to a document-level schema.
	i := Interface{
		OpenBindings: "0.2.0",
		Schemas: map[string]JSONSchema{
			"Person": {"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}, "required": []any{"name"}},
		},
		Operations: map[string]Operation{
			"greet": {
				Input: JSONSchema{"$ref": "#/schemas/Person"},
				Examples: map[string]OperationExample{
					"valid":   {Input: map[string]any{"name": "Bob"}},
					"invalid": {Input: map[string]any{"wrong": 1}},
				},
			},
		},
	}
	err := i.ValidateInterface(WithExampleValidation())
	if err == nil {
		t.Fatalf("expected error for invalid example against $ref schema")
	}
	// The valid example should not produce errors; only the invalid one should.
	if !containsProblemSubstring(err, `operations["greet"].examples["invalid"].input:`) {
		t.Fatalf("expected problem for invalid example, got %v", err)
	}
	if containsProblemSubstring(err, `operations["greet"].examples["valid"].input:`) {
		t.Fatalf("valid example should not produce errors, got %v", err)
	}
}
