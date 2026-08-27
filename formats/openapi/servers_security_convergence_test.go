package openapi

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/invoke"
)

type convergenceCapture struct {
	requests []*http.Request
}

func (capture *convergenceCapture) RoundTrip(request *http.Request) (*http.Response, error) {
	copy := request.Clone(request.Context())
	copy.Header = request.Header.Clone()
	capture.requests = append(capture.requests, copy)
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{}`)),
		Request:    request,
	}, nil
}

type convergenceResourceTransport struct {
	resources map[string]string
	requests  []*http.Request
}

func (transport *convergenceResourceTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if content, found := transport.resources[request.URL.String()]; found {
		return &http.Response{
			StatusCode: http.StatusOK, Status: "200 OK", Header: http.Header{},
			Body: io.NopCloser(strings.NewReader(content)), Request: request,
		}, nil
	}
	copy := request.Clone(request.Context())
	copy.Header = request.Header.Clone()
	transport.requests = append(transport.requests, copy)
	return &http.Response{
		StatusCode: http.StatusOK, Status: "200 OK", Header: http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{}`)), Request: request,
	}, nil
}

func invokeConvergenceDocument(t *testing.T, bindingSpec, document string, bindCtx map[string]any, input any) (*convergenceCapture, *invoke.InvocationError) {
	t.Helper()
	capture := &convergenceCapture{}
	call := NewInvokerWithClient(&http.Client{Transport: capture}).InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
		Source:   invoke.InvocationSource{BindingSpec: bindingSpec, Content: openbindings.TextContent(document)},
		Selector: "#/paths/~1items~1{id}/get",
		Context:  bindCtx,
	})
	_, invocationErr := driveSingle(t, call, input)
	return capture, invocationErr
}

func serverSecurityDocument(version, servers, parameters, security, schemes string) string {
	return `{
		"openapi":` + convergenceQuoteJSON(version) + `,
		"info":{"title":"convergence","version":"1"},
		"servers":` + servers + `,
		"paths":{"/items/{id}":{"get":{
			"parameters":[{"name":"id","in":"path","required":true,"schema":{"type":"string"}}` + parameters + `],
			"security":` + security + `,
			"responses":{"200":{"description":"ok","content":{"application/json":{}}}}
		}}},
		"components":{"securitySchemes":` + schemes + `}
	}`
}

func convergenceQuoteJSON(value string) string {
	return `"` + value + `"`
}

func TestServerConvergencePreservesEmptyValuesAndVerbatimAppend(t *testing.T) {
	document := serverSecurityDocument(
		"3.1.2",
		`[{"url":"https://api.example.test/{segment}","variables":{"segment":{"default":"","enum":[""]}}}]`,
		`,{"name":"q","in":"query","schema":{"type":"string"}}`,
		`[]`, `{}`,
	)
	capture, invocationErr := invokeConvergenceDocument(t, BindingSpecOpenAPI31, document, nil, map[string]any{
		"parameters": map[string]any{"id": "42", "q": "yes"},
	})
	if invocationErr != nil {
		t.Fatalf("invoke: %v", invocationErr)
	}
	if len(capture.requests) != 1 || capture.requests[0].URL.String() != "https://api.example.test//items/42?q=yes" {
		t.Fatalf("requests = %#v", capture.requests)
	}
}

func TestServerConvergenceRejectsDeclarationAndValueDefects(t *testing.T) {
	tests := []struct {
		name    string
		version string
		server  string
		context map[string]any
	}{
		{name: "empty enum", version: "3.1.2", server: `{"url":"https://{env}.example.test","variables":{"env":{"default":"prod","enum":[]}}}`},
		{name: "default outside enum", version: "3.1.2", server: `{"url":"https://{env}.example.test","variables":{"env":{"default":"prod","enum":["stage"]}}}`},
		{name: "supplied outside enum", version: "3.1.2", server: `{"url":"https://{env}.example.test","variables":{"env":{"default":"prod","enum":["prod"]}}}`, context: map[string]any{"configuration": map[string]any{"server": map[string]any{"variables": map[string]any{"env": "stage"}}}}},
		{name: "3.0 missing default", version: "3.0.4", server: `{"url":"https://{env}.example.test","variables":{"env":{"enum":["prod"]}}}`},
		{name: "query", version: "3.1.2", server: `{"url":"https://api.example.test/base?x=1"}`},
		{name: "fragment", version: "3.1.2", server: `{"url":"https://api.example.test/base#frag"}`},
		{name: "duplicate template variable", version: "3.1.2", server: `{"url":"https://{env}.example.test/{env}","variables":{"env":{"default":"prod"}}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := serverSecurityDocument(test.version, `[`+test.server+`]`, ``, `[]`, `{}`)
			capture, invocationErr := invokeConvergenceDocument(t, bindingSpecForVersion(test.version), document, test.context, map[string]any{"parameters": map[string]any{"id": "42"}})
			if invocationErr == nil || invocationErr.Code != invoke.ErrCodeRefused {
				t.Fatalf("error = %v", invocationErr)
			}
			if len(capture.requests) != 0 {
				t.Fatalf("defect dispatched %d request(s)", len(capture.requests))
			}
		})
	}
}

func bindingSpecForVersion(version string) string {
	if strings.HasPrefix(version, "3.0.") {
		return BindingSpecOpenAPI30
	}
	return BindingSpecOpenAPI31
}

func TestConfiguredServerReplacementPreservesOperationConstruction(t *testing.T) {
	document := serverSecurityDocument(
		"3.1.2", `[{"url":"https://artifact.example/base"}]`,
		`,{"name":"q","in":"query","schema":{"type":"string"}}`, `[]`, `{}`,
	)
	bindCtx := map[string]any{"configuration": map[string]any{"server": map[string]any{"baseUrl": "https://configured.example/root/"}}}
	capture, invocationErr := invokeConvergenceDocument(t, BindingSpecOpenAPI31, document, bindCtx, map[string]any{
		"parameters": map[string]any{"id": "42", "q": "yes"},
	})
	if invocationErr != nil {
		t.Fatalf("invoke: %v", invocationErr)
	}
	if got := capture.requests[0].URL.String(); got != "https://configured.example/root//items/42?q=yes" {
		t.Fatalf("configured URL = %q", got)
	}
}

func TestRelativeServerUsesContainingDocumentLocation(t *testing.T) {
	entry := `{
		"openapi":"3.1.2","info":{"title":"entry","version":"1"},
		"paths":{"/items/{id}":{"$ref":"https://docs.example/specs/parts.json#/paths/~1items~1{id}"}}
	}`
	external := `{
		"openapi":"3.1.2","info":{"title":"parts","version":"1"},
		"paths":{"/items/{id}":{"get":{
			"servers":[{"url":"../api/"}],
			"parameters":[{"name":"id","in":"path","required":true,"schema":{"type":"string"}}],
			"responses":{"200":{"description":"ok","content":{"application/json":{}}}}
		}}}
	}`
	transport := &convergenceResourceTransport{resources: map[string]string{"https://docs.example/specs/parts.json": external}}
	call := NewInvokerWithClient(&http.Client{Transport: transport}).InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
		Source:   invoke.InvocationSource{BindingSpec: BindingSpecOpenAPI31, Location: "https://entry.example/root/openapi.json", Content: openbindings.TextContent(entry)},
		Selector: "#/paths/~1items~1{id}/get",
	})
	if _, invocationErr := driveSingle(t, call, map[string]any{"parameters": map[string]any{"id": "42"}}); invocationErr != nil {
		t.Fatalf("invoke: %#v", invocationErr)
	}
	if len(transport.requests) != 1 || transport.requests[0].URL.String() != "https://docs.example/api//items/42" {
		t.Fatalf("dispatches = %#v", transport.requests)
	}
}

func TestImplicitConnectionScopeSelectsEntryOrReferringDocument(t *testing.T) {
	entry := `{
		"openapi":"3.1.2","info":{"title":"entry","version":"1"},
		"servers":[{"url":"https://api.example.test"}],
		"paths":{"/items/{id}":{"$ref":"https://docs.example/parts.json#/paths/~1items~1{id}"}},
		"components":{"securitySchemes":{"key":{"type":"apiKey","in":"header","name":"X-Entry"}}}
	}`
	external := `{
		"openapi":"3.1.2","info":{"title":"parts","version":"1"},
		"paths":{"/items/{id}":{"get":{
			"security":[{"key":[]}],
			"parameters":[{"name":"id","in":"path","required":true,"schema":{"type":"string"}}],
			"responses":{"200":{"description":"ok","content":{"application/json":{}}}}
		}}},
		"components":{"securitySchemes":{"key":{"type":"apiKey","in":"header","name":"X-Referring"}}}
	}`
	probeTransport := &convergenceResourceTransport{resources: map[string]string{"https://docs.example/parts.json": external}}
	probe, _, _, probeErr := loadDocumentForSynthesis(context.Background(), &http.Client{Transport: probeTransport}, "https://entry.example/openapi.json", openbindings.TextContent(entry))
	if probeErr != nil {
		t.Fatal(probeErr)
	}
	probeOperation := probe.Paths.Find("/items/{id}").Get
	if probeOperation.Extensions[operationDocumentMarker] == nil || probeOperation.Extensions[referringSecuritySchemesMarker] == nil {
		t.Fatalf("external operation markers = %#v", probeOperation.Extensions)
	}
	for _, test := range []struct {
		name       string
		config     map[string]any
		headerName string
	}{
		{name: "entry default", headerName: "X-Entry"},
		{name: "referring", config: map[string]any{"implicitConnectionScope": "referring"}, headerName: "X-Referring"},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := &convergenceResourceTransport{resources: map[string]string{"https://docs.example/parts.json": external}}
			bindCtx := map[string]any{"apiKeys": map[string]any{"key": "credential"}}
			if test.config != nil {
				bindCtx["configuration"] = test.config
			}
			call := NewInvokerWithClient(&http.Client{Transport: transport}).InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
				Source:   invoke.InvocationSource{BindingSpec: BindingSpecOpenAPI31, Location: "https://entry.example/openapi.json", Content: openbindings.TextContent(entry)},
				Selector: "#/paths/~1items~1{id}/get", Context: bindCtx,
			})
			if _, invocationErr := driveSingle(t, call, map[string]any{"parameters": map[string]any{"id": "42"}}); invocationErr != nil {
				t.Fatalf("invoke: %#v", invocationErr)
			}
			if len(transport.requests) != 1 || transport.requests[0].Header.Get(test.headerName) != "credential" {
				var headers http.Header
				if len(transport.requests) > 0 {
					headers = transport.requests[0].Header
				}
				t.Fatalf("dispatch count = %d, headers = %#v", len(transport.requests), headers)
			}
		})
	}
}

func TestSecurityConvergenceRequiresAndAppliesExplicitAlternative(t *testing.T) {
	document := serverSecurityDocument(
		"3.1.2", `[{"url":"https://api.example.test"}]`, ``,
		`[{"first":[]},{"second":[]}]`,
		`{"first":{"type":"apiKey","in":"header","name":"X-First"},"second":{"type":"apiKey","in":"header","name":"X-Second"}}`,
	)
	credentials := map[string]any{"apiKeys": map[string]any{"first": "one", "second": "two"}}
	capture, invocationErr := invokeConvergenceDocument(t, BindingSpecOpenAPI31, document, credentials, map[string]any{"parameters": map[string]any{"id": "42"}})
	if invocationErr == nil || invocationErr.Code != invoke.ErrCodeRefused || len(capture.requests) != 0 {
		t.Fatalf("unselected invocation = (%v, %d dispatches)", invocationErr, len(capture.requests))
	}
	credentials["configuration"] = map[string]any{"security": map[string]any{"index": float64(1)}}
	capture, invocationErr = invokeConvergenceDocument(t, BindingSpecOpenAPI31, document, credentials, map[string]any{"parameters": map[string]any{"id": "42"}})
	if invocationErr != nil {
		t.Fatalf("selected invoke: %#v", invocationErr)
	}
	if got := capture.requests[0].Header.Get("X-Second"); got != "two" || capture.requests[0].Header.Get("X-First") != "" {
		t.Fatalf("selected headers = %#v", capture.requests[0].Header)
	}
}

func TestSecurityConvergenceNoSecurityAlternativesComplete(t *testing.T) {
	for _, security := range []string{`[]`, `[{}]`} {
		document := serverSecurityDocument("3.1.2", `[{"url":"https://api.example.test"}]`, ``, security, `{}`)
		capture, invocationErr := invokeConvergenceDocument(t, BindingSpecOpenAPI31, document, map[string]any{"apiKey": "unrelated"}, map[string]any{"parameters": map[string]any{"id": "42"}})
		if invocationErr != nil || len(capture.requests) != 1 {
			t.Fatalf("security %s = (%v, %d dispatches)", security, invocationErr, len(capture.requests))
		}
		if capture.requests[0].Header.Get("Authorization") != "" {
			t.Fatalf("security %s volunteered credentials", security)
		}
	}
}

func TestSecurityConvergenceAPIKeyCarriageAndAlternativeConfinement(t *testing.T) {
	t.Run("query percent encoding", func(t *testing.T) {
		document := serverSecurityDocument(
			"3.1.2", `[{"url":"https://api.example.test"}]`, ``,
			`[{"key":[]}]`, `{"key":{"type":"apiKey","in":"query","name":"api_key"}}`,
		)
		capture, invocationErr := invokeConvergenceDocument(t, BindingSpecOpenAPI31, document, map[string]any{"apiKeys": map[string]any{"key": "a/b? c&d=é"}}, map[string]any{"parameters": map[string]any{"id": "42"}})
		if invocationErr != nil {
			t.Fatalf("invoke: %v", invocationErr)
		}
		if got := capture.requests[0].URL.RawQuery; got != "api_key=a%2Fb%3F%20c%26d%3D%C3%A9" {
			t.Fatalf("query = %q", got)
		}
	})

	t.Run("uncarriable cookie", func(t *testing.T) {
		document := serverSecurityDocument(
			"3.1.2", `[{"url":"https://api.example.test"}]`, ``,
			`[{"key":[]}]`, `{"key":{"type":"apiKey","in":"cookie","name":"session"}}`,
		)
		capture, invocationErr := invokeConvergenceDocument(t, BindingSpecOpenAPI31, document, map[string]any{"apiKeys": map[string]any{"key": "bad;value"}}, map[string]any{"parameters": map[string]any{"id": "42"}})
		if invocationErr == nil || invocationErr.Code != invoke.ErrCodeRefused || len(capture.requests) != 0 {
			t.Fatalf("cookie invocation = (%v, %d dispatches)", invocationErr, len(capture.requests))
		}
	})

	t.Run("collision confined to unselected alternative", func(t *testing.T) {
		document := serverSecurityDocument(
			"3.1.2", `[{"url":"https://api.example.test"}]`, `,{"name":"X-Key","in":"header","schema":{"type":"string"}}`,
			`[{"colliding":[]},{"safe":[]}]`,
			`{"colliding":{"type":"apiKey","in":"header","name":"X-Key"},"safe":{"type":"apiKey","in":"header","name":"X-Safe"}}`,
		)
		bindCtx := map[string]any{
			"configuration": map[string]any{"security": map[string]any{"index": float64(1)}},
			"apiKeys":       map[string]any{"colliding": "bad", "safe": "good"},
		}
		capture, invocationErr := invokeConvergenceDocument(t, BindingSpecOpenAPI31, document, bindCtx, map[string]any{"parameters": map[string]any{"id": "42"}})
		if invocationErr != nil || len(capture.requests) != 1 || capture.requests[0].Header.Get("X-Safe") != "good" {
			t.Fatalf("safe alternative = (%#v, %#v)", invocationErr, capture.requests)
		}
	})
}

func TestSecurityConvergenceCredentialGrammar(t *testing.T) {
	tests := []struct {
		name     string
		scheme   string
		security string
		context  map[string]any
	}{
		{name: "basic colon user", scheme: `{"basic":{"type":"http","scheme":"basic"}}`, security: `[{"basic":[]}]`, context: map[string]any{"credentials": map[string]any{"basic": map[string]any{"username": "a:b", "password": "secret"}}}},
		{name: "basic non ASCII", scheme: `{"basic":{"type":"http","scheme":"basic"}}`, security: `[{"basic":[]}]`, context: map[string]any{"credentials": map[string]any{"basic": map[string]any{"username": "é", "password": "secret"}}}},
		{name: "basic control", scheme: `{"basic":{"type":"http","scheme":"basic"}}`, security: `[{"basic":[]}]`, context: map[string]any{"credentials": map[string]any{"basic": map[string]any{"username": "user", "password": "bad\nvalue"}}}},
		{name: "bearer grammar", scheme: `{"bearer":{"type":"http","scheme":"bearer"}}`, security: `[{"bearer":[]}]`, context: map[string]any{"credentials": map[string]any{"bearer": "bad token"}}},
		{name: "bearer padding only", scheme: `{"bearer":{"type":"http","scheme":"bearer"}}`, security: `[{"bearer":[]}]`, context: map[string]any{"credentials": map[string]any{"bearer": "="}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := serverSecurityDocument("3.1.2", `[{"url":"https://api.example.test"}]`, ``, test.security, test.scheme)
			capture, invocationErr := invokeConvergenceDocument(t, BindingSpecOpenAPI31, document, test.context, map[string]any{"parameters": map[string]any{"id": "42"}})
			if invocationErr == nil || invocationErr.Code != invoke.ErrCodeRefused || len(capture.requests) != 0 {
				t.Fatalf("credential invocation = (%v, %d dispatches)", invocationErr, len(capture.requests))
			}
		})
	}
}

func TestSecurityConvergenceRoleArraysFollowEditionSemantics(t *testing.T) {
	requirements := openapi3.SecurityRequirements{{"key": []string{"operator", "auditor"}}}
	op := &openapi3.Operation{Security: &requirements}
	scheme := &openapi3.SecuritySchemeRef{Value: &openapi3.SecurityScheme{Type: "apiKey", In: "header", Name: "X-Key"}}

	doc31 := &openapi3.T{OpenAPI: "3.1.2", Components: &openapi3.Components{SecuritySchemes: openapi3.SecuritySchemes{"key": scheme}}}
	plans := securityPlans(doc31, op, "https://api.example.test")
	if len(plans) != 1 || len(plans[0].context.Requirements) != 1 {
		t.Fatalf("3.1 role plan = %#v", plans)
	}
	roles, ok := plans[0].context.Requirements[0].Extra["roles"].([]string)
	if !ok || strings.Join(roles, ",") != "operator,auditor" {
		t.Fatalf("3.1 roles = %#v", plans[0].context.Requirements[0].Extra["roles"])
	}

	doc30 := &openapi3.T{OpenAPI: "3.0.4", Components: &openapi3.Components{SecuritySchemes: openapi3.SecuritySchemes{"key": scheme}}}
	if plans := securityPlans(doc30, op, "https://api.example.test"); len(plans) != 0 {
		t.Fatalf("3.0 non-OAuth role array remained usable: %#v", plans)
	}
	mutual := &openapi3.SecuritySchemeRef{Value: &openapi3.SecurityScheme{Type: "mutualTLS"}}
	mutualRequirements := openapi3.SecurityRequirements{{"mtls": []string{}}}
	mutualOperation := &openapi3.Operation{Security: &mutualRequirements}
	doc30.Components.SecuritySchemes = openapi3.SecuritySchemes{"mtls": mutual}
	if plans := securityPlans(doc30, mutualOperation, "https://api.example.test"); len(plans) != 0 {
		t.Fatalf("3.0 mutualTLS remained usable: %#v", plans)
	}
}

func TestImplicitConnectionScopeIsTypedWhenOnlyReferringScopeResolves(t *testing.T) {
	requirements := openapi3.SecurityRequirements{{"key": []string{}}}
	op := &openapi3.Operation{
		Security: &requirements,
		Extensions: map[string]any{
			referringSecuritySchemesMarker: map[string]any{
				"key": map[string]any{"type": "apiKey", "in": "header", "name": "X-Referring"},
			},
		},
	}
	doc := &openapi3.T{OpenAPI: "3.1.2", Components: &openapi3.Components{SecuritySchemes: openapi3.SecuritySchemes{}}}
	details, pending, err := requiredImplicitConnectionScopeContext(doc, op, nil, "https://api.example.test", nil, "https://api.example.test")
	if err != nil || !pending || details == nil || len(details.Alternatives) != 1 || len(details.Alternatives[0].Requirements) != 1 {
		t.Fatalf("implicit-scope preflight = (%#v, %v, %v)", details, pending, err)
	}
	requirement := details.Alternatives[0].Requirements[0]
	if requirement.Type != "config.value" || requirement.Extra["point"] != "implicitConnectionScope" || requirement.Extra["path"] != "" {
		t.Fatalf("implicit-scope requirement = %#v", requirement)
	}
	coverageRequirements := openAPISecurityRequirements(doc, op, nil)
	if len(coverageRequirements) != 1 || coverageRequirements[0] != "configuration.implicitConnectionScope" {
		t.Fatalf("coverage requirements = %v", coverageRequirements)
	}
}
