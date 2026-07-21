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
	if got, _ := resolveServer(doc, nil, &openapi3.Operation{}, nil, ""); got != "https://doc-a.example.com" {
		t.Errorf("document servers next (first entry), got %q", got)
	}
	// Implied "/" resolves against the artifact's base URI (the location).
	if got, _ := resolveServer(&openapi3.T{}, nil, nil, nil, "https://host.example.com/openapi.json"); got != "https://host.example.com" {
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

	// Select another entry by url (trailing slash trimmed for joining).
	if got, _ := resolveServer(doc, nil, nil, ctxWith(map[string]any{"url": "https://alt.example.com/base/"}), ""); got != "https://alt.example.com/base" {
		t.Errorf("url selection = %q", got)
	}
	// A string matching a declared entry's url template selects that entry.
	if got, _ := resolveServer(doc, nil, nil, ctxWith("https://{env}.example.com"), ""); got != "https://api.example.com" {
		t.Errorf("string url-template selection = %q", got)
	}
	// Select by index.
	if got, _ := resolveServer(doc, nil, nil, ctxWith(map[string]any{"index": float64(1)}), ""); got != "https://alt.example.com/base" {
		t.Errorf("index selection = %q", got)
	}

	// Variables against the default entry, enum-validated.
	if got, _ := resolveServer(doc, nil, nil, ctxWith(map[string]any{"variables": map[string]any{"env": "staging"}}), ""); got != "https://staging.example.com" {
		t.Errorf("variables = %q", got)
	}
	// An out-of-enum value is NOT refused (§9.3, R1): the enum is the
	// author's expectation, not a boundary; a full base-URL override bypasses
	// the declaration anyway.
	if got, err := resolveServer(doc, nil, nil, ctxWith(map[string]any{"variables": map[string]any{"env": "prod"}}), ""); err != nil || got != "https://prod.example.com" {
		t.Errorf("out-of-enum value must substitute, got %q err %v", got, err)
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
	if got, _ := resolveServer(doc, nil, nil, ctx, ""); got != "https://meta.example.com" {
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
