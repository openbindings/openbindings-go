package schemaprofile

// Reason-string alignment table: pins the EXACT diagnostic each directional
// check produces, byte-identical with the TypeScript SDK (the parity rule).
// Mirrored in packages/sdk/src/schema-profile/reasons.test.ts — the two
// tables carry the same fixtures and the same expected strings; a change on
// one side must land on both.
//
// Conventions pinned here:
//   - values and counts interpolate in JCS (RFC 8785) rendering — strings
//     quoted, numbers in ECMAScript form (no %g exponent spellings);
//   - the const/enum prefix names the DECIDING keyword: the keyword whose
//     constraint rejects the flowing value (input: the candidate's; output:
//     the target's);
//   - exclusive bounds are marked ("exclusive 0");
//   - unions carry the real union key and the failing variant index;
//   - multi-member faults (types, enum values, required, properties) name
//     the lexicographically FIRST failing member;
//   - property/required member names interpolate in the same JCS rendering
//     as values (quoted, JSON-string escaping) — visible only for names
//     carrying quotes, backslashes, or control characters; plain names
//     render exactly as before;
//   - tell-tale non-normalized inputs (scalar type, unresolved $ref,
//     unflattened allOf) are refused loudly — see refusalCases below.

import (
	"encoding/json"
	"errors"
	"testing"
)

type reasonCase struct {
	name      string
	direction string // "input" or "output"
	target    string // schema JSON (normalized form)
	candidate string
	reason    string // expected exact reason; "" = compatible
}

var reasonCases = []reasonCase{
	// --- type ---
	{"input type missing single", "input",
		`{"type":["string","integer"]}`, `{"type":["string"]}`,
		`type: candidate does not allow "integer"`},
	{"input type missing multiple sorted", "input",
		`{"type":["string","boolean","integer"]}`, `{"type":["number"]}`,
		`type: candidate does not allow "boolean", "string"`},
	{"output type extra", "output",
		`{"type":["string"]}`, `{"type":["string","number"]}`,
		`type: candidate allows "number" but target does not`},
	{"input untyped target vs typed candidate", "input",
		`{"minimum":1}`, `{"type":["number"]}`,
		`type: candidate does not allow all types`},
	{"output untyped candidate vs typed target", "output",
		`{"type":["string"]}`, `{"minimum":1}`,
		`type: candidate allows all types but target does not`},
	// Type names join the JCS canon (extension of the member-name escaping
	// ruling): the expected string derives from JSON.stringify semantics
	// (canonicalKey), not Go %q. The TS table carries the identical case.
	{"pathological type name renders as JCS", "input",
		`{"type":["string","weird\"type"]}`, `{"type":["string"]}`,
		`type: candidate does not allow "weird\"type"`},

	// --- const/enum: input (deciding keyword = candidate's) ---
	{"input const vs const mismatch", "input",
		`{"const":"a"}`, `{"const":"b"}`,
		`const: candidate const "b" does not match target const "a"`},
	{"input const vs enum missing", "input",
		`{"const":"a"}`, `{"enum":["b","c"]}`,
		`enum: target const "a" not in candidate enum`},
	{"input enum vs const cannot cover", "input",
		`{"enum":["a","b"]}`, `{"const":"a"}`,
		`const: candidate const "a" cannot cover 2 target enum values`},
	{"input single enum vs const mismatch", "input",
		`{"enum":["a"]}`, `{"const":"b"}`,
		`const: candidate const "b" not in target enum`},
	{"input enum vs enum missing sorted", "input",
		`{"enum":["b","a","c"]}`, `{"enum":["c"]}`,
		`enum: target value "a" not in candidate enum`},

	// --- const/enum: output (deciding keyword = target's) ---
	{"output enum vs const outside", "output",
		`{"enum":["a"]}`, `{"const":"b"}`,
		`enum: candidate const "b" not in target enum`},
	{"output enum vs enum extra sorted", "output",
		`{"enum":["a"]}`, `{"enum":["a","c","b"]}`,
		`enum: candidate value "b" not in target enum`},
	{"output enum vs unconstrained", "output",
		`{"enum":["a"]}`, `{"type":["string"]}`,
		`enum: candidate is unconstrained but target has enum`},
	{"output const vs const mismatch", "output",
		`{"const":"a"}`, `{"const":"b"}`,
		`const: candidate const "b" does not match target const "a"`},
	{"output const vs multi enum", "output",
		`{"const":"a"}`, `{"enum":["a","b"]}`,
		`const: candidate enum has 2 values but target allows only const "a"`},
	{"output const vs single enum mismatch", "output",
		`{"const":"a"}`, `{"enum":["b"]}`,
		`const: candidate enum value does not match target const "a"`},
	{"output const vs unconstrained", "output",
		`{"const":"a"}`, `{"type":["string"]}`,
		`const: candidate is unconstrained but target requires const "a"`},
	{"const values render as JCS", "input",
		`{"const":1}`, `{"const":2}`,
		`const: candidate const 2 does not match target const 1`},

	// --- Top / empty ---
	{"output empty candidate vs constrained target", "output",
		`{"type":["string"]}`, `{}`,
		`candidate is unconstrained but target is not`},
	{"input constrained candidate vs Top target", "input",
		`{}`, `{"type":["string"]}`,
		`candidate is constrained but target is unconstrained (Top)`},

	// --- objects ---
	{"input required extra sorted", "input",
		`{"type":["object"]}`, `{"type":["object"],"required":["z","y"]}`,
		`required: candidate requires "y" but target does not`},
	{"output required missing", "output",
		`{"type":["object"],"required":["a"]}`, `{"type":["object"]}`,
		`required: target requires "a" but candidate does not`},
	{"nested property prefix", "input",
		`{"type":["object"],"properties":{"count":{"type":["integer"]}}}`,
		`{"type":["object"],"properties":{"count":{"type":["string"]}}}`,
		`properties["count"]: type: candidate does not allow "integer"`},
	{"multi-fault properties name first sorted", "input",
		`{"type":["object"],"properties":{"b":{"type":["integer"]},"a":{"type":["integer"]}}}`,
		`{"type":["object"],"properties":{"b":{"type":["string"]},"a":{"type":["string"]}}}`,
		`properties["a"]: type: candidate does not allow "integer"`},
	{"output extra property against closed target", "output",
		`{"type":["object"],"properties":{"a":{"type":["string"]}},"additionalProperties":false}`,
		`{"type":["object"],"properties":{"a":{"type":["string"]},"b":{"type":["string"]}}}`,
		`properties["b"]: target forbids additional properties`},
	{"output additionalProperties forbidden vs open", "output",
		`{"type":["object"],"additionalProperties":false}`, `{"type":["object"]}`,
		`additionalProperties: target forbids but candidate allows`},
	{"output additionalProperties schema vs absent", "output",
		`{"type":["object"],"additionalProperties":{"type":["string"]}}`, `{"type":["object"]}`,
		`additionalProperties: candidate is less restrictive than target`},

	// --- arrays ---
	{"items prefix", "input",
		`{"type":["array"],"items":{"type":["string"]}}`,
		`{"type":["array"],"items":{"type":["number"]}}`,
		`items: type: candidate does not allow "string"`},

	// --- numeric bounds ---
	{"input minimum narrower", "input",
		`{"type":["number"],"minimum":0}`, `{"type":["number"],"minimum":5}`,
		`minimum: candidate minimum 5 is greater than target minimum 0`},
	{"input exclusive minimum marked", "input",
		`{"type":["number"],"minimum":0}`, `{"type":["number"],"exclusiveMinimum":0}`,
		`minimum: candidate minimum exclusive 0 is greater than target minimum 0`},
	{"output minimum missing", "output",
		`{"type":["number"],"minimum":0}`, `{"type":["number"]}`,
		`minimum: target has minimum 0 but candidate has none`},
	{"output maximum wider", "output",
		`{"type":["number"],"maximum":10}`, `{"type":["number"],"maximum":20}`,
		`maximum: candidate maximum 20 is greater than target maximum 10`},
	{"bounds render as JCS numbers", "output",
		`{"type":["number"],"maximum":100000000}`, `{"type":["number"]}`,
		`maximum: target has maximum 100000000 but candidate has none`},
	{"fractional bound", "output",
		`{"type":["number"],"minimum":2.5}`, `{"type":["number"]}`,
		`minimum: target has minimum 2.5 but candidate has none`},

	// --- string/array bounds ---
	{"input minLength narrower", "input",
		`{"type":["string"],"minLength":1}`, `{"type":["string"],"minLength":2}`,
		`minLength: candidate minLength 2 is greater than target minLength 1`},
	{"output maxItems missing", "output",
		`{"type":["array"],"maxItems":5}`, `{"type":["array"]}`,
		`maxItems: target has maxItems 5 but candidate has none`},

	// --- member-name escaping (JCS) ---
	{"property name with quote and backslash escapes as JCS", "input",
		`{"type":["object"],"properties":{"wei\"rd\\name":{"type":["integer"]}}}`,
		`{"type":["object"],"properties":{"wei\"rd\\name":{"type":["string"]}}}`,
		`properties["wei\"rd\\name"]: type: candidate does not allow "integer"`},
	{"property name with control character escapes as JCS", "input",
		`{"type":["object"],"properties":{"bad\u0001name":{"type":["integer"]}}}`,
		`{"type":["object"],"properties":{"bad\u0001name":{"type":["string"]}}}`,
		`properties["bad\u0001name"]: type: candidate does not allow "integer"`},
	{"required name with quote escapes as JCS", "input",
		`{"type":["object"]}`,
		`{"type":["object"],"required":["say\"cheese"]}`,
		`required: candidate requires "say\"cheese" but target does not`},

	// --- unions ---
	{"input union real key and index", "input",
		`{"anyOf":[{"type":["string"]},{"type":["number"]}]}`,
		`{"anyOf":[{"type":["string"]}]}`,
		`anyOf: target variant 1 has no compatible candidate variant`},
	{"output union real key and index", "output",
		`{"oneOf":[{"type":["string"]}]}`,
		`{"oneOf":[{"type":["string"]},{"type":["boolean"]}]}`,
		`oneOf: candidate variant 1 has no compatible target variant`},
	{"candidate union target not", "input",
		`{"type":["string"]}`, `{"oneOf":[{"type":["string"]}]}`,
		`oneOf: target is not a union but candidate is`},
	{"target union candidate not", "output",
		`{"oneOf":[{"type":["string"]}]}`, `{"type":["number"]}`,
		`oneOf: candidate is not a union but target is`},
}

func TestReasonStringAlignment(t *testing.T) {
	for _, tc := range reasonCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			var tgt, cand map[string]any
			if err := json.Unmarshal([]byte(tc.target), &tgt); err != nil {
				t.Fatalf("target fixture: %v", err)
			}
			if err := json.Unmarshal([]byte(tc.candidate), &cand); err != nil {
				t.Fatalf("candidate fixture: %v", err)
			}

			var (
				ok     bool
				reason string
				err    error
			)
			switch tc.direction {
			case "input":
				ok, reason, err = InputCompatible(tgt, cand)
			case "output":
				ok, reason, err = OutputCompatible(tgt, cand)
			default:
				t.Fatalf("unknown direction %q", tc.direction)
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.reason == "" {
				if !ok {
					t.Fatalf("expected compatible, got reason %q", reason)
				}
				return
			}
			if ok {
				t.Fatalf("expected incompatible with reason %q, got compatible", tc.reason)
			}
			if reason != tc.reason {
				t.Fatalf("reason mismatch:\n  got:  %q\n  want: %q", reason, tc.reason)
			}
		})
	}
}

// Non-normalized-input refusal table: pins the EXACT error each directional
// check raises when handed a schema the Normalizer never emits (the inputs
// MUST be pre-normalized contract). Byte-identical with the TypeScript SDK's
// table in reasons.test.ts. The target is checked before the candidate;
// nested walks visit properties (sorted), additionalProperties, items, then
// oneOf/anyOf variants.
type refusalCase struct {
	name      string
	direction string // "input" or "output"
	target    string // schema JSON
	candidate string
	err       string // expected exact error message
}

var refusalCases = []refusalCase{
	{"scalar type target refused", "input",
		`{"type":"string"}`, `{"type":["string"]}`,
		`not normalized at target: keyword "type" must be an array`},
	{"scalar type candidate refused", "output",
		`{"type":["string"]}`, `{"type":"string"}`,
		`not normalized at candidate: keyword "type" must be an array`},
	{"unresolved nested $ref refused", "input",
		`{"type":["object"],"properties":{"a":{"$ref":"#/schemas/A"}}}`,
		`{"type":["object"]}`,
		`not normalized at target.properties["a"]: keyword "$ref" must be resolved`},
	{"unflattened allOf refused", "output",
		`{"type":["object"]}`, `{"allOf":[{"type":["object"]}]}`,
		`not normalized at candidate: keyword "allOf" must be flattened`},
}

func TestNotNormalizedRefusalAlignment(t *testing.T) {
	for _, tc := range refusalCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			var tgt, cand map[string]any
			if err := json.Unmarshal([]byte(tc.target), &tgt); err != nil {
				t.Fatalf("target fixture: %v", err)
			}
			if err := json.Unmarshal([]byte(tc.candidate), &cand); err != nil {
				t.Fatalf("candidate fixture: %v", err)
			}

			var (
				ok  bool
				err error
			)
			switch tc.direction {
			case "input":
				ok, _, err = InputCompatible(tgt, cand)
			case "output":
				ok, _, err = OutputCompatible(tgt, cand)
			default:
				t.Fatalf("unknown direction %q", tc.direction)
			}
			if err == nil {
				t.Fatalf("expected error %q, got none (ok=%v)", tc.err, ok)
			}
			var nne *NotNormalizedError
			if !errors.As(err, &nne) {
				t.Fatalf("expected *NotNormalizedError, got %T: %v", err, err)
			}
			if err.Error() != tc.err {
				t.Fatalf("error mismatch:\n  got:  %q\n  want: %q", err.Error(), tc.err)
			}
			if ok {
				t.Fatal("refusal must report not compatible")
			}
		})
	}
}
