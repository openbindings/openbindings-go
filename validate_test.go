package openbindings

import (
	"encoding/json"
	"testing"
)

func TestInterfaceValidate_RequiresOpenBindingsAndOperations(t *testing.T) {
	i := Interface{}
	err := i.Validate()
	if err == nil {
		t.Fatalf("expected error")
	}
	if _, ok := err.(*ValidationError); !ok {
		t.Fatalf("expected ValidationError, got %T", err)
	}
}

func TestInterfaceValidate_EventPayloadOptionalByDefault(t *testing.T) {
	i := Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"evt": {Kind: OperationKindEvent},
		},
	}
	if err := i.Validate(); err != nil {
		t.Fatalf("expected no error (payload optional by default), got %v", err)
	}
}

func TestInterfaceValidate_EventRequiresPayloadWhenOpted(t *testing.T) {
	i := Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"evt": {Kind: OperationKindEvent},
		},
	}
	if err := i.Validate(WithRequireEventPayload()); err == nil {
		t.Fatalf("expected error when requiring event payload")
	}
}

func TestInterfaceValidate_EventAllowsEmptyPayloadObject(t *testing.T) {
	i := Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"evt": {
				Kind:    OperationKindEvent,
				Payload: JSONSchema{},
			},
		},
	}
	if err := i.Validate(); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestInterfaceValidate_RequireSupportedVersion(t *testing.T) {
	i := Interface{
		OpenBindings: "0.2.0",
		Operations:   map[string]Operation{},
	}
	if err := i.Validate(WithRequireSupportedVersion()); err == nil {
		t.Fatalf("expected error")
	}
}

func TestInterfaceValidate_UnknownTopLevelFields_StrictMode(t *testing.T) {
	i := Interface{
		OpenBindings: "0.1.0",
		Operations:   map[string]Operation{},
		LosslessFields: LosslessFields{
			Unknown: map[string]json.RawMessage{
				"unknownField": json.RawMessage(`{"value":"unknownFieldValue"}`),
			},
		},
	}
	if err := i.Validate(WithRejectUnknownTypedFields()); err == nil {
		t.Fatalf("expected error")
	}
}

func TestInterfaceValidate_UnknownFields_StrictMode_CatchesNestedTypedObjects(t *testing.T) {
	i := Interface{
		OpenBindings: "0.1.0",
		Imports: map[string]string{
			"io.example@1.0": "https://example.com/interface.json",
		},
		Operations: map[string]Operation{
			"op": {
				Kind: OperationKindMethod,
				Satisfies: []Satisfies{
					{
						Interface: "io.example@1.0",
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
	if err := i.Validate(WithRejectUnknownTypedFields()); err == nil {
		t.Fatalf("expected error")
	}
}

func TestInterfaceValidate_StrictMode_CatchesOperationExampleUnknownFields(t *testing.T) {
	i := Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"op": {
				Kind: OperationKindMethod,
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
	if err := i.Validate(WithRejectUnknownTypedFields()); err == nil {
		t.Fatalf("expected error for unknown field in example")
	}
}

func TestInterfaceValidate_StrictMode_CatchesInlineTransformUnknownFields(t *testing.T) {
	i := Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"op": {Kind: OperationKindMethod},
		},
		Sources: map[string]Source{
			"api": {Format: "openapi@3.1", Location: "./api.json"},
		},
		Bindings: map[string]BindingEntry{
			"op.api": {
				Operation: "op",
				Source:    "api",
				InputTransform: &TransformOrRef{
					Transform: &Transform{
						Type:       "jsonata",
						Expression: "{ a: b }",
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
	if err := i.Validate(WithRejectUnknownTypedFields()); err == nil {
		t.Fatalf("expected error for unknown field in inline transform")
	}
}

func TestInterfaceValidate_AliasesMustBeUniqueAcrossOperations(t *testing.T) {
	i := Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"a": {Kind: OperationKindMethod, Aliases: []string{"shared"}},
			"b": {Kind: OperationKindMethod, Aliases: []string{"shared"}},
		},
	}
	if err := i.Validate(); err == nil {
		t.Fatalf("expected error")
	}
}

func TestInterfaceValidate_SatisfiesFieldsMustBeNonEmpty(t *testing.T) {
	i := Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"a": {Kind: OperationKindMethod, Satisfies: []Satisfies{{Interface: "", Operation: ""}}},
		},
	}
	if err := i.Validate(); err == nil {
		t.Fatalf("expected error")
	}
}

func TestInterfaceValidate_SatisfiesInterfaceMustExistInImports(t *testing.T) {
	i := Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"a": {
				Kind: OperationKindMethod,
				Satisfies: []Satisfies{
					{Interface: "nonexistent", Operation: "a"},
				},
			},
		},
	}
	err := i.Validate()
	if err == nil {
		t.Fatalf("expected error for dangling satisfies reference")
	}
	if !containsProblem(err, `operations["a"].satisfies[0].interface: references unknown import "nonexistent"`) {
		t.Fatalf("expected import reference error, got %v", err)
	}
}

func TestInterfaceValidate_SatisfiesInterfaceValidWhenImportExists(t *testing.T) {
	i := Interface{
		OpenBindings: "0.1.0",
		Imports: map[string]string{
			"taskmanager": "https://example.com/taskmanager/v1.json",
		},
		Operations: map[string]Operation{
			"tasks.create": {
				Kind: OperationKindMethod,
				Satisfies: []Satisfies{
					{Interface: "taskmanager", Operation: "tasks.create"},
				},
			},
		},
	}
	if err := i.Validate(); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestInterfaceValidate_OpenBindingsVersionErrorMessageIsStable(t *testing.T) {
	i := Interface{
		OpenBindings: "0.1",
		Operations:   map[string]Operation{},
	}
	err := i.Validate()
	if err == nil {
		t.Fatalf("expected error")
	}
	if err.Error() == "" || err.Error() == "invalid interface" {
		t.Fatalf("expected detailed error, got %q", err.Error())
	}
	if want := "openbindings: must be MAJOR.MINOR.PATCH (e.g. 0.1.0)"; !containsProblem(err, want) {
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

func TestInterfaceValidate_SourceMustHaveLocationOrContent(t *testing.T) {
	i := Interface{
		OpenBindings: "0.1.0",
		Operations:   map[string]Operation{},
		Sources: map[string]Source{
			"empty": {Format: "openapi@3.1"},
		},
	}
	err := i.Validate()
	if err == nil {
		t.Fatalf("expected error")
	}
	if !containsProblem(err, "sources[\"empty\"]: must have location or content") {
		t.Fatalf("expected location/content error, got %v", err)
	}
}

func TestInterfaceValidate_SourceCannotHaveBothLocationAndContent(t *testing.T) {
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
	err := i.Validate()
	if err == nil {
		t.Fatalf("expected error")
	}
	if !containsProblem(err, "sources[\"both\"]: cannot have both location and content") {
		t.Fatalf("expected location+content conflict error, got %v", err)
	}
}

func TestInterfaceValidate_TransformTypeMustBeJsonata(t *testing.T) {
	i := Interface{
		OpenBindings: "0.1.0",
		Operations:   map[string]Operation{},
		Transforms: map[string]Transform{
			"bad": {Type: "jq", Expression: ".foo"},
		},
	}
	err := i.Validate()
	if err == nil {
		t.Fatalf("expected error")
	}
	if !containsProblem(err, "transforms[\"bad\"].type: must be \"jsonata\" (got \"jq\")") {
		t.Fatalf("expected transform type error, got %v", err)
	}
}

func TestInterfaceValidate_TransformExpressionRequired(t *testing.T) {
	i := Interface{
		OpenBindings: "0.1.0",
		Operations:   map[string]Operation{},
		Transforms: map[string]Transform{
			"empty": {Type: "jsonata", Expression: ""},
		},
	}
	err := i.Validate()
	if err == nil {
		t.Fatalf("expected error")
	}
	if !containsProblem(err, "transforms[\"empty\"].expression: required") {
		t.Fatalf("expected expression required error, got %v", err)
	}
}

func TestInterfaceValidate_BindingTransformRefMustExist(t *testing.T) {
	i := Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"op": {Kind: OperationKindMethod},
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
	err := i.Validate()
	if err == nil {
		t.Fatalf("expected error")
	}
	if !containsProblem(err, "bindings[\"op.api\"].inputTransform.$ref: references unknown transform \"nonexistent\"") {
		t.Fatalf("expected transform ref error, got %v", err)
	}
}

func TestInterfaceValidate_OperationRefMustExist(t *testing.T) {
	i := Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"op": {Kind: OperationKindMethod},
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
	err := i.Validate()
	if err == nil {
		t.Fatalf("expected error")
	}
	if !containsProblem(err, "bindings[\"nonexistent.api\"].operation: references unknown operation \"nonexistent\"") {
		t.Fatalf("expected operation ref error, got %v", err)
	}
}

func TestInterfaceValidate_SourceRefMustExist(t *testing.T) {
	i := Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"op": {Kind: OperationKindMethod},
		},
		Bindings: map[string]BindingEntry{
			"op.nonexistent": {
				Operation: "op",
				Source:    "nonexistent",
			},
		},
	}
	err := i.Validate()
	if err == nil {
		t.Fatalf("expected error")
	}
	if !containsProblem(err, "bindings[\"op.nonexistent\"].source: references unknown source \"nonexistent\"") {
		t.Fatalf("expected source ref error, got %v", err)
	}
}

func TestInterfaceValidate_InlineTransformMustBeValid(t *testing.T) {
	i := Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"op": {Kind: OperationKindMethod},
		},
		Sources: map[string]Source{
			"api": {Format: "openapi@3.1", Location: "./api.json"},
		},
		Bindings: map[string]BindingEntry{
			"op.api": {
				Operation: "op",
				Source:    "api",
				OutputTransform: &TransformOrRef{
					Transform: &Transform{Type: "wrong", Expression: ""},
				},
			},
		},
	}
	err := i.Validate()
	if err == nil {
		t.Fatalf("expected error")
	}
	if !containsProblem(err, "bindings[\"op.api\"].outputTransform.type: must be \"jsonata\" (got \"wrong\")") {
		t.Fatalf("expected inline transform type error, got %v", err)
	}
	if !containsProblem(err, "bindings[\"op.api\"].outputTransform.expression: required") {
		t.Fatalf("expected inline transform expression error, got %v", err)
	}
}

func TestInterfaceValidate_ValidInterfaceWithTransforms(t *testing.T) {
	i := Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"pay": {Kind: OperationKindMethod},
		},
		Transforms: map[string]Transform{
			"toApi": {Type: "jsonata", Expression: "{ amount: total * 100 }"},
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
	if err := i.Validate(); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}
