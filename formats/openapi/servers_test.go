package openapi

import (
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

func serversDoc() *openapi3.T {
	return &openapi3.T{
		OpenAPI: "3.0.3",
		Servers: openapi3.Servers{
			{URL: "https://doc-a.example.com"},
			{URL: "https://doc-b.example.com"},
		},
	}
}

// OAPI-P-05: the effective server list is operation servers, else path-item
// servers, else document servers, else the implied "/".
func TestEffectiveServers_Precedence(t *testing.T) {
	doc := serversDoc()
	pathItem := &openapi3.PathItem{Servers: openapi3.Servers{{URL: "https://path.example.com"}}}
	opServers := openapi3.Servers{&openapi3.Server{URL: "https://op.example.com"}}
	op := &openapi3.Operation{Servers: &opServers}

	if got, _ := resolveServer(doc, pathItem, op, nil, ""); got != "https://op.example.com" {
		t.Errorf("operation servers must win, got %q", got)
	}
	if got, _ := resolveServer(doc, pathItem, &openapi3.Operation{}, nil, ""); got != "https://path.example.com" {
		t.Errorf("path-item servers next, got %q", got)
	}
	if got, err := resolveServer(doc, nil, &openapi3.Operation{}, nil, ""); err == nil || got != "" {
		t.Errorf("multiple document servers require selection, got (%q, %v)", got, err)
	}
	// Implied "/" resolves against the artifact's base URI (the location).
	if got, _ := resolveServer(&openapi3.T{}, nil, nil, nil, "https://host.example.com/openapi.json"); got != "https://host.example.com/" {
		t.Errorf("implied / must resolve against the location, got %q", got)
	}
}

// The default substitutes each server variable's declared default.
func TestResolveServer_VariableDefaults(t *testing.T) {
	doc := &openapi3.T{
		Servers: openapi3.Servers{{
			URL: "https://{env}.example.com:{port}/v2",
			Variables: map[string]*openapi3.ServerVariable{
				"env":  {Default: "api", Enum: []string{"api", "staging"}},
				"port": {Default: "8443"},
			},
		}},
	}
	got, err := resolveServer(doc, nil, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://api.example.com:8443/v2" {
		t.Errorf("resolved = %q", got)
	}
}

// The server configuration point: entry selection by url or index, variable
// values (enum-validated), and an outright baseUrl.
func TestResolveServer_ConfigurationPoint(t *testing.T) {
	doc := &openapi3.T{
		Servers: openapi3.Servers{
			{
				URL: "https://{env}.example.com",
				Variables: map[string]*openapi3.ServerVariable{
					"env": {Default: "api", Enum: []string{"api", "staging"}},
				},
			},
			{URL: "https://alt.example.com/base/"},
		},
	}
	ctxWith := func(server any) map[string]any {
		return map[string]any{"configuration": map[string]any{"server": server}}
	}

	// Outright base URL (string shorthand and object form).
	if got, _ := resolveServer(doc, nil, nil, ctxWith("https://override.example.com"), ""); got != "https://override.example.com" {
		t.Errorf("string baseUrl shorthand = %q", got)
	}
	if got, _ := resolveServer(doc, nil, nil, ctxWith(map[string]any{"baseUrl": "https://override.example.com/x"}), ""); got != "https://override.example.com/x" {
		t.Errorf("object baseUrl = %q", got)
	}

	// Select another entry by url. The authored trailing slash is preserved;
	// operation path bytes are appended verbatim later.
	if got, _ := resolveServer(doc, nil, nil, ctxWith(map[string]any{"url": "https://alt.example.com/base/"}), ""); got != "https://alt.example.com/base/" {
		t.Errorf("url selection = %q", got)
	}
	// A string matching a declared entry's url template selects that entry.
	if got, _ := resolveServer(doc, nil, nil, ctxWith("https://{env}.example.com"), ""); got != "https://api.example.com" {
		t.Errorf("string url-template selection = %q", got)
	}
	// Select by index.
	if got, _ := resolveServer(doc, nil, nil, ctxWith(map[string]any{"index": float64(1)}), ""); got != "https://alt.example.com/base/" {
		t.Errorf("index selection = %q", got)
	}

	// Variables against the default entry, enum-validated.
	if got, _ := resolveServer(doc, nil, nil, ctxWith(map[string]any{"variables": map[string]any{"env": "staging"}}), ""); got != "https://staging.example.com" {
		t.Errorf("variables = %q", got)
	}
	// Variable substitutions honor the artifact's enum. A complete base URL
	// remains the distinct target-override lane tested above.
	if got, err := resolveServer(doc, nil, nil, ctxWith(map[string]any{"variables": map[string]any{"env": "prod"}}), ""); err == nil || got != "" {
		t.Errorf("out-of-enum value must refuse, got %q err %v", got, err)
	}
	if _, err := resolveServer(doc, nil, nil, ctxWith(map[string]any{"variables": map[string]any{"nope": "x"}}), ""); err == nil {
		t.Error("an undeclared variable name must refuse loudly")
	}

	// A config that selects nothing is loud, not silently ignored.
	if _, err := resolveServer(doc, nil, nil, ctxWith(map[string]any{"url": "https://unknown.example.com"}), ""); err == nil {
		t.Error("a url matching no declared entry must refuse loudly")
	}
	if _, err := resolveServer(doc, nil, nil, ctxWith(map[string]any{"index": float64(9)}), ""); err == nil {
		t.Error("an out-of-range index must refuse loudly")
	}
}

// A relative effective-server URL resolves against the artifact's base URI
// (the source's location) per RFC 3986; the one pre-dispatch refusal is a
// server URL that cannot resolve absolute.
func TestResolveServer_RelativeResolution(t *testing.T) {
	doc := &openapi3.T{Servers: openapi3.Servers{{URL: "/api/v3"}}}

	got, err := resolveServer(doc, nil, nil, nil, "https://example.com/specs/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://example.com/api/v3" {
		t.Errorf("relative server = %q, want RFC 3986 resolution against the location", got)
	}

	// No location → no base URI → refusal.
	if _, err := resolveServer(doc, nil, nil, nil, ""); err == nil {
		t.Error("a relative server URL with no base URI must refuse pre-dispatch")
	}
	// A relative reference that is not path-absolute also resolves per RFC 3986.
	rel := &openapi3.T{Servers: openapi3.Servers{{URL: "v2"}}}
	got, err = resolveServer(rel, nil, nil, nil, "https://example.com/specs/openapi.json")
	if err != nil || got != "https://example.com/specs/v2" {
		t.Errorf("relative-path server = (%q, %v)", got, err)
	}
}

// The legacy context.metadata.baseURL override still works, below the
// configuration point.
func TestResolveServer_LegacyMetadataBaseURL(t *testing.T) {
	doc := serversDoc()
	ctx := map[string]any{"metadata": map[string]any{"baseURL": "https://meta.example.com/"}}
	if got, _ := resolveServer(doc, nil, nil, ctx, ""); got != "https://meta.example.com/" {
		t.Errorf("metadata baseURL = %q", got)
	}
	// configuration.server wins over metadata.baseURL.
	ctx["configuration"] = map[string]any{"server": "https://config.example.com"}
	if got, _ := resolveServer(doc, nil, nil, ctx, ""); got != "https://config.example.com" {
		t.Errorf("configuration point must win over legacy metadata, got %q", got)
	}
}

// A declared variable with no default and no supplied value is loud.
func TestResolveServer_MissingVariableDefault(t *testing.T) {
	doc := &openapi3.T{
		Servers: openapi3.Servers{{
			URL:       "https://{host}/v1",
			Variables: map[string]*openapi3.ServerVariable{"host": {}},
		}},
	}
	_, err := resolveServer(doc, nil, nil, nil, "")
	if err == nil || !strings.Contains(err.Error(), "host") {
		t.Errorf("expected a loud missing-default error naming the variable, got %v", err)
	}
}

// TestResolveServer_ConfigRequired pins R1a: a resolvable-missing server value
// surfaces as a *configRequired (point "server"), which the invoke path turns
// into a config.value CONTEXT_REQUIRED — not a terminal error. An undefaulted,
// unsupplied server variable and a server URL with no absolute base are both
// resolvable by consumer supply.
func TestResolveServer_ConfigRequired(t *testing.T) {
	// Undefaulted, unsupplied variable → config.value on the server point.
	doc := &openapi3.T{
		OpenAPI: "3.1.2",
		Servers: openapi3.Servers{{
			URL:       "https://{env}.example.com",
			Variables: map[string]*openapi3.ServerVariable{"env": {Enum: []string{"prod", "staging"}}},
		}},
	}
	_, err := resolveServer(doc, nil, nil, nil, "")
	var cr *configRequired
	if !errorsAsConfigRequired(err, &cr) {
		t.Fatalf("expected *configRequired for an undefaulted server variable, got %v", err)
	}
	if cr.point != "server" || cr.path != "/variables/env" {
		t.Errorf("configRequired = {point:%q path:%q}, want {server /variables/env}", cr.point, cr.path)
	}
	if members, _ := cr.schema["enum"].([]any); len(members) != 2 {
		t.Errorf("schema = %v, want {\"enum\": <the declared enum>}", cr.schema)
	}

	// Relative server URL with no absolute base → config.value at /url.
	docRel := &openapi3.T{OpenAPI: "3.0.3", Servers: openapi3.Servers{{URL: "/"}}}
	_, err = resolveServer(docRel, nil, nil, nil, "")
	if !errorsAsConfigRequired(err, &cr) {
		t.Fatalf("expected *configRequired for an unresolvable server URL, got %v", err)
	}
	if cr.point != "server" || cr.path != "/url" {
		t.Errorf("configRequired = {point:%q path:%q}, want {server /url}", cr.point, cr.path)
	}
}

func errorsAsConfigRequired(err error, target **configRequired) bool {
	for err != nil {
		if cr, ok := err.(*configRequired); ok {
			*target = cr
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
