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
		// The 3.0 line's Schema Object is not the 2020-12 dialect and a
		// boolean is not a Schema Object there. The spelling is REFERRED
		// rather than classified, the same device D1n/D1a use, and it is
		// referred BECAUSE `openbindings.openapi@1` §9.2 ascribes an
		// interpretation to a boolean-valued schema on this line -- "A Media
		// Type Object whose `schema` is the JSON Schema boolean `true` … is
		// the same declaration as an omitted `schema` … UNDER EITHER EDITION".
		//
		// TWO CORRECTIONS TO THE SENTENCE THAT USED TO STAND HERE, both
		// verified rather than inherited (record 114 part C).
		//
		//  1. It cited §9.2 as ascribing a part interpretation to a
		//     boolean-valued MULTIPART PART on this line. §9.2 states the
		//     OPPOSITE in terms: that reading "governs a **Media Type
		//     Object's** own `schema` and does not descend to a form part".
		//     The referral's real basis is the Media Type Object clause, one
		//     position up, and this comment now names it.
		//  2. The referral now has a PINNED counter-authority on the other
		//     side, which it did not have when it was written. JSON Schema
		//     draft-wright-json-schema-00 §4.4: "A JSON schema MUST be an
		//     object"; its validation companion §5.16: the value of
		//     `properties` "MUST be an object. Each value of this object MUST
		//     be an object, and each object MUST be a valid JSON Schema"; and
		//     OAS 3.0.4 §4.7.24: "properties - Property definitions MUST be a
		//     Schema Object and not a standard JSON Schema", with "Additional
		//     keywords … not mentioned here are strictly unsupported" and
		//     `additionalProperties` the ONLY position the 3.0 line grants a
		//     boolean. So the authority says this IS a defect on the 3.0 line,
		//     and the referral is now a conflict between this floor and
		//     `openbindings.openapi@1` §9.2's own "under either edition",
		//     not a gap. It is filed as a defect report against §9.2 rather
		//     than corrected here, because the correction starts in the
		//     binding specification and not in an engine.
		//
		// Zero corpus incidence, re-derived at the artifacts' bytes over all
		// 260: the only two boolean `properties` members on the 3.0 line are
		// inside `example` payloads (`MITK/MITK`), which are not Schema Object
		// positions and which this walk never reaches.
		name: "a boolean properties member on the 3.0 line is REFERRED, not this class",
		document: `{"openapi":"3.0.0","paths":{"/a":{"get":{"responses":{"200":{"description":"ok",
		  "content":{"application/json":{"schema":{"type":"object","properties":{"f":true}}}}}}}}}}`,
		wantPositions:   nil,
		wantMethod:      "get",
		wantDisposition: "represented",
	},
	{
		// The clause that DOES fire at a `properties` member on the 3.0 line:
		// the bohr-io shape, a bare string where a Schema Object belongs.
		name: "a string properties member is this class on the 3.0 line",
		document: `{"openapi":"3.0.0","paths":{"/a":{"get":{"responses":{"200":{"description":"ok",
		  "content":{"application/json":{"schema":{"type":"object","properties":{"f":"array"}}}}}}}}}}`,
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
