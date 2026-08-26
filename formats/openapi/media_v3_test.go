package openapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/openbindings/openbindings-go/invoke"
	"github.com/openbindings/openbindings-go/synthesize"

	"github.com/getkin/kin-openapi/openapi3"

	openbindings "github.com/openbindings/openbindings-go"
)

func TestBindingSpecRegistryWarrantsOnlyImplementedFamilies(t *testing.T) {
	got := NewInvoker().BindingSpecs()
	want := []string{BindingSpecOpenAPI30, BindingSpecOpenAPI31}
	var ids []string
	for _, info := range got {
		ids = append(ids, info.BindingSpec)
	}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("BindingSpecs = %v, want %v", ids, want)
	}
	verdicts := NewInvoker().CheckBindingSpecs([]string{
		BindingSpecOpenAPI20, BindingSpecOpenAPI30, BindingSpecOpenAPI31, BindingSpecOpenAPI32,
	})
	if got := []bool{verdicts[0].Supported, verdicts[1].Supported, verdicts[2].Supported, verdicts[3].Supported}; !reflect.DeepEqual(got, []bool{false, true, true, false}) {
		t.Fatalf("support verdicts = %v", got)
	}
}

func TestInputTransformContainsNoPrivateMarker(t *testing.T) {
	params := openapi3.Parameters{&openapi3.ParameterRef{Value: &openapi3.Parameter{Name: "id", In: "query"}}}
	routes := abstractInputRoutes{
		parameters:     []abstractParameterRoute{{In: "query", Name: "id", Field: "id"}},
		wholeBodyField: "body",
	}
	expression := routes.transformExpression(params)
	if strings.Contains(expression, "$openbindings") || strings.Contains(expression, BindingSpecOpenAPI31) {
		t.Fatalf("transform leaked adapter-private material: %s", expression)
	}
}

func TestRevision3RawOctetPlanningAndBytes(t *testing.T) {
	binary := &openapi3.SchemaRef{Value: &openapi3.Schema{
		Type:   &openapi3.Types{"string"},
		Format: "binary",
	}}
	encoded := &openapi3.SchemaRef{Value: &openapi3.Schema{
		Type:       &openapi3.Types{"string"},
		Extensions: map[string]any{"contentEncoding": "base64"},
	}}
	tests := []struct {
		name        string
		doc         *openapi3.T
		media       *openapi3.MediaType
		value       string
		want        string
		rawBoundary bool
	}{
		{
			name: "oas30 binary schema decodes boundary base64",
			doc:  &openapi3.T{OpenAPI: "3.0.3"}, media: &openapi3.MediaType{Schema: binary},
			value: base64.StdEncoding.EncodeToString([]byte{0, 255, 1, 2}), want: string([]byte{0, 255, 1, 2}), rawBoundary: true,
		},
		{
			name: "oas31 omitted schema decodes boundary base64",
			doc:  &openapi3.T{OpenAPI: "3.1.0"}, media: &openapi3.MediaType{},
			value: base64.StdEncoding.EncodeToString([]byte("raw-image")), want: "raw-image", rawBoundary: true,
		},
		{
			name: "oas31 contentEncoding application string rides unchanged",
			doc:  &openapi3.T{OpenAPI: "3.1.0"}, media: &openapi3.MediaType{Schema: encoded},
			value: "cmF3LWltYWdl", want: "cmF3LWltYWdl", rawBoundary: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			op := opWithRequestBody(openapi3.Content{"image/png": tt.media}, true)
			plans, err := planRequestBodiesFor(tt.doc, op, BindingSpec)
			if err != nil || len(plans) != 1 {
				t.Fatalf("planRequestBodiesFor = %#v, %v", plans, err)
			}
			plan := plans[0]
			if plan.family != familyOctets || !plan.synthetic || plan.rawBoundary != tt.rawBoundary {
				t.Fatalf("raw plan = %#v", plan)
			}
			body, contentType, err := buildRequestBody(tt.doc, plan, &routedInput{bodySet: true, bodyValue: tt.value})
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(body)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want || contentType != "image/png" {
				t.Fatalf("wire = (%q, %q), want (%q, image/png)", got, contentType, tt.want)
			}
		})
	}
}

func TestRevision3RawBoundaryRejectsInvalidBase64(t *testing.T) {
	doc := &openapi3.T{OpenAPI: "3.0.3"}
	op := opWithRequestBody(openapi3.Content{"image/png": {Schema: &openapi3.SchemaRef{Value: &openapi3.Schema{
		Type: &openapi3.Types{"string"}, Format: "binary",
	}}}}, true)
	plans, err := planRequestBodiesFor(doc, op, BindingSpec)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"%%%", "YQ", "AB==", "YQ==\n"} {
		if _, _, err := buildRequestBody(doc, plans[0], &routedInput{bodySet: true, bodyValue: value}); err == nil || !strings.Contains(err.Error(), "invalid base64") {
			t.Errorf("non-canonical Base64 %q error = %v", value, err)
		}
	}
}

func TestRevision3MediaParameterSemantics(t *testing.T) {
	declared, err := parseMediaDeclaration(`application/json; charset=UTF-8; profile="Alpha"`)
	if err != nil {
		t.Fatal(err)
	}
	matching, err := parseRevision3MediaType(`Application/JSON; profile=Alpha; charset=utf-8; extra=x`)
	if err != nil {
		t.Fatal(err)
	}
	if !requestMediaDeclarationMatches(declared, matching) {
		t.Fatal("charset case and equivalent quoted-string spelling should match")
	}
	nonmatching, err := parseRevision3MediaType(`application/json; charset=utf-8; profile=alpha`)
	if err != nil {
		t.Fatal(err)
	}
	if requestMediaDeclarationMatches(declared, nonmatching) {
		t.Fatal("unknown parameter values must remain byte-sensitive")
	}

	media := func() *openapi3.MediaType {
		return &openapi3.MediaType{Schema: &openapi3.SchemaRef{Value: &openapi3.Schema{Type: &openapi3.Types{"object"}}}}
	}
	// A content map whose ONLY entries collide has no usable alternative left
	// once the collision confines to its parsed identity, so the body refuses.
	op := opWithRequestBody(openapi3.Content{
		"application/json; charset=UTF-8": media(),
		"Application/JSON;charset=utf-8":  media(),
	}, true)
	if _, err := planRequestBodiesFor(&openapi3.T{OpenAPI: "3.1.0"}, op, BindingSpec); err == nil || !strings.Contains(err.Error(), "normalized-colliding") {
		t.Fatalf("charset-semantic collision error = %v", err)
	}

	// The same collision beside a non-colliding sibling confines: the sibling
	// stays a usable candidate and the colliding identity contributes none.
	confined := opWithRequestBody(openapi3.Content{
		"application/json; charset=UTF-8": media(),
		"Application/JSON;charset=utf-8":  media(),
		"application/vnd.safe+json":       media(),
	}, true)
	plans, err := planRequestBodiesFor(&openapi3.T{OpenAPI: "3.1.0"}, confined, BindingSpec)
	if err != nil {
		t.Fatalf("confined collision must not poison the sibling: %v", err)
	}
	if len(plans) != 1 || plans[0].mediaKey != "application/vnd.safe+json" {
		t.Fatalf("confined candidate set = %#v", plans)
	}
}

func TestRevision3MediaRangeDetectionIsStructural(t *testing.T) {
	parsed, err := parseMediaDeclaration("application/vnd.foo*bar")
	if err != nil || parsed.rangeSpecificity != 2 || parsed.base != "application/vnd.foo*bar" {
		t.Fatalf("exact subtype containing tchar '*' = %#v, %v", parsed, err)
	}
	if !isMediaRange("application/*") || !isMediaRange("*/*") || isMediaRange("application/vnd.foo*bar") {
		t.Fatal("media range detection is not structural")
	}
	if _, err := parseMediaDeclaration("*/json"); err == nil {
		t.Fatal("wildcard type outside */* was accepted")
	}

	op := opWithRequestBody(openapi3.Content{"application/vnd.foo*bar": emptyMedia()}, true)
	plans, err := planRequestBodiesFor(&openapi3.T{OpenAPI: "3.1.0"}, op, BindingSpec)
	if err != nil || len(plans) != 1 || plans[0].mediaRange || plans[0].family != familyOctets {
		t.Fatalf("exact starred subtype plan = %#v, %v", plans, err)
	}
}

func TestRevision3SchemaOmittedJSONUsesConservativeWholeBody(t *testing.T) {
	doc := &openapi3.T{OpenAPI: "3.1.2"}
	op := opWithRequestBody(openapi3.Content{"application/json": emptyMedia()}, true)
	plans, err := planRequestBodiesFor(doc, op, BindingSpec)
	if err != nil || len(plans) != 1 || !plans[0].synthetic {
		t.Fatalf("revision-3 schema-omitted JSON plan = %#v, %v", plans, err)
	}
	for _, value := range []any{"scalar", []any{"array"}, map[string]any{"object": true}} {
		body, contentType, err := buildRequestBody(doc, plans[0], &routedInput{bodySet: true, bodyValue: value})
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(body)
		if err != nil {
			t.Fatal(err)
		}
		want, _ := json.Marshal(value)
		if !reflect.DeepEqual(got, want) || contentType != "application/json" {
			t.Errorf("value %#v wire = (%s, %q), want %s", value, got, contentType, want)
		}
	}

	spec := `{"openapi":"3.1.2","info":{"title":"t","version":"1"},"servers":[{"url":"https://example.test"}],"paths":{"/value":{"post":{"operationId":"putValue","requestBody":{"required":true,"content":{"application/json":{}}},"responses":{"204":{"description":"ok"}}}}}}`
	iface, err := NewSynthesizer().SynthesizeInterface(context.Background(), &synthesize.SynthesizeInput{
		Sources: []synthesize.SynthesizeSource{{BindingSpec: bindingSpecForTestDocument(spec), Content: openbindings.TextContent(spec)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := iface.Operations["putValue"].Input.(map[string]any)
	properties, _ := input["properties"].(map[string]any)
	if body, present := properties[syntheticBodyProperty]; !present || !reflect.DeepEqual(body, map[string]any{}) {
		t.Fatalf("schema-omitted projected whole body = %#v", properties)
	}
}

func TestRevision3RangeSelectionIsConfiguredAndMostSpecific(t *testing.T) {
	media := func() *openapi3.MediaType {
		return &openapi3.MediaType{Schema: &openapi3.SchemaRef{Value: &openapi3.Schema{Type: &openapi3.Types{"object"}}}}
	}
	op := opWithRequestBody(openapi3.Content{
		"*/*":                          media(),
		"application/*":                media(),
		"application/*; profile=exact": media(),
		"application/json":             media(),
	}, true)
	doc := &openapi3.T{OpenAPI: "3.1.0"}
	plans, err := planRequestBodiesFor(doc, op, BindingSpec)
	if err != nil {
		t.Fatal(err)
	}

	selectPlan := func(requestMedia string) *bodyPlan {
		t.Helper()
		selected, err := configuredRequestPlansFor(doc, op, plans, map[string]any{
			"configuration": map[string]any{"requestMedia": requestMedia},
		}, BindingSpec)
		if err != nil || len(selected) != 1 {
			t.Fatalf("select %q = %#v, %v", requestMedia, selected, err)
		}
		return selected[0]
	}
	if got := selectPlan("application/json; charset=utf-8"); got.mediaKey != "application/json" || got.mediaType != "application/json; charset=utf-8" {
		t.Fatalf("exact selection = %#v", got)
	}
	if got := selectPlan("application/problem+json; profile=exact; charset=utf-8"); got.mediaKey != "application/*; profile=exact" || got.mediaType != "application/problem+json; charset=utf-8; profile=exact" {
		t.Fatalf("parameter-specific type range selection = %#v", got)
	}
	textOp := opWithRequestBody(openapi3.Content{"*/*": emptyMedia()}, true)
	textPlans, err := planRequestBodiesFor(doc, textOp, BindingSpec)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := configuredRequestPlansFor(doc, textOp, textPlans, map[string]any{
		"configuration": map[string]any{"requestMedia": "text/plain"},
	}, BindingSpec)
	if err != nil || len(selected) != 1 || selected[0].mediaKey != "*/*" || selected[0].mediaType != "text/plain" {
		t.Fatalf("universal range selection = %#v, %v", selected, err)
	}
}

func TestRevision3RangeSelectionRefusesTiesAndUnsupportedConcreteLane(t *testing.T) {
	doc := &openapi3.T{OpenAPI: "3.1.0"}
	op := opWithRequestBody(openapi3.Content{
		"application/*; a=1": emptyMedia(),
		"application/*; b=2": emptyMedia(),
	}, true)
	plans, err := planRequestBodiesFor(doc, op, BindingSpec)
	if err != nil {
		t.Fatal(err)
	}
	_, err = configuredRequestPlansFor(doc, op, plans, map[string]any{
		"configuration": map[string]any{"requestMedia": "application/json; a=1; b=2"},
	}, BindingSpec)
	if err == nil || !strings.Contains(err.Error(), "ambiguously") {
		t.Fatalf("equal-specificity tie error = %v", err)
	}

	object := &openapi3.MediaType{Schema: &openapi3.SchemaRef{Value: &openapi3.Schema{Type: &openapi3.Types{"object"}}}}
	op = opWithRequestBody(openapi3.Content{"*/*": object}, true)
	plans, err = planRequestBodiesFor(doc, op, BindingSpec)
	if err != nil {
		t.Fatal(err)
	}
	_, err = configuredRequestPlansFor(doc, op, plans, map[string]any{
		"configuration": map[string]any{"requestMedia": "image/png"},
	}, BindingSpec)
	if err == nil || !strings.Contains(err.Error(), "no existing request carriage family") {
		t.Fatalf("object shape must not auto-select JSON for image/png, got %v", err)
	}
}

func TestRevision3ConfiguredRawRangeUsesGoverningSchema(t *testing.T) {
	tests := []struct {
		name string
		doc  *openapi3.T
		op   *openapi3.Operation
	}{
		{
			name: "oas30 binary image range",
			doc:  &openapi3.T{OpenAPI: "3.0.4"},
			op: opWithRequestBody(openapi3.Content{"image/*": {Schema: &openapi3.SchemaRef{Value: &openapi3.Schema{
				Type: &openapi3.Types{"string"}, Format: "binary",
			}}}}, true),
		},
		{
			name: "oas31 schema-omitted universal range",
			doc:  &openapi3.T{OpenAPI: "3.1.2"},
			op:   opWithRequestBody(openapi3.Content{"*/*": emptyMedia()}, true),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plans, err := planRequestBodiesFor(tt.doc, tt.op, BindingSpec)
			if err != nil {
				t.Fatal(err)
			}
			selected, err := configuredRequestPlansFor(tt.doc, tt.op, plans, map[string]any{
				"configuration": map[string]any{"requestMedia": "image/png"},
			}, BindingSpec)
			if err != nil || len(selected) != 1 || selected[0].family != familyOctets || !selected[0].rawBoundary {
				t.Fatalf("selected raw range = %#v, %v", selected, err)
			}
			body, contentType, err := buildRequestBody(tt.doc, selected[0], &routedInput{bodySet: true, bodyValue: "AAH+/w=="})
			if err != nil {
				t.Fatal(err)
			}
			bytes, err := io.ReadAll(body)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(bytes, []byte{0, 1, 254, 255}) || contentType != "image/png" {
				t.Fatalf("raw range wire = (%v, %q)", bytes, contentType)
			}
		})
	}
}

func TestRevision3RangeCanSelectJSONSuffixForAnyType(t *testing.T) {
	doc := &openapi3.T{OpenAPI: "3.1.0"}
	op := opWithRequestBody(openapi3.Content{"image/*": {Schema: &openapi3.SchemaRef{Value: &openapi3.Schema{
		Type: &openapi3.Types{"object"}, Properties: openapi3.Schemas{"name": {Value: &openapi3.Schema{Type: &openapi3.Types{"string"}}}},
	}}}}, true)
	plans, err := planRequestBodiesFor(doc, op, BindingSpec)
	if err != nil || len(plans) != 1 || plans[0].family != familyJSON || plans[0].synthetic {
		t.Fatalf("image wildcard skeleton = %#v, %v", plans, err)
	}
	selected, err := configuredRequestPlansFor(doc, op, plans, map[string]any{
		"configuration": map[string]any{"requestMedia": "image/problem+json"},
	}, BindingSpec)
	if err != nil || len(selected) != 1 || selected[0].family != familyJSON {
		t.Fatalf("+json selection = %#v, %v", selected, err)
	}
	body, contentType, err := buildRequestBody(doc, selected[0], &routedInput{bodyFields: map[string]any{"name": "pixel"}})
	if err != nil {
		t.Fatal(err)
	}
	wire, _ := io.ReadAll(body)
	if string(wire) != `{"name":"pixel"}` || contentType != "image/problem+json" {
		t.Fatalf("+json range wire = (%s, %q)", wire, contentType)
	}
}

func TestRevision3RawSynthesisProjectsBase64WithoutReplacingSchema(t *testing.T) {
	for _, tc := range []struct {
		name, version, media string
		schema               string
		wantFormat           any
	}{
		{"oas30", "3.0.3", "image/png", `"schema":{"type":"string","format":"binary","description":"pixels"}`, "binary"},
		{"oas31 omitted schema", "3.1.0", "application/octet-stream", ``, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mediaObject := "{}"
			if tc.schema != "" {
				mediaObject = "{" + tc.schema + "}"
			}
			spec := `{"openapi":"` + tc.version + `","info":{"title":"t","version":"1"},"servers":[{"url":"https://example.test"}],"paths":{"/asset":{"post":{"operationId":"putAsset","requestBody":{"required":true,"content":{"` + tc.media + `":` + mediaObject + `}},"responses":{"200":{"description":"ok"}}}}}}`
			iface, err := NewSynthesizer().SynthesizeInterface(context.Background(), &synthesize.SynthesizeInput{
				Sources: []synthesize.SynthesizeSource{{BindingSpec: bindingSpecForTestDocument(spec), Content: openbindings.TextContent(spec)}},
			})
			if err != nil {
				t.Fatal(err)
			}
			input, ok := iface.Operations["putAsset"].Input.(map[string]any)
			if !ok {
				t.Fatalf("input = %#v", iface.Operations["putAsset"].Input)
			}
			properties, _ := input["properties"].(map[string]any)
			body, _ := properties[syntheticBodyProperty].(map[string]any)
			if body["type"] != "string" || body["contentEncoding"] != "base64" {
				t.Fatalf("projected body schema = %#v", body)
			}
			if tc.wantFormat != nil && (body["format"] != tc.wantFormat || body["description"] != "pixels") {
				t.Fatalf("authored schema was not retained: %#v", body)
			}
		})
	}
}

func TestRevision3RangeSynthesisIsConservativeAcrossConcreteLanes(t *testing.T) {
	for _, tc := range []struct {
		name, version, media, schema string
		assert                       func(*testing.T, map[string]any, map[string]any)
	}{
		{
			name: "oas31 schema omitted", version: "3.1.0", media: "*/*",
			assert: func(t *testing.T, input, body map[string]any) {
				if !reflect.DeepEqual(body, map[string]any{}) {
					t.Fatalf("schema-omitted range body = %#v", body)
				}
			},
		},
		{
			name: "oas30 binary", version: "3.0.3", media: "image/*", schema: `"schema":{"type":"string","format":"binary"}`,
			assert: func(t *testing.T, input, body map[string]any) {
				if body["type"] != "string" || body["format"] != "binary" || body["contentEncoding"] != nil {
					t.Fatalf("binary range body must not claim universal Base64: %#v", body)
				}
			},
		},
		{
			name: "object remains open", version: "3.1.0", media: "image/*", schema: `"schema":{"type":"object","properties":{"name":{"type":"string"}}}`,
			assert: func(t *testing.T, input, body map[string]any) {
				if body != nil || input["additionalProperties"] != nil {
					t.Fatalf("range object should be an open flattened object: input=%#v body=%#v", input, body)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mediaObject := "{}"
			if tc.schema != "" {
				mediaObject = "{" + tc.schema + "}"
			}
			spec := `{"openapi":"` + tc.version + `","info":{"title":"t","version":"1"},"servers":[{"url":"https://example.test"}],"paths":{"/x":{"post":{"operationId":"put","requestBody":{"required":true,"content":{"` + tc.media + `":` + mediaObject + `}},"responses":{"204":{"description":"ok"}}}}}}`
			iface, err := NewSynthesizer().SynthesizeInterface(context.Background(), &synthesize.SynthesizeInput{Sources: []synthesize.SynthesizeSource{{BindingSpec: bindingSpecForTestDocument(spec), Content: openbindings.TextContent(spec)}}})
			if err != nil {
				t.Fatal(err)
			}
			input, _ := iface.Operations["put"].Input.(map[string]any)
			properties, _ := input["properties"].(map[string]any)
			body, _ := properties[syntheticBodyProperty].(map[string]any)
			tc.assert(t, input, body)
		})
	}
}

func TestRevision3RangeCoverageRequiresRequestMediaAtCorrectScopes(t *testing.T) {
	resultFor := func(content string) *synthesize.SynthesizeResult {
		t.Helper()
		spec := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://example.test"}],"paths":{"/items":{"post":{"operationId":"putItems","requestBody":{"required":true,"content":` + content + `},"responses":{"200":{"description":"ok"}}}}}}`
		result, err := NewSynthesizer().SynthesizeInterfaceWithCoverage(context.Background(), &synthesize.SynthesizeInput{
			Sources: []synthesize.SynthesizeSource{{BindingSpec: bindingSpecForTestDocument(spec), Content: openbindings.TextContent(spec)}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}

	assertRequirements := func(result *synthesize.SynthesizeResult, wantTarget bool) {
		t.Helper()
		var target, rangeAlternative []string
		for _, entry := range result.Coverage.Entries {
			switch {
			case entry.SourceRef == "#/paths/~1items/post" && entry.Scope == synthesize.SynthesisCoverageTarget:
				target = entry.Requirements
			case strings.Contains(entry.SourceRef, "application~1*"):
				rangeAlternative = entry.Requirements
			}
		}
		if !reflect.DeepEqual(rangeAlternative, []string{"configuration.requestMedia"}) {
			t.Fatalf("range requirements = %v", rangeAlternative)
		}
		if got := reflect.DeepEqual(target, []string{"configuration.requestMedia"}); got != wantTarget {
			t.Fatalf("target requirements = %v, want media requirement %v", target, wantTarget)
		}
	}
	assertRequirements(resultFor(`{"application/*":{"schema":{"type":"object","properties":{"name":{"type":"string"}}}}}`), true)
	assertRequirements(resultFor(`{"application/json":{"schema":{"type":"object"}},"application/*":{"schema":{"type":"object"}}}`), false)
}

func TestRevision3PrepareBindingChallengesForRequiredRange(t *testing.T) {
	spec := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://api.example.test"}],"paths":{"/x":{"post":{"operationId":"put","requestBody":{"required":true,"content":{"application/*":{"schema":{"type":"object"}}}},"responses":{"204":{"description":"ok"}}}}}}`
	args := &invoke.BindingInvocationArgs{
		Source:   invoke.InvocationSource{BindingSpec: bindingSpecForTestDocument(spec), Content: openbindings.TextContent(spec)},
		Selector: "#/paths/~1x/post",
	}
	details, err := NewInvoker().PrepareBinding(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if details == nil || len(details.Alternatives) != 1 || len(details.Alternatives[0].Requirements) != 1 {
		t.Fatalf("prepare details = %#v", details)
	}
	requirement := details.Alternatives[0].Requirements[0]
	if requirement.Type != "config.value" || requirement.Extra["point"] != "requestMedia" || requirement.Extra["path"] != "" {
		t.Fatalf("prepare requirement = %#v", requirement)
	}
	args.Context = map[string]any{"configuration": map[string]any{"requestMedia": "application/json"}}
	details, err = NewInvoker().PrepareBinding(context.Background(), args)
	if err != nil || details != nil {
		t.Fatalf("configured prepare = %#v, %v", details, err)
	}
	args.Context = map[string]any{"configuration": map[string]any{"requestMedia": ""}}
	if details, err := NewInvoker().PrepareBinding(context.Background(), args); err == nil || details != nil {
		t.Fatalf("empty requestMedia prepare = %#v, %v; want refusal", details, err)
	}
}

func TestRevision3MultipartPreservesOAS31ApplicationStrings(t *testing.T) {
	schema := &openapi3.Schema{
		Type: &openapi3.Types{"object"},
		Properties: openapi3.Schemas{
			"encoded": {Value: &openapi3.Schema{
				Type: &openapi3.Types{"string"}, ContentEncoding: "base64", ContentMediaType: "application/json",
			}},
			"identity": {Value: &openapi3.Schema{
				Type: &openapi3.Types{"string"}, ContentMediaType: "text/custom",
			}},
		},
	}
	media := &openapi3.MediaType{Schema: &openapi3.SchemaRef{Value: schema}}
	r, ct, err := buildMultipartBodyForRevision(&openapi3.T{OpenAPI: "3.1.0"}, media, map[string]any{
		"encoded":  "eyJ4IjoxfQ==",
		"identity": "raw-text",
	}, BindingSpec)
	if err != nil {
		t.Fatal(err)
	}
	parts := parseMultipart(t, r, ct)
	if got := parts["encoded"][0]; got != [2]string{"application/octet-stream", "eyJ4IjoxfQ=="} {
		t.Fatalf("encoded string part = %v", got)
	}
	if got := parts["identity"][0]; got != [2]string{"text/plain", "raw-text"} {
		t.Fatalf("identity string part = %v", got)
	}
}

func TestRevision3MultipartOAS30UsesCanonicalBoundaryBase64(t *testing.T) {
	media := &openapi3.MediaType{Schema: &openapi3.SchemaRef{Value: &openapi3.Schema{
		Type: &openapi3.Types{"object"}, Properties: openapi3.Schemas{
			"file": {Value: &openapi3.Schema{Type: &openapi3.Types{"string"}, Format: "binary"}},
		},
	}}}
	doc := &openapi3.T{OpenAPI: "3.0.3"}
	for _, value := range []string{"YQ", "AB==", "YQ==\n"} {
		if _, _, err := buildMultipartBodyForRevision(doc, media, map[string]any{"file": value}, BindingSpec); err == nil || !strings.Contains(err.Error(), "invalid base64") {
			t.Errorf("non-canonical multipart Base64 %q error = %v", value, err)
		}
	}
	r, ct, err := buildMultipartBodyForRevision(doc, media, map[string]any{"file": "YQ=="}, BindingSpec)
	if err != nil {
		t.Fatal(err)
	}
	if got := parseMultipart(t, r, ct)["file"][0][1]; got != "a" {
		t.Fatalf("canonical multipart bytes = %q", got)
	}
	if _, _, err := buildMultipartBodyForRevision(doc, media, map[string]any{"file": []byte("a")}, BindingSpec); err == nil {
		t.Fatal("revision 3 accepted the legacy in-process []byte bypass")
	}
}

func TestRevision3MultipartSynthesisDecoratesNestedBinaryValues(t *testing.T) {
	spec := `{"openapi":"3.0.3","info":{"title":"t","version":"1"},"servers":[{"url":"https://example.test"}],"paths":{"/assets":{"post":{"operationId":"putAssets","requestBody":{"required":true,"content":{"multipart/form-data":{"schema":{"type":"object","properties":{"files":{"type":"array","items":{"type":"string","format":"binary"}},"profile":{"type":"object","properties":{"avatar":{"type":"string","format":"binary"}}}}}}}},"responses":{"204":{"description":"ok"}}}}}}`
	iface, err := NewSynthesizer().SynthesizeInterface(context.Background(), &synthesize.SynthesizeInput{Sources: []synthesize.SynthesizeSource{{BindingSpec: bindingSpecForTestDocument(spec), Content: openbindings.TextContent(spec)}}})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := iface.Operations["putAssets"].Input.(map[string]any)
	properties, _ := input["properties"].(map[string]any)
	files, _ := properties["files"].(map[string]any)
	items, _ := files["items"].(map[string]any)
	profile, _ := properties["profile"].(map[string]any)
	profileProperties, _ := profile["properties"].(map[string]any)
	avatar, _ := profileProperties["avatar"].(map[string]any)
	if items["contentEncoding"] != "base64" || avatar["contentEncoding"] != nil {
		t.Fatalf("nested multipart boundary schemas: items=%#v avatar=%#v", items, avatar)
	}

}

func TestRevision3MultipartDecorationIsCandidateLocal(t *testing.T) {
	shared := &openapi3.SchemaRef{Value: &openapi3.Schema{
		Type: &openapi3.Types{"object"}, Properties: openapi3.Schemas{
			"file": {Value: &openapi3.Schema{Type: &openapi3.Types{"string"}, Format: "binary"}},
		},
	}}
	op := opWithRequestBody(openapi3.Content{
		"application/json":    {Schema: shared},
		"multipart/form-data": {Schema: shared},
	}, true)
	plans, err := planRequestBodiesFor(&openapi3.T{OpenAPI: "3.0.3"}, op, BindingSpec)
	if err != nil {
		t.Fatal(err)
	}
	withBoundary, withoutBoundary := 0, 0
	routes := planAbstractInputRoutes(nil, plans)
	for _, plan := range plans {
		input := buildInputSchema(op, nil, plan, nil, routes, nil)
		properties, _ := input["properties"].(map[string]any)
		file, _ := properties["file"].(map[string]any)
		if file["contentEncoding"] == "base64" {
			withBoundary++
		} else {
			withoutBoundary++
		}
	}
	if withBoundary != 1 || withoutBoundary != 1 {
		t.Fatalf("shared schema projections leaked candidate decoration: with=%d without=%d", withBoundary, withoutBoundary)
	}
}

func TestRevision3ConflictingContentEncodingRefuses(t *testing.T) {
	conflict := &openapi3.Schema{AllOf: openapi3.SchemaRefs{
		{Value: &openapi3.Schema{Type: &openapi3.Types{"string"}, ContentEncoding: "base64"}},
		{Value: &openapi3.Schema{ContentEncoding: "base64url"}},
	}}
	doc := &openapi3.T{OpenAPI: "3.1.0"}
	op := opWithRequestBody(openapi3.Content{"image/png": {Schema: &openapi3.SchemaRef{Value: conflict}}}, true)
	if _, err := planRequestBodiesFor(doc, op, BindingSpec); err == nil || !strings.Contains(err.Error(), "conflicting contentEncoding") {
		t.Fatalf("whole-body conflict error = %v", err)
	}

	multipartMedia := &openapi3.MediaType{Schema: &openapi3.SchemaRef{Value: &openapi3.Schema{
		Type: &openapi3.Types{"object"}, Properties: openapi3.Schemas{"file": {Value: conflict}},
	}}}
	if _, _, err := buildMultipartBodyForRevision(doc, multipartMedia, map[string]any{"file": "value"}, BindingSpec); err == nil || !strings.Contains(err.Error(), "conflicting contentEncoding") {
		t.Fatalf("multipart conflict error = %v", err)
	}
}

func TestRevision3ExternalContentEncodingControlsCarriage(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{},
			Body: io.NopCloser(strings.NewReader(`Encoded:
  type: string
  contentEncoding: base64
  contentMediaType: image/png
`)),
			Request: req,
		}, nil
	})}
	doc, err := loadDocumentWithResolver(context.Background(), client, "https://description.example/openapi.yaml", openbindings.TextContent(`openapi: 3.1.0
info: {title: external, version: "1"}
paths:
  /asset:
    post:
      requestBody:
        required: true
        content:
          image/png:
            schema: {$ref: "./schema.yaml#/Encoded"}
      responses: {"204": {description: ok}}
`))
	if err != nil {
		t.Fatal(err)
	}
	op := doc.Paths.Find("/asset").Post
	plans, err := planRequestBodiesFor(doc, op, BindingSpec)
	if err != nil || len(plans) != 1 || plans[0].family != familyOctets || plans[0].rawBoundary {
		t.Fatalf("external encoded-string plan = %#v, %v", plans, err)
	}
}

// §9.2: a schema that ASSERTS NOTHING — the JSON Schema boolean `true`, or a
// memberless Schema Object — is the same declaration as an omitted `schema`,
// because the artifact made no claim about the body at all; it takes the
// artifact-authorized byte lane rather than falling between two lanes. The
// boolean `false` asserts that no value is admissible and is not that case.
func TestRevision3UnconstrainedSchemaIsAnOmittedRawShape(t *testing.T) {
	for _, schema := range []string{"true", "false"} {
		spec := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://example.test"}],"paths":{"/x":{"post":{"requestBody":{"required":true,"content":{"image/png":{"schema":` + schema + `}}},"responses":{"204":{"description":"ok"}}}}}}`
		doc, err := loadDocument("", openbindings.TextContent(spec))
		if err != nil {
			t.Fatalf("load boolean schema %s: %v", schema, err)
		}
		plans, planErr := planRequestBodiesFor(doc, doc.Paths.Find("/x").Post, BindingSpec)
		if schema == "true" {
			if planErr != nil || len(plans) != 1 || plans[0].family != familyOctets || !plans[0].rawBoundary {
				t.Fatalf("boolean schema true must take the byte lane: plans=%#v err=%v", plans, planErr)
			}
		} else if planErr == nil || len(plans) != 0 {
			t.Fatalf("unsatisfiable boolean schema false must select no lane: plans=%#v err=%v", plans, planErr)
		}

		jsonSpec := strings.Replace(spec, `"image/png"`, `"application/json"`, 1)
		jsonDoc, err := loadDocument("", openbindings.TextContent(jsonSpec))
		if err != nil {
			t.Fatal(err)
		}
		jsonPlans, err := planRequestBodiesFor(jsonDoc, jsonDoc.Paths.Find("/x").Post, BindingSpec)
		if err != nil || len(jsonPlans) != 1 || !jsonPlans[0].synthetic {
			t.Fatalf("JSON boolean schema %s plan = %#v, %v", schema, jsonPlans, err)
		}
		body, contentType, err := buildRequestBody(jsonDoc, jsonPlans[0], &routedInput{bodySet: true, bodyValue: []any{"value"}})
		if err != nil {
			t.Fatal(err)
		}
		wire, _ := io.ReadAll(body)
		if string(wire) != `["value"]` || contentType != "application/json" {
			t.Fatalf("JSON boolean wire = (%s, %q)", wire, contentType)
		}
	}
}

func TestRevision3BooleanSchemasSurviveRequestParameterAndOutputSynthesis(t *testing.T) {
	spec := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://example.test"}],"paths":{"/json":{"post":{"operationId":"json","requestBody":{"required":true,"content":{"application/json":{"schema":true}}},"responses":{"200":{"description":"ok","content":{"application/json":{"schema":false}}}}}},"/parameter":{"get":{"operationId":"parameter","parameters":[{"name":"q","in":"query","description":"query false","schema":false},{"name":"c","in":"query","description":"content true","content":{"application/json":{"schema":true}}}],"responses":{"200":{"description":"ok","content":{"application/json":{"schema":true}}}}}}}}`
	t.Run(BindingSpec, func(t *testing.T) {
		iface, err := NewSynthesizer().SynthesizeInterface(context.Background(), &synthesize.SynthesizeInput{Sources: []synthesize.SynthesizeSource{{BindingSpec: bindingSpecForTestDocument(spec), Content: openbindings.TextContent(spec)}}})
		if err != nil {
			t.Fatal(err)
		}
		jsonInput, _ := iface.Operations["json"].Input.(map[string]any)
		jsonProperties, _ := jsonInput["properties"].(map[string]any)
		if jsonProperties[syntheticBodyProperty] != true || iface.Operations["json"].Output != false {
			t.Fatalf("JSON boolean schemas = input %#v output %#v", jsonProperties, iface.Operations["json"].Output)
		}
		parameterInput, _ := iface.Operations["parameter"].Input.(map[string]any)
		parameterProperties, _ := parameterInput["properties"].(map[string]any)
		q, _ := parameterProperties["q"].(map[string]any)
		c, _ := parameterProperties["c"].(map[string]any)
		qAllOf, _ := q["allOf"].([]any)
		cAllOf, _ := c["allOf"].([]any)
		if q["description"] != "query false" || len(qAllOf) != 1 || qAllOf[0] != false ||
			c["description"] != "content true" || len(cAllOf) != 1 || cAllOf[0] != true || iface.Operations["parameter"].Output != true {
			t.Fatalf("parameter/output boolean schemas = input %#v output %#v", parameterProperties, iface.Operations["parameter"].Output)
		}
	})
}

func TestRevision3ExternalBooleanSchemaSurvivesSynthesis(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("false")), Request: req}, nil
	})}
	spec := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://example.test"}],"paths":{"/x":{"post":{"operationId":"put","requestBody":{"required":true,"content":{"application/json":{"schema":{"$ref":"./schema.json"}}}},"responses":{"204":{"description":"ok"}}}}}}`
	iface, err := NewSynthesizerWithClient(client).SynthesizeInterface(context.Background(), &synthesize.SynthesizeInput{Sources: []synthesize.SynthesizeSource{{
		BindingSpec: bindingSpecForTestDocument(spec), Location: "https://description.example/openapi.json", Content: openbindings.TextContent(spec),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := iface.Operations["put"].Input.(map[string]any)
	properties, _ := input["properties"].(map[string]any)
	if properties[syntheticBodyProperty] != false {
		t.Fatalf("external false schema = %#v", properties)
	}
}

func TestRevision3ResponseRangesAndCollisions(t *testing.T) {
	response := &openapi3.Response{Content: openapi3.Content{
		"application/*":    emptyMedia(),
		"application/json": emptyMedia(),
	}}
	matched, err := governingResponseMediaFor(response, "application/json", BindingSpec)
	if err != nil || matched.base != "application/json" {
		t.Fatalf("concrete response beside range = %#v, %v", matched, err)
	}
	if matched, err := governingResponseMediaFor(&openapi3.Response{Content: openapi3.Content{"application/*": emptyMedia()}}, "application/json", BindingSpec); err != nil || matched.base != "application/*" {
		t.Fatalf("range-only response match = %#v, %v", matched, err)
	}

	// §9.2 confinement: the collision owns its parsed identity and nothing
	// more. The clean image/png sibling still governs the actual response...
	colliding := &openapi3.Response{Content: openapi3.Content{
		"image/png":                          emptyMedia(),
		"application/json; charset=UTF-8":    emptyMedia(),
		"Application/JSON;charset=\"utf-8\"": emptyMedia(),
	}}
	if matched, err := governingResponseMediaFor(colliding, "image/png", BindingSpec); err != nil || matched.base != "image/png" {
		t.Fatalf("confined collision poisoned a clean sibling = %#v, %v", matched, err)
	}
	// ...while no response match may be governed by the colliding identity.
	if _, err := governingResponseMediaFor(colliding, "application/json; charset=utf-8", BindingSpec); err == nil || !strings.Contains(err.Error(), "normalized collision") {
		t.Fatalf("colliding-identity response error = %v", err)
	}
	// A collision between two RANGE keys cannot govern a concrete decode in
	// revision 3 at all, and it no longer poisons the concrete match either.
	rangeCollision := &openapi3.Response{Content: openapi3.Content{
		"image/png":                       emptyMedia(),
		"application/*; charset=UTF-8":    emptyMedia(),
		"Application/*;charset=\"utf-8\"": emptyMedia(),
	}}
	if matched, err := governingResponseMediaFor(rangeCollision, "image/png", BindingSpec); err != nil || matched.base != "image/png" {
		t.Fatalf("confined range collision = %#v, %v", matched, err)
	}
	// A map whose ONLY entries collide governs nothing.
	onlyColliding := &openapi3.Response{Content: openapi3.Content{
		"application/json; charset=UTF-8":    emptyMedia(),
		"Application/JSON;charset=\"utf-8\"": emptyMedia(),
	}}
	if _, err := governingResponseMediaFor(onlyColliding, "application/json; charset=utf-8", BindingSpec); err == nil || !strings.Contains(err.Error(), "normalized collision") {
		t.Fatalf("all-colliding response error = %v", err)
	}
	ordinary := &openapi3.Response{Content: openapi3.Content{"application/json; profile=UPPER": emptyMedia()}}
	if _, err := governingResponseMediaFor(ordinary, "application/json; profile=upper", BindingSpec); err == nil {
		t.Fatal("ordinary response parameter values must remain case-sensitive")
	}
}

func TestRevision3ParameterizedSSEIsStreamingCapable(t *testing.T) {
	op := &openapi3.Operation{Responses: openapi3.NewResponses()}
	op.Responses.Set("200", &openapi3.ResponseRef{Value: &openapi3.Response{Content: openapi3.Content{
		"text/event-stream; charset=utf-8": emptyMedia(),
	}}})
	if !isStreamingCapableFor(op, BindingSpec) {
		t.Fatal("parameterized concrete SSE declaration must confer revision-3 streaming capability")
	}
}

func TestRevision3AcceptUsesParsedParameterSemantics(t *testing.T) {
	op := &openapi3.Operation{Responses: openapi3.NewResponses()}
	op.Responses.Set("200", &openapi3.ResponseRef{Value: &openapi3.Response{Content: openapi3.Content{
		`application/json; note="a\z"`:       emptyMedia(),
		"application/json; charset=UTF-8":    emptyMedia(),
		"Application/JSON;charset=\"utf-8\"": emptyMedia(),
	}}})
	// The two application/json spellings denote one parsed identity in ONE
	// content map: a normalized collision. No response match may be governed
	// by it (§9.2), so it is not an available representation and advertising
	// it would invite exactly the response the decode lane must refuse. The
	// non-colliding quoted-pair sibling advertises unaffected -- confinement,
	// not a first-key pick between the two colliding spellings.
	types := successMediaTypesFor(op, BindingSpec)
	if len(types) != 1 {
		t.Fatalf("semantic Accept membership = %v", types)
	}
	parsed, err := parseRevision3MediaType(types[0])
	if err != nil {
		t.Fatal(err)
	}
	if parsed.params["note"] != "az" {
		t.Fatalf("Accept lost governed parameters: %v", types)
	}
	if got := acceptHeaderFor(op, BindingSpec); strings.Contains(got, "charset=") || strings.Contains(got, `\z`) {
		t.Fatalf("Accept = %q, want the colliding charset identity dropped and the quoted-pair value unescaped", got)
	}

	// One identity declared by two DIFFERENT response content maps is not a
	// collision: §9.2's unit is one content map. The set carries the identity
	// once, with a deterministic spelling rather than a first-key pick.
	crossMap := &openapi3.Operation{Responses: openapi3.NewResponses()}
	crossMap.Responses.Set("200", &openapi3.ResponseRef{Value: &openapi3.Response{Content: openapi3.Content{
		"text/plain; charset=UTF-8": emptyMedia(),
	}}})
	crossMap.Responses.Set("default", &openapi3.ResponseRef{Value: &openapi3.Response{Content: openapi3.Content{
		"TEXT/PLAIN; CHARSET=utf-8": emptyMedia(),
	}}})
	if got := acceptHeaderFor(crossMap, BindingSpec); got != "text/plain; charset=UTF-8" {
		t.Fatalf("cross-map identity Accept = %q", got)
	}
}

func TestRevision3HTTPMediaParserDoesNotApplyRFC2231(t *testing.T) {
	parsed, err := parseRevision3MediaType(`application/json;; title*=utf-8''hello; title*0=one; title*1=two; charset=UTF-8; note="a\z";`)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.params["title*"] != "utf-8''hello" || parsed.params["title*0"] != "one" || parsed.params["title*1"] != "two" || parsed.params["title"] != "" {
		t.Fatalf("RFC2231 names were not preserved literally: %#v", parsed.params)
	}
	if parsed.params["charset"] != "utf-8" || parsed.params["note"] != "az" {
		t.Fatalf("semantic/unescaped params = %#v", parsed.params)
	}
	for _, invalid := range []string{
		"application/json; name =x",
		"application/json; name= x",
		"application/json\r\n; name=x",
		"application/json; name=\x7f",
		"application/j\xc3\xa9son",
	} {
		if _, err := parseRevision3MediaType(invalid); err == nil {
			t.Errorf("accepted invalid HTTP media type %q", invalid)
		}
	}
	obs := "text/plain; note=\"" + string([]byte{0x80}) + "\""
	if _, err := parseRevision3MediaType(obs); err != nil {
		t.Fatalf("quoted obs-text refused: %v", err)
	}
	if _, err := parseRevision3MediaType("text/plain; charset=\"\""); err != nil {
		t.Fatalf("empty quoted parameter is syntactically valid: %v", err)
	}

	left, _ := parseMediaDeclaration(`multipart/related; type="Application/JSON; Charset=UTF-8"`)
	right, _ := parseMediaDeclaration(`multipart/related; type="application/json; charset=utf-8"`)
	if left.semanticIdentity != right.semanticIdentity {
		t.Fatalf("registered nested-media type semantics differ: %q != %q", left.semanticIdentity, right.semanticIdentity)
	}
}

func TestRevision3MediaParameterPresenceAndMalformedResponses(t *testing.T) {
	declaration, _ := parseMediaDeclaration(`application/json; foo=""`)
	concrete, _ := parseRevision3MediaType(`application/json`)
	if requestMediaDeclarationMatches(declaration, concrete) {
		t.Fatal("a declared empty parameter matched a missing configured parameter")
	}
	response := &openapi3.Response{Content: openapi3.Content{`application/json; foo=""`: emptyMedia()}}
	if _, err := governingResponseMediaFor(response, "application/json", BindingSpec); err == nil {
		t.Fatal("a declared empty response parameter matched a missing actual parameter")
	}
	malformed := &openapi3.Response{Content: openapi3.Content{"application/json": emptyMedia(), "bad": emptyMedia()}}
	if _, err := governingResponseMediaFor(malformed, "application/json", BindingSpec); err == nil || !strings.Contains(err.Error(), "invalid response media declaration") {
		t.Fatalf("malformed response declaration error = %v", err)
	}
	if _, err := parseRevision3MediaType(`text/plain; charset=""`); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeTextLaneFor(`text/plain; charset=""`, []byte("x"), BindingSpec); err == nil {
		t.Fatal("present empty charset defaulted to UTF-8")
	}
	decoded, err := decodeTextLaneFor(`text/plain; charset*=utf-8''iso-8859-1`, []byte("é"), BindingSpec)
	if err != nil || decoded != "é" {
		t.Fatalf("charset* was reinterpreted as RFC2231 charset: (%v, %v)", decoded, err)
	}
}

func TestRevision3TextRequestCharsetEncoding(t *testing.T) {
	doc := &openapi3.T{OpenAPI: "3.1.2"}
	op := opWithRequestBody(openapi3.Content{`text/plain; charset=iso-8859-1`: {Schema: &openapi3.SchemaRef{Value: &openapi3.Schema{Type: &openapi3.Types{"string"}}}}}, true)
	plans, err := planRequestBodiesFor(doc, op, BindingSpec)
	if err != nil || len(plans) != 1 {
		t.Fatalf("plan = %#v, %v", plans, err)
	}
	body, _, err := buildRequestBody(doc, plans[0], &routedInput{bodySet: true, bodyValue: "café"})
	if err != nil {
		t.Fatal(err)
	}
	wire, _ := io.ReadAll(body)
	if !reflect.DeepEqual(wire, []byte{'c', 'a', 'f', 0xe9}) {
		t.Fatalf("Latin-1 body = %v", wire)
	}
	if _, _, err := buildRequestBody(doc, plans[0], &routedInput{bodySet: true, bodyValue: "snowman ☃"}); err == nil {
		t.Fatal("out-of-range Latin-1 value was not refused")
	}
	for _, contentType := range []string{`text/plain; charset=""`, `text/plain; charset=shift_jis`} {
		bad := opWithRequestBody(openapi3.Content{contentType: {Schema: &openapi3.SchemaRef{Value: &openapi3.Schema{Type: &openapi3.Types{"string"}}}}}, true)
		if _, err := planRequestBodiesFor(doc, bad, BindingSpec); err == nil {
			t.Errorf("unsupported request charset %q was planned", contentType)
		}
	}
}

func TestRevision3MultipartParametersCharsetsAndEncodingRules(t *testing.T) {
	doc := &openapi3.T{OpenAPI: "3.1.2"}
	schema := &openapi3.Schema{Type: &openapi3.Types{"object"}, Properties: openapi3.Schemas{
		"text": {Value: &openapi3.Schema{Type: &openapi3.Types{"string"}}},
	}}
	media := &openapi3.MediaType{Schema: &openapi3.SchemaRef{Value: schema}}
	media.Encoding = map[string]*openapi3.Encoding{"text": {ContentType: `text/plain; charset=iso-8859-1`}}
	r, ct, err := buildMultipartBodyForMediaType(doc, media, map[string]any{"text": "café"}, BindingSpec, `multipart/form-data; profile="a\z"; boundary=OpenBindingsBoundary`)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseRevision3MediaType(ct)
	if err != nil || parsed.params["profile"] != "az" || parsed.params["boundary"] != "OpenBindingsBoundary" {
		t.Fatalf("emitted Content-Type = %q (%#v, %v)", ct, parsed.params, err)
	}
	base, params, _ := mime.ParseMediaType(ct)
	reader := multipart.NewReader(r, params["boundary"])
	part, err := reader.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	partBytes, _ := io.ReadAll(part)
	if base != "multipart/form-data" || part.Header.Get("Content-Type") != "text/plain; charset=iso-8859-1" || !reflect.DeepEqual(partBytes, []byte{'c', 'a', 'f', 0xe9}) {
		t.Fatalf("multipart part = ct %q bytes %v", part.Header.Get("Content-Type"), partBytes)
	}

	generated, generatedCT, err := buildMultipartBodyForMediaType(doc, media, map[string]any{"text": "ok"}, BindingSpec, `multipart/form-data; profile=asset`)
	if err != nil {
		t.Fatal(err)
	}
	_ = generated
	generatedParsed, _ := parseRevision3MediaType(generatedCT)
	if generatedParsed.params["profile"] != "asset" || generatedParsed.params["boundary"] == "" {
		t.Fatalf("generated boundary lost configured params: %q", generatedCT)
	}

	encodedSchema := &openapi3.Schema{Type: &openapi3.Types{"object"}, Properties: openapi3.Schemas{
		"payload": {Value: &openapi3.Schema{Type: &openapi3.Types{"string"}, ContentEncoding: "base64", ContentMediaType: "image/png"}},
	}}
	encodedMedia := &openapi3.MediaType{Schema: &openapi3.SchemaRef{Value: encodedSchema}}
	encodedBody, encodedCT, err := buildMultipartBodyForRevision(doc, encodedMedia, map[string]any{"payload": "é"}, BindingSpec)
	if err != nil {
		t.Fatal(err)
	}
	_, encodedParams, _ := mime.ParseMediaType(encodedCT)
	encodedReader := multipart.NewReader(encodedBody, encodedParams["boundary"])
	encodedPart, _ := encodedReader.NextPart()
	if encodedPart.Header.Get("Content-Type") != "application/octet-stream" || encodedPart.Header.Get("Content-Transfer-Encoding") != "base64" {
		t.Fatalf("encoded headers = %#v", encodedPart.Header)
	}
	encodedMedia.Encoding = map[string]*openapi3.Encoding{"payload": {ContentType: "application/json"}}
	encodedBody, encodedCT, err = buildMultipartBodyForRevision(doc, encodedMedia, map[string]any{"payload": "YQ=="}, BindingSpec)
	if err != nil {
		t.Fatal(err)
	}
	_, encodedParams, _ = mime.ParseMediaType(encodedCT)
	encodedReader = multipart.NewReader(encodedBody, encodedParams["boundary"])
	encodedPart, _ = encodedReader.NextPart()
	encodedBytes, _ := io.ReadAll(encodedPart)
	if string(encodedBytes) != "YQ==" || encodedPart.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("encoded JSON-typed part = %q %#v", encodedBytes, encodedPart.Header)
	}
}

func TestRevision3FormCandidatesFailClosedAndHonorEncodingObjects(t *testing.T) {
	doc := &openapi3.T{OpenAPI: "3.1.2"}
	for _, mediaType := range []string{"multipart/form-data", "application/x-www-form-urlencoded"} {
		op := opWithRequestBody(openapi3.Content{mediaType: emptyMedia()}, true)
		if _, err := planRequestBodiesFor(doc, op, BindingSpec); err == nil || !strings.Contains(err.Error(), "schema-omitted") {
			t.Errorf("schema-omitted %s plan error = %v", mediaType, err)
		}
	}
	rangeOp := opWithRequestBody(openapi3.Content{"application/*": emptyMedia()}, true)
	plans, err := planRequestBodiesFor(doc, rangeOp, BindingSpec)
	if err != nil {
		t.Fatal(err)
	}
	wanted, _ := parseRevision3MediaType("application/x-www-form-urlencoded")
	if _, err := selectRevision3RequestPlan(doc, rangeOp, plans, wanted, BindingSpec); err == nil || !strings.Contains(err.Error(), "schema-omitted") {
		t.Fatalf("schema-omitted range form selection error = %v", err)
	}

	object := &openapi3.Schema{Type: &openapi3.Types{"object"}, Properties: openapi3.Schemas{
		"address": {Value: &openapi3.Schema{Type: &openapi3.Types{"object"}}},
	}}
	formMedia := &openapi3.MediaType{Schema: &openapi3.SchemaRef{Value: object}}
	body, err := buildURLEncodedBodyForRevision(doc, formMedia, map[string]any{"address": map[string]any{"line": "a b"}}, BindingSpec)
	if err != nil || body != `address=%7B%22line%22%3A%22a+b%22%7D` {
		t.Fatalf("content-based urlencoded body = %q, %v", body, err)
	}
	explode := true
	formMedia.Encoding = map[string]*openapi3.Encoding{"address": {Style: "form", Explode: &explode, ContentType: "application/json"}}
	body, err = buildURLEncodedBodyForRevision(doc, formMedia, map[string]any{"address": map[string]any{"line": "a b"}}, BindingSpec)
	if err != nil || body != `line=a%20b` {
		t.Fatalf("RFC6570 urlencoded body = %q, %v", body, err)
	}
	stringSchema := &openapi3.Schema{Type: &openapi3.Types{"object"}, Properties: openapi3.Schemas{
		"value": {Value: &openapi3.Schema{Type: &openapi3.Types{"string"}}},
	}}
	reservedMedia := &openapi3.MediaType{Schema: &openapi3.SchemaRef{Value: stringSchema}, Encoding: map[string]*openapi3.Encoding{
		"value": {AllowReserved: true},
	}}
	body, err = buildURLEncodedBodyForRevision(doc, reservedMedia, map[string]any{"value": "a/b&c+d#e[f]"}, BindingSpec)
	if err != nil || body != `value=a/b%26c%2Bd%23e%5Bf%5D` {
		t.Fatalf("safe allowReserved form body = %q, %v", body, err)
	}
}

func TestRevision3MultipartNestedArrayCandidateRefuses(t *testing.T) {
	doc := &openapi3.T{OpenAPI: "3.0.3"}
	nested := &openapi3.Schema{Type: &openapi3.Types{"array"}, Items: &openapi3.SchemaRef{Value: &openapi3.Schema{
		Type: &openapi3.Types{"array"}, Items: &openapi3.SchemaRef{Value: &openapi3.Schema{Type: &openapi3.Types{"string"}}},
	}}}
	media := &openapi3.MediaType{Schema: &openapi3.SchemaRef{Value: &openapi3.Schema{
		Type: &openapi3.Types{"object"}, Properties: openapi3.Schemas{"value": {Value: nested}},
	}}}
	if err := validateRevision3MultipartMedia(doc, media); err == nil || !strings.Contains(err.Error(), "nested array items") {
		t.Fatalf("nested multipart array validation error = %v", err)
	}
}

// TestRevision3MultipartAdmitsInertContentKeywordsOnDeclaredNonString is the
// re-pointed incumbent of a case that read
// `{"type":"object","contentMediaType":"application/json"}` -> refused until
// 2026-08-17, on a pre-check that required a resolved string schema whenever
// either Content-vocabulary keyword was present. No accepted OAS edition states
// that refusal, and two authorities state the opposite:
//
//   - [JSON Schema 2020-12] Section 8.1 makes the Content vocabulary
//     ANNOTATIONS that "do not function as validation assertions", and Section
//     8.3 / Section 8.4 each condition their keyword on the instance being a
//     string. On a declared non-string type both are inert.
//   - OAS 3.1.1 and 3.1.2 Section 4.8.15.1.1 tabulate `object` with `n/a` in
//     the contentEncoding column -> application/json, where the table's own
//     note defines `n/a` as "the presence or value of contentEncoding is
//     irrelevant"; 3.1.0 Section 4.8.14.5 gives complex properties
//     application/json without reference to either keyword.
//
// The cell is pinned HERE as well as in the shared part-content-encoding case
// table, because it is the cell whose incumbent pin this block moved and a
// moved pin owes an assertion, not a pointer.
func TestRevision3MultipartAdmitsInertContentKeywordsOnDeclaredNonString(t *testing.T) {
	for _, edition := range []string{"3.1.0", "3.1.1", "3.1.2"} {
		t.Run(edition, func(t *testing.T) {
			doc := &openapi3.T{OpenAPI: edition}
			media := &openapi3.MediaType{Schema: &openapi3.SchemaRef{Value: &openapi3.Schema{
				Type: &openapi3.Types{"object"},
				Properties: openapi3.Schemas{"value": {Value: &openapi3.Schema{
					Type:             &openapi3.Types{"object"},
					ContentMediaType: "application/json",
				}}},
			}}}
			op := opWithRequestBody(openapi3.Content{"multipart/form-data": media}, true)
			if _, err := planRequestBodiesFor(doc, op, BindingSpec); err != nil {
				t.Fatalf("declared object part carrying contentMediaType = %v, want admitted", err)
			}
			parsed, err := revision3PartContentType(media.Schema.Value.Properties["value"].Value, nil, false)
			if err != nil {
				t.Fatalf("part content type = %v", err)
			}
			if parsed.canonical != "application/json" {
				t.Fatalf("part Content-Type = %q, want application/json (the `object` row)", parsed.canonical)
			}
		})
	}
}

func TestRevision3MultipartRefusesUnrepresentableEncodingFacts(t *testing.T) {
	doc := &openapi3.T{OpenAPI: "3.1.2"}
	baseSchema := func(property *openapi3.Schema) *openapi3.Schema {
		return &openapi3.Schema{Type: &openapi3.Types{"object"}, Properties: openapi3.Schemas{"value": {Value: property}}}
	}
	tests := []struct {
		name  string
		media *openapi3.MediaType
		want  string
	}{
		{"dynamic headers", &openapi3.MediaType{Schema: &openapi3.SchemaRef{Value: baseSchema(&openapi3.Schema{Type: &openapi3.Types{"string"}})}, Encoding: map[string]*openapi3.Encoding{"value": {Headers: openapi3.Headers{"X-Part": {Value: &openapi3.Header{Parameter: openapi3.Parameter{Schema: &openapi3.SchemaRef{Value: &openapi3.Schema{Type: &openapi3.Types{"string"}}}}}}}}}}, "encoding.headers"},
		{"content type list", &openapi3.MediaType{Schema: &openapi3.SchemaRef{Value: baseSchema(&openapi3.Schema{Type: &openapi3.Types{"string"}})}, Encoding: map[string]*openapi3.Encoding{"value": {ContentType: "text/plain, application/json"}}}, "member selection"},
		{"content type wildcard", &openapi3.MediaType{Schema: &openapi3.SchemaRef{Value: baseSchema(&openapi3.Schema{Type: &openapi3.Types{"string"}})}, Encoding: map[string]*openapi3.Encoding{"value": {ContentType: "text/*"}}}, "not one supported concrete"},
		{"multi-non-null choice", &openapi3.MediaType{Schema: &openapi3.SchemaRef{Value: baseSchema(&openapi3.Schema{AnyOf: openapi3.SchemaRefs{
			{Value: &openapi3.Schema{Type: &openapi3.Types{"string"}}},
			{Value: &openapi3.Schema{Type: &openapi3.Types{"integer"}}},
		}})}}, "choice applicator"},
		{"nullable multi-non-null choice", &openapi3.MediaType{Schema: &openapi3.SchemaRef{Value: baseSchema(&openapi3.Schema{AnyOf: openapi3.SchemaRefs{
			{Value: &openapi3.Schema{Type: &openapi3.Types{"string"}}},
			{Value: &openapi3.Schema{Type: &openapi3.Types{"integer"}}},
			{Value: &openapi3.Schema{Type: &openapi3.Types{"null"}}},
		}})}}, "choice applicator"},
		{"content media type with no declared type", &openapi3.MediaType{Schema: &openapi3.SchemaRef{Value: baseSchema(&openapi3.Schema{ContentMediaType: "application/json"})}}, "no JSON-to-octet part boundary"},
		{"content media conflict", &openapi3.MediaType{Schema: &openapi3.SchemaRef{Value: baseSchema(&openapi3.Schema{AllOf: openapi3.SchemaRefs{{Value: &openapi3.Schema{Type: &openapi3.Types{"string"}, ContentMediaType: "image/png"}}, {Value: &openapi3.Schema{ContentMediaType: "image/jpeg"}}}})}}, "conflicting contentMediaType"},
		{"content encoding header injection", &openapi3.MediaType{Schema: &openapi3.SchemaRef{Value: baseSchema(&openapi3.Schema{Type: &openapi3.Types{"string"}, ContentEncoding: "base64\r\nX-Evil"})}}, "valid HTTP token"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			op := opWithRequestBody(openapi3.Content{"multipart/form-data": test.media}, true)
			if _, err := planRequestBodiesFor(doc, op, BindingSpec); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("plan error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestRevision3MultipartTypeAbsentPartRefusesOn30 pins the 3.0 half of §9.2's
// type-absent part cell. It replaces
// TestRevision3MultipartUnconstrainedPartUsesValueTypedDefaults, which pinned
// the specification's own value-keyed convention as an executed assertion --
// a bare-description part admitted, with the OAS per-type default keyed from
// the supplied value's JSON application type. Escalation M2 (ruled
// 2026-08-20) deleted that convention: no accepted 3.0 edition states a
// default contentType row that reaches a declaration carrying no `type`, and
// this specification authors none for that residue, so the part refuses
// before dispatch here exactly as it does on the 3.1 line -- one outcome,
// two grounds, which is why the two tests state their reasons separately.
func TestRevision3MultipartTypeAbsentPartRefusesOn30(t *testing.T) {
	for _, edition := range []string{"3.0.0", "3.0.1", "3.0.2", "3.0.3", "3.0.4"} {
		doc := &openapi3.T{OpenAPI: edition}
		media := &openapi3.MediaType{Schema: &openapi3.SchemaRef{Value: &openapi3.Schema{
			Type: &openapi3.Types{"object"},
			Properties: openapi3.Schemas{
				"file": {Value: &openapi3.Schema{Description: "Profile picture file"}},
			},
		}}}
		err := validateRevision3MultipartMedia(doc, media)
		if err == nil || !strings.Contains(err.Error(), "no default part Content-Type row on any accepted OAS 3.0 edition") {
			t.Fatalf("%s type-absent part admission = %v", edition, err)
		}
		op := opWithRequestBody(openapi3.Content{"multipart/form-data": media}, true)
		if _, err := planRequestBodiesFor(doc, op, BindingSpec); err == nil {
			t.Fatalf("%s type-absent part plan succeeded; the only alternative must leave no candidate", edition)
		}
		// The body-encoding lane refuses it too, so the admission refusal is
		// not the only thing standing between the declaration and the wire.
		if _, _, err := buildMultipartBodyForRevision(doc, media, map[string]any{"file": "hello"}, BindingSpec); err == nil {
			t.Fatalf("%s type-absent part encoded a body", edition)
		}
	}
}

// TestRevision3MultipartTypeAbsentPartRefusesOn31 pins the other half of the
// edition split. Every accepted 3.1 edition states application/octet-stream
// for a part whose `type` is absent -- 3.1.1 and 3.1.2 in the Encoding
// Object default table's first row, 3.1.0 through the total catch-all closing
// its prose enumeration -- and this revision defines no JSON-to-octet part
// boundary, so the part refuses before dispatch on all three.
func TestRevision3MultipartTypeAbsentPartRefusesOn31(t *testing.T) {
	for _, edition := range []string{"3.1.0", "3.1.1", "3.1.2"} {
		doc := &openapi3.T{OpenAPI: edition}
		media := &openapi3.MediaType{Schema: &openapi3.SchemaRef{Value: &openapi3.Schema{
			Type: &openapi3.Types{"object"},
			Properties: openapi3.Schemas{
				"file": {Value: &openapi3.Schema{Description: "Profile picture file"}},
			},
		}}}
		err := validateRevision3MultipartMedia(doc, media)
		if err == nil || !strings.Contains(err.Error(), "application/octet-stream") {
			t.Fatalf("%s type-absent part admission = %v", edition, err)
		}
	}
}

// §9.2: a part schema declaring one non-null anyOf/oneOf branch beside
// {type: "null"}-only branches collapses to the non-null branch's carriage;
// a JSON null value elides the optional part from the wire.
func TestRevision3MultipartNullableChoiceCollapsesToBranchCarriage(t *testing.T) {
	doc := &openapi3.T{OpenAPI: "3.1.0"}
	dynamicTrue := true
	nullBranch := &openapi3.SchemaRef{Value: &openapi3.Schema{Type: &openapi3.Types{"null"}}}
	media := &openapi3.MediaType{Schema: &openapi3.SchemaRef{Value: &openapi3.Schema{
		Type: &openapi3.Types{"object"},
		Properties: openapi3.Schemas{
			"file": {Value: &openapi3.Schema{Description: "nullable upload", AnyOf: openapi3.SchemaRefs{
				{Value: &openapi3.Schema{Type: &openapi3.Types{"string"}, ContentMediaType: "application/octet-stream"}},
				nullBranch,
			}}},
			"file_id": {Value: &openapi3.Schema{AnyOf: openapi3.SchemaRefs{
				{Value: &openapi3.Schema{Type: &openapi3.Types{"string"}}},
				nullBranch,
			}}},
			"options": {Value: &openapi3.Schema{OneOf: openapi3.SchemaRefs{
				{Value: &openapi3.Schema{Type: &openapi3.Types{"object"}, AdditionalProperties: openapi3.AdditionalProperties{Has: &dynamicTrue}}},
				nullBranch,
			}}},
		},
	}}}
	if err := validateRevision3MultipartMedia(doc, media); err != nil {
		t.Fatalf("nullable-choice admission = %v", err)
	}
	op := opWithRequestBody(openapi3.Content{"multipart/form-data": media}, true)
	if _, err := planRequestBodiesFor(doc, op, BindingSpec); err != nil {
		t.Fatalf("nullable-choice plan = %v", err)
	}

	r, ct, err := buildMultipartBodyForRevision(doc, media, map[string]any{
		"file":    "raw-text",
		"file_id": "id-1",
		"options": map[string]any{"k": "v"},
	}, BindingSpec)
	if err != nil {
		t.Fatal(err)
	}
	parts := parseMultipart(t, r, ct)
	if got := parts["file"][0]; got != [2]string{"text/plain", "raw-text"} {
		t.Fatalf("collapsed contentMediaType string part = %v", got)
	}
	if got := parts["file_id"][0]; got != [2]string{"text/plain", "id-1"} {
		t.Fatalf("collapsed string part = %v", got)
	}
	if got := parts["options"][0]; got != [2]string{"application/json", `{"k":"v"}`} {
		t.Fatalf("collapsed object part = %v", got)
	}

	r, ct, err = buildMultipartBodyForRevision(doc, media, map[string]any{
		"file":    nil,
		"file_id": "id-2",
	}, BindingSpec)
	if err != nil {
		t.Fatal(err)
	}
	parts = parseMultipart(t, r, ct)
	if _, present := parts["file"]; present {
		t.Fatalf("null nullable part must be elided, got %v", parts["file"])
	}
	if got := parts["file_id"][0]; got != [2]string{"text/plain", "id-2"} {
		t.Fatalf("sibling part after elision = %v", got)
	}
}

// The urlencoded lane observes the same §9.2 rules: unconstrained
// properties take value-typed per-type defaults and a nullable-choice
// property's JSON null elides the field.
// The nullable-choice collapse is read under the 3.1 line, which is where a
// {"type": "null"} branch belongs; the type-absent field beside it was moved
// to the 3.0 line on 2026-08-17, because every accepted 3.1 edition states
// application/octet-stream for a part declaring no `type`.
func TestRevision3URLEncodedNullableChoiceProperties(t *testing.T) {
	doc := &openapi3.T{OpenAPI: "3.1.2"}
	media := &openapi3.MediaType{Schema: &openapi3.SchemaRef{Value: &openapi3.Schema{
		Type: &openapi3.Types{"object"},
		Properties: openapi3.Schemas{
			"tag": {Value: &openapi3.Schema{AnyOf: openapi3.SchemaRefs{
				{Value: &openapi3.Schema{Type: &openapi3.Types{"string"}}},
				{Value: &openapi3.Schema{Type: &openapi3.Types{"null"}}},
			}}},
		},
	}}}
	if err := validateRevision3URLEncodedMedia(doc, media); err != nil {
		t.Fatalf("urlencoded admission = %v", err)
	}
	body, err := buildURLEncodedBodyForRevision(doc, media, map[string]any{"tag": "t1"}, BindingSpec)
	if err != nil || body != "tag=t1" {
		t.Fatalf("urlencoded body = %q, %v", body, err)
	}
	if body, err = buildURLEncodedBodyForRevision(doc, media, map[string]any{"tag": nil}, BindingSpec); err != nil || body != "" {
		t.Fatalf("elided null = %q, %v", body, err)
	}
}

// §9.2's type-absent part cell on the urlencoded lane, both lines. It pinned
// the deleted 3.0-line value-keyed convention until 2026-08-20 (escalation
// M2); the two lines now refuse alike, each naming its own ground.
func TestRevision3URLEncodedTypeAbsentPropertyRefusesOnEveryEdition(t *testing.T) {
	media := &openapi3.MediaType{Schema: &openapi3.SchemaRef{Value: &openapi3.Schema{
		Type: &openapi3.Types{"object"},
		Properties: openapi3.Schemas{
			"note": {Value: &openapi3.Schema{Description: "free-form"}},
		},
	}}}
	for _, edition := range []string{"3.0.0", "3.0.1", "3.0.2", "3.0.3", "3.0.4"} {
		doc := &openapi3.T{OpenAPI: edition}
		if err := validateRevision3URLEncodedMedia(doc, media); err == nil ||
			!strings.Contains(err.Error(), "no default part Content-Type row on any accepted OAS 3.0 edition") {
			t.Fatalf("%s type-absent urlencoded property admission = %v", edition, err)
		}
		if _, err := buildURLEncodedBodyForRevision(doc, media, map[string]any{"note": "x y"}, BindingSpec); err == nil {
			t.Fatalf("%s type-absent urlencoded property encoded a body", edition)
		}
	}
	for _, edition := range []string{"3.1.0", "3.1.1", "3.1.2"} {
		doc := &openapi3.T{OpenAPI: edition}
		if err := validateRevision3URLEncodedMedia(doc, media); err == nil || !strings.Contains(err.Error(), "application/octet-stream") {
			t.Fatalf("%s type-absent urlencoded property admission = %v", edition, err)
		}
	}
}

func TestRevision3MultipartDefaultArraysAndStructuredStyle(t *testing.T) {
	doc := &openapi3.T{OpenAPI: "3.1.2"}
	arraySchema := &openapi3.Schema{Type: &openapi3.Types{"object"}, Properties: openapi3.Schemas{
		"tags": {Value: &openapi3.Schema{Type: &openapi3.Types{"array"}, Items: &openapi3.SchemaRef{Value: &openapi3.Schema{Type: &openapi3.Types{"string"}}}}},
	}}
	media := &openapi3.MediaType{Schema: &openapi3.SchemaRef{Value: arraySchema}}
	r, ct, err := buildMultipartBodyForRevision(doc, media, map[string]any{"tags": []any{"a", "b"}}, BindingSpec)
	if err != nil {
		t.Fatal(err)
	}
	parts := parseMultipart(t, r, ct)
	if !reflect.DeepEqual(parts["tags"], [][2]string{{"text/plain", "a"}, {"text/plain", "b"}}) {
		t.Fatalf("default array parts = %#v", parts["tags"])
	}

	explode := true
	objectSchema := &openapi3.Schema{Type: &openapi3.Types{"object"}, Properties: openapi3.Schemas{
		"meta": {Value: &openapi3.Schema{Type: &openapi3.Types{"object"}}},
	}}
	styled := &openapi3.MediaType{Schema: &openapi3.SchemaRef{Value: objectSchema}, Encoding: map[string]*openapi3.Encoding{
		"meta": {Style: "form", Explode: &explode, ContentType: "application/json"},
	}}
	r, ct, err = buildMultipartBodyForRevision(doc, styled, map[string]any{"meta": map[string]any{"a=b": "x=y"}}, BindingSpec)
	if err != nil {
		t.Fatal(err)
	}
	parts = parseMultipart(t, r, ct)
	if got := parts["a=b"]; len(got) != 1 || got[0][1] != "x=y" || got[0][0] != "" {
		t.Fatalf("structured multipart unit = %#v", parts)
	}
}

func TestRevision3EncodingStyleCellsRefuseUndefinedCombinations(t *testing.T) {
	doc := &openapi3.T{OpenAPI: "3.1.2"}
	falseValue := false
	trueValue := true
	tests := []struct {
		style    string
		explode  *bool
		typeName string
	}{
		{"deepObject", nil, "object"},
		{"deepObject", &falseValue, "object"},
		{"spaceDelimited", &trueValue, "array"},
		{"pipeDelimited", &falseValue, "string"},
	}
	for _, test := range tests {
		schema := &openapi3.Schema{Type: &openapi3.Types{"object"}, Properties: openapi3.Schemas{
			"value": {Value: &openapi3.Schema{Type: &openapi3.Types{test.typeName}}},
		}}
		media := &openapi3.MediaType{Schema: &openapi3.SchemaRef{Value: schema}, Encoding: map[string]*openapi3.Encoding{
			"value": {Style: test.style, Explode: test.explode},
		}}
		op := opWithRequestBody(openapi3.Content{"multipart/form-data": media}, true)
		if _, err := planRequestBodiesFor(doc, op, BindingSpec); err == nil {
			t.Errorf("accepted undefined style cell %s/%s explode=%v", test.style, test.typeName, test.explode)
		}
	}
}

func TestRevision3ParameterContentCharsetAndEscaping(t *testing.T) {
	for _, location := range []string{openapi3.ParameterInPath, openapi3.ParameterInQuery, openapi3.ParameterInHeader, openapi3.ParameterInCookie} {
		p := &openapi3.Parameter{Name: "p", In: location, Content: openapi3.Content{
			`text/plain; charset=iso-8859-1; note="a\z"`: {Schema: &openapi3.SchemaRef{Value: &openapi3.Schema{Type: &openapi3.Types{"string"}}}},
		}}
		r := &routedInput{resolvedPath: "/{p}", populated: map[string]map[string]bool{"query": {}, "header": {}, "cookie": {}}}
		if err := routeParameterFor(r, p, "é", BindingSpec); err != nil {
			t.Fatalf("%s content param: %v", location, err)
		}
		switch location {
		case openapi3.ParameterInPath:
			if r.resolvedPath != "/%E9" {
				t.Fatalf("path = %q", r.resolvedPath)
			}
		case openapi3.ParameterInQuery:
			if !reflect.DeepEqual(r.queryUnits, []string{"p=%E9"}) {
				t.Fatalf("query = %v", r.queryUnits)
			}
		case openapi3.ParameterInHeader:
			if len(r.headers) != 1 || r.headers[0][1] != string([]byte{0xe9}) {
				t.Fatalf("header = %v", r.headers)
			}
		case openapi3.ParameterInCookie:
			if len(r.cookieUnits) != 1 || r.cookieUnits[0] != "p="+string([]byte{0xe9}) {
				t.Fatalf("cookie = %v", r.cookieUnits)
			}
		}
	}

	p := &openapi3.Parameter{Name: "q", In: openapi3.ParameterInQuery, Schema: &openapi3.SchemaRef{Value: &openapi3.Schema{Type: &openapi3.Types{"string"}}}}
	r := &routedInput{populated: map[string]map[string]bool{"query": {}, "header": {}, "cookie": {}}}
	if err := routeParameterFor(r, p, `!*'()`, BindingSpec); err != nil {
		t.Fatal(err)
	}
	if got := r.queryUnits[0]; got != `q=%21%2A%27%28%29` {
		t.Fatalf("revision-3 unreserved escaping = %q", got)
	}
}

func TestRevision3PrivateLookingAuthorExtensionsDoNotCollide(t *testing.T) {
	spec := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://example.test"}],"paths":{"/x":{"get":{"operationId":"x","parameters":[{"name":"q","in":"query","schema":{"type":"string","x-openbindings-internal-boolean-schema":true}}],"responses":{"200":{"description":"ok","content":{"application/json":{"schema":{"type":"string","x-openbindings-internal-encoding-allow-reserved-present":true}}}}}}}}}`
	iface, err := NewSynthesizer().SynthesizeInterface(context.Background(), &synthesize.SynthesizeInput{Sources: []synthesize.SynthesizeSource{{BindingSpec: bindingSpecForTestDocument(spec), Content: openbindings.TextContent(spec)}}})
	if err != nil {
		t.Fatalf("authored extension collision: %v", err)
	}
	encoded, _ := json.Marshal(iface)
	if !strings.Contains(string(encoded), "x-openbindings-internal-boolean-schema") {
		t.Fatalf("authored extension was lost: %s", encoded)
	}
}

func TestRevision3InvalidResponseMediaDoesNotInventTextOutput(t *testing.T) {
	op := &openapi3.Operation{Responses: openapi3.NewResponses()}
	op.Responses.Set("200", &openapi3.ResponseRef{Value: &openapi3.Response{Content: openapi3.Content{"*": emptyMedia()}}})
	if output := buildOutputSchema(op, nil, BindingSpec); output != nil {
		t.Fatalf("invalid response media invented output schema %#v", output)
	}
}

func TestRevision3SSEUsesWHATWGUTF8AndEmptySuccessSkipsMedia(t *testing.T) {
	spec := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://example.test"}],"paths":{"/events":{"get":{"responses":{"200":{"description":"ok","content":{"text/event-stream":{"schema":{"type":"string"}}}}}}}}}`
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body := append([]byte("data: café\n\ndata: "), 0xff)
		body = append(body, []byte("\n\ndata: ")...)
		body = append(body, 0xff, 0xff)
		body = append(body, []byte("\n\ndata: ")...)
		body = append(body, 0xf0, 0x9f, 0x92)
		body = append(body, []byte("\n\n")...)
		return &http.Response{StatusCode: 200, Status: "200 OK", Header: http.Header{"Content-Type": {"text/event-stream; charset=iso-8859-1"}}, Body: io.NopCloser(strings.NewReader(string(body))), Request: req}, nil
	})
	call := NewInvokerWithClient(&http.Client{Transport: transport}).InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
		Source: invoke.InvocationSource{BindingSpec: bindingSpecForTestDocument(spec), Content: openbindings.TextContent(spec)}, Selector: "#/paths/~1events/get",
	})
	outputs, ierr := driveOutputs(context.Background(), call, nil)
	if ierr != nil || !reflect.DeepEqual(outputs, []any{"café", "�", "��", "�"}) {
		t.Fatalf("SSE UTF-8 replacement outputs = %#v, %v", outputs, ierr)
	}

	emptySpec := strings.Replace(spec, `,"content":{"text/event-stream":{"schema":{"type":"string"}}}`, "", 1)
	emptyTransport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Status: "200 OK", Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
	})
	call = NewInvokerWithClient(&http.Client{Transport: emptyTransport}).InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
		Source: invoke.InvocationSource{BindingSpec: bindingSpecForTestDocument(emptySpec), Content: openbindings.TextContent(emptySpec)}, Selector: "#/paths/~1events/get",
	})
	outputs, ierr = driveOutputs(context.Background(), call, nil)
	if ierr != nil || len(outputs) != 0 {
		t.Fatalf("empty successful SSE-labeled response = %#v, %v", outputs, ierr)
	}
}

func TestFullProfileResponseContentTypeMustBeSingleton(t *testing.T) {
	spec := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://example.test"}],"paths":{"/x":{"get":{"responses":{"200":{"description":"ok","content":{"application/json":{"schema":{"type":"object"}}}}}}}}}`
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Status: "200 OK", Header: http.Header{"Content-Type": {"application/json", "text/plain"}}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: req}, nil
	})
	call := NewInvokerWithClient(&http.Client{Transport: transport}).InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
		Source: invoke.InvocationSource{BindingSpec: bindingSpecForTestDocument(spec), Content: openbindings.TextContent(spec)}, Selector: "#/paths/~1x/get",
	})
	_, ierr := driveOutputs(context.Background(), call, nil)
	if ierr == nil || ierr.Code != invoke.ErrCodeProtocol {
		t.Fatalf("duplicate Content-Type error = %v", ierr)
	}
	emptyTransport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Status: "200 OK", Header: http.Header{"Content-Type": {"garbage", "also-garbage"}}, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
	})
	call = NewInvokerWithClient(&http.Client{Transport: emptyTransport}).InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
		Source: invoke.InvocationSource{BindingSpec: bindingSpecForTestDocument(spec), Content: openbindings.TextContent(spec)}, Selector: "#/paths/~1x/get",
	})
	outputs, ierr := driveOutputs(context.Background(), call, nil)
	if ierr != nil || len(outputs) != 0 {
		t.Fatalf("empty response consulted duplicate Content-Type: %#v, %v", outputs, ierr)
	}
	failureTransport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 500, Status: "500 Broken", Header: http.Header{"Content-Type": {"garbage", "also-garbage"}}, Body: io.NopCloser(strings.NewReader("failure")), Request: req}, nil
	})
	call = NewInvokerWithClient(&http.Client{Transport: failureTransport}).InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
		Source: invoke.InvocationSource{BindingSpec: bindingSpecForTestDocument(spec), Content: openbindings.TextContent(spec)}, Selector: "#/paths/~1x/get",
	})
	_, ierr = driveOutputs(context.Background(), call, nil)
	if ierr == nil || ierr.Code == invoke.ErrCodeProtocol || ierr.HasData() {
		t.Fatalf("non-2xx native failure was preempted by Content-Type: %v", ierr)
	}
	emptyFailureTransport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 500, Status: "500 Empty", Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
	})
	call = NewInvokerWithClient(&http.Client{Transport: emptyFailureTransport}).InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
		Source: invoke.InvocationSource{BindingSpec: bindingSpecForTestDocument(spec), Content: openbindings.TextContent(spec)}, Selector: "#/paths/~1x/get",
	})
	_, ierr = driveOutputs(context.Background(), call, nil)
	if ierr == nil || ierr.HasData() {
		t.Fatalf("empty native failure leaked abstract data: %v", ierr)
	}
}

type revision3CaptureTransport struct {
	requests int
	body     []byte
	header   http.Header
}

func (c *revision3CaptureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c.requests++
	c.header = req.Header.Clone()
	c.body, _ = io.ReadAll(req.Body)
	return &http.Response{
		StatusCode: 200,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		Request:    req,
	}, nil
}

func TestRevision3InvokerCarriesRawBytesAndRequiresRangeConfiguration(t *testing.T) {
	spec := `{"openapi":"3.0.3","info":{"title":"t","version":"1"},"servers":[{"url":"https://example.test"}],"paths":{"/asset":{"post":{"operationId":"putAsset","requestBody":{"required":true,"content":{"image/png":{"schema":{"type":"string","format":"binary"}}}},"responses":{"200":{"description":"ok","content":{"application/json":{"schema":{"type":"object"}}}}}}}}}`
	capture := &revision3CaptureTransport{}
	call := NewInvokerWithClient(&http.Client{Transport: capture}).InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
		Source:   invoke.InvocationSource{BindingSpec: bindingSpecForTestDocument(spec), Content: openbindings.TextContent(spec)},
		Selector: "#/paths/~1asset/post",
	})
	_, ierr := driveSingle(t, call, map[string]any{"body": "AP8BAg=="})
	if ierr != nil {
		t.Fatal(ierr)
	}
	if !reflect.DeepEqual(capture.body, []byte{0, 255, 1, 2}) || capture.header.Get("Content-Type") != "image/png" {
		t.Fatalf("captured body/content-type = (%v, %q)", capture.body, capture.header.Get("Content-Type"))
	}

	rangeSpec := strings.Replace(spec, `"image/png":{"schema":{"type":"string","format":"binary"}}`, `"application/*":{"schema":{"type":"object"}}`, 1)
	capture = &revision3CaptureTransport{}
	call = NewInvokerWithClient(&http.Client{Transport: capture}).InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
		Source:   invoke.InvocationSource{BindingSpec: bindingSpecForTestDocument(rangeSpec), Content: openbindings.TextContent(rangeSpec)},
		Selector: "#/paths/~1asset/post",
	})
	_, ierr = driveSingle(t, call, map[string]any{"body": map[string]any{"name": "pixel"}})
	if ierr == nil || ierr.Code != invoke.ErrCodeContextRequired || capture.requests != 0 {
		t.Fatalf("missing range configuration = %v after %d requests", ierr, capture.requests)
	}
	details := invoke.ContextRequiredFrom(ierr)
	if details == nil || len(details.Alternatives) != 1 || len(details.Alternatives[0].Requirements) != 1 || details.Alternatives[0].Requirements[0].Type != "config.value" {
		t.Fatalf("range context details = %#v", details)
	}

	capture = &revision3CaptureTransport{}
	call = NewInvokerWithClient(&http.Client{Transport: capture}).InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
		Source:   invoke.InvocationSource{BindingSpec: bindingSpecForTestDocument(rangeSpec), Content: openbindings.TextContent(rangeSpec)},
		Selector: "#/paths/~1asset/post",
		Context:  map[string]any{"configuration": map[string]any{"requestMedia": ""}},
	})
	_, ierr = driveSingle(t, call, map[string]any{"body": map[string]any{"name": "pixel"}})
	if ierr == nil || ierr.Code == invoke.ErrCodeContextRequired || capture.requests != 0 {
		t.Fatalf("empty requestMedia invocation = %v after %d requests", ierr, capture.requests)
	}

	capture = &revision3CaptureTransport{}
	call = NewInvokerWithClient(&http.Client{Transport: capture}).InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
		Source:   invoke.InvocationSource{BindingSpec: bindingSpecForTestDocument(rangeSpec), Content: openbindings.TextContent(rangeSpec)},
		Selector: "#/paths/~1asset/post",
		Context: map[string]any{"configuration": map[string]any{
			"requestMedia": "application/problem+json; profile=asset",
		}},
	})
	_, ierr = driveSingle(t, call, map[string]any{"body": map[string]any{"name": "pixel"}})
	if ierr != nil {
		t.Fatal(ierr)
	}
	if string(capture.body) != `{"name":"pixel"}` || capture.header.Get("Content-Type") != "application/problem+json; profile=asset" {
		t.Fatalf("configured range wire = (%q, %q)", capture.body, capture.header.Get("Content-Type"))
	}
}

func TestRevision3InvokerRejectsNoncanonicalRawBase64BeforeDispatch(t *testing.T) {
	spec := `{"openapi":"3.0.3","info":{"title":"t","version":"1"},"servers":[{"url":"https://example.test"}],"paths":{"/asset":{"post":{"operationId":"putAsset","requestBody":{"required":true,"content":{"image/png":{"schema":{"type":"string","format":"binary"}}}},"responses":{"204":{"description":"ok"}}}}}}`
	for _, value := range []string{"YQ", "AB=="} {
		capture := &revision3CaptureTransport{}
		call := NewInvokerWithClient(&http.Client{Transport: capture}).InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
			Source:   invoke.InvocationSource{BindingSpec: bindingSpecForTestDocument(spec), Content: openbindings.TextContent(spec)},
			Selector: "#/paths/~1asset/post",
		})
		_, ierr := driveSingle(t, call, map[string]any{"body": value})
		if ierr == nil || ierr.Code != invoke.ErrCodeRefused || capture.requests != 0 {
			t.Errorf("noncanonical %q = %v after %d requests", value, ierr, capture.requests)
		}
	}
}
