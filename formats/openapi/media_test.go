package openapi

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"reflect"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

func opWithRequestBody(content openapi3.Content, required bool) *openapi3.Operation {
	return &openapi3.Operation{
		RequestBody: &openapi3.RequestBodyRef{
			Value: &openapi3.RequestBody{Required: required, Content: content},
		},
	}
}

func emptyMedia() *openapi3.MediaType { return &openapi3.MediaType{} }

// stringMedia declares the `type: string` governing schema §9.2's
// string-carriage lane is scoped on.
func stringMedia() *openapi3.MediaType {
	return &openapi3.MediaType{Schema: &openapi3.SchemaRef{Value: &openapi3.Schema{Type: &openapi3.Types{"string"}}}}
}

// OAPI-P-04 gives artifact alternatives no preference. planRequestBody's
// declaration-identity sort is only the reference SDK's deterministic
// convenience for callers that request one candidate; invocation preserves
// and tests the full candidate set.
func TestPlanRequestBody_SelectionOrder(t *testing.T) {
	cases := []struct {
		name       string
		content    openapi3.Content
		wantType   string
		wantFamily string
	}{
		{
			"exact json wins over everything",
			openapi3.Content{
				"application/json":                  emptyMedia(),
				"application/vnd.api+json":          emptyMedia(),
				"multipart/form-data":               emptyMedia(),
				"application/x-www-form-urlencoded": emptyMedia(),
				"text/plain":                        emptyMedia(),
			},
			"application/json", familyJSON,
		},
		{
			"lexicographically least +json",
			openapi3.Content{
				"application/vnd.b+json": emptyMedia(),
				"application/vnd.a+json": emptyMedia(),
				"multipart/form-data":    emptyMedia(),
			},
			"application/vnd.a+json", familyJSON,
		},
		{
			// §9.2's string-carriage lane is declaration-scoped (ruled
			// 2026-08-15): the governing schema, not the media type, selects
			// it. A schema-omitted text declaration is not a string
			// declaration and takes the artifact-authorized byte lanes.
			"string-schema text last",
			openapi3.Content{"text/plain": stringMedia()},
			"text/plain", familyText,
		},
		{
			"parameters remain part of candidate identity and content type",
			openapi3.Content{"application/JSON; charset=utf-8": emptyMedia()},
			"application/json; charset=utf-8", familyJSON,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := planRequestBody(opWithRequestBody(tc.content, false))
			if err != nil {
				t.Fatalf("planRequestBody: %v", err)
			}
			if plan.mediaType != tc.wantType || plan.family != tc.wantFamily {
				t.Errorf("selected (%q, %s), want (%q, %s)", plan.mediaType, plan.family, tc.wantType, tc.wantFamily)
			}
		})
	}
}

// Declarations with no definition-complete form carriage refuse loudly.
func TestPlanRequestBody_UnderdefinedFormCarriageRefuses(t *testing.T) {
	cases := []openapi3.Content{
		{"application/x-www-form-urlencoded": emptyMedia()},
		{"multipart/form-data": emptyMedia()},
	}
	for _, content := range cases {
		if _, err := planRequestBody(opWithRequestBody(content, false)); err == nil {
			t.Errorf("content %v must refuse selection", content)
		}
	}
}

// A non-object JSON body schema makes the plan synthetic (§9.1).
func TestPlanRequestBody_SyntheticModes(t *testing.T) {
	arraySchema := &openapi3.SchemaRef{Value: &openapi3.Schema{Type: &openapi3.Types{"array"}}}
	objectSchema := &openapi3.SchemaRef{Value: &openapi3.Schema{
		Type:       &openapi3.Types{"object"},
		Properties: openapi3.Schemas{"a": {Value: &openapi3.Schema{Type: &openapi3.Types{"string"}}}},
	}}

	plan, err := planRequestBody(opWithRequestBody(openapi3.Content{
		"application/json": &openapi3.MediaType{Schema: arraySchema},
	}, false))
	if err != nil || !plan.synthetic {
		t.Errorf("array body schema must be synthetic, got %+v (%v)", plan, err)
	}

	plan, err = planRequestBody(opWithRequestBody(openapi3.Content{
		"application/json": &openapi3.MediaType{Schema: objectSchema},
	}, false))
	if err != nil || plan.synthetic {
		t.Errorf("object body schema must not be synthetic, got %+v (%v)", plan, err)
	}
	if !plan.props["a"] {
		t.Errorf("object plan must carry declared property names, got %v", plan.props)
	}

	// A string-declared text body always rides the synthetic lane.
	plan, err = planRequestBody(opWithRequestBody(openapi3.Content{"text/plain": stringMedia()}, false))
	if err != nil || !plan.synthetic {
		t.Errorf("string-schema text/plain must be synthetic, got %+v (%v)", plan, err)
	}

	// §9.1's object determination is declaration-only, one predicate shared
	// with synthesis (bodySchemaFlattens): a TYPELESS schema — neither
	// `properties` nor an explicit object type — is non-object, so the plan
	// is synthetic exactly as the synthesized contract wraps it.
	typelessSchema := &openapi3.SchemaRef{Value: &openapi3.Schema{}}
	plan, err = planRequestBody(opWithRequestBody(openapi3.Content{
		"application/json": &openapi3.MediaType{Schema: typelessSchema},
	}, false))
	if err != nil || !plan.synthetic {
		t.Errorf("typeless body schema must be synthetic, got %+v (%v)", plan, err)
	}

	// The other half of the declaration: `properties` without a type is
	// object by declaration — flattened, never synthetic.
	propsNoTypeSchema := &openapi3.SchemaRef{Value: &openapi3.Schema{
		Properties: openapi3.Schemas{"a": {Value: &openapi3.Schema{Type: &openapi3.Types{"string"}}}},
	}}
	plan, err = planRequestBody(opWithRequestBody(openapi3.Content{
		"application/json": &openapi3.MediaType{Schema: propsNoTypeSchema},
	}, false))
	if err != nil || plan.synthetic {
		t.Errorf("properties-without-type schema must not be synthetic, got %+v (%v)", plan, err)
	}
	if !plan.props["a"] {
		t.Errorf("properties-without-type plan must carry declared property names, got %v", plan.props)
	}

	// A 3.1 two-element type array is not an EXPLICIT object type (only
	// the single-element form is): synthetic without properties.
	nullableObjectSchema := &openapi3.SchemaRef{Value: &openapi3.Schema{Type: &openapi3.Types{"object", "null"}}}
	plan, err = planRequestBody(opWithRequestBody(openapi3.Content{
		"application/json": &openapi3.MediaType{Schema: nullableObjectSchema},
	}, false))
	if err != nil || !plan.synthetic {
		t.Errorf("nullable-object schema without properties must be synthetic, got %+v (%v)", plan, err)
	}
}

// The remaining-body rule (§9.1): JSON-family selection with every field
// consumed by parameters sends {} when required, omits the body otherwise.
func TestBuildRequestBody_RemainingBodyRule(t *testing.T) {
	doc := &openapi3.T{OpenAPI: "3.0.3"}
	routed := &routedInput{bodyFields: map[string]any{}}

	planRequired := &bodyPlan{declared: true, required: true, family: familyJSON, mediaType: "application/json"}
	r, ct, err := buildRequestBody(doc, planRequired, routed)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r)
	if string(b) != "{}" || ct != "application/json" {
		t.Errorf("required empty body = (%q, %q), want ({}, application/json)", b, ct)
	}

	planOptional := &bodyPlan{declared: true, required: false, family: familyJSON, mediaType: "application/json"}
	r, ct, err = buildRequestBody(doc, planOptional, routed)
	if err != nil {
		t.Fatal(err)
	}
	if r != nil || ct != "" {
		t.Errorf("optional empty body must be omitted, got (%v, %q)", r, ct)
	}
}

// Synthetic unwrap: the `body` property's value IS the request body (§9.1).
func TestBuildRequestBody_SyntheticUnwrap(t *testing.T) {
	doc := &openapi3.T{OpenAPI: "3.0.3"}
	plan := &bodyPlan{declared: true, family: familyJSON, mediaType: "application/json", synthetic: true}
	routed := &routedInput{bodyValue: []any{float64(1), float64(2)}, bodySet: true}
	r, ct, err := buildRequestBody(doc, plan, routed)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r)
	if string(b) != "[1,2]" || ct != "application/json" {
		t.Errorf("synthetic body = (%q, %q), want ([1,2], application/json)", b, ct)
	}
}

// text/plain's selection condition: a non-string body value is a loud
// refusal (OAPI-P-04).
func TestBuildRequestBody_TextPlainConditions(t *testing.T) {
	doc := &openapi3.T{OpenAPI: "3.0.3"}
	plan := &bodyPlan{declared: true, family: familyText, mediaType: "text/plain", synthetic: true}

	r, ct, err := buildRequestBody(doc, plan, &routedInput{bodyValue: "hello", bodySet: true})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r)
	if string(b) != "hello" || ct != "text/plain" {
		t.Errorf("text body = (%q, %q)", b, ct)
	}

	if _, _, err := buildRequestBody(doc, plan, &routedInput{bodyValue: float64(42), bodySet: true}); err == nil {
		t.Error("text/plain with a non-string body value must refuse")
	}
}

// parseMultipart splits a built multipart body into part name → (content-type, bytes).
func parseMultipart(t *testing.T, r io.Reader, contentType string) map[string][][2]string {
	t.Helper()
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		t.Fatalf("parse content type: %v", err)
	}
	mr := multipart.NewReader(r, params["boundary"])
	parts := map[string][][2]string{}
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			return parts
		}
		if err != nil {
			t.Fatalf("next part: %v", err)
		}
		data, _ := io.ReadAll(p)
		parts[p.FormName()] = append(parts[p.FormName()], [2]string{p.Header.Get("Content-Type"), string(data)})
	}
}

// 3.0.x: format:binary signals a binary part; with no declarable encoding in
// 3.0, the caller's string is Base64-decoded (the boundary encoding).
func TestBuildMultipartBody_30BinaryBase64(t *testing.T) {
	doc := &openapi3.T{OpenAPI: "3.0.3"}
	media := &openapi3.MediaType{
		Schema: &openapi3.SchemaRef{Value: &openapi3.Schema{
			Type: &openapi3.Types{"object"},
			Properties: openapi3.Schemas{
				"file": {Value: &openapi3.Schema{Type: &openapi3.Types{"string"}, Format: "binary"}},
				"desc": {Value: &openapi3.Schema{Type: &openapi3.Types{"string"}}},
			},
		}},
	}
	fields := map[string]any{
		"file": base64.StdEncoding.EncodeToString([]byte("raw-bytes")),
		"desc": "a file",
	}
	r, ct, err := buildMultipartBody(doc, media, fields)
	if err != nil {
		t.Fatal(err)
	}
	parts := parseMultipart(t, r, ct)
	if got := parts["file"][0]; got[0] != "application/octet-stream" || got[1] != "raw-bytes" {
		t.Errorf("file part = %v, want decoded raw-bytes as application/octet-stream", got)
	}
	if got := parts["desc"][0][1]; got != "a file" {
		t.Errorf("desc part = %q", got)
	}

	// An invalid base64 string is a loud error, never silent bytes.
	if _, _, err := buildMultipartBody(doc, media, map[string]any{"file": "!!not-base64!!"}); err == nil {
		t.Error("invalid base64 for a binary-signaled part must refuse")
	}
}

// The Accept header advertises the declared concrete media types of the
// SUCCESS responses (2xx literals + 2XX, plus default only when it can
// actually govern an otherwise-unmatched 2xx); ranges are not concrete and
// absent declarations do not invent application/json (§9.2, §8).
func TestAcceptHeader(t *testing.T) {
	op := &openapi3.Operation{Responses: openapi3.NewResponses()}
	op.Responses.Set("200", &openapi3.ResponseRef{Value: &openapi3.Response{
		Content: openapi3.Content{"application/json": emptyMedia(), "text/event-stream": emptyMedia()},
	}})
	op.Responses.Set("2XX", &openapi3.ResponseRef{Value: &openapi3.Response{
		Content: openapi3.Content{"text/csv": emptyMedia()},
	}})
	op.Responses.Set("404", &openapi3.ResponseRef{Value: &openapi3.Response{
		Content: openapi3.Content{"application/problem+json": emptyMedia()},
	}})
	op.Responses.Set("default", &openapi3.ResponseRef{Value: &openapi3.Response{
		Content: openapi3.Content{"application/xml": emptyMedia(), "*/*": emptyMedia()},
	}})

	got := successMediaTypes(op)
	want := []string{"application/json", "text/csv", "text/event-stream"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("successMediaTypes = %v, want %v (404 excluded; 2XX shadows default; media ranges excluded)", got, want)
	}
	if !isStreamingCapable(op) {
		t.Error("operation declaring text/event-stream on a success response is streaming-capable")
	}

	empty := &openapi3.Operation{Responses: openapi3.NewResponses()}
	empty.Responses.Set("204", &openapi3.ResponseRef{Value: &openapi3.Response{}})
	if got := acceptHeader(empty); got != "" {
		t.Errorf("acceptHeader with no declared media = %q, want omission", got)
	}
	if isStreamingCapable(empty) {
		t.Error("operation with no declared media is not streaming-capable")
	}

	// A default-only response can govern a successful status and therefore
	// does confer streaming capability.
	defOnly := &openapi3.Operation{Responses: openapi3.NewResponses()}
	defOnly.Responses.Set("default", &openapi3.ResponseRef{Value: &openapi3.Response{
		Content: openapi3.Content{"text/event-stream": emptyMedia()},
	}})
	if !isStreamingCapable(defOnly) {
		t.Error("a default entry that can govern a 2xx participates in shape determination (§8)")
	}
}

// §9.2's string-carriage lane, ruled 2026-08-15: every concrete non-JSON,
// non-form selection whose GOVERNING SCHEMA resolves to type: string carries
// the supplied string. The lane is stated once for every such media type
// rather than as a list of admitted subtypes, and it is selected by the
// declaration — never by the media type's primary type, and never by the
// caller's value.
func TestRequestStringCarriage_DeclarationScoped(t *testing.T) {
	doc31 := &openapi3.T{OpenAPI: "3.1.2"}
	doc30 := &openapi3.T{OpenAPI: "3.0.4"}
	stringSchema := func() *openapi3.SchemaRef {
		return &openapi3.SchemaRef{Value: &openapi3.Schema{Type: &openapi3.Types{"string"}}}
	}
	objectSchema := func() *openapi3.SchemaRef {
		return &openapi3.SchemaRef{Value: &openapi3.Schema{
			Type:       &openapi3.Types{"object"},
			Properties: openapi3.Schemas{"a": {Value: &openapi3.Schema{Type: &openapi3.Types{"string"}}}},
		}}
	}

	for _, media := range []string{"text/plain", "text/csv", "text/markdown", "text/x-markdown", "application/xml", "text/xml", "application/x-custom"} {
		plans, err := planRequestBodiesFor(doc31, opWithRequestBody(openapi3.Content{
			media: &openapi3.MediaType{Schema: stringSchema()},
		}, true), BindingSpec)
		if err != nil || len(plans) != 1 {
			t.Fatalf("string-declared %s: plans=%d err=%v", media, len(plans), err)
		}
		if plans[0].family != familyText || !plans[0].synthetic {
			t.Errorf("string-declared %s: family=%s synthetic=%v, want text/synthetic", media, plans[0].family, plans[0].synthetic)
		}
	}

	// No lane builds a document from an object model, so an object-declared
	// selection outside the JSON and form lanes selects nothing at all.
	for _, media := range []string{"text/plain", "text/csv", "text/json", "application/xml", "text/xml"} {
		if _, err := planRequestBodiesFor(doc31, opWithRequestBody(openapi3.Content{
			media: &openapi3.MediaType{Schema: objectSchema()},
		}, true), BindingSpec); err == nil {
			t.Errorf("object-declared %s must select no carriage lane", media)
		}
	}

	// The incumbent text lane's value-dependent test is gone: a declaration
	// that does not resolve to type: string selects nothing, whatever the
	// caller later supplies.
	anyOfSchema := &openapi3.SchemaRef{Value: &openapi3.Schema{AnyOf: openapi3.SchemaRefs{
		{Value: &openapi3.Schema{Type: &openapi3.Types{"string"}}},
		{Value: &openapi3.Schema{Type: &openapi3.Types{"object"}}},
	}}}
	if _, err := planRequestBodiesFor(doc31, opWithRequestBody(openapi3.Content{
		"text/plain": &openapi3.MediaType{Schema: anyOfSchema},
	}, true), BindingSpec); err == nil {
		t.Error("a two-branch anyOf text declaration must select no carriage lane")
	}

	// The artifact-authorized byte lanes are evaluated first and carry no
	// text carve-out: a schema-omitted text declaration is a schema-omitted
	// declaration like any other, not an orphan between two lanes.
	plans, err := planRequestBodiesFor(doc31, opWithRequestBody(openapi3.Content{
		"text/csv": &openapi3.MediaType{},
	}, true), BindingSpec)
	if err != nil || len(plans) != 1 || plans[0].family != familyOctets {
		t.Errorf("schema-omitted text/csv must take the raw lane, got %d plans (%v)", len(plans), err)
	}

	// OAS 3.0 `format: binary` is the artifact declaring octets; it wins.
	binarySchema := &openapi3.SchemaRef{Value: &openapi3.Schema{Type: &openapi3.Types{"string"}, Format: "binary"}}
	plans, err = planRequestBodiesFor(doc30, opWithRequestBody(openapi3.Content{
		"text/csv": &openapi3.MediaType{Schema: binarySchema},
	}, true), BindingSpec)
	if err != nil || len(plans) != 1 || plans[0].family != familyOctets {
		t.Errorf("OAS 3.0 binary text/csv must take the raw lane, got %d plans (%v)", len(plans), err)
	}

	// The charset rule applies to the whole family, not just text/plain.
	if _, err := planRequestBodiesFor(doc31, opWithRequestBody(openapi3.Content{
		"text/csv; charset=shift_jis": &openapi3.MediaType{Schema: stringSchema()},
	}, true), BindingSpec); err == nil {
		t.Error("an unsupported charset must refuse for the whole string-carriage family")
	}
	plans, err = planRequestBodiesFor(doc31, opWithRequestBody(openapi3.Content{
		"text/csv; charset=iso-8859-1": &openapi3.MediaType{Schema: stringSchema()},
	}, true), BindingSpec)
	if err != nil || len(plans) != 1 {
		t.Fatalf("iso-8859-1 text/csv: plans=%d err=%v", len(plans), err)
	}
	body, contentType, err := buildRequestBody(doc31, plans[0], &routedInput{bodySet: true, bodyValue: "é"})
	if err != nil {
		t.Fatalf("build iso-8859-1 body: %v", err)
	}
	if contentType != "text/csv; charset=iso-8859-1" {
		t.Errorf("Content-Type = %q", contentType)
	}
	encoded, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) != 1 || encoded[0] != 0xe9 {
		t.Errorf("emitted bytes = %v, want [233]", encoded)
	}

	// A non-string supplied value is a refusal against the artifact's own
	// declaration, and the diagnostic names that declaration.
	plans, err = planRequestBodiesFor(doc31, opWithRequestBody(openapi3.Content{
		"application/xml": &openapi3.MediaType{Schema: stringSchema()},
	}, true), BindingSpec)
	if err != nil || len(plans) != 1 {
		t.Fatalf("application/xml string plan: %d (%v)", len(plans), err)
	}
	_, _, err = buildRequestBody(doc31, plans[0], &routedInput{bodySet: true, bodyValue: 42})
	if err == nil || !strings.Contains(err.Error(), "request media application/xml declares a string body") {
		t.Errorf("non-string value error = %v", err)
	}

	// An unconstrained boolean `true` schema declares no type, so it selects
	// no lane: the string lane needs `type: string` and OAS 3.1's raw lane
	// needs an OMITTED schema. This narrows what the incumbent unconditional
	// text/plain lane accepted; the corner is filed for a ruling rather than
	// repaired by inventing an "unconstrained schema" admissibility rule the
	// 2026-08-15 ruling does not state. Twin-pinned with the TypeScript
	// "selects no lane for an unconstrained boolean text schema" case.
	if _, err := planRequestBodiesFor(doc31, opWithRequestBody(openapi3.Content{
		"text/plain": &openapi3.MediaType{Schema: booleanTrueSchemaRef(t)},
	}, true), BindingSpec); err == nil {
		t.Error("an unconstrained boolean text declaration must select no carriage lane")
	}
}

// booleanTrueSchemaRef builds the JSON Schema boolean `true` literal in the
// structural form kin-openapi round-trips it through.
func booleanTrueSchemaRef(t *testing.T) *openapi3.SchemaRef {
	t.Helper()
	schema := &openapi3.Schema{}
	if err := json.Unmarshal([]byte(`{"anyOf":[{},{"not":{}}]}`), schema); err != nil {
		t.Fatalf("build boolean schema: %v", err)
	}
	if _, boolean := booleanSchemaLiteral(schema); !boolean {
		t.Fatalf("constructed schema is not the boolean literal form")
	}
	return &openapi3.SchemaRef{Value: schema}
}
