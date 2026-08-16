package openapi

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"
)

// These pin the closure pass's own pieces in the layer that owns them. The
// end-to-end verdicts live in the shared twin table
// (external_composition_test.go); these say why each verdict comes out that
// way, and they fail loudly if a piece is changed for a reason that looks
// local.

func TestMayReferenceAnotherResource(t *testing.T) {
	cases := map[string]struct {
		document string
		want     bool
	}{
		"no reference at all": {
			document: "openapi: 3.1.2\npaths: {}\n",
			want:     false,
		},
		"only fragment references": {
			document: "components:\n  schemas:\n    A: {$ref: '#/components/schemas/B'}\n    B: {type: string}\n",
			want:     false,
		},
		"only fragment references, JSON spelling": {
			document: `{"components":{"schemas":{"A":{"$ref":"#/components/schemas/B"}}}}`,
			want:     false,
		},
		"a relative reference": {
			document: "components:\n  schemas:\n    A: {$ref: './other.yaml#/components/schemas/B'}\n",
			want:     true,
		},
		"an absolute reference": {
			document: `{"$ref":"https://example.com/other.json#/A"}`,
			want:     true,
		},
		"a reference with no fragment": {
			document: "paths:\n  /items: {$ref: './path-item.yaml'}\n",
			want:     true,
		},
		"a discriminator, whose mapping may address another resource": {
			document: "components:\n  schemas:\n    A:\n      discriminator: {propertyName: kind}\n",
			want:     true,
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			if got := mayReferenceAnotherResource([]byte(testCase.document)); got != testCase.want {
				t.Fatalf("mayReferenceAnotherResource = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestRawReferenceStringsIsPositionBlindAndOverApproximates(t *testing.T) {
	tree, ok := parseRawResource([]byte(`
components:
  schemas:
    A:
      properties:
        one: {$ref: "#/components/schemas/B"}
      discriminator:
        propertyName: kind
        mapping:
          plain: PlainName
          qualified: "#/components/schemas/C"
          external: "./other.yaml#/components/schemas/D"
      example:
        note: {$ref: "./example.yaml#/Sample"}
    B: {allOf: [{$ref: "./deep.yaml#/E"}]}
`))
	if !ok {
		t.Fatal("parse")
	}
	var found []string
	rawReferenceStrings(tree, func(ref string) { found = append(found, ref) })
	sort.Strings(found)
	want := []string{
		"#/components/schemas/B",
		"#/components/schemas/C",
		"./deep.yaml#/E",
		"./example.yaml#/Sample",
		"./other.yaml#/components/schemas/D",
	}
	if !reflect.DeepEqual(found, want) {
		t.Fatalf("references = %v, want %v", found, want)
	}
}

func TestRawPointerReachStopsAtAReferenceAndAtAMissingToken(t *testing.T) {
	tree, ok := parseRawResource([]byte(`
components:
  schemas:
    Alias: {$ref: "#/components/schemas/Real"}
    Real:
      properties:
        name: {type: string}
    List:
      items: [{type: string}, {type: integer}]
`))
	if !ok {
		t.Fatal("parse")
	}

	// Through a reference: evaluation stops AT the reference object, whose own
	// target the caller then composes.
	node, _ := rawPointerReach(tree, "/components/schemas/Alias/properties/name")
	object, isObject := node.(map[string]any)
	if !isObject || object["$ref"] != "#/components/schemas/Real" {
		t.Fatalf("reach = %#v, want the reference object", node)
	}

	// A token the artifact does not declare: evaluation stops where the
	// artifact stops describing it, and the loader owns the diagnostic.
	node, _ = rawPointerReach(tree, "/components/schemas/Real/properties/absent/deeper")
	if _, isObject := node.(map[string]any); !isObject {
		t.Fatalf("reach = %#v, want the deepest declared value", node)
	}

	// A sequence index resolves; a non-index token does not.
	node, _ = rawPointerReach(tree, "/components/schemas/List/items/1")
	if object, _ := node.(map[string]any); object["type"] != "integer" {
		t.Fatalf("reach = %#v, want the second item", node)
	}
	node, _ = rawPointerReach(tree, "/components/schemas/List/items/01")
	if _, isSlice := node.([]any); !isSlice {
		t.Fatalf("reach = %#v, want the sequence itself: RFC 6901 forbids a leading zero", node)
	}
}

func TestPruneToRetainedKeepsTheClosureAndEveryScalarOnTheWay(t *testing.T) {
	tree, ok := parseRawResource([]byte(`
openapi: "3.1.2"
info: {title: Library, version: "1"}
components:
  schemas:
    Big:
      type: object
      $id: "https://example.com/big"
      required: [kept]
      properties:
        kept: {type: string}
        dropped: {$ref: "#/components/schemas/Gone"}
    Unrelated: {type: string}
`))
	if !ok {
		t.Fatal("parse")
	}
	retained := &retainedPointers{}
	retained.add("/components/schemas/Big/properties/kept")

	pruned, changed := pruneToRetained(tree, retained.root)
	if !changed {
		t.Fatal("nothing was pruned")
	}
	encoded, err := json.Marshal(pruned)
	if err != nil {
		t.Fatal(err)
	}
	var reparsed map[string]any
	if err := json.Unmarshal(encoded, &reparsed); err != nil {
		t.Fatal(err)
	}

	// A scalar cannot carry a reference, and dropping one would take a field
	// that decides how the resource itself is read.
	if reparsed["openapi"] != "3.1.2" {
		t.Fatalf("the resource's own edition was dropped: %s", encoded)
	}
	if _, present := reparsed["info"]; present {
		t.Fatalf("an object outside the closure survived: %s", encoded)
	}
	big := reparsed["components"].(map[string]any)["schemas"].(map[string]any)["Big"].(map[string]any)
	if big["$id"] != "https://example.com/big" || big["type"] != "object" {
		t.Fatalf("a scalar on the retained path was dropped: %s", encoded)
	}
	if _, present := big["required"]; present {
		t.Fatalf("a sequence outside the closure survived: %s", encoded)
	}
	properties := big["properties"].(map[string]any)
	if _, present := properties["dropped"]; present {
		t.Fatalf("a sibling outside the closure survived: %s", encoded)
	}
	if !reflect.DeepEqual(properties["kept"], map[string]any{"type": "string"}) {
		t.Fatalf("the composed value did not survive whole: %s", encoded)
	}
	if _, present := reparsed["components"].(map[string]any)["schemas"].(map[string]any)["Unrelated"]; present {
		t.Fatalf("an uncomposed component survived: %s", encoded)
	}
}

func TestPruneToRetainedKeepsASequenceWhole(t *testing.T) {
	tree, ok := parseRawResource([]byte(`
components:
  schemas:
    Composed:
      allOf:
        - {type: object}
        - {$ref: "#/components/schemas/Other"}
`))
	if !ok {
		t.Fatal("parse")
	}
	retained := &retainedPointers{}
	// An index-addressed member cannot be dropped without renumbering its
	// siblings, so a sequence any pointer enters is retained whole.
	retained.add("/components/schemas/Composed/allOf/0")
	pruned, _ := pruneToRetained(tree, retained.root)
	encoded, err := json.Marshal(pruned)
	if err != nil {
		t.Fatal(err)
	}
	var reparsed map[string]any
	if err := json.Unmarshal(encoded, &reparsed); err != nil {
		t.Fatal(err)
	}
	allOf := reparsed["components"].(map[string]any)["schemas"].(map[string]any)["Composed"].(map[string]any)["allOf"].([]any)
	if len(allOf) != 2 {
		t.Fatalf("sequence length = %d, want 2: %s", len(allOf), encoded)
	}
}

func TestRetainedPointersSubsumeDeeperOnes(t *testing.T) {
	retained := &retainedPointers{}
	retained.add("/components/schemas/A/properties/x")
	retained.add("/components/schemas/A")
	if !retained.root.children["components"].children["schemas"].children["A"].whole {
		t.Fatal("composing a value whole must subsume a pointer into it")
	}
	if retained.root.children["components"].children["schemas"].children["A"].children != nil {
		t.Fatal("a whole retention keeps no partial children")
	}
}
