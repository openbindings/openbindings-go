package openapi

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/openbindings/openbindings-go/synthesize"
)

// The cut-point case table is SHARED with the TypeScript engine:
// testdata/cut-point-cases.json is byte-identical to
// openbindings-ts/packages/openapi/src/testdata/cut-point-cases.json, and each
// case pins the emission BOTH engines must produce. A divergence in either
// engine therefore fails the other engine's suite, which is the only way this
// obligation can be checked by execution rather than by reading two
// implementations.
//
// The cases cover, in order: a same-document component cycle; a mutual
// two-cycle; a three-member SCC; a cycle read/write projection removes in one
// direction only (both directions); a cycle through a position that is not a
// declared component, alone and reached from a second component; an
// artifact-declared nested `$defs` beside a cycle; a cycle plus a definition
// projection makes unreachable; a cycle behind `allOf` and array `items`; two
// cut points whose trailing pointer tokens collide; a cycle reached only
// through a parameter, with and without a Parameter Object description; two
// documents where the two directions cut for different reasons; and a
// referenced component declaring an empty `required` list.
//
// Both parameter cases declare `content` rather than `schema`. A cycle through
// an object parameter requires a composite member, and a style-lane parameter
// declaring one is excluded at synthesis (styleLaneUndefinedExpansionMember in
// media.go), so the style lane cannot carry a cyclic parameter at all. The
// content lane keeps the cut-point question the cases exist to ask.
//
// The last two came from the corpus rather than from the drawing board: they
// are the classes the first measured run of this change moved, and both are
// here because the measurement caught them.

type cutPointCase struct {
	Name     string          `json:"name"`
	Document json.RawMessage `json:"document"`
	Expect   struct {
		Strict     string                    `json:"strict"`
		Operations map[string]map[string]any `json:"operations"`
	} `json:"expect"`
}

func loadCutPointCases(t *testing.T) []cutPointCase {
	t.Helper()
	data, err := os.ReadFile("testdata/cut-point-cases.json")
	if err != nil {
		t.Fatalf("read case table: %v", err)
	}
	var table struct {
		Cases []cutPointCase `json:"cases"`
	}
	if err := json.Unmarshal(data, &table); err != nil {
		t.Fatalf("parse case table: %v", err)
	}
	if len(table.Cases) == 0 {
		t.Fatal("case table is empty")
	}
	return table.Cases
}

func canonicalJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var reparsed any
	if err := json.Unmarshal(encoded, &reparsed); err != nil {
		t.Fatalf("reparse: %v", err)
	}
	// encoding/json sorts object keys, so re-marshaling the reparsed value is a
	// canonical form for comparison against the table.
	canonical, err := json.MarshalIndent(reparsed, "", " ")
	if err != nil {
		t.Fatalf("marshal canonical: %v", err)
	}
	return string(canonical)
}

func TestCutPointCaseTable(t *testing.T) {
	for _, testCase := range loadCutPointCases(t) {
		t.Run(testCase.Name, func(t *testing.T) {
			input := func() *synthesize.SynthesizeInput {
				return &synthesize.SynthesizeInput{
					Sources: []synthesize.SynthesizeSource{{
						BindingSpec: BindingSpec,
						Content:     json.RawMessage(testCase.Document),
					}},
				}
			}

			result, err := NewSynthesizer().SynthesizeInterfaceWithCoverage(context.Background(), input())
			if err != nil {
				t.Fatalf("synthesize: %v", err)
			}
			for key, sides := range testCase.Expect.Operations {
				op, found := result.Interface.Operations[key]
				if !found {
					t.Fatalf("operation %q missing", key)
				}
				for side, want := range map[string]any{"input": sides["input"], "output": sides["output"]} {
					got := op.Input
					if side == "output" {
						got = op.Output
					}
					if canonicalJSON(t, got) != canonicalJSON(t, want) {
						t.Errorf("operation %q %s schema:\n got %s\nwant %s",
							key, side, canonicalJSON(t, got), canonicalJSON(t, want))
					}
				}
			}
			if len(result.Interface.Operations) != len(testCase.Expect.Operations) {
				t.Errorf("operation count = %d, want %d",
					len(result.Interface.Operations), len(testCase.Expect.Operations))
			}

			// The strict surface is where an unresolvable emitted `$ref` shows
			// up: it refuses the whole source at OBI-D-16.
			strict := "accepted"
			if _, err := NewSynthesizer().SynthesizeInterface(context.Background(), input()); err != nil {
				strict = "refused: " + err.Error()
			}
			if strict != testCase.Expect.Strict {
				t.Errorf("strict surface = %q, want %q", strict, testCase.Expect.Strict)
			}
		})
	}
}

// TestCutPointsAreDirectionalAndAddressed states the two properties the case
// table exercises end to end, at the unit the convention is stated in.
func TestCutPointsAreDirectionalAndAddressed(t *testing.T) {
	// `parent` is readOnly, so the request direction has no cycle to cut and the
	// response direction does.
	registry := map[string]any{
		"#/components/schemas/Folder": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string"},
				"parent": map[string]any{
					"readOnly": true,
					"allOf":    []any{map[string]any{"$ref": "#/components/schemas/Folder"}},
				},
			},
		},
	}
	request, response := newDirectionGraphs(registry)
	if len(request.cyclic) != 0 {
		t.Errorf("request cut points = %v, want none: projection removed the only edge", request.cyclic)
	}
	if !response.cyclic["#/components/schemas/Folder"] {
		t.Errorf("response cut points = %v, want Folder", response.cyclic)
	}

	// A cycle closing through a position that is not a declared component is a
	// cycle, and the position the artifact addressed is the cut point.
	addressed := map[string]any{
		"#/components/schemas/Wrapper": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"inner": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"back": map[string]any{"$ref": "#/components/schemas/Wrapper/properties/inner"},
					},
				},
			},
		},
	}
	graph, _ := newDirectionGraphs(addressed)
	if !graph.cyclic["#/components/schemas/Wrapper/properties/inner"] {
		t.Errorf("cut points = %v, want the addressed non-component position", graph.cyclic)
	}
	if graph.cyclic["#/components/schemas/Wrapper"] {
		t.Error("Wrapper is not on the cycle and must not be hoisted")
	}
	if !graph.readdressed["#/components/schemas/Wrapper"] {
		t.Error("Wrapper declares the cut point, so its declaration must be readdressed")
	}
	declaration := graph.registry["#/components/schemas/Wrapper"].(map[string]any)
	properties := declaration["properties"].(map[string]any)
	if ref, _ := properties["inner"].(map[string]any)["$ref"].(string); ref != "#/components/schemas/Wrapper/properties/inner" {
		t.Errorf("declaration site = %#v, want an explicit reference to the cut point", properties["inner"])
	}
}
