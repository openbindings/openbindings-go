package openapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/invoke"
)

func boolPointer(value bool) *bool { return &value }

func TestParameterConversionIsConfiguredAndRecursive(t *testing.T) {
	value := map[string]any{
		"flag": true,
		"list": []any{json.Number("2.5"), "already-text"},
	}
	if _, err := convertParameterScalars(value, nil, false); err == nil || !strings.Contains(err.Error(), "parameterConversion") {
		t.Fatalf("missing conversion error = %v", err)
	}

	converted, err := convertParameterScalars(value, func(value any) (string, error) {
		return fmt.Sprintf("configured<%v>", value), nil
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"flag": "configured<true>",
		"list": []any{"configured<2.5>", "already-text"},
	}
	if !reflect.DeepEqual(converted, want) {
		t.Fatalf("converted = %#v, want %#v", converted, want)
	}
}

func TestOpenAPI30ContentFormConversionAndNullableDisposition(t *testing.T) {
	media := &openapi3.MediaType{Schema: &openapi3.SchemaRef{Value: openapi3.NewObjectSchema().
		WithProperty("count", openapi3.NewIntegerSchema()).
		WithProperty("note", &openapi3.Schema{Type: &openapi3.Types{"string"}, Nullable: true})}}
	plan := &bodyPlan{
		bindingSpec: BindingSpecOpenAPI30,
		oas30:       true,
		family:      familyURLEncoded,
		media:       media,
	}
	if _, err := prepareEncodingStylePropertyValue(plan, "count", float64(7), BindingSpecOpenAPI30, nil); err == nil || !strings.Contains(err.Error(), "parameterConversion") {
		t.Fatalf("missing content-form conversion error = %v", err)
	}
	converted, err := prepareEncodingStylePropertyValue(plan, "count", float64(7), BindingSpecOpenAPI30, func(any) (string, error) {
		return "seven", nil
	})
	if err != nil || converted != "seven" {
		t.Fatalf("content-form conversion = (%#v, %v), want seven", converted, err)
	}

	note := media.Schema.Value.Properties["note"].Value
	collapsed, nullable := effectiveRevision3PartSchema(note, true)
	if !nullable || collapsed == nil || !resolveDeclaration(collapsed, true).declaresOnly("string") {
		t.Fatalf("3.0 nullable form declaration = (%#v, %v)", collapsed, nullable)
	}
	body, err := buildURLEncodedBodyForRevision(
		&openapi3.T{OpenAPI: "3.0.4"}, media, map[string]any{"note": nil}, BindingSpecOpenAPI30,
	)
	if err != nil || body != "" {
		t.Fatalf("nullable content-form body = %q, %v; want elided", body, err)
	}
}

func TestOpenAPI30ParameterConversionAndNullConverged(t *testing.T) {
	parameter := &openapi3.Parameter{
		Name: "q", In: openapi3.ParameterInQuery,
		Schema: &openapi3.SchemaRef{Value: openapi3.NewArraySchema().WithItems(openapi3.NewIntegerSchema())},
	}
	if _, err := prepareStyleValue(parameter.Name, []any{float64(7)}, openapi3.SerializationForm, BindingSpecOpenAPI30, nil); err == nil || !strings.Contains(err.Error(), "parameterConversion") {
		t.Fatalf("missing 3.0 conversion error = %v", err)
	}
	if _, err := prepareStyleValue(parameter.Name, []any{float64(7), nil}, openapi3.SerializationForm, BindingSpecOpenAPI30, func(value any) (string, error) {
		return fmt.Sprintf("converted<%v>", value), nil
	}); err == nil || !strings.Contains(err.Error(), "null array/object member") {
		t.Fatalf("3.0 null-member error = %v", err)
	}
}

func TestParameterNullWholeValueMatrix(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		style string
		wire  string
	}{
		{name: "matrix", style: openapi3.SerializationMatrix, wire: ";id"},
		{name: "label", style: openapi3.SerializationLabel, wire: "."},
		{name: "simple", style: openapi3.SerializationSimple, wire: ""},
		{name: "form", style: openapi3.SerializationForm, wire: "id="},
	} {
		for _, bindingSpec := range []string{BindingSpecOpenAPI30, BindingSpecOpenAPI31} {
			t.Run(testCase.name+"/"+bindingSpec, func(t *testing.T) {
				prepared, err := prepareStyleValue("id", nil, testCase.style, bindingSpec, nil)
				if err != nil {
					t.Fatal(err)
				}
				var wire string
				switch testCase.style {
				case openapi3.SerializationForm:
					units, err := serializeQueryValueForRevision("id", prepared, testCase.style, true, false, bindingSpec, false)
					if err != nil {
						t.Fatal(err)
					}
					wire = strings.Join(units, "&")
				default:
					wire, err = serializePathValueForRevision("id", prepared, testCase.style, false, bindingSpec)
					if err != nil {
						t.Fatal(err)
					}
				}
				if wire != testCase.wire {
					t.Fatalf("wire = %q, want %q", wire, testCase.wire)
				}
			})
		}
	}

	for _, style := range []string{openapi3.SerializationSpaceDelimited, openapi3.SerializationPipeDelimited, openapi3.SerializationDeepObject} {
		for _, bindingSpec := range []string{BindingSpecOpenAPI30, BindingSpecOpenAPI31} {
			if _, err := prepareStyleValue("id", nil, style, bindingSpec, nil); err == nil {
				t.Errorf("%s null unexpectedly admitted for style %q", bindingSpec, style)
			}
		}
	}
	if _, err := convertParameterScalars([]any{"ok", nil}, nil, false); err == nil || !strings.Contains(err.Error(), "null") {
		t.Fatalf("null-member error = %v", err)
	}
}

func TestNonRFCStyleDelimitersAreRefusedBeforeExpansion(t *testing.T) {
	for _, testCase := range []struct {
		style string
		name  string
		value any
	}{
		{style: openapi3.SerializationSpaceDelimited, name: "q", value: []any{"contains space"}},
		{style: openapi3.SerializationPipeDelimited, name: "q", value: []any{"contains|pipe"}},
		{style: openapi3.SerializationDeepObject, name: "filter", value: map[string]any{"a&b": "value"}},
		{style: openapi3.SerializationDeepObject, name: "filter", value: map[string]any{"key": "a=b"}},
	} {
		if _, err := prepareStyleValue(testCase.name, testCase.value, testCase.style, BindingSpecOpenAPI31, nil); err == nil || !strings.Contains(err.Error(), "structural delimiter") {
			t.Errorf("style %q value %#v error = %v", testCase.style, testCase.value, err)
		}
	}
}

func TestHeaderInvalidFieldBytesRefuse(t *testing.T) {
	dispatches := 0
	transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		dispatches++
		return &http.Response{StatusCode: http.StatusNoContent, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
	})
	spec := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://api.example.test"}],"paths":{"/x":{"get":{"parameters":[{"name":"X-Test","in":"header","schema":{"type":"string"}}],"responses":{"204":{"description":"ok"}}}}}}`
	for _, value := range []string{"safe\r\nInjected: yes", "safe\x00unsafe"} {
		call := NewInvokerWithOptions(RuntimeOptions{HTTPClient: &http.Client{Transport: transport}}).InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
			Source: invoke.InvocationSource{BindingSpec: BindingSpecOpenAPI31, Content: openbindings.TextContent(spec)}, Selector: "#/paths/~1x/get",
		})
		if _, err := driveSingle(t, call, map[string]any{"parameters": map[string]any{"X-Test": value}}); err == nil || err.Code != invoke.ErrCodeRefused {
			t.Errorf("value %q error = %v", value, err)
		}
	}
	if dispatches != 0 {
		t.Fatalf("invalid header values dispatched %d times", dispatches)
	}
}

func TestOpenAPICookieStaticAndRuntimeProofs(t *testing.T) {
	explode := boolPointer(true)
	array := &openapi3.Parameter{
		Name: "parts", In: openapi3.ParameterInCookie, Style: openapi3.SerializationForm, Explode: explode,
		Schema: &openapi3.SchemaRef{Value: openapi3.NewArraySchema().WithItems(openapi3.NewStringSchema())},
	}
	object := &openapi3.Parameter{
		Name: "parts", In: openapi3.ParameterInCookie, Style: openapi3.SerializationForm, Explode: explode,
		Schema: &openapi3.SchemaRef{Value: openapi3.NewObjectSchema().WithProperty("one", openapi3.NewStringSchema())},
	}
	typeless := &openapi3.Parameter{
		Name: "parts", In: openapi3.ParameterInCookie, Style: openapi3.SerializationForm, Explode: explode,
		Schema: &openapi3.SchemaRef{Value: &openapi3.Schema{}},
	}
	arrayOrNull := *openapi3.NewArraySchema().WithItems(openapi3.NewStringSchema())
	arrayOrNull.Type = &openapi3.Types{"array", "null"}
	union := &openapi3.Parameter{
		Name: "parts", In: openapi3.ParameterInCookie, Style: openapi3.SerializationForm, Explode: explode,
		Schema: &openapi3.SchemaRef{Value: &arrayOrNull},
	}
	if !formStyleCookieMultiValueProof(array, false) || !formStyleCookieMultiValueProof(object, false) {
		t.Fatal("provably multi-pair 3.1 cookie declarations survived static proof")
	}
	if formStyleCookieMultiValueProof(typeless, false) || formStyleCookieMultiValueProof(union, false) {
		t.Fatal("typeless or nullable-array declaration was guessed into static exclusion")
	}
	if !formStyleCookieMultiValueProof(array, true) || !formStyleCookieMultiValueProof(object, true) {
		t.Fatal("provably multi-pair 3.0 cookie declarations survived static proof")
	}
}

func TestOpenAPI31PathCorrespondenceAndEquivalentHierarchy(t *testing.T) {
	pathParameter := func(name string) *openapi3.ParameterRef {
		return &openapi3.ParameterRef{Value: &openapi3.Parameter{Name: name, In: openapi3.ParameterInPath}}
	}
	if err := checkPathTemplateDeclaration("/items", openapi3.Parameters{pathParameter("id")}, BindingSpecOpenAPI31); err == nil || !strings.Contains(err.Error(), "no path template expression") {
		t.Fatalf("reverse-correspondence error = %v", err)
	}
	if err := checkPathTemplateDeclaration("/items", openapi3.Parameters{pathParameter("id")}, BindingSpecOpenAPI30); err == nil || !strings.Contains(err.Error(), "OAPI30-P-02") {
		t.Fatalf("3.0 reverse-correspondence error = %v", err)
	}
	if err := checkPathTemplateDeclaration("/{id}/{id}", openapi3.Parameters{pathParameter("id")}, BindingSpecOpenAPI31); err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Fatalf("duplicate-expression error = %v", err)
	}
	paths := openapi3.NewPaths(
		openapi3.WithPath("/items/{id}", &openapi3.PathItem{}),
		openapi3.WithPath("/items/{name}", &openapi3.PathItem{}),
	)
	if got := equivalentPathTemplateCollision(paths, "/items/{id}"); got != "/items/{name}" {
		t.Fatalf("collision = %q, want /items/{name}", got)
	}
}

func TestOpenAPI30IgnoredHeaderParametersAndAllowEmptyValue(t *testing.T) {
	parameter := func(name, in string) *openapi3.ParameterRef {
		return &openapi3.ParameterRef{Value: &openapi3.Parameter{
			Name: name, In: in, Schema: &openapi3.SchemaRef{Value: openapi3.NewStringSchema()},
		}}
	}
	pathItem := &openapi3.PathItem{Parameters: openapi3.Parameters{
		parameter("aCcEpT", openapi3.ParameterInHeader),
		parameter("CONTENT-TYPE", openapi3.ParameterInHeader),
		parameter("authorization", openapi3.ParameterInHeader),
		parameter("q", openapi3.ParameterInQuery),
	}}
	params := effectiveParameters(pathItem, &openapi3.Operation{})
	if len(params) != 1 || params[0].Value.Name != "q" {
		t.Fatalf("effective parameters = %#v, want only q", params)
	}
	units, err := serializeQueryValueForRevision("q", "", openapi3.SerializationForm, true, false, BindingSpecOpenAPI30, false)
	if err != nil || !reflect.DeepEqual(units, []string{"q="}) {
		t.Fatalf("allowEmptyValue wire = %#v, %v; want q=", units, err)
	}
}

func TestParameterDeclarationFloorAndRootDialectExclusion(t *testing.T) {
	malformed := computeAcceptanceFloorFromBytes([]byte(`{
		"openapi":"3.1.0","info":{"title":"t","version":"1"},
		"paths":{"/x":{"get":{"parameters":[{"name":"q","in":"query","schema":{},"content":{"application/json":{}}}],"responses":{"204":{"description":"ok"}}}}}
	}`))
	verdict := malformed.opVerdict("#/paths/~1x/get")
	if verdict == nil || verdict.Disposition != "invalid" || len(verdict.Defects) != 1 || verdict.Defects[0].Class != floorD10 {
		t.Fatalf("malformed effective parameter verdict = %#v", verdict)
	}

	unknownField := computeAcceptanceFloorFromBytes([]byte(`{
		"openapi":"3.1.0","info":{"title":"t","version":"1"},
		"paths":{"/x":{"get":{"parameters":[{"name":"q","in":"query","schema":{},"futureKeyword":true}],"responses":{"204":{"description":"ok"}}}}}
	}`))
	if verdict := unknownField.opVerdict("#/paths/~1x/get"); verdict == nil || verdict.Disposition != "represented" {
		t.Fatalf("unknown non-extension field was guessed defective: %#v", verdict)
	}

	closed30 := computeAcceptanceFloorFromBytes([]byte(`{
		"openapi":"3.0.3","info":{"title":"t","version":"1"},
		"paths":{"/x":{"get":{"parameters":[{"name":"q","in":"query","schema":{},"content":{"application/json":{}}}],"responses":{"204":{"description":"ok"}}}}}
	}`))
	if verdict := closed30.opVerdict("#/paths/~1x/get"); verdict == nil || verdict.Disposition != "invalid" {
		t.Fatalf("3.0 complete Parameter gate verdict = %#v", verdict)
	}

	dialect := computeAcceptanceFloorFromBytes([]byte(`{
		"openapi":"3.1.0","jsonSchemaDialect":"https://example.test/custom","info":{"title":"t","version":"1"},
		"paths":{"/x":{"get":{"responses":{"204":{"description":"ok"}}}}}
	}`))
	if dialect.SourceExclusion == "" || dialect.Refusal != "" {
		t.Fatalf("dialect source exclusion/refusal = %q / %q", dialect.SourceExclusion, dialect.Refusal)
	}
	baseWithFragment := computeAcceptanceFloorFromBytes([]byte(`{
		"openapi":"3.1.0","jsonSchemaDialect":"https://spec.openapis.org/oas/3.1/dialect/base#","info":{"title":"t","version":"1"},
		"paths":{"/x":{"get":{"responses":{"204":{"description":"ok"}}}}}
	}`))
	if baseWithFragment.SourceExclusion == "" {
		t.Fatal("a URI differing from the exact incorporated dialect by a fragment was admitted")
	}
}

func TestTypedEffectiveParameterDeclarationGateCoversResolvedReferences(t *testing.T) {
	valid := openapi3.Parameters{&openapi3.ParameterRef{Value: &openapi3.Parameter{
		Name: "q", In: openapi3.ParameterInQuery, Schema: &openapi3.SchemaRef{Value: openapi3.NewStringSchema()},
	}}}
	if got := malformedEffectiveParameterFor(valid, BindingSpecOpenAPI31); got != "" {
		t.Fatalf("valid parameter reported as %q", got)
	}
	missingCarriage := openapi3.Parameters{&openapi3.ParameterRef{Value: &openapi3.Parameter{Name: "q", In: openapi3.ParameterInQuery}}}
	if got := malformedEffectiveParameterFor(missingCarriage, BindingSpecOpenAPI31); got != "q" {
		t.Fatalf("missing-carriage parameter = %q", got)
	}
	if got := malformedEffectiveParameterFor(missingCarriage, BindingSpecOpenAPI30); got != "q" {
		t.Fatalf("3.0 missing-carriage parameter = %q", got)
	}
}

func TestRuntimeParameterConversionAndRawCookieNativePath(t *testing.T) {
	response := func(request *http.Request) *http.Response {
		return &http.Response{
			StatusCode: http.StatusOK, Status: "200 OK",
			Header: http.Header{"Content-Type": []string{"application/json"}},
			Body:   io.NopCloser(strings.NewReader(`{}`)), Request: request,
		}
	}

	t.Run("configured number spelling reaches query", func(t *testing.T) {
		var got string
		transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			got = request.URL.RawQuery
			return response(request), nil
		})
		invoker := NewInvokerWithOptions(RuntimeOptions{
			HTTPClient:          &http.Client{Transport: transport},
			ParameterConversion: func(value any) (string, error) { return "configured-seven", nil },
		})
		spec := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://api.example.test"}],"paths":{"/x":{"get":{"parameters":[{"name":"q","in":"query","schema":{"type":"number"}}],"responses":{"200":{"description":"ok","content":{"application/json":{}}}}}}}}`
		call := invoker.InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
			Source: invoke.InvocationSource{BindingSpec: BindingSpecOpenAPI31, Content: openbindings.TextContent(spec)}, Selector: "#/paths/~1x/get",
		})
		if _, ierr := driveSingle(t, call, map[string]any{"parameters": map[string]any{"q": float64(7)}}); ierr != nil {
			t.Fatalf("invoke: %v", ierr)
		}
		if got != "q=configured-seven" {
			t.Fatalf("query = %q", got)
		}
	})

	t.Run("raw and structured declarations coexist until both emit", func(t *testing.T) {
		var cookies []string
		dispatches := 0
		transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			dispatches++
			cookies = append([]string(nil), request.Header.Values("Cookie")...)
			return response(request), nil
		})
		invoker := NewInvokerWithOptions(RuntimeOptions{HTTPClient: &http.Client{Transport: transport}})
		spec := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://api.example.test"}],"paths":{"/x":{"get":{"parameters":[{"name":"Cookie","in":"header","schema":{"type":"string"}},{"name":"session","in":"cookie","schema":{"type":"string"}}],"responses":{"200":{"description":"ok","content":{"application/json":{}}}}}}}}`
		args := &invoke.BindingInvocationArgs{
			Source: invoke.InvocationSource{BindingSpec: BindingSpecOpenAPI31, Content: openbindings.TextContent(spec)}, Selector: "#/paths/~1x/get",
		}
		call := invoker.InvokeBinding(context.Background(), args)
		if _, ierr := driveSingle(t, call, map[string]any{"parameters": map[string]any{"Cookie": "raw=1"}}); ierr != nil {
			t.Fatalf("raw-only invoke: %v", ierr)
		}
		if !reflect.DeepEqual(cookies, []string{"raw=1"}) {
			t.Fatalf("Cookie fields = %#v", cookies)
		}

		call = invoker.InvokeBinding(context.Background(), args)
		_, ierr := driveSingle(t, call, map[string]any{"parameters": map[string]any{"Cookie": "raw=1", "session": "structured"}})
		if ierr == nil || ierr.Code != invoke.ErrCodeRefused {
			t.Fatalf("raw+structured error = %v", ierr)
		}
		if dispatches != 1 {
			t.Fatalf("collision dispatched; total dispatches = %d", dispatches)
		}
	})
}

func TestRuntimeEncodingStyleUsesCompoundObjectDeclaration(t *testing.T) {
	var body string
	transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		encoded, _ := io.ReadAll(request.Body)
		body = string(encoded)
		return &http.Response{
			StatusCode: http.StatusOK, Status: "200 OK",
			Header: http.Header{"Content-Type": []string{"application/json"}},
			Body:   io.NopCloser(strings.NewReader(`{}`)), Request: request,
		}, nil
	})
	spec := `{
		"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://api.example.test"}],
		"paths":{"/x":{"post":{"requestBody":{"content":{"application/x-www-form-urlencoded":{
			"schema":{"type":"object","properties":{"filter":{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"string"}}}}},
			"encoding":{"filter":{"style":"pipeDelimited","explode":false}}
		}}},"responses":{"200":{"description":"ok","content":{"application/json":{}}}}}}}
	}`
	call := NewInvokerWithOptions(RuntimeOptions{
		HTTPClient:          &http.Client{Transport: transport},
		ParameterConversion: func(value any) (string, error) { return "configured-seven", nil },
	}).InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
		Source: invoke.InvocationSource{BindingSpec: BindingSpecOpenAPI31, Content: openbindings.TextContent(spec)}, Selector: "#/paths/~1x/post",
	})
	if _, ierr := driveSingle(t, call, map[string]any{"body": map[string]any{"filter": map[string]any{"a": float64(7), "b": "two"}}}); ierr != nil {
		t.Fatalf("invoke: %v", ierr)
	}
	if body != "filter=a|configured-seven|b|two" {
		t.Fatalf("body = %q", body)
	}
}
