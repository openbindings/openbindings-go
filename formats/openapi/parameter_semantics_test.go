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

func TestOpenAPI30ParameterConversionAndNullRemainForM3(t *testing.T) {
	parameter := &openapi3.Parameter{
		Name: "q", In: openapi3.ParameterInQuery,
		Schema: &openapi3.SchemaRef{Value: openapi3.NewArraySchema().WithItems(openapi3.NewIntegerSchema())},
	}
	value := []any{float64(7), nil}
	prepared, _, err := prepareSchemaParameterValue(parameter, value, BindingSpecOpenAPI30, nil)
	if err != nil {
		t.Fatalf("3.0-only M3 behavior changed during M2a: %v", err)
	}
	if !reflect.DeepEqual(prepared, value) {
		t.Fatalf("3.0 value = %#v, want unchanged %#v", prepared, value)
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
		t.Run(testCase.name, func(t *testing.T) {
			prepared, err := prepareStyleValue("id", nil, testCase.style, BindingSpecOpenAPI31, nil)
			if err != nil {
				t.Fatal(err)
			}
			var wire string
			switch testCase.style {
			case openapi3.SerializationForm:
				units, err := serializeQueryValueForRevision("id", prepared, testCase.style, true, false, BindingSpecOpenAPI31, false)
				if err != nil {
					t.Fatal(err)
				}
				wire = strings.Join(units, "&")
			default:
				wire, err = serializePathValueForRevision("id", prepared, testCase.style, false, BindingSpecOpenAPI31)
				if err != nil {
					t.Fatal(err)
				}
			}
			if wire != testCase.wire {
				t.Fatalf("wire = %q, want %q", wire, testCase.wire)
			}
		})
	}

	for _, style := range []string{openapi3.SerializationSpaceDelimited, openapi3.SerializationPipeDelimited, openapi3.SerializationDeepObject} {
		if _, err := prepareStyleValue("id", nil, style, BindingSpecOpenAPI31, nil); err == nil {
			t.Errorf("null unexpectedly admitted for style %q", style)
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
	parameter := &openapi3.Parameter{
		Name: "X-Test", In: openapi3.ParameterInHeader,
		Schema: &openapi3.SchemaRef{Value: openapi3.NewStringSchema()},
	}
	for _, value := range []string{"safe\r\nInjected: yes", "safe\x00unsafe"} {
		if _, _, err := prepareSchemaParameterValue(parameter, value, BindingSpecOpenAPI31, nil); err == nil || !strings.Contains(err.Error(), "invalid HTTP field byte") {
			t.Errorf("value %q error = %v", value, err)
		}
	}
}

func TestOpenAPI31CookieStaticAndRuntimeProofs(t *testing.T) {
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
	if formStyleCookieMultiValueProof(array, true) {
		t.Fatal("the 3.1-only static proof leaked into the 3.0 sibling")
	}
	if _, _, err := prepareSchemaParameterValue(typeless, []any{"a", "b"}, BindingSpecOpenAPI31, nil); err == nil || !strings.Contains(err.Error(), "multiple cookie pairs") {
		t.Fatalf("runtime multi-pair error = %v", err)
	}
}

func TestOpenAPI31PathCorrespondenceAndEquivalentHierarchy(t *testing.T) {
	pathParameter := func(name string) *openapi3.ParameterRef {
		return &openapi3.ParameterRef{Value: &openapi3.Parameter{Name: name, In: openapi3.ParameterInPath}}
	}
	if err := checkPathTemplateDeclaration("/items", openapi3.Parameters{pathParameter("id")}, BindingSpecOpenAPI31); err == nil || !strings.Contains(err.Error(), "no path template expression") {
		t.Fatalf("reverse-correspondence error = %v", err)
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

	deferred30 := computeAcceptanceFloorFromBytes([]byte(`{
		"openapi":"3.0.3","info":{"title":"t","version":"1"},
		"paths":{"/x":{"get":{"parameters":[{"name":"q","in":"query","schema":{},"content":{"application/json":{}}}],"responses":{"204":{"description":"ok"}}}}}
	}`))
	if verdict := deferred30.opVerdict("#/paths/~1x/get"); verdict == nil || verdict.Disposition != "represented" {
		t.Fatalf("3.0-only complete Parameter gate must remain for M3: %#v", verdict)
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
	if got := malformedEffectiveParameterFor(missingCarriage, BindingSpecOpenAPI30); got != "" {
		t.Fatalf("3.0-only complete declaration gate leaked from M3: %q", got)
	}
}

func TestRuntimeParameterConversionAndRawCookieBridge(t *testing.T) {
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

func TestCompletedURLValidationIsOpenAPI31PreDispatch(t *testing.T) {
	called := 0
	base := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		called++
		return &http.Response{
			StatusCode: http.StatusNoContent, Status: "204 No Content",
			Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: request,
		}, nil
	})
	transport := rawCookieBridgeTransport{base: base}

	request, err := http.NewRequest(http.MethodGet, "https://api.example.test/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.URL.RawQuery = "q=%ZZ"
	request = request.WithContext(context.WithValue(request.Context(), completedURLValidationContextKey{}, BindingSpecOpenAPI31))
	if _, err := transport.RoundTrip(request); err == nil || !strings.Contains(err.Error(), "RFC 3986") {
		t.Fatalf("3.1 completed-URL error = %v", err)
	}
	if called != 0 {
		t.Fatalf("invalid 3.1 completed URL dispatched %d times", called)
	}

	request = request.WithContext(context.WithValue(request.Context(), completedURLValidationContextKey{}, BindingSpecOpenAPI30))
	if _, err := transport.RoundTrip(request); err != nil {
		t.Fatalf("3.0-only completed-target work escaped M2a: %v", err)
	}
	if called != 1 {
		t.Fatalf("3.0 request dispatches = %d, want 1", called)
	}
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
