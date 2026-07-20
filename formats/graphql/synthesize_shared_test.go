package graphql

import (
	"encoding/json"
	"testing"
)

// parityPinnedUserSchema is the exact canonical (sorted-key) JSON both the Go
// and TS graphql synthesizers must emit for a `User { id: ID, name: String }`
// object in a shared (sibling-reused) position. The TS package's
// synthesize.test.ts asserts against this same literal, so equality here plus
// equality there pins Go≡TS output for the shared-type case (C8f parity pin).
const parityPinnedUserSchema = `{"properties":{"id":{"type":"string"},"name":{"type":"string"}},"type":"object"}`

// TestGraphqlTypeToJSONSchemaSharedObjectNotTruncated pins the F1/C8f fix: an
// object type reused in sibling (non-cyclic) positions must carry the full
// schema in EVERY position, not collapse to a bare {"type":"object"} after the
// first. `primary` and `secondary` share one `visited` set here — the exact
// bug scenario.
func TestGraphqlTypeToJSONSchemaSharedObjectNotTruncated(t *testing.T) {
	tm := map[string]*fullType{
		"SearchPayload": {
			Kind: "OBJECT",
			Name: "SearchPayload",
			Fields: []field{
				{Name: "primary", Type: typeRef{Kind: "OBJECT", Name: "User"}},
				{Name: "secondary", Type: typeRef{Kind: "OBJECT", Name: "User"}},
			},
		},
		"User": {
			Kind: "OBJECT",
			Name: "User",
			Fields: []field{
				{Name: "id", Type: typeRef{Kind: "SCALAR", Name: "ID"}},
				{Name: "name", Type: typeRef{Kind: "SCALAR", Name: "String"}},
			},
		},
	}

	got := graphqlTypeToJSONSchema(typeRef{Kind: "OBJECT", Name: "SearchPayload"}, tm, make(map[string]bool))
	props, ok := got["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected SearchPayload properties, got %v", got)
	}

	for _, fieldName := range []string{"primary", "secondary"} {
		f, ok := props[fieldName].(map[string]any)
		if !ok {
			t.Fatalf("expected %s property, got %v", fieldName, props)
		}
		fp, ok := f["properties"].(map[string]any)
		if !ok {
			t.Errorf("field %q truncated to a property-less object (shared-type reuse mistaken for a cycle): %v", fieldName, f)
			continue
		}
		if _, ok := fp["name"]; !ok {
			t.Errorf("field %q is missing User.name; got %v", fieldName, fp)
		}
		// Parity pin: the exact shared-position schema, canonicalized.
		if canon := canonicalJSON(t, f); canon != parityPinnedUserSchema {
			t.Errorf("field %q schema = %s, want %s (Go≡TS parity pin)", fieldName, canon, parityPinnedUserSchema)
		}
	}
}

// TestGraphqlTypeToJSONSchemaSharedInputObjectNotTruncated is the input-object
// mirror: an input type reused in sibling input positions must also carry the
// full schema in every position.
func TestGraphqlTypeToJSONSchemaSharedInputObjectNotTruncated(t *testing.T) {
	tm := map[string]*fullType{
		"Pair": {
			Kind: "INPUT_OBJECT",
			Name: "Pair",
			InputFields: []inputValue{
				{Name: "a", Type: typeRef{Kind: "INPUT_OBJECT", Name: "Point"}},
				{Name: "b", Type: typeRef{Kind: "INPUT_OBJECT", Name: "Point"}},
			},
		},
		"Point": {
			Kind: "INPUT_OBJECT",
			Name: "Point",
			InputFields: []inputValue{
				{Name: "label", Type: typeRef{Kind: "SCALAR", Name: "String"}},
			},
		},
	}

	got := graphqlTypeToJSONSchema(typeRef{Kind: "INPUT_OBJECT", Name: "Pair"}, tm, make(map[string]bool))
	props, ok := got["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected Pair properties, got %v", got)
	}

	for _, fieldName := range []string{"a", "b"} {
		f, ok := props[fieldName].(map[string]any)
		if !ok {
			t.Fatalf("expected %s property, got %v", fieldName, props)
		}
		fp, ok := f["properties"].(map[string]any)
		if !ok {
			t.Errorf("input field %q truncated to a property-less object: %v", fieldName, f)
			continue
		}
		if _, ok := fp["label"]; !ok {
			t.Errorf("input field %q is missing Point.label; got %v", fieldName, fp)
		}
	}
}

// canonicalJSON marshals v with sorted keys (Go's encoding/json sorts map keys
// by default), yielding a canonical form comparable byte-for-byte with the TS
// package's canonicalize() output.
func canonicalJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}
