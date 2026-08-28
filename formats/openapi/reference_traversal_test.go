package openapi

import (
	"encoding/json"
	"testing"
)

// TestPointerBelowReferenceEditionPartition asserts the partition as a literal
// map over all nine accepted editions, so a change to it is a change to a
// visible table rather than to a switch statement's default arm. The
// end-to-end verdicts are executed from the shared twin case table in
// external_composition_test.go; this pins the rule the table exercises.
//
// The authorities per edition are quoted in reference_traversal.go and
// re-derived at the pinned bytes by
// corpus-lab/scripts/verify-pointer-below-reference-authorities.mjs.
func TestPointerBelowReferenceEditionPartition(t *testing.T) {
	want := map[string]bool{
		"3.0.0": true,  // JSON Reference incorporated for $ref processing
		"3.0.1": true,  //
		"3.0.2": true,  //
		"3.0.3": true,  //
		"3.0.4": true,  //
		"3.1.0": false, // fragment is a JSON-Pointer over the referenced document
		"3.1.1": false, //
		"3.1.2": false, //
		"3.2.0": false, // fragment is a JSON-Pointer over the referenced document
	}
	for edition, follows := range want {
		if got := openAPIFollowsPointerBelowReference(edition); got != follows {
			t.Errorf("openAPIFollowsPointerBelowReference(%q) = %v, want %v", edition, got, follows)
		}
	}
	// An edition this specification does not accept is refused by OAPI-P-01
	// with its own diagnostic, so this rule must not pre-empt it.
	for _, edition := range []string{"", "2.0", "3.0.5", "3.1.3", "3.2.1"} {
		if !openAPIFollowsPointerBelowReference(edition) {
			t.Errorf("openAPIFollowsPointerBelowReference(%q) refuses; an unaccepted edition must reach OAPI-P-01's diagnostic instead", edition)
		}
	}
}

// TestPointerBelowReference pins what counts as running below a reference, and
// what does not. Every negative here is a case an engine would fail if it
// refused on the mere presence of a `$ref` anywhere near a pointer.
func TestPointerBelowReference(t *testing.T) {
	const document = `{
	  "components": {
	    "schemas": {
	      "Alias": {"$ref": "#/components/schemas/Real"},
	      "Sibling": {"$ref": "#/components/schemas/Real", "properties": {"name": {"type": "integer"}}},
	      "Real": {"type": "object", "properties": {"name": {"type": "string"}}},
	      "List": {"prefixItems": [{"$ref": "#/components/schemas/Real"}, {"type": "null"}]}
	    }
	  }
	}`
	var root any
	if err := json.Unmarshal([]byte(document), &root); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}

	cases := []struct {
		pointer  string
		ref      string
		token    string
		expected bool
		why      string
	}{
		{
			pointer: "/components/schemas/Alias/properties/name", ref: "#/components/schemas/Real",
			token: "properties", expected: true,
			why: "the alias's only member is $ref, so `properties` identifies nothing there",
		},
		{
			pointer: "/components/schemas/Alias", expected: false,
			why: "the pointer LANDS on the reference; no token is looked up inside it",
		},
		{
			pointer: "/components/schemas/Real/properties/name", expected: false,
			why: "every node on the path is spelled out",
		},
		{
			pointer: "/components/schemas/Sibling/properties/name", expected: false,
			why: "the token names the node's own sibling member, which JSON Schema 2020-12 §8.2.3.1's note contemplates",
		},
		{
			pointer: "/components/schemas/Real/properties/absent", expected: false,
			why: "names nothing, with no reference in the way: an ordinary unresolvable reference",
		},
		{
			pointer: "/components/schemas/List/prefixItems/0/properties/name",
			ref:     "#/components/schemas/Real", token: "properties", expected: true,
			why: "a sequence index is a legal step on the way to a reference",
		},
		{
			pointer: "/components/schemas/List/prefixItems/2", expected: false,
			why: "an out-of-range index names nothing, with no reference in the way",
		},
		{
			pointer: "/components/schemas/Ali~as/properties/name", expected: false,
			why: "a literal ~ is not a legal reference token (RFC 6901 §3), so this is not a traversal question",
		},
		{
			pointer: "components/schemas/Alias/properties/name", expected: false,
			why: "not a JSON Pointer at all; an anchor or a malformed fragment carries no path",
		},
	}

	for _, testCase := range cases {
		ref, token, found := pointerBelowReference(root, testCase.pointer)
		if found != testCase.expected {
			t.Errorf("pointerBelowReference(%q) found = %v, want %v (%s)", testCase.pointer, found, testCase.expected, testCase.why)
			continue
		}
		if !testCase.expected {
			continue
		}
		if ref != testCase.ref || token != testCase.token {
			t.Errorf("pointerBelowReference(%q) = (%q, %q), want (%q, %q)", testCase.pointer, ref, token, testCase.ref, testCase.token)
		}
	}
}
