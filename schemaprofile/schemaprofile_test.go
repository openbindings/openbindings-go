package schemaprofile

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestNormalize_FailsClosedOnOutOfProfileKeyword(t *testing.T) {
	n := &Normalizer{Root: map[string]any{}}
	_, err := n.Normalize(map[string]any{"type": "string", "pattern": "^[a-z]+$"})
	var ope *OutsideProfileError
	if err == nil || !errors.As(err, &ope) {
		t.Fatalf("expected OutsideProfileError, got %v", err)
	}
	if ope.Keyword != "pattern" {
		t.Fatalf("expected keyword pattern, got %q", ope.Keyword)
	}
}

func TestNormalize_UnionOrderingDeterministic(t *testing.T) {
	n := &Normalizer{Root: map[string]any{}}
	in := map[string]any{
		"oneOf": []any{
			map[string]any{"type": "object", "properties": map[string]any{"b": map[string]any{"type": "string"}}},
			map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}}},
		},
	}
	out, err := n.Normalize(in)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	arr, ok := out["oneOf"].([]any)
	if !ok || len(arr) != 2 {
		t.Fatalf("expected oneOf array, got %#v", out["oneOf"])
	}
	first := arr[0].(map[string]any)
	props := first["properties"].(map[string]any)
	if _, hasA := props["a"]; !hasA {
		t.Fatalf("expected variant with property a to sort first, got %#v", arr[0])
	}
}

func TestNormalize_RefResolutionAgainstRoot(t *testing.T) {
	root := map[string]any{
		"schemas": map[string]any{
			"Thing": map[string]any{"type": "object", "required": []any{"x"}, "properties": map[string]any{"x": map[string]any{"type": "string"}}},
		},
	}
	n := &Normalizer{Root: root}
	out, err := n.Normalize(map[string]any{"$ref": "#/schemas/Thing"})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if _, ok := out["$ref"]; ok {
		t.Fatalf("expected $ref to be inlined")
	}
	if _, ok := out["properties"]; !ok {
		t.Fatalf("expected resolved schema object, got %#v", out)
	}
}

type fetcherFunc func(u *url.URL) ([]byte, error)

func (f fetcherFunc) Fetch(u *url.URL) ([]byte, error) {
	return f(u)
}

func TestNormalize_RefResolutionExternalWithoutFetcher(t *testing.T) {
	n := &Normalizer{Root: map[string]any{}}
	_, err := n.Normalize(map[string]any{"$ref": "https://example.com/schema.json#/schemas/Foo"})
	var re *RefError
	if err == nil || !errors.As(err, &re) {
		t.Fatalf("expected RefError, got %v", err)
	}
	if !strings.Contains(re.Err.Error(), "external $ref unsupported") {
		t.Fatalf("expected external $ref unsupported, got %v", re.Err)
	}
}

func TestNormalize_RefResolutionRelativeWithoutBase(t *testing.T) {
	n := &Normalizer{Root: map[string]any{}}
	_, err := n.Normalize(map[string]any{"$ref": "schemas.json#/schemas/Foo"})
	var re *RefError
	if err == nil || !errors.As(err, &re) {
		t.Fatalf("expected RefError, got %v", err)
	}
	if !strings.Contains(re.Err.Error(), "relative $ref with no base") {
		t.Fatalf("expected relative $ref with no base, got %v", re.Err)
	}
}

func TestNormalize_RefResolutionExternalWithFetcher(t *testing.T) {
	n := &Normalizer{
		Root: map[string]any{},
		Fetch: fetcherFunc(func(u *url.URL) ([]byte, error) {
			if u.String() != "https://example.com/schema.json#/schemas/Foo" {
				return nil, errors.New("unexpected URL")
			}
			return []byte(`{"schemas":{"Foo":{"type":"string"}}}`), nil
		}),
	}
	out, err := n.Normalize(map[string]any{"$ref": "https://example.com/schema.json#/schemas/Foo"})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	types, ok := out["type"].([]any)
	if !ok || len(types) != 1 || types[0] != "string" {
		t.Fatalf("expected resolved type string, got %#v", out)
	}
}

func TestNormalize_RefResolutionRelativeWithBase(t *testing.T) {
	base, err := url.Parse("https://example.com/base/")
	if err != nil {
		t.Fatalf("parse base: %v", err)
	}
	n := &Normalizer{
		Root: map[string]any{},
		Base: base,
		Fetch: fetcherFunc(func(u *url.URL) ([]byte, error) {
			if u.String() != "https://example.com/base/schemas.json#/schemas/Foo" {
				return nil, errors.New("unexpected URL")
			}
			return []byte(`{"schemas":{"Foo":{"type":"number"}}}`), nil
		}),
	}
	out, err := n.Normalize(map[string]any{"$ref": "schemas.json#/schemas/Foo"})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	types, ok := out["type"].([]any)
	if !ok || len(types) != 1 || types[0] != "number" {
		t.Fatalf("expected resolved type number, got %#v", out)
	}
}

func TestInputCompatible_EmptyTargetRequiresUnconstrainedCandidate(t *testing.T) {
	n := &Normalizer{Root: map[string]any{}}
	// Empty target ({}) is Top — the interface may send any value.
	// A constrained candidate cannot handle the full domain, so it is incompatible.
	candidate := map[string]any{
		"type": "object",
		"required": []any{"id"},
		"properties": map[string]any{
			"id": map[string]any{"type": "string"},
		},
	}
	ok, _, err := n.InputCompatible(map[string]any{}, candidate)
	if err != nil {
		t.Fatalf("input compatible: %v", err)
	}
	if ok {
		t.Fatalf("expected incompatible: constrained candidate cannot handle Top (empty) target")
	}
}

func TestInputCompatible_BasicObjectAndAdditionalProperties(t *testing.T) {
	n := &Normalizer{Root: map[string]any{}}
	target := map[string]any{
		"type": "object",
		"required": []any{
			"id",
		},
		"properties": map[string]any{
			"id": map[string]any{"type": "string"},
		},
		"additionalProperties": false,
	}
	// Candidate accepts more fields (input compatible).
	candidate := map[string]any{
		"type": "object",
		"required": []any{
			"id",
		},
		"properties": map[string]any{
			"id":    map[string]any{"type": "string"},
			"extra": map[string]any{"type": "string"},
		},
		// input profile: additionalProperties does not restrict; omit it entirely
	}
	ok, _, err := n.InputCompatible(target, candidate)
	if err != nil {
		t.Fatalf("input compatible: %v", err)
	}
	if !ok {
		t.Fatalf("expected compatible")
	}
}

func TestOutputCompatible_AdditionalPropertiesFalseDisallowsExtraProps(t *testing.T) {
	n := &Normalizer{Root: map[string]any{}}
	target := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"id": map[string]any{"type": "string"},
		},
	}
	candidate := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":    map[string]any{"type": "string"},
			"extra": map[string]any{"type": "string"},
		},
	}
	ok, _, err := n.OutputCompatible(target, candidate)
	if err != nil {
		t.Fatalf("output compatible: %v", err)
	}
	if ok {
		t.Fatalf("expected incompatible (extra prop with additionalProperties=false)")
	}
}

type profileCaseFile struct {
	Cases []profileCase `json:"cases"`
}

type profileCase struct {
	Name       string         `json:"name"`
	Direction  string         `json:"direction"`
	Target     map[string]any `json:"target"`
	Candidate  map[string]any `json:"candidate"`
	Compatible *bool          `json:"compatible,omitempty"`
	Error      string         `json:"error,omitempty"`
}

func TestNumericBounds_ExclusiveVsInclusiveAtSameBoundary(t *testing.T) {
	n := &Normalizer{Root: map[string]any{}}

	// Input direction: target has exclusiveMinimum:5, candidate has minimum:5.
	// Target accepts (5, ...), candidate accepts [5, ...).
	// For input compat, cand's lower bound must be <= tgt's lower bound.
	// cand lower = 5 inclusive, tgt lower = 5 exclusive.
	// 5-inclusive < 5-exclusive (inclusive is less restrictive), so cand accepts more → compatible.
	target := map[string]any{"type": "number", "exclusiveMinimum": float64(5)}
	candidate := map[string]any{"type": "number", "minimum": float64(5)}
	ok, _, err := n.InputCompatible(target, candidate)
	if err != nil {
		t.Fatalf("input compatible: %v", err)
	}
	if !ok {
		t.Fatalf("expected compatible: candidate minimum:5 accepts at least as much as target exclusiveMinimum:5")
	}

	// Reverse: target has minimum:5, candidate has exclusiveMinimum:5.
	// Target accepts [5, ...), candidate accepts (5, ...).
	// cand lower = 5 exclusive > tgt lower = 5 inclusive → cand is stricter → incompatible.
	target2 := map[string]any{"type": "number", "minimum": float64(5)}
	candidate2 := map[string]any{"type": "number", "exclusiveMinimum": float64(5)}
	ok2, _, err := n.InputCompatible(target2, candidate2)
	if err != nil {
		t.Fatalf("input compatible: %v", err)
	}
	if ok2 {
		t.Fatalf("expected incompatible: candidate exclusiveMinimum:5 is stricter than target minimum:5")
	}
}

func TestNumericBounds_ExclusiveBothSides(t *testing.T) {
	n := &Normalizer{Root: map[string]any{}}

	// Output direction: target has exclusiveMinimum:0, exclusiveMaximum:10.
	// Candidate has exclusiveMinimum:0, exclusiveMaximum:10.
	// Same bounds → compatible.
	target := map[string]any{"type": "number", "exclusiveMinimum": float64(0), "exclusiveMaximum": float64(10)}
	candidate := map[string]any{"type": "number", "exclusiveMinimum": float64(0), "exclusiveMaximum": float64(10)}
	ok, _, err := n.OutputCompatible(target, candidate)
	if err != nil {
		t.Fatalf("output compatible: %v", err)
	}
	if !ok {
		t.Fatalf("expected compatible: same exclusive bounds")
	}

	// Candidate with wider exclusive bounds → incompatible for output (may return values outside target range).
	candidate2 := map[string]any{"type": "number", "exclusiveMinimum": float64(-1), "exclusiveMaximum": float64(11)}
	ok2, _, err := n.OutputCompatible(target, candidate2)
	if err != nil {
		t.Fatalf("output compatible: %v", err)
	}
	if ok2 {
		t.Fatalf("expected incompatible: candidate has wider bounds than target for output")
	}

	// Input direction: candidate with wider bounds → compatible (accepts more).
	ok3, _, err := n.InputCompatible(target, candidate2)
	if err != nil {
		t.Fatalf("input compatible: %v", err)
	}
	if !ok3 {
		t.Fatalf("expected compatible: candidate has wider bounds for input")
	}
}

func TestAllOf_ContradictoryTypeConstraints(t *testing.T) {
	n := &Normalizer{Root: map[string]any{}}

	// allOf with one branch type:string and another type:integer → empty type intersection → should error.
	schema := map[string]any{
		"allOf": []any{
			map[string]any{"type": "string"},
			map[string]any{"type": "integer"},
		},
	}
	_, err := n.Normalize(schema)
	if err == nil {
		t.Fatalf("expected error for contradictory type constraints in allOf")
	}
	var se *SchemaError
	if !errors.As(err, &se) {
		t.Fatalf("expected SchemaError, got %T: %v", err, err)
	}
}

func TestAllOf_ContradictoryEnumConstraints(t *testing.T) {
	n := &Normalizer{Root: map[string]any{}}

	// allOf with enum intersection that is empty → should error.
	schema := map[string]any{
		"allOf": []any{
			map[string]any{"type": "string", "enum": []any{"a", "b"}},
			map[string]any{"type": "string", "enum": []any{"c", "d"}},
		},
	}
	_, err := n.Normalize(schema)
	if err == nil {
		t.Fatalf("expected error for contradictory enum constraints in allOf")
	}
	var se *SchemaError
	if !errors.As(err, &se) {
		t.Fatalf("expected SchemaError, got %T: %v", err, err)
	}
}

func TestOneOf_InputDirection_CandidateHasMoreVariants(t *testing.T) {
	n := &Normalizer{Root: map[string]any{}}

	// Input direction: target has 2 variants, candidate has 3.
	// For input compat, every variant in target must have a matching variant in candidate.
	// Since candidate has a superset of variants, it should be compatible.
	target := map[string]any{
		"oneOf": []any{
			map[string]any{"type": "string"},
			map[string]any{"type": "integer"},
		},
	}
	candidate := map[string]any{
		"oneOf": []any{
			map[string]any{"type": "string"},
			map[string]any{"type": "integer"},
			map[string]any{"type": "number"},
		},
	}
	ok, _, err := n.InputCompatible(target, candidate)
	if err != nil {
		t.Fatalf("input compatible: %v", err)
	}
	if !ok {
		t.Fatalf("expected compatible: candidate has superset of target variants for input")
	}
}

func TestOneOf_OutputDirection_CandidateHasMoreVariants(t *testing.T) {
	n := &Normalizer{Root: map[string]any{}}

	// Output direction: candidate has 3 variants, target has 2.
	// For output compat, every variant in candidate must have a matching variant in target.
	// The extra candidate variant (boolean) has no match in target → incompatible.
	target := map[string]any{
		"oneOf": []any{
			map[string]any{"type": "string"},
			map[string]any{"type": "integer"},
		},
	}
	candidate := map[string]any{
		"oneOf": []any{
			map[string]any{"type": "string"},
			map[string]any{"type": "integer"},
			map[string]any{"type": "boolean"},
		},
	}
	ok, _, err := n.OutputCompatible(target, candidate)
	if err != nil {
		t.Fatalf("output compatible: %v", err)
	}
	if ok {
		t.Fatalf("expected incompatible: candidate has extra variant (boolean) not in target for output")
	}
}

func TestProfileV01_GoldenCases(t *testing.T) {
	data, err := os.ReadFile("testdata/profile_v01_cases.json")
	if err != nil {
		t.Fatalf("read cases: %v", err)
	}
	var f profileCaseFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("unmarshal cases: %v", err)
	}
	if len(f.Cases) == 0 {
		t.Fatalf("no cases")
	}

	n := &Normalizer{Root: map[string]any{}}
	for _, c := range f.Cases {
		if c.Name == "" {
			t.Fatalf("case missing name")
		}
		var (
			ok  bool
			err error
		)
		switch c.Direction {
		case "input":
			ok, _, err = n.InputCompatible(c.Target, c.Candidate)
		case "output":
			ok, _, err = n.OutputCompatible(c.Target, c.Candidate)
		default:
			t.Fatalf("case %q: unknown direction %q", c.Name, c.Direction)
		}

		if c.Error != "" {
			if err == nil {
				t.Fatalf("case %q: expected error %q", c.Name, c.Error)
			}
			if c.Error == "outside_profile" {
				var ope *OutsideProfileError
				if !errors.As(err, &ope) {
					t.Fatalf("case %q: expected OutsideProfileError, got %v", c.Name, err)
				}
			}
			continue
		}

		if err != nil {
			t.Fatalf("case %q: unexpected error: %v", c.Name, err)
		}
		if c.Compatible == nil {
			t.Fatalf("case %q: missing compatible", c.Name)
		}
		if ok != *c.Compatible {
			t.Fatalf("case %q: expected compatible=%v, got %v", c.Name, *c.Compatible, ok)
		}
	}
}

func TestNormalize_StripsFormatKeyword(t *testing.T) {
	n := &Normalizer{Root: map[string]any{}}
	out, err := n.Normalize(map[string]any{"type": "string", "format": "email"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, has := out["format"]; has {
		t.Fatal("expected format to be stripped")
	}
}

func TestNormalize_StripsDiscriminatorKeyword(t *testing.T) {
	n := &Normalizer{Root: map[string]any{}}
	out, err := n.Normalize(map[string]any{
		"oneOf": []any{
			map[string]any{"type": "object", "properties": map[string]any{"kind": map[string]any{"const": "a"}}, "required": []any{"kind"}},
			map[string]any{"type": "object", "properties": map[string]any{"kind": map[string]any{"const": "b"}}, "required": []any{"kind"}},
		},
		"discriminator": map[string]any{"propertyName": "kind"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, has := out["discriminator"]; has {
		t.Fatal("expected discriminator to be stripped")
	}
}

func TestNormalize_StripsExtensionKeywords(t *testing.T) {
	n := &Normalizer{Root: map[string]any{}}
	out, err := n.Normalize(map[string]any{
		"type":     "string",
		"x-ob":     map[string]any{"delegate": "ob"},
		"x-custom": true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, has := out["x-ob"]; has {
		t.Fatal("expected x-ob to be stripped")
	}
	if _, has := out["x-custom"]; has {
		t.Fatal("expected x-custom to be stripped")
	}
}

func TestNormalize_NullableConvertsToTypeUnion(t *testing.T) {
	n := &Normalizer{Root: map[string]any{}}
	out, err := n.Normalize(map[string]any{"type": "string", "nullable": true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, has := out["nullable"]; has {
		t.Fatal("expected nullable to be removed")
	}
	types, ok := out["type"].([]any)
	if !ok {
		t.Fatalf("expected type to be array, got %T", out["type"])
	}
	hasNull := false
	hasString := false
	for _, v := range types {
		if s, ok := v.(string); ok {
			if s == "null" {
				hasNull = true
			}
			if s == "string" {
				hasString = true
			}
		}
	}
	if !hasNull || !hasString {
		t.Fatalf("expected [null, string], got %v", types)
	}
}

func TestNormalize_NullableFalseStrippedWithoutChangingType(t *testing.T) {
	n := &Normalizer{Root: map[string]any{}}
	out, err := n.Normalize(map[string]any{"type": "string", "nullable": false})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, has := out["nullable"]; has {
		t.Fatal("expected nullable to be removed")
	}
	types, ok := out["type"].([]any)
	if !ok {
		t.Fatalf("expected type to be array, got %T", out["type"])
	}
	if len(types) != 1 || types[0] != "string" {
		t.Fatalf("expected [string], got %v", types)
	}
}

func TestNormalize_NullableInAllOfBranch(t *testing.T) {
	n := &Normalizer{Root: map[string]any{}}
	out, err := n.Normalize(map[string]any{
		"allOf": []any{
			map[string]any{"type": "string", "nullable": true},
			map[string]any{"minLength": json.Number("1")},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	types, ok := out["type"].([]any)
	if !ok {
		t.Fatalf("expected type to be array, got %T", out["type"])
	}
	hasNull := false
	hasString := false
	for _, v := range types {
		if s, ok := v.(string); ok {
			if s == "null" {
				hasNull = true
			}
			if s == "string" {
				hasString = true
			}
		}
	}
	if !hasNull || !hasString {
		t.Fatalf("expected type array with null and string, got %v", types)
	}
}
