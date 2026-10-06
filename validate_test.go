package openbindings

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestDocumentValidate_RequiresOpenBindingsAndOperations(t *testing.T) {
	i := Document{}
	_, err := i.Validate()
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

func TestDocumentValidate_RefusesHigherMajorVersion_OBI_T_04(t *testing.T) {
	// OBI-T-04: a higher major is outside the declared supported line.
	i := Document{
		OpenBindings: "1.0.0",
		Operations:   map[string]Operation{},
	}
	_, err := i.Validate()
	if err == nil {
		t.Fatalf("expected error for higher-major version")
	}
	var refusal *VersionRefusalError
	if !errors.As(err, &refusal) || err.Error() != `openbindings: document declares version "1.0.0", newer than the release line this implementation supports (0.2.x) (OBI-T-04)` {
		t.Fatalf("expected an OBI-T-04 version refusal, got %v", err)
	}
}

func TestDocumentValidate_RefusesHigherMinor_OBI_T_04(t *testing.T) {
	// OBI-T-04: a higher minor is also a different specification line.
	i := Document{
		OpenBindings: "0.99.0",
		Operations:   map[string]Operation{},
	}
	_, err := i.Validate()
	if err == nil {
		t.Fatalf("expected error for higher-minor version")
	}
	var refusal *VersionRefusalError
	if !errors.As(err, &refusal) || err.Error() != `openbindings: document declares version "0.99.0", newer than the release line this implementation supports (0.2.x) (OBI-T-04)` {
		t.Fatalf("expected an OBI-T-04 version refusal, got %v", err)
	}
}

func TestDocumentValidate_InvalidSemverViolatesD09(t *testing.T) {
	i := Document{
		OpenBindings: "0.1",
		Operations:   map[string]Operation{},
	}
	_, err := i.Validate()
	if err == nil {
		t.Fatalf("expected error for invalid semver")
	}
	if !containsProblem(err, `/openbindings: "0.1" is not a valid SemVer 2.0.0 string (OBI-D-09)`) {
		t.Fatalf("expected OBI-D-09 problem, got %v", err)
	}
}

func TestDocumentValidate_AliasesMustBeUniqueAcrossOperations(t *testing.T) {
	i := Document{
		OpenBindings: "0.2.0",
		Operations: map[string]Operation{
			"a": {Aliases: []string{"shared"}},
			"b": {Aliases: []string{"shared"}},
		},
	}
	if _, err := i.Validate(); err == nil {
		t.Fatalf("expected error")
	}
}

func TestDocumentValidate_AliasAsContractNameIsValid(t *testing.T) {
	// Cross-document correspondence is now expressed by adopting the shared
	// contract's operation name as an alias; no roles/satisfies machinery.
	i := Document{
		OpenBindings: "0.2.0",
		Operations: map[string]Operation{
			"createTask": {
				Aliases: []string{"tasks.create"},
			},
		},
	}
	if _, err := i.Validate(); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestDocumentValidate_OpenBindingsVersionErrorMessageIsStable(t *testing.T) {
	i := Document{
		OpenBindings: "0.1",
		Operations:   map[string]Operation{},
	}
	_, err := i.Validate()
	if err == nil {
		t.Fatalf("expected error")
	}
	if err.Error() == "" || err.Error() == "non-conformant interface" {
		t.Fatalf("expected detailed error, got %q", err.Error())
	}
	if want := `/openbindings: "0.1" is not a valid SemVer 2.0.0 string (OBI-D-09)`; !containsProblem(err, want) {
		t.Fatalf("expected problem %q, got %q", want, err.Error())
	}
}

// problemLines renders a *ValidationError's findings as its message does,
// one line each, or returns nil for any other error.
func problemLines(err error) []string {
	ve, ok := err.(*ValidationError)
	if !ok {
		return nil
	}
	lines := make([]string, len(ve.Findings))
	for i, finding := range ve.Findings {
		lines[i] = formatFinding(finding.Path, finding.Message, finding.Rule)
	}
	return lines
}

func containsProblem(err error, want string) bool {
	for _, p := range problemLines(err) {
		if p == want {
			return true
		}
	}
	return false
}

// A source is its kind and optional content the core gives no meaning
// (§5.4), and a binding's content is likewise any JSON value its source's
// kind defines (§5.3): a source without
// content, source and binding content of every JSON type, and anything within
// them, relative addresses and $ref members included, break no core rule.
func TestDocumentValidate_SourceAndBindingContentAreTheKinds(t *testing.T) {
	i := Document{
		OpenBindings: "0.2.0",
		Operations:   map[string]Operation{"a": {}},
		Sources: map[string]Source{
			"bare":     {Kind: "x@1"},
			"null":     {Kind: "x@1", Content: json.RawMessage(`null`)},
			"relative": {Kind: "x@1", Content: json.RawMessage(`{"location":"./openapi.json","$ref":"#anchor"}`)},
			"text":     {Kind: "x@1", Content: json.RawMessage(`"openapi: 3.1.0"`)},
		},
		Bindings: map[string]Binding{
			"a.bare":     {Operation: "a", Source: "bare"},
			"a.null":     {Operation: "a", Source: "null", Content: json.RawMessage(`null`)},
			"a.relative": {Operation: "a", Source: "relative", Content: json.RawMessage(`{"$ref":"other.json"}`)},
			"a.text":     {Operation: "a", Source: "text", Content: json.RawMessage(`["a",1,true]`)},
		},
	}
	report, err := i.Validate()
	if err != nil {
		t.Fatalf("source and binding content are the kind's, got %v", err)
	}
	if report.Conclusion != ConclusionConformant {
		t.Fatalf("every rule is decided for a host object: conclusion %s, inconclusive %v", report.Conclusion, report.Inconclusive)
	}
}

func TestDocumentValidate_PlainNameFragments(t *testing.T) {
	// OBI-D-12: a plain-name fragment in the document resource identifies the
	// schema there that declares the name, and fails when none does.
	i := Document{
		OpenBindings: "0.2.0",
		Operations: map[string]Operation{
			"getTask": {Output: map[string]any{"$ref": "#task"}},
		},
		Schemas: map[string]JSONSchema{
			"Task": map[string]any{"$anchor": "task", "type": "object"},
		},
	}
	if _, err := i.Validate(); err != nil {
		t.Fatalf("a declared plain name identifies its schema, got %v", err)
	}
	i.Operations["getTask"] = Operation{Output: map[string]any{"$ref": "#missing"}}
	_, err := i.Validate()
	if err == nil || !strings.Contains(err.Error(), "names a plain name no schema in the document resource declares (OBI-D-12)") {
		t.Fatalf("an undeclared plain name violates OBI-D-12, got %v", err)
	}
}

func TestDocumentValidate_DanglingSchemaRefRejected(t *testing.T) {
	// OBI-D-12: a same-document schema $ref resolves from the document root;
	// a dangling pointer invalidates the document (internal referential
	// integrity, matching OBI-D-07/09).
	i := Document{
		OpenBindings: "0.2.0",
		Operations: map[string]Operation{
			"getTask": {Output: map[string]any{"$ref": "#/schemas/Missing"}},
		},
		Schemas: map[string]JSONSchema{"Task": map[string]any{"type": "object"}},
	}
	_, err := i.Validate()
	if err == nil {
		t.Fatal("dangling same-document $ref should be rejected")
	}
	if !strings.Contains(err.Error(), "does not resolve within the document (OBI-D-12)") {
		t.Fatalf("expected OBI-D-12 error, got %v", err)
	}
}

func TestDocumentValidate_PercentEncodedFragmentResolves(t *testing.T) {
	// URI-fragment JSON Pointers are percent-decoded before pointer evaluation.
	i := Document{
		OpenBindings: "0.2.0",
		Operations: map[string]Operation{
			"getTask": {Output: map[string]any{"$ref": "#/schemas/T%61sk"}},
		},
		Schemas: map[string]JSONSchema{"Task": map[string]any{"type": "object"}},
	}
	if _, err := i.Validate(); err != nil {
		t.Fatalf("percent-encoded fragment should resolve to Task, got %v", err)
	}
}

func TestDocumentValidate_DanglingPercentEncodedFragmentRejected(t *testing.T) {
	// Decoding identifies Missing, so the reference violates OBI-D-12.
	i := Document{
		OpenBindings: "0.2.0",
		Operations: map[string]Operation{
			"getTask": {Output: map[string]any{"$ref": "#/schemas/M%69ssing"}},
		},
		Schemas: map[string]JSONSchema{"Task": map[string]any{"type": "object"}},
	}
	_, err := i.Validate()
	if err == nil {
		t.Fatal("dangling percent-encoded $ref should be rejected")
	}
	if !strings.Contains(err.Error(), "(OBI-D-12)") {
		t.Fatalf("expected OBI-D-12 unresolved-reference error, got %v", err)
	}
}

func TestDocumentValidate_NestedIDScopeSkipsD12(t *testing.T) {
	// A $ref inside a schema declaring its own $id resolves against that
	// resource's base per §10 and is out of OBI-D-12's scope.
	i := Document{
		OpenBindings: "0.2.0",
		Operations: map[string]Operation{
			"getTask": {Output: map[string]any{"$ref": "#/schemas/Task"}},
		},
		Schemas: map[string]JSONSchema{
			"Task": map[string]any{
				"$id":        "https://example.com/task.schema.json",
				"type":       "object",
				"properties": map[string]any{"parent": map[string]any{"$ref": "#/$defs/base"}},
				"$defs":      map[string]any{"base": map[string]any{"type": "string"}},
			},
		},
	}
	if _, err := i.Validate(); err != nil {
		t.Fatalf("resource-internal $ref should be out of D-16 scope, got %v", err)
	}
}

func TestDocumentValidate_AnchorInsideIDScopePermitted(t *testing.T) {
	// OBI-D-05's pointer-form rule carves out $id-declaring schemas: their
	// internal fragments (including plain-name anchors) are that
	// resource's business, per the same scope rule as OBI-D-12.
	i := Document{
		OpenBindings: "0.2.0",
		Operations: map[string]Operation{
			"getTask": {Output: map[string]any{"$ref": "#/schemas/Task"}},
		},
		Schemas: map[string]JSONSchema{
			"Task": map[string]any{
				"$id":        "https://example.com/task.schema.json",
				"type":       "object",
				"properties": map[string]any{"kind": map[string]any{"$ref": "#kindAnchor"}},
				"$defs":      map[string]any{"kind": map[string]any{"$anchor": "kindAnchor", "type": "string"}},
			},
		},
	}
	if _, err := i.Validate(); err != nil {
		t.Fatalf("anchor inside $id scope must be permitted, got %v", err)
	}
}

func TestDocumentValidate_NestedRelativeIDInsideIDScopePermitted(t *testing.T) {
	// §10 clause 2 / OBI-D-05: a nested $id inside a schema that already
	// declares its own $id resolves against that resource's base per JSON
	// Schema 2020-12 and MAY be relative — that resource's internal
	// business, the same scope carve-out as $ref/$anchor/dynamic-pair.
	i := Document{
		OpenBindings: "0.2.0",
		Operations: map[string]Operation{
			"getTask": {Output: map[string]any{"$ref": "#/schemas/Task"}},
		},
		Schemas: map[string]JSONSchema{
			"Task": map[string]any{
				"$id":   "https://example.com/task.schema.json",
				"type":  "object",
				"$defs": map[string]any{"kind": map[string]any{"$id": "kind.schema.json", "type": "string"}},
			},
		},
	}
	if _, err := i.Validate(); err != nil {
		t.Fatalf("nested relative $id inside $id scope must be permitted, got %v", err)
	}
}

func TestDocumentValidate_TopLevelRelativeIDRejected(t *testing.T) {
	// A schema $id at an OBI position (not nested inside another
	// $id-declaring schema) MUST still be absolute (OBI-D-05).
	i := Document{
		OpenBindings: "0.2.0",
		Operations: map[string]Operation{
			"getTask": {Output: map[string]any{"$ref": "#/schemas/Task"}},
		},
		Schemas: map[string]JSONSchema{
			"Task": map[string]any{"$id": "task.schema.json", "type": "object"},
		},
	}
	_, err := i.Validate()
	if err == nil {
		t.Fatal("relative $id at an OBI position should be rejected")
	}
	if !strings.Contains(err.Error(), `$id: "task.schema.json" must be an absolute URI (OBI-D-05)`) {
		t.Fatalf("expected OBI-D-05 $id error, got %v", err)
	}
}

func TestDocumentValidate_DynamicReferencesInTheDocumentResource(t *testing.T) {
	// The dynamic pair may appear at OBI positions: OBI-D-05 judges a
	// $dynamicRef's form, and OBI-D-12 its initial resolution, like a $ref's.
	i := Document{
		OpenBindings: "0.2.0",
		Operations: map[string]Operation{
			"getTask": {Output: map[string]any{"$dynamicRef": "#node"}},
		},
		Schemas: map[string]JSONSchema{
			"Node": map[string]any{"$dynamicAnchor": "node", "type": "object"},
		},
	}
	if _, err := i.Validate(); err != nil {
		t.Fatalf("a $dynamicRef naming a declared $dynamicAnchor conforms, got %v", err)
	}
	i.Schemas = nil
	_, err := i.Validate()
	if err == nil || !strings.Contains(err.Error(), "OBI-D-12") {
		t.Fatalf("a $dynamicRef naming nothing violates OBI-D-12, got %v", err)
	}
}

func TestDocumentValidate_DynamicPairInsideIDScopePermitted(t *testing.T) {
	// A schema resource declaring its own $id may use the dynamic pair
	// internally, per the same scope rule as $ref/$anchor — including full
	// 2020-12 recursive-extension semantics (a sibling $dynamicAnchor plus a
	// nested $dynamicRef referencing it).
	i := Document{
		OpenBindings: "0.2.0",
		Operations: map[string]Operation{
			"getTask": {Output: map[string]any{"$ref": "#/schemas/Tree"}},
		},
		Schemas: map[string]JSONSchema{
			"Tree": map[string]any{
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
	if _, err := i.Validate(); err != nil {
		t.Fatalf("dynamic pair inside $id scope must be permitted, got %v", err)
	}
}

func TestDocumentValidate_PropertyNamedDynamicRefIsData(t *testing.T) {
	// A property NAMED $dynamicRef under `properties` is data, not a
	// keyword: the walker is keyword-shape-aware, mirroring the same guard
	// already in place for $ref.
	i := Document{
		OpenBindings: "0.2.0",
		Operations: map[string]Operation{
			"getTask": {Output: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"$dynamicRef":    map[string]any{"type": "string"},
					"$dynamicAnchor": map[string]any{"type": "string"},
				},
			}},
		},
	}
	if _, err := i.Validate(); err != nil {
		t.Fatalf("property named $dynamicRef/$dynamicAnchor must be treated as data, got %v", err)
	}
}

func TestDocumentValidate_OperationRefMustExist(t *testing.T) {
	i := Document{
		OpenBindings: "0.2.0",
		Operations: map[string]Operation{
			"op": {},
		},
		Sources: map[string]Source{
			"api": {Kind: "openapi@3.1"},
		},
		Bindings: map[string]Binding{
			"nonexistent.api": {
				Operation: "nonexistent",
				Source:    "api",
			},
		},
	}
	_, err := i.Validate()
	if err == nil {
		t.Fatalf("expected error")
	}
	if !containsProblem(err, `/bindings/nonexistent.api/operation: references unknown operation key "nonexistent" (OBI-D-07)`) {
		t.Fatalf("expected operation ref error, got %v", err)
	}
}

func TestDocumentValidate_SourceRefMustExist(t *testing.T) {
	i := Document{
		OpenBindings: "0.2.0",
		Operations: map[string]Operation{
			"op": {},
		},
		Bindings: map[string]Binding{
			"op.nonexistent": {
				Operation: "op",
				Source:    "nonexistent",
			},
		},
	}
	_, err := i.Validate()
	if err == nil {
		t.Fatalf("expected error")
	}
	if !containsProblem(err, `/bindings/op.nonexistent/source: references unknown source "nonexistent" (OBI-D-08)`) {
		t.Fatalf("expected source ref error, got %v", err)
	}
}

// newDocumentWithExamples builds a minimal valid Document with one operation
// that has the given input/output schemas and the given examples map.
func newDocumentWithExamples(inputSchema, outputSchema JSONSchema, examples map[string]OperationExample) Document {
	return Document{
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

// Examples are author claims (§5.1): an example that does not validate
// against its operation's schema is a false claim, which no document rule
// checks (OBI-T-10, OBI-T-11).
func TestDocumentValidate_ExamplesAreAuthorClaims(t *testing.T) {
	i := newDocumentWithExamples(
		map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}, "required": []any{"name"}},
		map[string]any{"type": "object", "properties": map[string]any{"count": map[string]any{"type": "integer"}}, "additionalProperties": false},
		map[string]OperationExample{
			"good":     {Input: exampleValue(map[string]any{"name": "Alice"}), Output: exampleValue(map[string]any{"count": 1})},
			"bad":      {Input: exampleValue(map[string]any{"wrong": 42}), Output: exampleValue(map[string]any{"count": "x", "extra": true})},
			"nullCase": {Input: json.RawMessage("null")},
		},
	)
	report, err := i.Validate()
	if err != nil {
		t.Fatalf("a false example is not a document-rule violation, got %v", err)
	}
	if report.Conclusion != ConclusionConformant {
		t.Fatalf("every rule is decided for a host object: conclusion %s, inconclusive %v", report.Conclusion, report.Inconclusive)
	}
}

// Value validation reads same-document references from the OBI document
// root (OBI-T-08, §7.2), however the operation reaches its schemas.
func TestInputContract_ResolvesFromTheDocumentRoot(t *testing.T) {
	i := Document{
		OpenBindings: "0.2.0",
		Schemas: map[string]JSONSchema{
			"Person": map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}, "required": []any{"name"}},
		},
		Operations: map[string]Operation{
			"byRef": {Input: map[string]any{"$ref": "#/schemas/Person"}},
			"byPointer": {Input: map[string]any{
				"type":       "object",
				"properties": map[string]any{"name": map[string]any{"$ref": "#/operations/byPointer/input/$defs/Name"}},
				"required":   []any{"name"},
				"$defs":      map[string]any{"Name": map[string]any{"type": "string"}},
			}},
		},
	}
	for _, op := range []string{"byRef", "byPointer"} {
		if err := validateWithTestEvaluator(t, &i, op, "input", map[string]any{"name": "Bob"}); err != nil {
			t.Errorf("%s: a conforming value: %v", op, err)
		}
		if err := validateWithTestEvaluator(t, &i, op, "input", map[string]any{"name": json.Number("42")}); !errors.Is(err, ErrMismatch) {
			t.Errorf("%s: want a mismatch, got %v", op, err)
		}
	}
}

func TestParseDocument_UnknownTopLevelFieldViolatesD02(t *testing.T) {
	// An unprefixed name the specification does not define is reserved for it
	// (§12): "security", a 0.1 member, makes a 0.2 document non-conformant.
	doc := []byte(`{"openbindings":"0.2.0","operations":{},"security":"abc"}`)
	var violation *ValidationError
	if _, err := ParseDocument(doc); !errors.As(err, &violation) || !strings.Contains(err.Error(), "security") || !strings.Contains(err.Error(), "OBI-D-02") {
		t.Fatalf("want an OBI-D-02 violation naming security, got %v", err)
	}
}

func TestParseDocument_RemovedTransformMembersViolateOBI_D_02(t *testing.T) {
	// The core defines no transforms and a binding carries content, not a
	// selector (§5.3): the draft's transforms map and binding selector and
	// transform members are unprefixed names the specification does not
	// define, so each makes the document non-conformant (§12).
	for member, doc := range map[string]string{
		"transforms":      `{"openbindings":"0.2.0","operations":{"op":{}},"transforms":{"t":"$.payload"}}`,
		"selector":        `{"openbindings":"0.2.0","operations":{"op":{}},"sources":{"api":{"kind":"x@1"}},"bindings":{"op.api":{"operation":"op","source":"api","selector":"#/paths/~1op/get"}}}`,
		"inputTransform":  `{"openbindings":"0.2.0","operations":{"op":{}},"sources":{"api":{"kind":"x@1"}},"bindings":{"op.api":{"operation":"op","source":"api","inputTransform":"$"}}}`,
		"outputTransform": `{"openbindings":"0.2.0","operations":{"op":{}},"sources":{"api":{"kind":"x@1"}},"bindings":{"op.api":{"operation":"op","source":"api","outputTransform":{"$ref":"#/transforms/t"}}}}`,
	} {
		var violation *ValidationError
		if _, err := ParseDocument([]byte(doc)); !errors.As(err, &violation) || !strings.Contains(err.Error(), member) || !strings.Contains(err.Error(), "OBI-D-02") {
			t.Fatalf("%s: want an OBI-D-02 violation naming it, got %v", member, err)
		}
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

// A null value is a value like any other (§5.1), and a graph reaching a
// resource the document does not embed reaches no verdict (OBI-T-08).
func TestInputContract_NullAndExternalReferences(t *testing.T) {
	i := Document{
		OpenBindings: "0.2.0",
		Schemas: map[string]JSONSchema{
			"User": map[string]any{"$ref": "https://schemas.example.com/user.json"},
		},
		Operations: map[string]Operation{
			"object":   {Input: map[string]any{"type": "object"}},
			"external": {Input: map[string]any{"$ref": "https://schemas.example.com/user.json"}},
			"viaMap":   {Input: map[string]any{"$ref": "#/schemas/User"}},
		},
	}
	if err := validateWithTestEvaluator(t, &i, "object", "input", nil); !errors.Is(err, ErrMismatch) {
		t.Fatalf("null against an object schema: want a mismatch, got %v", err)
	}
	for _, op := range []string{"external", "viaMap"} {
		if err := validateWithTestEvaluator(t, &i, op, "input", map[string]any{"anything": true}); !errors.Is(err, ErrNoVerdict) || errors.Is(err, ErrUndefined) {
			t.Errorf("%s: want no verdict, a capability core lacks, got %v", op, err)
		}
	}
}

// OBI-T-04's refusal runs downward too: support for one major.minor line
// does not imply support for an earlier line.
func TestDocumentValidate_RefusesEarlierLine(t *testing.T) {
	iface := Document{
		OpenBindings: "0.1.0",
		Operations:   map[string]Operation{},
	}
	_, err := iface.Validate()
	if err == nil {
		t.Fatal("a document below the supported release line must refuse")
	}
	msg := err.Error()
	if !strings.Contains(msg, "older than the release line this implementation supports (0.2.x)") || !strings.Contains(msg, "OBI-T-04") {
		t.Errorf("refusal must cite the floor and the rule, got: %s", msg)
	}
}

func TestDocumentValidate_SchemaWellFormedness_BooleanForms(t *testing.T) {
	// OBI-D-10 / §5.2: boolean schemas are valid at every schema position —
	// operation input/output, schemas-map entries, and nested subschema
	// positions.
	i := Document{
		OpenBindings: "0.2.0",
		Schemas: map[string]JSONSchema{
			"Anything": true,
		},
		Operations: map[string]Operation{
			"op": {
				Input:  true,
				Output: false,
			},
			"nested": {
				Input: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"locked": false,
						"values": map[string]any{"type": "array", "items": true},
					},
					"additionalProperties": false,
				},
			},
		},
	}
	if _, err := i.Validate(); err != nil {
		t.Fatalf("expected boolean-form schemas to validate, got %v", err)
	}
}

func TestDocumentValidate_SchemaWellFormedness_MetaSchemaViolations(t *testing.T) {
	// OBI-D-10: object-form schemas must validate against the 2020-12
	// meta-schemas, recursively through subschemas.
	cases := []struct {
		name    string
		op      Operation
		wantSub string
	}{
		{
			name:    "type as number at input",
			op:      Operation{Input: map[string]any{"type": 42.0}},
			wantSub: `/operations/op/input/type: not a well-formed JSON Schema 2020-12 schema:`,
		},
		{
			name:    "unknown simple type",
			op:      Operation{Input: map[string]any{"type": "str"}},
			wantSub: `/operations/op/input/type: not a well-formed JSON Schema 2020-12 schema:`,
		},
		{
			name:    "nested minLength as string",
			op:      Operation{Output: map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"minLength": "3"}}}},
			wantSub: `/operations/op/output/properties/a/minLength: not a well-formed JSON Schema 2020-12 schema:`,
		},
		{
			name:    "oneOf as object",
			op:      Operation{Output: map[string]any{"oneOf": map[string]any{"type": "string"}}},
			wantSub: `/operations/op/output/oneOf: not a well-formed JSON Schema 2020-12 schema:`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			i := Document{
				OpenBindings: "0.2.0",
				Operations:   map[string]Operation{"op": tc.op},
			}
			_, err := i.Validate()
			if err == nil {
				t.Fatal("expected OBI-D-10 violation")
			}
			if !strings.Contains(err.Error(), tc.wantSub) || !strings.Contains(err.Error(), "(OBI-D-10)") {
				t.Fatalf("expected OBI-D-10 problem containing %q, got %v", tc.wantSub, err)
			}
		})
	}
}

func TestDocumentValidate_SchemaWellFormedness_NonSchemaValues(t *testing.T) {
	// OBI-D-10: a value at a schema position that is neither object nor
	// boolean form is a document defect with a deterministic diagnostic.
	i := Document{
		OpenBindings: "0.2.0",
		Schemas: map[string]JSONSchema{
			"Task": 42.0,
		},
		Operations: map[string]Operation{
			"op": {Output: "not-a-schema"},
		},
	}
	_, err := i.Validate()
	if err == nil {
		t.Fatal("expected OBI-D-10 violations")
	}
	msg := err.Error()
	if !strings.Contains(msg, `/schemas/Task: a schema is a JSON Schema 2020-12 object or boolean; got number (OBI-D-10)`) {
		t.Errorf("expected schemas-map OBI-D-10 problem, got: %s", msg)
	}
	if !strings.Contains(msg, `/operations/op/output: a schema is a JSON Schema 2020-12 object or boolean; got string (OBI-D-10)`) {
		t.Errorf("expected output OBI-D-10 problem, got: %s", msg)
	}
}

func TestDocumentValidate_SchemaWellFormedness_DeliberatelyNarrow(t *testing.T) {
	// OBI-D-10 is narrow: unknown keywords, unparseable `pattern` regexes,
	// and unresolvable external $refs all pass — they surface when the
	// schema is used, not at document validation.
	i := Document{
		OpenBindings: "0.2.0",
		Operations: map[string]Operation{
			"op": {
				Input: map[string]any{
					"type":          "object",
					"x-internal":    true,
					"futureKeyword": map[string]any{"arbitrary": "annotation"},
					"properties": map[string]any{
						"code": map[string]any{"type": "string", "pattern": "([unclosed"},
					},
				},
				Output: map[string]any{"$ref": "https://schemas.example.com/never-published/task.json"},
			},
		},
	}
	if _, err := i.Validate(); err != nil {
		t.Fatalf("expected narrow OBI-D-10 to accept, got %v", err)
	}
}

func TestDocumentValidate_DependencyContracts(t *testing.T) {
	valid := Document{
		OpenBindings: "0.2.0",
		Operations:   map[string]Operation{"deliver": {}},
		Dependencies: map[string]Dependency{
			"customer.delivery": {Operation: "deliver"},
		},
	}
	if _, err := valid.Validate(); err != nil {
		t.Fatalf("valid dependency: %v", err)
	}

	emptyConstraint := valid
	emptyConstraint.Dependencies = map[string]Dependency{
		"customer.delivery": {Operation: "deliver", Kinds: []string{}},
	}
	if _, err := emptyConstraint.Validate(); err == nil || !strings.Contains(err.Error(), "OBI-D-02") {
		t.Fatalf("empty kinds validation = %v, want OBI-D-02", err)
	}

	missingOperation := valid
	missingOperation.Dependencies = map[string]Dependency{
		"customer.delivery": {Operation: "missing"},
	}
	if _, err := missingOperation.Validate(); err == nil || !strings.Contains(err.Error(), "OBI-D-11") {
		t.Fatalf("missing dependency operation validation = %v, want OBI-D-11", err)
	}
}

// unknownFieldViolations validates iface and returns OBI-D-02 violations by
// path. An unknown field without the x- prefix violates OBI-D-02 (§12).
func unknownFieldViolations(t *testing.T, iface Document) map[string]string {
	t.Helper()
	report, _ := iface.Validate()
	byPath := map[string]string{}
	for _, finding := range report.Violations() {
		if finding.Rule == "OBI-D-02" {
			byPath[finding.Path] = finding.Message
		}
	}
	return byPath
}

func requireUnknownFieldViolation(t *testing.T, byPath map[string]string, path, field string) {
	t.Helper()
	if message, ok := byPath[path]; !ok || !strings.Contains(message, field) {
		t.Fatalf("no OBI-D-02 violation naming %q at %q; got %v", field, path, byPath)
	}
}

func TestDocumentValidate_UnknownTopLevelFieldsViolateD02(t *testing.T) {
	byPath := unknownFieldViolations(t, Document{
		OpenBindings: "0.2.0",
		Operations:   map[string]Operation{},
		LosslessFields: LosslessFields{
			Unknown: map[string]json.RawMessage{
				"unknownField": json.RawMessage(`{"value":"unknownFieldValue"}`),
			},
		},
	})
	requireUnknownFieldViolation(t, byPath, `/unknownField`, "unknownField")
}

func TestDocumentValidate_UnknownFieldsInNestedTypedObjectsViolateD02(t *testing.T) {
	byPath := unknownFieldViolations(t, Document{
		OpenBindings: "0.2.0",
		Operations: map[string]Operation{
			"op": {},
		},
		Sources: map[string]Source{
			"src": {
				Kind: "openapi@3.1",
				LosslessFields: LosslessFields{
					Unknown: map[string]json.RawMessage{
						"unknownField": json.RawMessage(`{"value":"unknownFieldValue"}`),
					},
				},
			},
		},
		Bindings: map[string]Binding{
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
	})
	requireUnknownFieldViolation(t, byPath, `/sources/src/unknownField`, "unknownField")
	requireUnknownFieldViolation(t, byPath, `/bindings/op.src/unknownField`, "unknownField")
}

func TestDocumentValidate_OperationExampleUnknownFieldsViolateD02(t *testing.T) {
	byPath := unknownFieldViolations(t, Document{
		OpenBindings: "0.2.0",
		Operations: map[string]Operation{
			"op": {
				Examples: map[string]OperationExample{
					"ex1": {
						Description: Present("test"),
						LosslessFields: LosslessFields{
							Unknown: map[string]json.RawMessage{
								"unknownField": json.RawMessage(`"bad"`),
							},
						},
					},
				},
			},
		},
	})
	requireUnknownFieldViolation(t, byPath, `/operations/op/examples/ex1/unknownField`, "unknownField")
}

func TestDocumentValidate_BindingUnknownFieldsViolateD02(t *testing.T) {
	byPath := unknownFieldViolations(t, Document{
		OpenBindings: "0.2.0",
		Operations: map[string]Operation{
			"op": {},
		},
		Sources: map[string]Source{
			"api": {Kind: "openapi@3.1"},
		},
		Bindings: map[string]Binding{
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
	})
	requireUnknownFieldViolation(t, byPath, `/bindings/op.api/unknownBindingField`, "unknownBindingField")
}

func TestDocumentValidate_DependencyUnknownFieldsViolateD02(t *testing.T) {
	byPath := unknownFieldViolations(t, Document{
		OpenBindings: "0.2.0",
		Operations:   map[string]Operation{"deliver": {}},
		Dependencies: map[string]Dependency{
			"delivery": {
				Operation: "deliver",
				LosslessFields: LosslessFields{
					Unknown: map[string]json.RawMessage{"futurePolicy": json.RawMessage(`true`)},
				},
			},
		},
	})
	requireUnknownFieldViolation(t, byPath, `/dependencies/delivery/futurePolicy`, "futurePolicy")
}

func TestDocumentValidate_ExtensionFieldsDoNotViolateD02(t *testing.T) {
	byPath := unknownFieldViolations(t, Document{
		OpenBindings: "0.2.0",
		Operations:   map[string]Operation{},
		LosslessFields: LosslessFields{
			Extensions: map[string]json.RawMessage{"x-vendor": json.RawMessage(`true`)},
		},
	})
	if len(byPath) != 0 {
		t.Fatalf("x- extensions are not unknown fields; got %v", byPath)
	}
}
