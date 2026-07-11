package openbindings

import (
	"encoding/json"
	"strings"
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

func TestInterfaceValidate_RefusesHigherMajorVersion_OBI_T_04(t *testing.T) {
	// OBI-T-04: refuse to load when document's major version exceeds MaxTested.
	i := Interface{
		OpenBindings: "1.0.0",
		Operations:   map[string]Operation{},
	}
	err := i.Validate()
	if err == nil {
		t.Fatalf("expected error for higher-major version")
	}
	if !containsProblem(err, `openbindings: "1.0.0" exceeds this SDK's MaxTestedVersion "0.2.0" (OBI-T-04)`) {
		t.Fatalf("expected OBI-T-04 problem, got %v", err)
	}
}

func TestInterfaceValidate_RefusesPre1HigherMinor_OBI_T_04(t *testing.T) {
	// OBI-T-04: while MaxTested is pre-1.0, refuse strictly higher minor too.
	i := Interface{
		OpenBindings: "0.99.0",
		Operations:   map[string]Operation{},
	}
	err := i.Validate()
	if err == nil {
		t.Fatalf("expected error for pre-1.0 higher-minor version")
	}
	if !containsProblem(err, `openbindings: "0.99.0" exceeds this SDK's MaxTestedVersion "0.2.0" (OBI-T-04)`) {
		t.Fatalf("expected OBI-T-04 problem, got %v", err)
	}
}

func TestInterfaceValidate_RefusesInvalidSemver_OBI_D_16(t *testing.T) {
	i := Interface{
		OpenBindings: "0.1",
		Operations:   map[string]Operation{},
	}
	err := i.Validate()
	if err == nil {
		t.Fatalf("expected error for invalid semver")
	}
	if !containsProblem(err, `openbindings: "0.1" is not a valid SemVer 2.0.0 string (OBI-D-12)`) {
		t.Fatalf("expected OBI-D-12 problem, got %v", err)
	}
}

func TestInterfaceValidate_UnknownTopLevelFields_StrictMode(t *testing.T) {
	i := Interface{
		OpenBindings: "0.2.0",
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
		OpenBindings: "0.2.0",
		Operations: map[string]Operation{
			"op": {},
		},
		Sources: map[string]Source{
			"src": {
				Format:   "openapi@3.1",
				Location: "https://api.example.com/api.json",
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
		OpenBindings: "0.2.0",
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
	if err := i.Validate(WithRejectUnknownTypedFields()); err == nil {
		t.Fatalf("expected error for unknown field in example")
	}
}

func TestInterfaceValidate_StrictMode_CatchesInlineTransformUnknownFields(t *testing.T) {
	i := Interface{
		OpenBindings: "0.2.0",
		Operations: map[string]Operation{
			"op": {},
		},
		Sources: map[string]Source{
			"api": {Format: "openapi@3.1", Location: "https://api.example.com/api.json"},
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
	if err := i.Validate(WithRejectUnknownTypedFields()); err == nil {
		t.Fatalf("expected error for unknown field on binding entry")
	}
}

func TestInterfaceValidate_AliasesMustBeUniqueAcrossOperations(t *testing.T) {
	i := Interface{
		OpenBindings: "0.2.0",
		Operations: map[string]Operation{
			"a": {Aliases: []string{"shared"}},
			"b": {Aliases: []string{"shared"}},
		},
	}
	if err := i.Validate(); err == nil {
		t.Fatalf("expected error")
	}
}

func TestInterfaceValidate_AliasAsContractNameIsValid(t *testing.T) {
	// Cross-document correspondence is now expressed by adopting the shared
	// contract's operation name as an alias; no roles/satisfies machinery.
	i := Interface{
		OpenBindings: "0.2.0",
		Operations: map[string]Operation{
			"createTask": {
				Aliases: []string{"tasks.create"},
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
	if want := `openbindings: "0.1" is not a valid SemVer 2.0.0 string (OBI-D-12)`; !containsProblem(err, want) {
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
		OpenBindings: "0.2.0",
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

func TestInterfaceValidate_SourceAcceptsBothLocationAndContent(t *testing.T) {
	i := Interface{
		OpenBindings: "0.2.0",
		Operations:   map[string]Operation{},
		Sources: map[string]Source{
			"both": {
				Format:   "openapi@3.1",
				Location: "https://api.example.com/api.json",
				Content:  map[string]any{"openapi": "3.1.0"},
			},
		},
	}
	err := i.Validate()
	if err != nil {
		t.Fatalf("expected no error for source with both location and content, got %v", err)
	}
}

func TestInterfaceValidate_EmptyTransformExpressionAccepted(t *testing.T) {
	// No document rule forbids an empty transform expression (the schema
	// allows any string). Evaluation failures surface at invoke time via
	// ErrEmptyTransformExpression, not at document validation.
	i := Interface{
		OpenBindings: "0.2.0",
		Operations:   map[string]Operation{},
		Transforms: map[string]Transform{
			"empty": "",
		},
	}
	if err := i.Validate(); err != nil {
		t.Fatalf("expected empty named transform to be accepted, got %v", err)
	}
}

func TestInterfaceValidate_SourceLocationFormatDefinedAddress(t *testing.T) {
	// OBI-D-05: a sources[*].location may be a format-defined absolute address
	// (e.g. a gRPC host:port), not only a URI. These need no base URI and must
	// not be rejected as relative references, including IP-literal and IPv6
	// hosts that net/url cannot parse as a URI.
	for _, addr := range []string{
		"grpc.example.com:443",
		"localhost:50051",
		"10.0.0.1:443",
		"[::1]:443",
		"dns:///grpc.example.com:443",
		"https://api.example.com/openapi.json",
	} {
		t.Run(addr, func(t *testing.T) {
			i := Interface{
				OpenBindings: "0.2.0",
				Operations:   map[string]Operation{},
				Sources: map[string]Source{
					"svc": {Format: "grpc@1.0", Location: addr},
				},
			}
			if err := i.Validate(); err != nil {
				t.Fatalf("location %q should be accepted, got %v", addr, err)
			}
		})
	}
}

func TestInterfaceValidate_SourceLocationRelativeRejected(t *testing.T) {
	// OBI-D-05: a relative reference needs a base URI and is not allowed.
	for _, loc := range []string{"./openapi.json", "openapi.json", "../api/openapi.json", "/abs/openapi.json"} {
		t.Run(loc, func(t *testing.T) {
			i := Interface{
				OpenBindings: "0.2.0",
				Operations:   map[string]Operation{},
				Sources: map[string]Source{
					"api": {Format: "openapi@3.1", Location: loc},
				},
			}
			err := i.Validate()
			if err == nil {
				t.Fatalf("relative location %q should be rejected", loc)
			}
			if !strings.Contains(err.Error(), "not a relative reference (OBI-D-05)") {
				t.Fatalf("expected OBI-D-05 relative-reference error for %q, got %v", loc, err)
			}
		})
	}
}

func TestInterfaceValidate_PlainNameFragmentRefRejected(t *testing.T) {
	// OBI-D-05: same-document schema $refs are JSON Pointer fragments; a
	// plain-name ($anchor) fragment is rejected — the schemas map is the
	// document's named-schema mechanism.
	i := Interface{
		OpenBindings: "0.2.0",
		Operations: map[string]Operation{
			"getTask": {Output: JSONSchema{"$ref": "#task"}},
		},
		Schemas: map[string]JSONSchema{
			"Task": {"$anchor": "task", "type": "object"},
		},
	}
	err := i.Validate()
	if err == nil {
		t.Fatal("plain-name fragment $ref should be rejected")
	}
	if !strings.Contains(err.Error(), "plain-name fragment") {
		t.Fatalf("expected plain-name fragment error, got %v", err)
	}
}

func TestInterfaceValidate_DanglingSchemaRefRejected(t *testing.T) {
	// OBI-D-16: a same-document schema $ref resolves from the document root;
	// a dangling pointer invalidates the document (internal referential
	// integrity, matching OBI-D-08/09/10).
	i := Interface{
		OpenBindings: "0.2.0",
		Operations: map[string]Operation{
			"getTask": {Output: JSONSchema{"$ref": "#/schemas/Missing"}},
		},
		Schemas: map[string]JSONSchema{"Task": {"type": "object"}},
	}
	err := i.Validate()
	if err == nil {
		t.Fatal("dangling same-document $ref should be rejected")
	}
	if !strings.Contains(err.Error(), "does not resolve within the document (OBI-D-16)") {
		t.Fatalf("expected OBI-D-16 error, got %v", err)
	}
}

func TestInterfaceValidate_PercentEncodedFragmentResolves(t *testing.T) {
	// RFC 6901 §6 / OBI-D-16: the fragment is percent-decoded first, then
	// evaluated as a JSON Pointer — "#/schemas/T%61sk" addresses the
	// schemas key "Task", exactly as "#/schemas/Task" does.
	i := Interface{
		OpenBindings: "0.2.0",
		Operations: map[string]Operation{
			"getTask": {Output: JSONSchema{"$ref": "#/schemas/T%61sk"}},
		},
		Schemas: map[string]JSONSchema{"Task": {"type": "object"}},
	}
	if err := i.Validate(); err != nil {
		t.Fatalf("percent-encoded fragment should resolve, got %v", err)
	}
}

func TestInterfaceValidate_DanglingPercentEncodedFragmentRejected(t *testing.T) {
	// A percent-encoded fragment that decodes to a genuinely-missing
	// location still fails OBI-D-16 — decoding does not weaken the
	// referential-integrity check.
	i := Interface{
		OpenBindings: "0.2.0",
		Operations: map[string]Operation{
			"getTask": {Output: JSONSchema{"$ref": "#/schemas/M%69ssing"}},
		},
		Schemas: map[string]JSONSchema{"Task": {"type": "object"}},
	}
	err := i.Validate()
	if err == nil {
		t.Fatal("dangling percent-encoded $ref should be rejected")
	}
	if !strings.Contains(err.Error(), "does not resolve within the document (OBI-D-16)") {
		t.Fatalf("expected OBI-D-16 error, got %v", err)
	}
}

func TestInterfaceValidate_NestedIDScopeSkipsD16(t *testing.T) {
	// A $ref inside a schema declaring its own $id resolves against that
	// resource's base per §10 and is out of OBI-D-16's scope.
	i := Interface{
		OpenBindings: "0.2.0",
		Operations: map[string]Operation{
			"getTask": {Output: JSONSchema{"$ref": "#/schemas/Task"}},
		},
		Schemas: map[string]JSONSchema{
			"Task": {
				"$id":        "https://example.com/task.schema.json",
				"type":       "object",
				"properties": map[string]any{"parent": map[string]any{"$ref": "#/$defs/base"}},
				"$defs":      map[string]any{"base": map[string]any{"type": "string"}},
			},
		},
	}
	if err := i.Validate(); err != nil {
		t.Fatalf("resource-internal $ref should be out of D-16 scope, got %v", err)
	}
}

func TestInterfaceValidate_AnchorInsideIDScopePermitted(t *testing.T) {
	// OBI-D-05's pointer-form rule carves out $id-declaring schemas: their
	// internal fragments (including plain-name anchors) are that
	// resource's business, per the same scope rule as OBI-D-16.
	i := Interface{
		OpenBindings: "0.2.0",
		Operations: map[string]Operation{
			"getTask": {Output: JSONSchema{"$ref": "#/schemas/Task"}},
		},
		Schemas: map[string]JSONSchema{
			"Task": {
				"$id":        "https://example.com/task.schema.json",
				"type":       "object",
				"properties": map[string]any{"kind": map[string]any{"$ref": "#kindAnchor"}},
				"$defs":      map[string]any{"kind": map[string]any{"$anchor": "kindAnchor", "type": "string"}},
			},
		},
	}
	if err := i.Validate(); err != nil {
		t.Fatalf("anchor inside $id scope must be permitted, got %v", err)
	}
}

func TestInterfaceValidate_NestedRelativeIDInsideIDScopePermitted(t *testing.T) {
	// §10 clause 2 / OBI-D-05: a nested $id inside a schema that already
	// declares its own $id resolves against that resource's base per JSON
	// Schema 2020-12 and MAY be relative — that resource's internal
	// business, the same scope carve-out as $ref/$anchor/dynamic-pair.
	i := Interface{
		OpenBindings: "0.2.0",
		Operations: map[string]Operation{
			"getTask": {Output: JSONSchema{"$ref": "#/schemas/Task"}},
		},
		Schemas: map[string]JSONSchema{
			"Task": {
				"$id":   "https://example.com/task.schema.json",
				"type":  "object",
				"$defs": map[string]any{"kind": map[string]any{"$id": "kind.schema.json", "type": "string"}},
			},
		},
	}
	if err := i.Validate(); err != nil {
		t.Fatalf("nested relative $id inside $id scope must be permitted, got %v", err)
	}
}

func TestInterfaceValidate_TopLevelRelativeIDRejected(t *testing.T) {
	// A schema $id at an OBI position (not nested inside another
	// $id-declaring schema) MUST still be absolute (OBI-D-05).
	i := Interface{
		OpenBindings: "0.2.0",
		Operations: map[string]Operation{
			"getTask": {Output: JSONSchema{"$ref": "#/schemas/Task"}},
		},
		Schemas: map[string]JSONSchema{
			"Task": {"$id": "task.schema.json", "type": "object"},
		},
	}
	err := i.Validate()
	if err == nil {
		t.Fatal("relative $id at an OBI position should be rejected")
	}
	if !strings.Contains(err.Error(), `$id: "task.schema.json" must be an absolute URI (OBI-D-05)`) {
		t.Fatalf("expected OBI-D-05 $id error, got %v", err)
	}
}

func TestInterfaceValidate_DynamicRefAtOperationPositionRejected(t *testing.T) {
	// OBI-D-05: the dynamic pair does not appear at OBI positions at all;
	// $dynamicRef on an operation's output schema is a violation.
	i := Interface{
		OpenBindings: "0.2.0",
		Operations: map[string]Operation{
			"getTask": {Output: JSONSchema{"$dynamicRef": "#node"}},
		},
	}
	err := i.Validate()
	if err == nil {
		t.Fatal("$dynamicRef at an OBI position should be rejected")
	}
	if !strings.Contains(err.Error(), "$dynamicRef does not appear at OBI positions") || !strings.Contains(err.Error(), "OBI-D-05") {
		t.Fatalf("expected OBI-D-05 $dynamicRef error, got %v", err)
	}
}

func TestInterfaceValidate_DynamicAnchorInSchemasMapRejected(t *testing.T) {
	// OBI-D-05: $dynamicAnchor would be a second named-schema mechanism
	// competing with the schemas map, exactly as $anchor would.
	i := Interface{
		OpenBindings: "0.2.0",
		Operations: map[string]Operation{
			"getTask": {Output: JSONSchema{"$ref": "#/schemas/Task"}},
		},
		Schemas: map[string]JSONSchema{
			"Task": {"$dynamicAnchor": "task", "type": "object"},
		},
	}
	err := i.Validate()
	if err == nil {
		t.Fatal("$dynamicAnchor at an OBI position should be rejected")
	}
	if !strings.Contains(err.Error(), "$dynamicAnchor does not appear at OBI positions") || !strings.Contains(err.Error(), "OBI-D-05") {
		t.Fatalf("expected OBI-D-05 $dynamicAnchor error, got %v", err)
	}
}

func TestInterfaceValidate_DynamicPairInsideIDScopePermitted(t *testing.T) {
	// A schema resource declaring its own $id may use the dynamic pair
	// internally, per the same scope rule as $ref/$anchor — including full
	// 2020-12 recursive-extension semantics (a sibling $dynamicAnchor plus a
	// nested $dynamicRef referencing it).
	i := Interface{
		OpenBindings: "0.2.0",
		Operations: map[string]Operation{
			"getTask": {Output: JSONSchema{"$ref": "#/schemas/Tree"}},
		},
		Schemas: map[string]JSONSchema{
			"Tree": {
				"$id":            "https://example.com/tree.schema.json",
				"$dynamicAnchor": "node",
				"type":           "object",
				"properties": map[string]any{
					"children": map[string]any{
						"type":  "array",
						"items": map[string]any{"$dynamicRef": "#node"},
					},
				},
			},
		},
	}
	if err := i.Validate(); err != nil {
		t.Fatalf("dynamic pair inside $id scope must be permitted, got %v", err)
	}
}

func TestInterfaceValidate_PropertyNamedDynamicRefIsData(t *testing.T) {
	// A property NAMED $dynamicRef under `properties` is data, not a
	// keyword: the walker is keyword-shape-aware, mirroring the same guard
	// already in place for $ref.
	i := Interface{
		OpenBindings: "0.2.0",
		Operations: map[string]Operation{
			"getTask": {Output: JSONSchema{
				"type": "object",
				"properties": map[string]any{
					"$dynamicRef":    map[string]any{"type": "string"},
					"$dynamicAnchor": map[string]any{"type": "string"},
				},
			}},
		},
	}
	if err := i.Validate(); err != nil {
		t.Fatalf("property named $dynamicRef/$dynamicAnchor must be treated as data, got %v", err)
	}
}

func TestInterfaceValidate_BindingTransformRefMustExist(t *testing.T) {
	i := Interface{
		OpenBindings: "0.2.0",
		Operations: map[string]Operation{
			"op": {},
		},
		Sources: map[string]Source{
			"api": {Format: "openapi@3.1", Location: "https://api.example.com/api.json"},
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
	if !containsProblem(err, `bindings["op.api"].inputTransform.$ref: references unknown transform "nonexistent" (OBI-D-10)`) {
		t.Fatalf("expected transform ref error, got %v", err)
	}
}

func TestInterfaceValidate_OperationRefMustExist(t *testing.T) {
	i := Interface{
		OpenBindings: "0.2.0",
		Operations: map[string]Operation{
			"op": {},
		},
		Sources: map[string]Source{
			"api": {Format: "openapi@3.1", Location: "https://api.example.com/api.json"},
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
	if !containsProblem(err, `bindings["nonexistent.api"].operation: references unknown operation "nonexistent" (OBI-D-08)`) {
		t.Fatalf("expected operation ref error, got %v", err)
	}
}

func TestInterfaceValidate_SourceRefMustExist(t *testing.T) {
	i := Interface{
		OpenBindings: "0.2.0",
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
	err := i.Validate()
	if err == nil {
		t.Fatalf("expected error")
	}
	if !containsProblem(err, `bindings["op.nonexistent"].source: references unknown source "nonexistent" (OBI-D-09)`) {
		t.Fatalf("expected source ref error, got %v", err)
	}
}

func TestInterfaceValidate_EmptyInlineTransformAccepted(t *testing.T) {
	// No document rule forbids an empty inline transform expression; the
	// invoke layer fails empty expressions at run time instead.
	i := Interface{
		OpenBindings: "0.2.0",
		Operations: map[string]Operation{
			"op": {},
		},
		Sources: map[string]Source{
			"api": {Format: "openapi@3.1", Location: "https://api.example.com/api.json"},
		},
		Bindings: map[string]BindingEntry{
			"op.api": {
				Operation:       "op",
				Source:          "api",
				OutputTransform: &TransformOrRef{Inline: ""},
			},
		},
	}
	if err := i.Validate(); err != nil {
		t.Fatalf("expected empty inline transform to be accepted, got %v", err)
	}
}

func TestInterfaceValidate_ValidInterfaceWithTransforms(t *testing.T) {
	i := Interface{
		OpenBindings: "0.2.0",
		Operations: map[string]Operation{
			"pay": {},
		},
		Transforms: map[string]Transform{
			"toApi": "{ amount: total * 100 }",
		},
		Sources: map[string]Source{
			"stripe": {Format: "openapi@3.1", Location: "https://api.example.com/stripe.json"},
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

func TestInterfaceValidate_ExampleValidation_ValidExamplePasses(t *testing.T) {
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
	if err := i.Validate(); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestInterfaceValidate_ExampleValidation_InvalidInputFails(t *testing.T) {
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
	err := i.Validate()
	if err == nil {
		t.Fatalf("expected error for invalid example input")
	}
	if !containsProblemSubstring(err, `operations["greet"].examples["bad"].input:`) {
		t.Fatalf("expected OBI-D-11 input problem, got %v", err)
	}
	if !containsProblemSubstring(err, "OBI-D-11") {
		t.Fatalf("expected OBI-D-11 tag, got %v", err)
	}
}

func TestInterfaceValidate_ExampleValidation_InvalidOutputFails(t *testing.T) {
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
	err := i.Validate()
	if err == nil {
		t.Fatalf("expected error for invalid example output")
	}
	if !containsProblemSubstring(err, `operations["greet"].examples["bad"].output:`) {
		t.Fatalf("expected OBI-D-11 output problem, got %v", err)
	}
	if !containsProblemSubstring(err, "OBI-D-11") {
		t.Fatalf("expected OBI-D-11 tag, got %v", err)
	}
}

func TestInterfaceValidate_ExampleValidation_RunsByDefault(t *testing.T) {
	i := newInterfaceWithExamples(
		JSONSchema{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}, "required": []any{"name"}},
		nil,
		map[string]OperationExample{
			"bad": {
				Input: map[string]any{"wrong": 42}, // missing required "name"
			},
		},
	)
	if err := i.Validate(); err == nil {
		t.Fatalf("expected error because OBI-D-11 is always enforced")
	}
}

func TestInterfaceValidate_ExampleValidation_NoSchemasSkipsGracefully(t *testing.T) {
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
	if err := i.Validate(); err != nil {
		t.Fatalf("expected no error when schemas are absent, got %v", err)
	}
}

func TestInterfaceValidate_ExampleValidation_NoExamplesSkipsGracefully(t *testing.T) {
	// Operation has schemas but no examples.
	i := newInterfaceWithExamples(
		JSONSchema{"type": "object"},
		JSONSchema{"type": "object"},
		nil,
	)
	if err := i.Validate(); err != nil {
		t.Fatalf("expected no error when examples are absent, got %v", err)
	}
}

func TestInterfaceValidate_ExampleValidation_ExampleWithoutInputOrOutput(t *testing.T) {
	// Example has neither input nor output -- should be skipped, not crash.
	i := newInterfaceWithExamples(
		JSONSchema{"type": "object"},
		JSONSchema{"type": "object"},
		map[string]OperationExample{
			"empty": {Description: "an example with no data"},
		},
	)
	if err := i.Validate(); err != nil {
		t.Fatalf("expected no error for example without input/output, got %v", err)
	}
}

func TestInterfaceValidate_ExampleValidation_WithSchemaRef(t *testing.T) {
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
	err := i.Validate()
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

func TestParseDocument_UnknownTopLevelFieldValidates_OBI_T_02(t *testing.T) {
	// OBI-T-02: unknown non-x- fields are ignored, not fatal. The vendored
	// v0.2.0 schema keeps additionalProperties open at the root; "security"
	// (removed from the spec in 0.2.0) must validate as an unknown field.
	doc := []byte(`{"openbindings":"0.2.0","operations":{},"security":"abc"}`)
	iface, err := ParseDocument(doc)
	if err != nil {
		t.Fatalf("expected unknown top-level field to parse, got %v", err)
	}
	if err := iface.Validate(); err != nil {
		t.Fatalf("expected unknown top-level field to validate (OBI-T-02), got %v", err)
	}
}

func TestParseDocument_TransformRefWithExtensionKeyValidates_OBI_T_03(t *testing.T) {
	// OBI-T-03: x- prefixed fields are extensions and must not fail
	// validation; the v0.2.0 schema allows additional properties on the
	// transform $ref object form.
	doc := []byte(`{
		"openbindings": "0.2.0",
		"operations": {"op": {}},
		"transforms": {"t": "$.payload"},
		"sources": {"api": {"format": "openapi@3.1", "location": "https://api.example.com/api.json"}},
		"bindings": {
			"op.api": {
				"operation": "op",
				"source": "api",
				"inputTransform": {"$ref": "#/transforms/t", "x-note": "hi"}
			}
		}
	}`)
	iface, err := ParseDocument(doc)
	if err != nil {
		t.Fatalf("expected transform $ref with x- key to parse, got %v", err)
	}
	if err := iface.Validate(); err != nil {
		t.Fatalf("expected transform $ref with x- key to validate (OBI-T-03), got %v", err)
	}
}

func TestParseDocument_RejectsInvalidUTF8_OBI_D_01(t *testing.T) {
	// OBI-D-01: documents are UTF-8 encoded JSON. 0xFF can never appear in
	// valid UTF-8.
	doc := []byte(`{"openbindings":"0.2.0","name":"X`)
	doc = append(doc, 0xFF, 0xFE)
	doc = append(doc, []byte(`","operations":{}}`)...)
	_, err := ParseDocument(doc)
	if err == nil {
		t.Fatalf("expected invalid-UTF-8 parse error")
	}
	if !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("expected error mentioning UTF-8, got %v", err)
	}
}

func TestInterfaceValidate_ExampleValidation_ExplicitNullIsValidated(t *testing.T) {
	// OBI-D-11: an explicit JSON null is a provided example value distinct
	// from an absent field, and must validate against the schema.
	doc := []byte(`{
		"openbindings": "0.2.0",
		"operations": {
			"greet": {
				"input": {"type": "object"},
				"examples": {"nullCase": {"input": null}}
			}
		}
	}`)
	iface, err := ParseDocument(doc)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	err = iface.Validate()
	if err == nil {
		t.Fatalf("expected explicit-null example input to fail against {\"type\":\"object\"}")
	}
	if !containsProblemSubstring(err, `operations["greet"].examples["nullCase"].input:`) {
		t.Fatalf("expected null example input problem, got %v", err)
	}
	if !containsProblemSubstring(err, "OBI-D-11") {
		t.Fatalf("expected OBI-D-11 tag, got %v", err)
	}
}

func TestInterfaceValidate_ExampleValidation_AbsentInputStillSkipped(t *testing.T) {
	// Absent input/output (as opposed to explicit null) is not validated.
	doc := []byte(`{
		"openbindings": "0.2.0",
		"operations": {
			"greet": {
				"input": {"type": "object"},
				"examples": {"onlyOutput": {"output": {"ok": true}}}
			}
		}
	}`)
	iface, err := ParseDocument(doc)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := iface.Validate(); err != nil {
		t.Fatalf("expected absent example input to be skipped, got %v", err)
	}
}

func TestInterfaceValidate_ExampleValidation_ExternalRefAbstains(t *testing.T) {
	// A schema whose $ref points outside the document cannot be resolved by
	// a document validator; per the spec's capability-relative verification
	// posture it abstains from example validation rather than failing the
	// document.
	i := newInterfaceWithExamples(
		JSONSchema{"$ref": "https://schemas.example.com/user.json"},
		nil,
		map[string]OperationExample{
			"opaque": {Input: map[string]any{"anything": true}},
		},
	)
	if err := i.Validate(); err != nil {
		t.Fatalf("expected abstention for external $ref schema, got %v", err)
	}
}

func TestInterfaceValidate_ExampleValidation_ExternalRefInSchemasMapAbstains(t *testing.T) {
	// External $refs reachable via the document's schemas map also trigger
	// abstention: the local schema space is not fully resolvable.
	i := Interface{
		OpenBindings: "0.2.0",
		Schemas: map[string]JSONSchema{
			"User": {"$ref": "https://schemas.example.com/user.json"},
		},
		Operations: map[string]Operation{
			"greet": {
				Input: JSONSchema{"$ref": "#/schemas/User"},
				Examples: map[string]OperationExample{
					"opaque": {Input: map[string]any{"anything": true}},
				},
			},
		},
	}
	if err := i.Validate(); err != nil {
		t.Fatalf("expected abstention for external $ref via schemas map, got %v", err)
	}
}

func TestInterfaceValidate_ExampleValidation_InternalRefStillValidated(t *testing.T) {
	// Internal #/schemas/ refs remain fully validated (no abstention).
	i := Interface{
		OpenBindings: "0.2.0",
		Schemas: map[string]JSONSchema{
			"User": {"type": "object", "required": []any{"name"}},
		},
		Operations: map[string]Operation{
			"greet": {
				Input: JSONSchema{"$ref": "#/schemas/User"},
				Examples: map[string]OperationExample{
					"bad": {Input: map[string]any{"wrong": 42}},
				},
			},
		},
	}
	err := i.Validate()
	if err == nil {
		t.Fatalf("expected internal-ref example validation to fail")
	}
	if !containsProblemSubstring(err, "OBI-D-11") {
		t.Fatalf("expected OBI-D-11 tag, got %v", err)
	}
}

// OBI-T-04's refusal runs downward too: a version below the SDK's minimum is
// refused rather than processed under the wrong rules (pre-1.0 minors may
// change field semantics in either direction — the priority→preference
// inversion being the live example).
func TestInterfaceValidate_RefusesBelowMinSupported(t *testing.T) {
	iface := Interface{
		OpenBindings: "0.1.0",
		Operations:   map[string]Operation{},
	}
	err := iface.Validate()
	if err == nil {
		t.Fatal("a document below MinSupportedVersion must refuse")
	}
	msg := err.Error()
	if !strings.Contains(msg, "below this SDK's MinSupportedVersion") || !strings.Contains(msg, "OBI-T-04") {
		t.Errorf("refusal must cite the floor and the rule, got: %s", msg)
	}
}
