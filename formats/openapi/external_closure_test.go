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

// A sequence keeps every index and replaces the ones outside the closure. This
// is the piece that decides the shared table's sequence cases: a sibling
// element is outside the composed closure exactly as a sibling property is, so
// keeping it would refuse an artifact §6 says resolves.
//
// The replacement's kind is read off the RETAINED indices, not off the element
// being discarded: a retained mapping means the position holds Objects, so
// every non-composed index becomes `{}` whatever kind it had — including a
// scalar, which the mapping branch would have kept.
func TestPruneToRetainedEmptiesSequenceElementsOutsideTheClosure(t *testing.T) {
	tree, ok := parseRawResource([]byte(`
components:
  schemas:
    Composed:
      allOf:
        - {type: object}
        - {$ref: "#/components/schemas/Gone"}
        - {type: string}
        - [{$ref: "#/components/schemas/AlsoGone"}]
        - a scalar element
`))
	if !ok {
		t.Fatal("parse")
	}
	retained := &retainedPointers{}
	retained.add("/components/schemas/Composed/allOf/0")
	retained.add("/components/schemas/Composed/allOf/2")

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
	allOf := reparsed["components"].(map[string]any)["schemas"].(map[string]any)["Composed"].(map[string]any)["allOf"].([]any)

	// Every index survives: a JSON Pointer denotes an element by position, so
	// dropping one would rename its siblings.
	if len(allOf) != 5 {
		t.Fatalf("sequence length = %d, want 5: %s", len(allOf), encoded)
	}
	if !reflect.DeepEqual(allOf[0], map[string]any{"type": "object"}) {
		t.Fatalf("the first composed element did not survive whole: %s", encoded)
	}
	if !reflect.DeepEqual(allOf[2], map[string]any{"type": "string"}) {
		t.Fatalf("the second composed element did not survive at its own index: %s", encoded)
	}
	// Every element the closure never reaches becomes the empty mapping,
	// because a retained index holds one: a mapping element, a sequence
	// element and a scalar element alike. The last is the whole point — a
	// scalar at an Object-shaped index is a value the typed loader cannot
	// read, so keeping it would refuse the artifact.
	for _, index := range []int{1, 3, 4} {
		if !reflect.DeepEqual(allOf[index], map[string]any{}) {
			t.Fatalf("allOf/%d outside the closure was not replaced by the empty mapping: %s", index, encoded)
		}
	}
}

// Where the closure reaches no container in a sequence, nothing is substituted
// for a scalar. A pointer terminating at a scalar element is a doomed reference
// under every OAS position, but it must not corrupt the scalar array it sits
// in on the way to its own diagnostic.
func TestPruneToRetainedLeavesAScalarOnlySequenceAlone(t *testing.T) {
	tree, ok := parseRawResource([]byte(`
components:
  schemas:
    Composed:
      required: [alpha, beta, gamma]
`))
	if !ok {
		t.Fatal("parse")
	}
	retained := &retainedPointers{}
	retained.add("/components/schemas/Composed/required/1")

	required := tree.(map[string]any)["components"].(map[string]any)["schemas"].(map[string]any)["Composed"].(map[string]any)["required"]
	node := retained.root.children["components"].children["schemas"].children["Composed"].children["required"]
	pruned, changed := pruneToRetained(required, node)
	if changed {
		t.Fatalf("a scalar-only sequence was rewritten: %#v", pruned)
	}
	if !reflect.DeepEqual(pruned, []any{"alpha", "beta", "gamma"}) {
		t.Fatalf("scalar elements did not survive: %#v", pruned)
	}
}

// A retained index holding a sequence makes the neutral element `[]`, so the
// rule is total over both container kinds rather than assuming Objects.
func TestPruneToRetainedTakesTheNeutralElementFromTheRetainedIndex(t *testing.T) {
	tree, ok := parseRawResource([]byte(`
outer:
  - [{$ref: "#/kept"}]
  - [{$ref: "#/gone"}]
  - a scalar element
`))
	if !ok {
		t.Fatal("parse")
	}
	retained := &retainedPointers{}
	retained.add("/outer/0")

	pruned, changed := pruneToRetained(tree.(map[string]any)["outer"], retained.root.children["outer"])
	if !changed {
		t.Fatal("nothing was pruned")
	}
	elements, ok := pruned.([]any)
	if !ok || len(elements) != 3 {
		t.Fatalf("sequence length changed: %#v", pruned)
	}
	for _, index := range []int{1, 2} {
		if !reflect.DeepEqual(elements[index], []any{}) {
			t.Fatalf("outer/%d was not replaced by the empty sequence: %#v", index, elements[index])
		}
	}
	// No two positions may share one container: the tree is marshalled, and
	// aliasing would be a latent bug the marshaller happens not to expose.
	elements[1] = append(elements[1].([]any), "mutated")
	if len(elements[2].([]any)) != 0 {
		t.Fatal("two replaced indices share one container")
	}
}

// A sequence every element of which is composed is returned unchanged; one no
// pointer enters is dropped whole by its parent mapping (above).
func TestPruneToRetainedLeavesAFullyComposedSequenceAlone(t *testing.T) {
	tree, ok := parseRawResource([]byte(`
components:
  schemas:
    Composed:
      allOf:
        - {type: object}
        - {type: string}
`))
	if !ok {
		t.Fatal("parse")
	}
	retained := &retainedPointers{}
	retained.add("/components/schemas/Composed/allOf/0")
	retained.add("/components/schemas/Composed/allOf/1")

	composed := tree.(map[string]any)["components"].(map[string]any)["schemas"].(map[string]any)["Composed"].(map[string]any)
	pruned, _ := pruneToRetained(composed["allOf"], retained.root.
		children["components"].children["schemas"].children["Composed"].children["allOf"])
	if !reflect.DeepEqual(pruned, composed["allOf"]) {
		t.Fatalf("a fully composed sequence was rewritten: %#v", pruned)
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
