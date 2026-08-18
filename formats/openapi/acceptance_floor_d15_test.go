package openapi

// Block 8d-3: the registry-scoped class D15 -- a Schema Object keyword whose
// value violates the governing dialect's declared JSON type for it.
//
// The first four cases are the reviewed owning-unit census's own D15 mechanism
// fixtures (`corpus-lab/scripts/census-oas-owning-units.mjs`, mechanismTest),
// carried here verbatim so each engine is measured against the one answer
// rather than against itself. The last two extend the same edition-scoped
// clause in the direction the census does not fixture: a boolean schema is a
// 3.1 spelling, and is not a schema at all on the 3.0 line.
//
// These bite in both directions. Deleting the detector reddens the four
// positive cases; widening it past its edition guards reddens the two negative
// ones. Neither depends on the shared 66-cell case table, which carries no D15
// cell at all.

import (
	"strings"
	"testing"
)

type floorD15Case struct {
	name     string
	document string
	// wantPositions are the D15 positions the floor must record, exactly.
	wantPositions []string
	// wantDisposition is the ladder verdict for `#/paths/~1a/{method}`.
	wantMethod      string
	wantDisposition string
	// wantInvalidAlternatives counts the operation's invalidated request media
	// alternatives.
	wantInvalidAlternatives int
}

var floorD15Cases = []floorD15Case{
	{
		// The basset shape: the defect is inside the request alternative's
		// schema closure, and a request media alternative never climbs.
		name: "boolean required in a request media schema invalidates the alternative only",
		document: `{"openapi":"3.0.0","paths":{"/a":{"post":{
		  "requestBody":{"content":{"multipart/form-data":{"schema":{"type":"object","properties":{"f":{"type":"string","required":true}}}}}},
		  "responses":{"200":{"description":"ok"}}}}}}`,
		wantPositions:           []string{"#/paths/~1a/post/requestBody/content/multipart~1form-data/schema/properties/f/required"},
		wantMethod:              "post",
		wantDisposition:         "represented",
		wantInvalidAlternatives: 1,
	},
	{
		// The stoatchat shape: the tuple form is not the 3.0 line's `items`,
		// the response schema closure is unrepresentable, P2 climbs.
		name: "array-valued items in a response schema climbs P2",
		document: `{"openapi":"3.0.0","paths":{"/a":{"post":{"responses":{"200":{"description":"ok",
		  "content":{"application/json":{"schema":{"type":"object","additionalProperties":{"items":[{"type":"string"},{"type":"number"}]}}}}}}}}}}`,
		wantPositions:   []string{"#/paths/~1a/post/responses/200/content/application~1json/schema/additionalProperties/items"},
		wantMethod:      "post",
		wantDisposition: "invalid",
	},
	{
		// The nexu-io shape; edition-scoped, because the boolean spelling is
		// the 3.0 line's own correct draft-4 form.
		name: "boolean exclusiveMinimum fires on the 3.1 line",
		document: `{"openapi":"3.1.0","paths":{"/a":{"get":{"responses":{"200":{"description":"ok",
		  "content":{"application/json":{"schema":{"type":"number","exclusiveMinimum":true}}}}}}}}}`,
		wantPositions:   []string{"#/paths/~1a/get/responses/200/content/application~1json/schema/exclusiveMinimum"},
		wantMethod:      "get",
		wantDisposition: "invalid",
	},
	{
		// The edition guard, proven in the direction that does not fire.
		name: "the same boolean exclusiveMinimum on the 3.0 line is not this class",
		document: `{"openapi":"3.0.0","paths":{"/a":{"get":{"responses":{"200":{"description":"ok",
		  "content":{"application/json":{"schema":{"type":"number","minimum":0,"exclusiveMinimum":true}}}}}}}}}`,
		wantPositions:   nil,
		wantMethod:      "get",
		wantDisposition: "represented",
	},
	{
		// A boolean schema is the 2020-12 spelling the 3.1 line admits.
		name: "a boolean properties member is a schema on the 3.1 line",
		document: `{"openapi":"3.1.0","paths":{"/a":{"get":{"responses":{"200":{"description":"ok",
		  "content":{"application/json":{"schema":{"type":"object","properties":{"f":true}}}}}}}}}}`,
		wantPositions:   nil,
		wantMethod:      "get",
		wantDisposition: "represented",
	},
	{
		// The same spelling on the 3.0 line, whose Schema Object requires the
		// property definition to be a Schema Object.
		name: "the same boolean properties member is this class on the 3.0 line",
		document: `{"openapi":"3.0.0","paths":{"/a":{"get":{"responses":{"200":{"description":"ok",
		  "content":{"application/json":{"schema":{"type":"object","properties":{"f":true}}}}}}}}}}`,
		wantPositions:   []string{"#/paths/~1a/get/responses/200/content/application~1json/schema/properties/f"},
		wantMethod:      "get",
		wantDisposition: "invalid",
	},
}

func TestAcceptanceFloor_D15KeywordJSONTypes(t *testing.T) {
	for _, testCase := range floorD15Cases {
		t.Run(testCase.name, func(t *testing.T) {
			floor := computeAcceptanceFloorFromBytes([]byte(testCase.document))
			if floor == nil {
				t.Fatalf("the floor must read this document")
			}
			var got []string
			for position, class := range floor.Attributed {
				if class == floorD15 {
					got = append(got, position)
				}
			}
			if len(got) != len(testCase.wantPositions) {
				t.Fatalf("D15 positions %v, want %v", got, testCase.wantPositions)
			}
			for _, want := range testCase.wantPositions {
				found := false
				for _, position := range got {
					if position == want {
						found = true
					}
				}
				if !found {
					t.Fatalf("D15 positions %v, want %v", got, testCase.wantPositions)
				}
			}

			ref := "#/paths/~1a/" + testCase.wantMethod
			verdict := floor.opVerdict(ref)
			if verdict == nil {
				t.Fatalf("no ladder verdict at %s", ref)
			}
			if verdict.Disposition != testCase.wantDisposition {
				t.Fatalf("disposition %q, want %q", verdict.Disposition, testCase.wantDisposition)
			}
			if len(verdict.InvalidAlternatives) != testCase.wantInvalidAlternatives {
				t.Fatalf("invalid alternatives %d, want %d", len(verdict.InvalidAlternatives), testCase.wantInvalidAlternatives)
			}
			if testCase.wantDisposition == "invalid" {
				named := false
				for _, d := range verdict.Defects {
					if d.Class == floorD15 && d.Position == testCase.wantPositions[0] {
						named = true
					}
				}
				if !named {
					t.Fatalf("the climbing defects must name the D15 position, got %+v", verdict.Defects)
				}
			}
		})
	}
}

// The per-edition-line authority citation is carried byte-identically by every
// engine, so it is pinned here rather than left to drift.
func TestAcceptanceFloor_D15Authority(t *testing.T) {
	for line, want := range map[string]string{
		"3.0": "OAS 3.0 line, Schema Object: a keyword's value carries the JSON type the governing dialect declares for it -- `items` Value MUST be an object and not an array; `properties` definitions MUST be a Schema Object; `required` and `enum` are taken directly from JSON Schema, where each is an array",
		"3.1": "OAS 3.1 line via JSON Schema 2020-12: a keyword's value carries the JSON type the dialect declares for it -- `required` (Validation §6.5.3) and `enum` (§6.1.2) are arrays; `properties` members and `items` and `contains` are schemas, which on this line may be objects or booleans; `exclusiveMinimum` (§6.2.5) and `exclusiveMaximum` (§6.2.3) are numbers",
	} {
		if got := floorAuthority(floorD15, line); got != want {
			t.Errorf("floorAuthority(D15, %s) = %q", line, got)
		}
	}
	if !strings.Contains(floorAuthority(floorD15, "3.0"), "not an array") {
		t.Errorf("the 3.0 citation must carry the line's own items rule")
	}
}
