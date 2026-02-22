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
	ok, err := n.InputCompatible(target, candidate)
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
	ok, err := n.OutputCompatible(target, candidate)
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
			ok, err = n.InputCompatible(c.Target, c.Candidate)
		case "output":
			ok, err = n.OutputCompatible(c.Target, c.Candidate)
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
