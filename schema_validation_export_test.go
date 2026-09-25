package openbindings

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// documentWithInput is an OBI whose operation "op" has the given input
// schema, beside the given named schemas.
func documentWithInput(input JSONSchema, schemas map[string]JSONSchema) *Interface {
	return &Interface{
		OpenBindings: "0.2.0",
		Schemas:      schemas,
		Operations:   map[string]Operation{"op": {Input: input}},
	}
}

func TestValidateOperationInput_ResolvesNamedSchemasThroughTheDocument(t *testing.T) {
	schemas := map[string]JSONSchema{
		"Thing": map[string]any{
			"type":                 "object",
			"properties":           map[string]any{"name": map[string]any{"type": "string"}},
			"required":             []any{"name"},
			"additionalProperties": false,
		},
	}
	doc := documentWithInput(map[string]any{"$ref": "#/schemas/Thing"}, schemas)

	if err := ValidateOperationInput(map[string]any{"name": "ok"}, doc, "op"); err != nil {
		t.Fatalf("valid value rejected: %v", err)
	}
	if err := ValidateOperationInput(map[string]any{"stdout": "text"}, doc, "op"); err == nil {
		t.Fatal("invalid value accepted")
	}
}

// TestValidateOperationInput_ExternalRefFailsClosed pins the OBI-T-07/T-08
// clarification: validation is against the FULLY RESOLVED schema, so a
// schema carrying an external $ref the tool cannot fetch is a validation
// error (fail closed), never a partial pass.
func TestValidateOperationInput_ExternalRefFailsClosed(t *testing.T) {
	opSchema := map[string]any{"$ref": "https://example.com/schemas/user-input.json"}
	err := ValidateOperationInput(map[string]any{"id": "u1"}, documentWithInput(opSchema, nil), "op")
	if err == nil {
		t.Fatal("external $ref should fail closed, not validate partially")
	}
}

// TestValidateOperationInput_FormatIsAnnotationOnly pins §6.2's boundary
// rule: `format` never asserts at OBI validation boundaries — a value
// violating `format` still validates; enforced syntax belongs to `pattern`.
func TestValidateOperationInput_FormatIsAnnotationOnly(t *testing.T) {
	opSchema := map[string]any{"type": "string", "format": "email"}
	if err := ValidateOperationInput("not-an-email", documentWithInput(opSchema, nil), "op"); err != nil {
		t.Fatalf("format must be annotation-only at OBI boundaries, got %v", err)
	}
	patternSchema := map[string]any{"type": "string", "pattern": "^[^@]+@[^@]+$"}
	if err := ValidateOperationInput("not-an-email", documentWithInput(patternSchema, nil), "op"); err == nil {
		t.Fatal("pattern is the assertion lane and must reject")
	}
}

// Patterns use the schema library's engine, Go's regexp. A pattern it cannot
// compile, such as an ECMAScript lookahead, leaves no verdict rather than a
// wrong one.
func TestValidateOperationInput_PatternDialect(t *testing.T) {
	lookahead := map[string]any{"type": "string", "pattern": "^(?=.*[A-Z]).*$"}
	if err := ValidateOperationInput("Password1", documentWithInput(lookahead, nil), "op"); !errors.As(err, new(*SchemaGraphUnavailableError)) {
		t.Fatalf("a lookahead pattern must leave the graph unavailable, got %v", err)
	}
	digits := map[string]any{"type": "string", "pattern": "^[0-9]+$"}
	if err := ValidateOperationInput("123", documentWithInput(digits, nil), "op"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateOperationInput("12a", documentWithInput(digits, nil), "op"); !errors.As(err, new(*SchemaValidationError)) {
		t.Fatalf("want a mismatch, got %v", err)
	}
}

// TestValidateOperationInput_DynamicPairInsideEmbeddedID pins that the
// invocation-boundary compiler (the reachable-closure resolution performed
// by the document-rooted compile, via the underlying jsonschema/v6 library) does
// not choke on a legal $dynamicRef/$dynamicAnchor pair confined inside a
// schema declaring its own $id (OBI-D-05's carve-out; OBI-D-16 notes
// $dynamicRef does not participate in same-document reference resolution).
// Full 2020-12 recursive-extension semantics apply within the resource.
func TestValidateOperationInput_DynamicPairInsideEmbeddedID(t *testing.T) {
	schemas := map[string]JSONSchema{
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
	}
	opSchema := map[string]any{"$ref": "#/schemas/Tree"}

	doc := documentWithInput(opSchema, schemas)
	good := map[string]any{"children": []any{map[string]any{"children": []any{}}}}
	if err := ValidateOperationInput(good, doc, "op"); err != nil {
		t.Fatalf("legal embedded-$id dynamic pair must not choke the boundary scan, got %v", err)
	}

	bad := map[string]any{"children": []any{map[string]any{"children": "not-an-array"}}}
	if err := ValidateOperationInput(bad, doc, "op"); err == nil {
		t.Fatal("value violating the recursively-applied schema should be rejected")
	}
}

// TestValidateOperationInput_PercentEncodedFragmentResolves pins the
// document-rooted compile (via the underlying
// jsonschema/v6 library) resolving a percent-encoded same-document
// fragment end to end: RFC 6901 §6 decodes the fragment before evaluating
// it as a JSON Pointer, so "#/schemas/T%61sk" reaches the schemas key
// "Task" exactly as "#/schemas/Task" does — the compile backend needs no
// SDK-side normalization here (santhosh-tekuri/jsonschema/v6 is
// RFC-correct on its own).
func TestValidateOperationInput_PercentEncodedFragmentResolves(t *testing.T) {
	schemas := map[string]JSONSchema{
		"Task": map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}},
	}
	opSchema := map[string]any{"$ref": "#/schemas/T%61sk"}

	doc := documentWithInput(opSchema, schemas)
	if err := ValidateOperationInput(map[string]any{"name": "ok"}, doc, "op"); err != nil {
		t.Fatalf("percent-encoded fragment should resolve end to end, got %v", err)
	}
	if err := ValidateOperationInput(map[string]any{"name": 5}, doc, "op"); err == nil {
		t.Fatal("resolved schema should still enforce its constraints")
	}
}

// TestValidateOperationInput_ReachableClosureOnly pins the scope of
// T-07/T-08's "whole governing schema": the static closure REACHABLE from
// the governing root (keyword subschemas + reference targets,
// transitively). A lexically-present but unreachable entry — an
// unreferenced $defs member, an unrelated document-schemas entry merged
// into the compound — never participates in a verdict and must not poison
// the boundary. A dangling same-document ref there is OBI-D-16's
// document-level concern, not an invocation refusal. Mirrors the TS SDK's
// schema-conformance oracle.
func TestValidateOperationInput_ReachableClosureOnly(t *testing.T) {
	// Unreferenced $defs entry with an external $ref: must not refuse.
	dead := map[string]any{
		"type":  "string",
		"$defs": map[string]any{"dead": map[string]any{"$ref": "https://example.com/never-fetched.json"}},
	}
	if err := ValidateOperationInput("hi", documentWithInput(dead, nil), "op"); err != nil {
		t.Fatalf("unreachable $defs entry must not poison the boundary, got %v", err)
	}
	// Unrelated document-schemas entry with an external $ref: must not
	// poison an operation that never references it.
	unrelated := map[string]JSONSchema{
		"Unused": map[string]any{"$ref": "https://example.com/never-fetched.json"},
	}
	if err := ValidateOperationInput("hi", documentWithInput(map[string]any{"type": "string"}, unrelated), "op"); err != nil {
		t.Fatalf("unreachable document schema must not poison the boundary, got %v", err)
	}
	// The same external $ref REACHED from the root still fails closed.
	reached := map[string]any{
		"$ref":  "#/$defs/a",
		"$defs": map[string]any{"a": map[string]any{"$ref": "https://example.com/never-fetched.json"}},
	}
	if err := ValidateOperationInput("hi", documentWithInput(reached, nil), "op"); err == nil {
		t.Fatal("external $ref reached from the root must fail closed")
	}
	// And a document-schemas entry reached from the root still fails closed.
	used := map[string]JSONSchema{
		"Used": map[string]any{"$ref": "https://example.com/never-fetched.json"},
	}
	if err := ValidateOperationInput("hi", documentWithInput(map[string]any{"$ref": "#/schemas/Used"}, used), "op"); err == nil {
		t.Fatal("reachable document schema's external $ref must fail closed")
	}
}

// A file: reference names a resource outside the document. It stays
// unavailable even when the file exists and holds a schema the value would
// satisfy: a verdict never depends on the validating machine's files.
func TestSchemaValidation_NeverReadsLocalFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "string.json")
	if err := os.WriteFile(path, []byte(`{"type":"string"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ref := map[string]any{"$ref": (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()}
	var unavailable *SchemaGraphUnavailableError
	if err := ValidateOperationInput("text", documentWithInput(ref, nil), "op"); !errors.As(err, &unavailable) {
		t.Fatalf("operation input: want graph unavailable, got %v", err)
	}
}

// An absolute reference to a resource the document embeds by $id resolves
// without any loader, as does the built-in 2020-12 meta-schema.
func TestSchemaValidation_EmbeddedResourcesStillResolve(t *testing.T) {
	schemas := map[string]JSONSchema{
		"Title": map[string]any{"$id": "https://schemas.example.com/title.json", "type": "string"},
	}
	doc := documentWithInput(map[string]any{"$ref": "https://schemas.example.com/title.json"}, schemas)
	if err := ValidateOperationInput("a title", doc, "op"); err != nil {
		t.Fatalf("embedded $id resource must resolve: %v", err)
	}
	var mismatch *SchemaValidationError
	if err := ValidateOperationInput(42, doc, "op"); !errors.As(err, &mismatch) {
		t.Fatalf("embedded $id resource must be enforced, got %v", err)
	}
	withDialect := map[string]any{"$schema": "https://json-schema.org/draft/2020-12/schema", "type": "string"}
	if err := ValidateOperationInput("x", documentWithInput(withDialect, nil), "op"); err != nil {
		t.Fatalf("the 2020-12 meta-schema is built in: %v", err)
	}
}

// The OBI root is not a schema: its members, unknown ones included, never act
// as schema keywords or declare resources (§7, OBI-T-02).
func TestValidateOperationInput_UnknownRootMembersAreNotSchemaKeywords(t *testing.T) {
	var unavailable *SchemaGraphUnavailableError
	for name, document := range map[string]string{
		"a keyword with an invalid value": `{"openbindings":"0.2.0","type":5,"properties":3,"operations":{"op":{"input":{"type":"string"}}}}`,
		"a resource under root $defs":     `{"openbindings":"0.2.0","$defs":{"t":{"$id":"https://ext.example/T","type":"string"}},"operations":{"op":{"input":{"type":"string"}}}}`,
	} {
		iface := mustDecode(t, document)
		if err := ValidateOperationInput("text", iface, "op"); err != nil {
			t.Errorf("%s: an unrelated unknown member affected validation: %v", name, err)
		}
	}
	iface := mustDecode(t, `{"openbindings":"0.2.0","$defs":{"t":{"$id":"https://ext.example/T","type":"string"}},"operations":{"op":{"input":{"$ref":"https://ext.example/T"}}}}`)
	if err := ValidateOperationInput("text", iface, "op"); !errors.As(err, &unavailable) {
		t.Fatalf("a $id under an unknown root member is not embedded; want graph unavailable, got %v", err)
	}
	// A pointer into an unknown member addresses no schema position, so it
	// reaches no schema (OBI-D-16; JSON Schema 2020-12 §9.4.2).
	iface = mustDecode(t, `{"openbindings":"0.2.0","x-lib":{"s":{"type":"string"}},"operations":{"op":{"input":{"$ref":"#/x-lib/s"}}}}`)
	if err := ValidateOperationInput("text", iface, "op"); !errors.As(err, &unavailable) || !strings.Contains(err.Error(), "not a schema position") {
		t.Fatalf("a same-document pointer into an extension reaches no schema; want graph unavailable, got %v", err)
	}
}

// format never asserts at an operation boundary, even through the built-in
// meta-schemas of drafts that assert it by default (§5.2, OBI-T-16).
func TestValidateOperationInput_FormatIsAnnotationInEveryDialect(t *testing.T) {
	for _, meta := range []string{"http://json-schema.org/draft-04/schema#", "http://json-schema.org/draft-06/schema#", "http://json-schema.org/draft-07/schema#"} {
		iface := mustDecode(t, `{"openbindings":"0.2.0","operations":{"op":{"input":{"$ref":"`+meta+`"}}}}`)
		if err := ValidateOperationInput(map[string]any{"$schema": "not a uri", "pattern": "("}, iface, "op"); err != nil {
			t.Errorf("%s: format must not assert: %v", meta, err)
		}
	}
}

// A same-document pointer into the interior of an embedded resource resolves
// the references inside it against that resource's base.
func TestValidateOperationInput_PointerIntoAResourceUsesItsBase(t *testing.T) {
	iface := mustDecode(t, `{"openbindings":"0.2.0",
		"x-decoy":{"$defs":{"X":{"type":"number"}}},
		"$defs":{"X":{"type":"number"}},
		"schemas":{"T":{"$id":"https://e.com/T","$defs":{"X":{"type":"string"}},"properties":{"a":{"$ref":"#/$defs/X"}}}},
		"operations":{"op":{"input":{"$ref":"#/schemas/T/properties/a"}}}}`)
	if err := ValidateOperationInput("text", iface, "op"); err != nil {
		t.Fatalf("the reference inside the resource resolves against the resource: %v", err)
	}
	var mismatch *SchemaValidationError
	if err := ValidateOperationInput(5, iface, "op"); !errors.As(err, &mismatch) {
		t.Fatalf("want a mismatch against the resource's own definition, got %v", err)
	}
}

// Two schemas declaring one $id are ambiguous: no verdict is reached.
func TestValidateOperationInput_ConflictingIDsAreUnavailable(t *testing.T) {
	iface := mustDecode(t, `{"openbindings":"0.2.0",
		"schemas":{"A":{"$id":"https://e.com/T","type":"string"},"B":{"$id":"https://e.com/T","type":"number"}},
		"operations":{"op":{"input":{"$ref":"https://e.com/T"}}}}`)
	var unavailable *SchemaGraphUnavailableError
	if err := ValidateOperationInput("text", iface, "op"); !errors.As(err, &unavailable) {
		t.Fatalf("want graph unavailable, got %v", err)
	}
}

// Nothing to validate against is a caller error, not graph unavailability.
func TestValidateOperationInput_NothingToValidateIsNotAnOutcome(t *testing.T) {
	iface := mustDecode(t, `{"openbindings":"0.2.0","operations":{"op":{}}}`)
	for name, err := range map[string]error{
		"undefined operation": ValidateOperationInput("x", iface, "missing"),
		"no schema":           ValidateOperationInput("x", iface, "op"),
		"no interface":        ValidateOperationInput("x", nil, "op"),
	} {
		var unavailable *SchemaGraphUnavailableError
		var mismatch *SchemaValidationError
		if err == nil || errors.As(err, &unavailable) || errors.As(err, &mismatch) {
			t.Errorf("%s: want a plain error, got %T %v", name, err, err)
		}
	}
}

func mustDecode(t *testing.T, document string) *Interface {
	t.Helper()
	var iface Interface
	if err := json.Unmarshal([]byte(document), &iface); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return &iface
}

// A $id two schemas declare leaves only the graphs that reach it
// unavailable.
func TestValidateOperationInput_ConflictingIDsOnlyAffectGraphsThatReachThem(t *testing.T) {
	iface := mustDecode(t, `{"openbindings":"0.2.0",
		"schemas":{"A":{"$id":"https://e.com/T","type":"string"},"B":{"$id":"https://e.com/T","type":"number"}},
		"operations":{"op":{"input":{"type":"string"}}}}`)
	if err := ValidateOperationInput("text", iface, "op"); err != nil {
		t.Fatalf("an operation whose graph reaches neither schema must validate: %v", err)
	}
}

// Success needs the whole statically reachable graph, whatever branches the
// evaluator would skip for a value, a then or else no if selects included
// (§5.2, OBI-T-16).
func TestValidateOperationInput_ApplicatorsTheEvaluatorSkipsStillCount(t *testing.T) {
	var unavailable *SchemaGraphUnavailableError
	for name, input := range map[string]string{
		"then under a false if":   `{"if":false,"then":{"$ref":"https://ext.example/x"}}`,
		"then with no if":         `{"then":{"$ref":"https://ext.example/x"}}`,
		"else with a missing ref": `{"type":"string","else":{"$ref":"#/schemas/Missing"}}`,
	} {
		iface := mustDecode(t, `{"openbindings":"0.2.0","operations":{"op":{"input":`+input+`}}}`)
		if err := ValidateOperationInput("x", iface, "op"); !errors.As(err, &unavailable) {
			t.Errorf("%s: want graph unavailable, got %v", name, err)
		}
	}
	// An example whose graph reaches outside the document is outside
	// OBI-D-11, a then with no if notwithstanding.
	_, report, _ := ValidateDocument([]byte(`{"openbindings":"0.2.0","operations":{"op":{"input":{"type":"string","then":{"$ref":"https://ext.example/x.json"}},"examples":{"e":{"input":5}}}}}`), ValidateOptions{})
	if report.Evidence["OBI-D-11"] != EvidenceSatisfied {
		t.Fatalf("OBI-D-11 %q", report.Evidence["OBI-D-11"])
	}
}

// dependencies, $recursiveRef, and $recursiveAnchor are not 2020-12 keywords,
// so they constrain nothing (§5.2), though the schema library would evaluate
// them: the bundle it is given leaves them out. A property named like one is
// still a property.
func TestValidateOperationInput_PreviousDialectKeywordsAreNotEvaluated(t *testing.T) {
	for name, input := range map[string]string{
		"dependencies":  `{"type":"object","dependencies":{"a":["b"]}}`,
		"$recursiveRef": `{"$recursiveRef":"#/schemas/S"}`,
	} {
		iface := mustDecode(t, `{"openbindings":"0.2.0","schemas":{"S":{"type":"integer"}},"operations":{"op":{"input":`+input+`}}}`)
		if err := ValidateOperationInput(map[string]any{"a": 1.0}, iface, "op"); err != nil {
			t.Errorf("%s: want the value valid, got %v", name, err)
		}
	}
	named := mustDecode(t, `{"openbindings":"0.2.0","operations":{"op":{"input":{"properties":{"dependencies":{"type":"string"}}}}}}`)
	if err := ValidateOperationInput(map[string]any{"dependencies": 1.0}, named, "op"); !errors.As(err, new(*SchemaValidationError)) {
		t.Errorf("a property named dependencies is still checked, got %v", err)
	}
}

func TestValidateOperationInput_IllFormedGraphsAreUnavailable(t *testing.T) {
	var unavailable *SchemaGraphUnavailableError
	for name, input := range map[string]string{
		"draft-07 dialect": `{"$schema":"http://json-schema.org/draft-07/schema#","type":"string"}`,
		"vocabulary":       `{"$vocabulary":{},"type":"string"}`,
		"non-string title": `{"$ref":"#/x-lib/T"}`,
	} {
		iface := mustDecode(t, `{"openbindings":"0.2.0","x-lib":{"T":{"type":"string","title":5}},"operations":{"op":{"input":`+input+`}}}`)
		if err := ValidateOperationInput("x", iface, "op"); !errors.As(err, &unavailable) {
			t.Errorf("%s: want graph unavailable, got %v", name, err)
		}
	}
}

// Validation interprets a document only under a supported version, and
// resolves an operation by any of its identifiers.
func TestValidateOperationInput_RefusesVersionsAndResolvesAliases(t *testing.T) {
	refused := mustDecode(t, `{"openbindings":"0.3.0","operations":{"op":{"input":{"type":"string"}}}}`)
	if err := ValidateOperationInput("x", refused, "op"); !errors.As(err, new(*VersionRefusalError)) {
		t.Fatalf("want a version refusal, got %v", err)
	}
	aliased := mustDecode(t, `{"openbindings":"0.2.0","operations":{"op":{"aliases":["acme.op"],"input":{"type":"string"}}}}`)
	if err := ValidateOperationInput("x", aliased, "acme.op"); err != nil {
		t.Fatalf("an alias names the operation (OBI-T-12): %v", err)
	}
}

// A resource nested inside an embedded resource resolves by its own $id.
func TestValidateOperationInput_NestedResourcesResolve(t *testing.T) {
	iface := mustDecode(t, `{"openbindings":"0.2.0",
		"schemas":{"A":{"$id":"https://ex.com/a","$defs":{"b":{"$id":"b","type":"string"}}}},
		"operations":{"op":{"input":{"$ref":"https://ex.com/b"}}}}`)
	if err := ValidateOperationInput("x", iface, "op"); err != nil {
		t.Fatalf("the nested resource must resolve: %v", err)
	}
	if err := ValidateOperationInput(5, iface, "op"); !errors.As(err, new(*SchemaValidationError)) {
		t.Fatalf("the nested resource must be enforced, got %v", err)
	}
}

// A reference cycle that never advances cannot be evaluated: no verdict.
func TestValidateOperationInput_ProgresslessCyclesAreUnavailable(t *testing.T) {
	iface := mustDecode(t, `{"openbindings":"0.2.0","schemas":{"B":{"$ref":"#/schemas/B"}},"operations":{"op":{"input":{"$ref":"#/schemas/B"}}}}`)
	if err := ValidateOperationInput(1, iface, "op"); !errors.As(err, new(*SchemaGraphUnavailableError)) {
		t.Fatalf("want graph unavailable, got %v", err)
	}
}

// An embedded $id is the embedded schema's, whatever URI it is (§7). The
// schema backend resolves a meta-schema's URI to the meta-schema it carries,
// so a graph reaching an embedded schema that declares one is unavailable;
// graphs that do not reach it are unaffected.
func TestValidateOperationInput_EmbeddedIDsNameTheEmbeddedSchema(t *testing.T) {
	document := func(id string) *Interface {
		return mustDecode(t, `{"openbindings":"0.2.0","schemas":{"X":{"$id":"`+id+`","type":"number"}},
			"operations":{"op":{"input":{"type":"string"}},"reach":{"input":{"$ref":"`+id+`"}}}}`)
	}
	ordinary := document("openbindings:///document")
	if err := ValidateOperationInput(1, ordinary, "reach"); err != nil {
		t.Fatalf("an ordinary embedded $id resolves to its schema: %v", err)
	}
	if err := ValidateOperationInput("x", ordinary, "reach"); !errors.As(err, new(*SchemaValidationError)) {
		t.Fatalf("want a mismatch against the embedded schema, got %v", err)
	}
	// An embedded schema resolves by its $id even when that $id is a
	// meta-schema's URI (§7).
	shadow := document("https://json-schema.org/draft/2020-12/schema")
	if err := ValidateOperationInput("x", shadow, "op"); err != nil {
		t.Errorf("an unrelated operation must validate: %v", err)
	}
	if err := ValidateOperationInput(1, shadow, "reach"); err != nil {
		t.Errorf("the embedded schema accepts a number: %v", err)
	}
	if err := ValidateOperationInput("x", shadow, "reach"); !errors.As(err, new(*SchemaValidationError)) {
		t.Errorf("want a mismatch against the embedded schema, got %v", err)
	}
}

// Mismatches carry structured problems, located in the value.
func TestSchemaValidationError_Problems(t *testing.T) {
	iface := mustDecode(t, `{"openbindings":"0.2.0","operations":{"op":{"input":{"properties":{"a: b":{"type":"string"}}}}}}`)
	var mismatch *SchemaValidationError
	if err := ValidateOperationInput(map[string]any{"a: b": 5}, iface, "op"); !errors.As(err, &mismatch) {
		t.Fatalf("want a mismatch, got %v", err)
	}
	if len(mismatch.Problems) != 1 || mismatch.Problems[0].Path != "/a: b" {
		t.Fatalf("problems = %+v", mismatch.Problems)
	}
	if (&SchemaValidationError{}).Error() == "" {
		t.Fatal("a mismatch always has a message")
	}
}

// A nested resource's relative $id resolves once, against the resource that
// encloses it, however the resource is reached.
func TestValidateOperationInput_NestedRelativeIDsResolveOnce(t *testing.T) {
	iface := mustDecode(t, `{"openbindings":"0.2.0",
		"schemas":{"A":{"$id":"https://e.example/a/","$defs":{"B":{"$id":"b/","$ref":"#/$defs/X","$defs":{"X":{"type":"string"}}}}}},
		"operations":{"op":{"input":{"$ref":"https://e.example/a/b/"}}}}`)
	if err := ValidateOperationInput("x", iface, "op"); err != nil {
		t.Fatalf("an embedded, resolvable graph must validate: %v", err)
	}
	if err := ValidateOperationInput(5, iface, "op"); !errors.As(err, new(*SchemaValidationError)) {
		t.Fatalf("want a mismatch, got %v", err)
	}
}

// Only the meta-schemas the SDK carries are reserved; any other URI under
// json-schema.org is an ordinary embedded $id.
func TestValidateOperationInput_OnlyBuiltInMetaSchemaIDsAreReserved(t *testing.T) {
	iface := mustDecode(t, `{"openbindings":"0.2.0","schemas":{"S":{"$id":"https://json-schema.org/example/custom","type":"string"}},
		"operations":{"op":{"input":{"$ref":"https://json-schema.org/example/custom"}}}}`)
	if err := ValidateOperationInput("x", iface, "op"); err != nil {
		t.Fatalf("an embedded $id under json-schema.org resolves: %v", err)
	}
}

// A cycle of references that never advances into the value leaves no
// verdict wherever it sits in the graph, a branch evaluation might skip
// included (OBI-T-16).
func TestValidateOperationInput_ProgresslessCyclesAnywhereAreUnavailable(t *testing.T) {
	for name, input := range map[string]string{
		"under not":   `{"not":{"$ref":"#/schemas/Loop"}}`,
		"in an anyOf": `{"anyOf":[{"$ref":"#/schemas/Loop"},{"type":"string"}]}`,
		"under if":    `{"if":{"$ref":"#/schemas/Loop"},"then":false}`,
	} {
		iface := mustDecode(t, `{"openbindings":"0.2.0","schemas":{"Loop":{"$ref":"#/schemas/Loop"}},"operations":{"op":{"input":`+input+`}}}`)
		if err := ValidateOperationInput("s", iface, "op"); !errors.As(err, new(*SchemaGraphUnavailableError)) {
			t.Errorf("%s: want graph unavailable, got %v", name, err)
		}
	}
	recursive := mustDecode(t, `{"openbindings":"0.2.0","schemas":{"Node":{"type":"object","properties":{"next":{"$ref":"#/schemas/Node"}}}},
		"operations":{"op":{"input":{"$ref":"#/schemas/Node"}}}}`)
	if err := ValidateOperationInput(map[string]any{"next": map[string]any{}}, recursive, "op"); err != nil {
		t.Fatalf("a recursion through a property advances into the value: %v", err)
	}
}

// A $dynamicRef may land on any schema declaring its anchor dynamically, so
// the graph includes all of them.
func TestValidateOperationInput_DynamicReferencesReachEveryDynamicAnchor(t *testing.T) {
	iface := mustDecode(t, `{"openbindings":"0.2.0","schemas":{
		"R1":{"$id":"https://ex.test/r1","$ref":"https://ex.test/r2","$defs":{"x":{"$dynamicAnchor":"node","$ref":"https://outside.example/x"}}},
		"R2":{"$id":"https://ex.test/r2","$dynamicAnchor":"node","type":"object","properties":{"child":{"$dynamicRef":"#node"}}}},
		"operations":{"op":{"input":{"$ref":"https://ex.test/r1"}}}}`)
	if err := ValidateOperationInput(map[string]any{}, iface, "op"); !errors.As(err, new(*SchemaGraphUnavailableError)) {
		t.Fatalf("the dynamic scope reaches an external resource; want graph unavailable, got %v", err)
	}
}

// A relative $id at an OBI position, which OBI-D-05 excludes, gives its
// resource no URI (§7). What resolves without one is evaluated; a relative
// reference within it, which needs that URI as its base, leaves no verdict.
func TestValidateOperationInput_RelativeIDAtAnOBIPosition(t *testing.T) {
	alone := mustDecode(t, `{"openbindings":"0.2.0","operations":{"op":{"input":{"$id":"rel","type":"string","properties":{"a":{"$ref":"#/$defs/n"}},"$defs":{"n":{"type":"number"}}}}}}`)
	if err := ValidateOperationInput(map[string]any{"a": "x"}, alone, "op"); !errors.As(err, new(*SchemaValidationError)) {
		t.Errorf("want a mismatch, got %v", err)
	}
	relative := mustDecode(t, `{"openbindings":"0.2.0","operations":{"op":{"input":{"$id":"rel","$ref":"other"}}}}`)
	if err := ValidateOperationInput("s", relative, "op"); !errors.As(err, new(*SchemaGraphUnavailableError)) {
		t.Errorf("want graph unavailable, got %v", err)
	}
}

// Only the document's schema positions embed resources: a $id in an unknown
// member is no resource, whether or not another operation references it.
func TestValidateOperationInput_UnknownMembersEmbedNoResources(t *testing.T) {
	for _, extra := range []string{``, `,"b":{"input":{"$ref":"#/x-lib/S"}}`} {
		iface := mustDecode(t, `{"openbindings":"0.2.0","x-lib":{"S":{"$id":"https://ex.test/s","type":"string"}},
			"operations":{"a":{"input":{"$ref":"https://ex.test/s"}}`+extra+`}}`)
		if err := ValidateOperationInput("s", iface, "a"); !errors.As(err, new(*SchemaGraphUnavailableError)) {
			t.Errorf("with %q: want graph unavailable, got %v", extra, err)
		}
	}
}

// A document declaring no valid version is not interpreted.
func TestValidateOperationInput_MalformedVersionsAreNotInterpreted(t *testing.T) {
	for _, version := range []string{"latest", "", "0.2"} {
		iface := &Interface{OpenBindings: version, Operations: map[string]Operation{"op": {Input: map[string]any{"type": "string"}}}}
		err := ValidateOperationInput(5, iface, "op")
		if err == nil || errors.As(err, new(*SchemaValidationError)) || errors.As(err, new(*SchemaGraphUnavailableError)) {
			t.Errorf("%q: want a plain refusal to interpret, got %v", version, err)
		}
	}
}
