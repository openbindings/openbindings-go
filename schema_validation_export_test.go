package openbindings

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
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
	if err := ValidateAgainstNamedSchema(map[string]any{"name": "ok"}, doc, "Thing"); err != nil {
		t.Fatalf("named schema rejected a valid value: %v", err)
	}
	var mismatch *SchemaValidationError
	if err := ValidateAgainstNamedSchema(map[string]any{"name": 5}, doc, "Thing"); !errors.As(err, &mismatch) {
		t.Fatalf("named schema: want an instance mismatch, got %v", err)
	}
	var unavailable *SchemaGraphUnavailableError
	if err := ValidateAgainstNamedSchema("x", doc, "Missing"); !errors.As(err, &unavailable) {
		t.Fatalf("undefined named schema: want graph unavailable, got %v", err)
	}
}

// A standalone schema is its own resolution root. Its $defs are its own, so a
// document's schemas map can never shadow or be shadowed by them.
func TestValidateAgainstSchema_StandaloneSchemaIsItsOwnRoot(t *testing.T) {
	own := map[string]any{
		"$defs": map[string]any{"Task": map[string]any{"type": "string"}},
		"$ref":  "#/$defs/Task",
	}
	if err := ValidateAgainstSchema("hello", own); err != nil {
		t.Fatalf("own $defs entry must resolve within the schema: %v", err)
	}
	var mismatch *SchemaValidationError
	if err := ValidateAgainstSchema(map[string]any{}, own); !errors.As(err, &mismatch) {
		t.Fatalf("own $defs entry must be enforced, got %v", err)
	}
	var unavailable *SchemaGraphUnavailableError
	if err := ValidateAgainstSchema("hello", map[string]any{"$ref": "#/schemas/Task"}); !errors.As(err, &unavailable) {
		t.Fatalf("an OBI-position reference has no meaning in a standalone schema; want graph unavailable, got %v", err)
	}
}

// TestValidateAgainstSchema_ExternalRefFailsClosed pins the OBI-T-07/T-08
// clarification: validation is against the FULLY RESOLVED schema, so a
// schema carrying an external $ref the tool cannot fetch is a validation
// error (fail closed), never a partial pass.
func TestValidateAgainstSchema_ExternalRefFailsClosed(t *testing.T) {
	opSchema := map[string]any{"$ref": "https://example.com/schemas/user-input.json"}
	err := ValidateAgainstSchema(map[string]any{"id": "u1"}, opSchema)
	if err == nil {
		t.Fatal("external $ref should fail closed, not validate partially")
	}
}

// TestValidateAgainstSchema_FormatIsAnnotationOnly pins §6.2's boundary
// rule: `format` never asserts at OBI validation boundaries — a value
// violating `format` still validates; enforced syntax belongs to `pattern`.
func TestValidateAgainstSchema_FormatIsAnnotationOnly(t *testing.T) {
	opSchema := map[string]any{"type": "string", "format": "email"}
	if err := ValidateAgainstSchema("not-an-email", opSchema); err != nil {
		t.Fatalf("format must be annotation-only at OBI boundaries, got %v", err)
	}
	patternSchema := map[string]any{"type": "string", "pattern": "^[^@]+@[^@]+$"}
	if err := ValidateAgainstSchema("not-an-email", patternSchema); err == nil {
		t.Fatal("pattern is the assertion lane and must reject")
	}
}

// TestValidateAgainstSchema_ECMAPatternDialect pins the regex dialect at
// OBI boundaries: JSON Schema 2020-12 specifies ECMA-262 semantics, so a
// lookahead pattern (inexpressible in RE2) asserts correctly.
func TestValidateAgainstSchema_ECMAPatternDialect(t *testing.T) {
	lookahead := map[string]any{"type": "string", "pattern": "^(?=.*[A-Z]).*$"}
	if err := ValidateAgainstSchema("Password1", lookahead); err != nil {
		t.Fatalf("lookahead must match per ECMA dialect, got %v", err)
	}
	if err := ValidateAgainstSchema("nocaps", lookahead); err == nil {
		t.Fatal("lookahead must reject a string without uppercase")
	}
	// Engine-fidelity note: regexp2's ECMAScript mode tolerates some
	// non-ECMA extras (inline flags like (?i)) rather than rejecting
	// them — a named liberal-acceptance delta vs TS, confined to
	// patterns that are invalid per the spec's dialect anyway.
}

// TestValidateAgainstSchema_DynamicPairInsideEmbeddedID pins that the
// invocation-boundary compiler (the reachable-closure resolution performed
// by the document-rooted compile, via the underlying jsonschema/v6 library) does
// not choke on a legal $dynamicRef/$dynamicAnchor pair confined inside a
// schema declaring its own $id (OBI-D-05's carve-out; OBI-D-16 notes
// $dynamicRef does not participate in same-document reference resolution).
// Full 2020-12 recursive-extension semantics apply within the resource.
func TestValidateAgainstSchema_DynamicPairInsideEmbeddedID(t *testing.T) {
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

// TestValidateAgainstSchema_PercentEncodedFragmentResolves pins the
// document-rooted compile (via the underlying
// jsonschema/v6 library) resolving a percent-encoded same-document
// fragment end to end: RFC 6901 §6 decodes the fragment before evaluating
// it as a JSON Pointer, so "#/schemas/T%61sk" reaches the schemas key
// "Task" exactly as "#/schemas/Task" does — the compile backend needs no
// SDK-side normalization here (santhosh-tekuri/jsonschema/v6 is
// RFC-correct on its own).
func TestValidateAgainstSchema_PercentEncodedFragmentResolves(t *testing.T) {
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

// TestValidateAgainstSchema_ReachableClosureOnly pins the scope of
// T-07/T-08's "whole governing schema": the static closure REACHABLE from
// the governing root (keyword subschemas + reference targets,
// transitively). A lexically-present but unreachable entry — an
// unreferenced $defs member, an unrelated document-schemas entry merged
// into the compound — never participates in a verdict and must not poison
// the boundary. A dangling same-document ref there is OBI-D-16's
// document-level concern, not an invocation refusal. Mirrors the TS SDK's
// schema-conformance oracle.
func TestValidateAgainstSchema_ReachableClosureOnly(t *testing.T) {
	// Unreferenced $defs entry with an external $ref: must not refuse.
	dead := map[string]any{
		"type":  "string",
		"$defs": map[string]any{"dead": map[string]any{"$ref": "https://example.com/never-fetched.json"}},
	}
	if err := ValidateAgainstSchema("hi", dead); err != nil {
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
	if err := ValidateAgainstSchema("hi", reached); err == nil {
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
	if err := ValidateAgainstSchema("text", ref); !errors.As(err, &unavailable) {
		t.Fatalf("standalone: want graph unavailable, got %v", err)
	}
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
	if err := ValidateAgainstSchema("x", withDialect); err != nil {
		t.Fatalf("the 2020-12 meta-schema is built in: %v", err)
	}
}
