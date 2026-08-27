package openapi

import (
	"reflect"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

// The OAS style-examples table (openbindings.openapi-3.0@1 §8.2;
// openbindings.openapi-3.1@1 §8.2: serialization incorporated
// wholesale), exercised cell by cell with the OAS's own example values:
// primitive "blue", array [blue, black, brown], object {R:100, G:200,
// B:150}. One deliberate difference from the table's literal strings:
// JSON object members are unordered, so this implementation expands object
// members in sorted-key order (B, G, R) for determinism.
var (
	styleTablePrimitive = "blue"
	styleTableArray     = []any{"blue", "black", "brown"}
	styleTableObject    = map[string]any{"R": float64(100), "G": float64(200), "B": float64(150)}
)

func TestSerializePathValue_OASStyleTable(t *testing.T) {
	cases := []struct {
		style   string
		explode bool
		value   any
		want    string
	}{
		{"simple", false, styleTablePrimitive, "blue"},
		{"simple", true, styleTablePrimitive, "blue"},
		{"simple", false, styleTableArray, "blue,black,brown"},
		{"simple", true, styleTableArray, "blue,black,brown"},
		{"simple", false, styleTableObject, "B,150,G,200,R,100"},
		{"simple", true, styleTableObject, "B=150,G=200,R=100"},

		{"label", false, styleTablePrimitive, ".blue"},
		{"label", true, styleTablePrimitive, ".blue"},
		{"label", false, styleTableArray, ".blue,black,brown"},
		{"label", true, styleTableArray, ".blue.black.brown"},
		{"label", false, styleTableObject, ".B,150,G,200,R,100"},
		{"label", true, styleTableObject, ".B=150.G=200.R=100"},

		{"matrix", false, styleTablePrimitive, ";color=blue"},
		{"matrix", true, styleTablePrimitive, ";color=blue"},
		{"matrix", false, styleTableArray, ";color=blue,black,brown"},
		{"matrix", true, styleTableArray, ";color=blue;color=black;color=brown"},
		{"matrix", false, styleTableObject, ";color=B,150,G,200,R,100"},
		{"matrix", true, styleTableObject, ";B=150;G=200;R=100"},

		// Empty-value cells the OAS table defines.
		{"matrix", false, "", ";color"},
		{"label", false, "", "."},
	}
	for _, tc := range cases {
		got, err := serializePathValueForRevision("color", tc.value, tc.style, tc.explode, BindingSpec)
		if err != nil {
			t.Errorf("%s/explode=%v (%T): unexpected error %v", tc.style, tc.explode, tc.value, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s/explode=%v (%T) = %q, want %q", tc.style, tc.explode, tc.value, got, tc.want)
		}
	}
}

func TestSerializeQueryValue_OASStyleTable(t *testing.T) {
	cases := []struct {
		style   string
		explode bool
		value   any
		want    []string
	}{
		{"form", false, styleTablePrimitive, []string{"color=blue"}},
		{"form", true, styleTablePrimitive, []string{"color=blue"}},
		{"form", false, styleTableArray, []string{"color=blue,black,brown"}},
		{"form", true, styleTableArray, []string{"color=blue", "color=black", "color=brown"}},
		{"form", false, styleTableObject, []string{"color=B,150,G,200,R,100"}},
		{"form", true, styleTableObject, []string{"B=150", "G=200", "R=100"}},

		{"spaceDelimited", false, styleTableArray, []string{"color=blue%20black%20brown"}},
		{"spaceDelimited", false, styleTableObject, []string{"color=B%20150%20G%20200%20R%20100"}},
		{"pipeDelimited", false, styleTableArray, []string{"color=blue|black|brown"}},
		{"pipeDelimited", false, styleTableObject, []string{"color=B|150|G|200|R|100"}},

		{"deepObject", true, styleTableObject, []string{"color[B]=150", "color[G]=200", "color[R]=100"}},

		// The empty-string cell for form.
		{"form", false, "", []string{"color="}},
	}
	for _, tc := range cases {
		got, err := serializeQueryValueForRevision("color", tc.value, tc.style, tc.explode, false, BindingSpec, false)
		if err != nil {
			t.Errorf("%s/explode=%v (%T): unexpected error %v", tc.style, tc.explode, tc.value, err)
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s/explode=%v (%T) = %v, want %v", tc.style, tc.explode, tc.value, got, tc.want)
		}
	}
}

// Undefined table cells refuse loudly rather than inventing a serialization.
func TestSerializeQueryValue_UndefinedCellsRefuse(t *testing.T) {
	if _, err := serializeQueryValueForRevision("color", "blue", "spaceDelimited", false, false, BindingSpec, false); err == nil {
		t.Error("spaceDelimited with a primitive must refuse")
	}
	if _, err := serializeQueryValueForRevision("color", "blue", "pipeDelimited", false, false, BindingSpec, false); err == nil {
		t.Error("pipeDelimited with a primitive must refuse")
	}
	if _, err := serializeQueryValueForRevision("color", styleTableArray, "deepObject", true, false, BindingSpec, false); err == nil {
		t.Error("deepObject with a non-object must refuse")
	}
	if _, err := serializePathValueForRevision("id", "x", "form", false, BindingSpec); err == nil {
		t.Error("form style on a path parameter must refuse")
	}
	// Nested non-primitives inside an expansion have no OAS-defined form.
	if _, err := serializeQueryValueForRevision("f", []any{map[string]any{"x": 1}}, "form", false, false, BindingSpec, false); err == nil {
		t.Error("nested object inside an array expansion must refuse")
	}
}

func TestSerializeHeaderValue_Simple(t *testing.T) {
	got, err := serializeHeaderValue([]any{float64(3), float64(4)}, "simple", false)
	if err != nil || got != "3,4" {
		t.Errorf("header simple array = (%q, %v), want 3,4", got, err)
	}
	got, err = serializeHeaderValue(styleTableObject, "simple", true)
	if err != nil || got != "B=150,G=200,R=100" {
		t.Errorf("header simple exploded object = (%q, %v)", got, err)
	}
	// Header values are not URL components: no percent-encoding.
	got, err = serializeHeaderValue("a b/c", "simple", false)
	if err != nil || got != "a b/c" {
		t.Errorf("header value must not be percent-encoded, got (%q, %v)", got, err)
	}
}

// allowReserved lets RFC 3986 reserved characters pass unescaped in query
// values (openbindings.openapi-3.0@1 §8.2;
// openbindings.openapi-3.1@1 §8.2); names stay escaped.
func TestSerializeQueryValue_AllowReserved(t *testing.T) {
	got, err := serializeQueryValueForRevision("path", "a/b?c=d", "form", true, false, BindingSpec, false)
	if err != nil || got[0] != "path=a%2Fb%3Fc%3Dd" {
		t.Errorf("escaped = (%v, %v), want path=a%%2Fb%%3Fc%%3Dd", got, err)
	}
	got, err = serializeQueryValueForRevision("path", "a/b?c=d", "form", true, true, BindingSpec, false)
	if err != nil || got[0] != "path=a/b?c=d" {
		t.Errorf("allowReserved = (%v, %v), want path=a/b?c=d", got, err)
	}
}

// Primitive wire forms are defined (fmt.Sprintf("%v") is not serialization):
// numbers render canonically, booleans as true/false, null as empty.
func TestPrimitiveString(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{float64(1), "1"},
		{float64(1.5), "1.5"},
		{true, "true"},
		{false, "false"},
		{nil, ""},
		{"s", "s"},
	}
	for _, tc := range cases {
		got, err := primitiveString(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("primitiveString(%v) = (%q, %v), want %q", tc.in, got, err, tc.want)
		}
	}
	if _, err := primitiveString(map[string]any{}); err == nil {
		t.Error("objects are not primitives")
	}
}

// A content-form parameter serializes per its declared media type and rides
// its location as that string (openbindings.openapi-3.0@1 §8.3;
// openbindings.openapi-3.1@1 §8.3).
func TestRouteParameter_ContentFormParam(t *testing.T) {
	p := &openapi3.Parameter{
		Name: "filter",
		In:   "query",
		Content: openapi3.Content{
			"application/json": &openapi3.MediaType{},
		},
	}
	r := &routedInput{
		resolvedPath: "/x",
		bodyFields:   map[string]any{},
		populated:    map[string]map[string]bool{"header": {}, "query": {}, "cookie": {}},
	}
	if err := routeParameterFor(r, p, map[string]any{"a": float64(1)}, BindingSpec); err != nil {
		t.Fatalf("routeParameter: %v", err)
	}
	if len(r.queryUnits) != 1 || r.queryUnits[0] != "filter=%7B%22a%22%3A1%7D" {
		t.Errorf("queryUnits = %v, want the JSON-serialized value percent-encoded", r.queryUnits)
	}

	// An undefined content media type refuses loudly.
	p2 := &openapi3.Parameter{
		Name:    "blob",
		In:      "query",
		Content: openapi3.Content{"application/octet-stream": &openapi3.MediaType{}},
	}
	if err := routeParameterFor(r, p2, "x", BindingSpec); err == nil {
		t.Error("content parameter with an undefined media type must refuse")
	}
}

// Two declared parameters sharing one name across DIFFERENT locations make
// the operation unflattenable (openbindings.openapi-3.0@1 §7;
// openbindings.openapi-3.1@1 §7).
func TestUnflattenableParam(t *testing.T) {
	params := openapi3.Parameters{
		{Value: &openapi3.Parameter{Name: "id", In: "path", Required: true}},
		{Value: &openapi3.Parameter{Name: "id", In: "query"}},
	}
	if got := unflattenableParam(params); got != "id" {
		t.Errorf("unflattenableParam = %q, want id", got)
	}
	// Same name+location (the OAS forbids it; the merge dedupes) and
	// distinct names stay flattenable.
	ok := openapi3.Parameters{
		{Value: &openapi3.Parameter{Name: "id", In: "path", Required: true}},
		{Value: &openapi3.Parameter{Name: "verbose", In: "query"}},
	}
	if got := unflattenableParam(ok); got != "" {
		t.Errorf("unflattenableParam = %q, want none", got)
	}
}

// The OAS voids header parameter declarations named Accept, Content-Type,
// or Authorization; the effective set drops them.
func TestEffectiveParameters_DropsSpecialHeaders(t *testing.T) {
	pathItem := &openapi3.PathItem{}
	op := &openapi3.Operation{
		Parameters: openapi3.Parameters{
			{Value: &openapi3.Parameter{Name: "Authorization", In: "header"}},
			{Value: &openapi3.Parameter{Name: "accept", In: "header"}},
			{Value: &openapi3.Parameter{Name: "X-Custom", In: "header"}},
			{Value: &openapi3.Parameter{Name: "Authorization", In: "query"}}, // query is not special
		},
	}
	params := effectiveParameters(pathItem, op)
	if len(params) != 2 {
		t.Fatalf("effectiveParameters kept %d params, want 2 (X-Custom header + Authorization query)", len(params))
	}
}

// Synthetic-body routing: with a non-object body schema, the `body` field is
// the request body; other unmatched fields have nowhere to ride and refuse.
func TestRouteInput_SyntheticBody(t *testing.T) {
	plan := &bodyPlan{declared: true, family: familyJSON, synthetic: true}
	routed, err := routeInputFor(nil, map[string]any{"body": []any{float64(1), float64(2)}}, "/x", plan, BindingSpec)
	if err != nil {
		t.Fatalf("routeInput: %v", err)
	}
	if !routed.bodySet {
		t.Fatal("synthetic body not captured")
	}
	if _, err := routeInputFor(nil, map[string]any{"stray": 1}, "/x", plan, BindingSpec); err == nil {
		t.Error("a non-body field on a synthetic-body operation must refuse")
	}
}
