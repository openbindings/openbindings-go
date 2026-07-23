package openapi

import (
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"reflect"
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
			"lexical fallback does not invent family preference",
			openapi3.Content{
				"application/x-www-form-urlencoded": emptyMedia(),
				"multipart/form-data":               emptyMedia(),
			},
			"application/x-www-form-urlencoded", familyURLEncoded,
		},
		{
			"urlencoded over text/plain",
			openapi3.Content{
				"text/plain":                        emptyMedia(),
				"application/x-www-form-urlencoded": emptyMedia(),
			},
			"application/x-www-form-urlencoded", familyURLEncoded,
		},
		{
			"text/plain last",
			openapi3.Content{"text/plain": emptyMedia()},
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

// Out-of-family declarations (a raw binary body, ranges only) refuse loudly
// pre-dispatch (OAPI-P-04).
func TestPlanRequestBody_OutOfFamilyRefuses(t *testing.T) {
	cases := []openapi3.Content{
		{"application/octet-stream": emptyMedia()},
		{"image/png": emptyMedia()},
		{"*/*": emptyMedia()},           // a range is not a concrete declaration
		{"application/*": emptyMedia()}, // ditto
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

	// text/plain always rides the synthetic lane.
	plan, err = planRequestBody(opWithRequestBody(openapi3.Content{"text/plain": emptyMedia()}, false))
	if err != nil || !plan.synthetic {
		t.Errorf("text/plain must be synthetic, got %+v (%v)", plan, err)
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

// 3.1.x: a string schema carrying contentMediaType/contentEncoding signals
// binary; a declared contentEncoding decides the decode, and the declared
// contentMediaType rides as the part's content type.
func TestBuildMultipartBody_31ContentKeywords(t *testing.T) {
	doc := &openapi3.T{OpenAPI: "3.1.0"}
	media := &openapi3.MediaType{
		Schema: &openapi3.SchemaRef{Value: &openapi3.Schema{
			Type: &openapi3.Types{"object"},
			Properties: openapi3.Schemas{
				"img": {Value: &openapi3.Schema{
					Type: &openapi3.Types{"string"},
					Extensions: map[string]any{
						"contentMediaType": "image/png",
						"contentEncoding":  "base64url",
					},
				}},
			},
		}},
	}
	payload := base64.URLEncoding.EncodeToString([]byte{0xff, 0xfe})
	r, ct, err := buildMultipartBody(doc, media, map[string]any{"img": payload})
	if err != nil {
		t.Fatal(err)
	}
	parts := parseMultipart(t, r, ct)
	got := parts["img"][0]
	if got[0] != "image/png" {
		t.Errorf("img part content type = %q, want image/png", got[0])
	}
	if got[1] != string([]byte{0xff, 0xfe}) {
		t.Errorf("img part bytes not decoded per contentEncoding base64url")
	}
}

// Parts that are not binary-signaled follow the per-type defaults: objects
// as application/json parts, primitives as plain fields; the encoding
// object's contentType overrides; declared arrays expand into repeated parts.
func TestBuildMultipartBody_PartDefaultsAndEncoding(t *testing.T) {
	doc := &openapi3.T{OpenAPI: "3.0.3"}
	media := &openapi3.MediaType{
		Schema: &openapi3.SchemaRef{Value: &openapi3.Schema{
			Type: &openapi3.Types{"object"},
			Properties: openapi3.Schemas{
				"meta":  {Value: &openapi3.Schema{Type: &openapi3.Types{"object"}}},
				"count": {Value: &openapi3.Schema{Type: &openapi3.Types{"integer"}}},
				"tags":  {Value: &openapi3.Schema{Type: &openapi3.Types{"array"}, Items: &openapi3.SchemaRef{Value: &openapi3.Schema{Type: &openapi3.Types{"string"}}}}},
				"note":  {Value: &openapi3.Schema{Type: &openapi3.Types{"string"}}},
			},
		}},
		Encoding: map[string]*openapi3.Encoding{
			"note": {ContentType: "text/markdown"},
		},
	}
	fields := map[string]any{
		"meta":  map[string]any{"k": "v"},
		"count": float64(42),
		"tags":  []any{"a", "b"},
		"note":  "# hi",
	}
	r, ct, err := buildMultipartBody(doc, media, fields)
	if err != nil {
		t.Fatal(err)
	}
	parts := parseMultipart(t, r, ct)
	if got := parts["meta"][0]; got[0] != "application/json" || got[1] != `{"k":"v"}` {
		t.Errorf("meta part = %v, want application/json object", got)
	}
	if got := parts["count"][0][1]; got != "42" {
		t.Errorf("count part = %q, want 42", got)
	}
	if len(parts["tags"]) != 2 || parts["tags"][0][1] != "a" || parts["tags"][1][1] != "b" {
		t.Errorf("tags must expand into repeated parts, got %v", parts["tags"])
	}
	if got := parts["note"][0]; got[0] != "text/markdown" || got[1] != "# hi" {
		t.Errorf("note part = %v, want text/markdown verbatim", got)
	}
}

// A Go []byte value passes through raw (in-process convenience; it cannot
// have arrived as JSON).
func TestBuildMultipartBody_ByteSlicePassthrough(t *testing.T) {
	doc := &openapi3.T{OpenAPI: "3.0.3"}
	media := &openapi3.MediaType{
		Schema: &openapi3.SchemaRef{Value: &openapi3.Schema{
			Type: &openapi3.Types{"object"},
			Properties: openapi3.Schemas{
				"file": {Value: &openapi3.Schema{Type: &openapi3.Types{"string"}, Format: "binary"}},
			},
		}},
	}
	r, ct, err := buildMultipartBody(doc, media, map[string]any{"file": []byte{0x01, 0x02}})
	if err != nil {
		t.Fatal(err)
	}
	parts := parseMultipart(t, r, ct)
	if got := parts["file"][0][1]; got != string([]byte{0x01, 0x02}) {
		t.Errorf("byte-slice part = %x, want raw passthrough", got)
	}
}

// urlencoded bodies serialize per the OAS encoding rules: form/explode=true
// default, overridable per field.
func TestBuildURLEncodedBody(t *testing.T) {
	media := &openapi3.MediaType{
		Encoding: map[string]*openapi3.Encoding{
			"tags": {Style: "pipeDelimited", Explode: openapi3.Ptr(false)},
		},
	}
	fields := map[string]any{
		"name": "a b",
		"ids":  []any{float64(1), float64(2)},
		"tags": []any{"x", "y"},
	}
	got, err := buildURLEncodedBody(media, fields)
	if err != nil {
		t.Fatal(err)
	}
	// Sorted field order: ids (form explode default → repeated), name, tags
	// (pipeDelimited non-explode via the encoding object).
	want := "ids=1&ids=2&name=a%20b&tags=x|y"
	if got != want {
		t.Errorf("urlencoded body = %q, want %q", got, want)
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
