package asyncapi

import (
	"testing"

)

func TestParseRef_BareID(t *testing.T) {
	got, err := parseRef("sendMessage")
	if err != nil {
		t.Fatal(err)
	}
	if got != "sendMessage" {
		t.Errorf("parseRef(%q) = %q, want %q", "sendMessage", got, "sendMessage")
	}
}

func TestParseRef_HashOperations(t *testing.T) {
	got, err := parseRef("#/operations/receiveEvents")
	if err != nil {
		t.Fatal(err)
	}
	if got != "receiveEvents" {
		t.Errorf("parseRef(%q) = %q, want %q", "#/operations/receiveEvents", got, "receiveEvents")
	}
}

func TestParseRef_Empty(t *testing.T) {
	_, err := parseRef("")
	if err == nil {
		t.Error("expected error for empty ref")
	}
}

func TestResolveServer_HTTPServer(t *testing.T) {
	doc := &Document{
		Servers: map[string]Server{
			"prod": {Host: "api.example.com", Protocol: "https"},
		},
	}
	url, proto, err := resolveServer(doc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if proto != "https" {
		t.Errorf("protocol = %q, want https", proto)
	}
	if url != "https://api.example.com" {
		t.Errorf("url = %q, want https://api.example.com", url)
	}
}

func TestResolveServer_MetadataOverride(t *testing.T) {
	doc := &Document{
		Servers: map[string]Server{
			"prod": {Host: "api.example.com", Protocol: "https"},
		},
	}
	ctx := map[string]any{"metadata": map[string]any{"baseURL": "http://localhost:8080"}}
	url, proto, err := resolveServer(doc, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if proto != "http" {
		t.Errorf("protocol = %q, want http", proto)
	}
	if url != "http://localhost:8080" {
		t.Errorf("url = %q, want http://localhost:8080", url)
	}
}

func TestResolveServer_NoServers(t *testing.T) {
	doc := &Document{}
	_, _, err := resolveServer(doc, nil)
	if err == nil {
		t.Error("expected error for doc with no servers")
	}
}

// secureDoc builds a doc whose server declares the named security schemes.
func secureDoc(schemes map[string]SecurityScheme, names ...string) *Document {
	reqs := make([]map[string][]string, 0, len(names))
	for _, n := range names {
		reqs = append(reqs, map[string][]string{n: {}})
	}
	return &Document{
		Servers: map[string]Server{
			"prod": {Host: "api.example.com", Protocol: "https", Security: reqs},
		},
		Operations: map[string]Operation{
			"op": {Action: "send", Channel: ChannelRef{Ref: "#/channels/ch"}},
		},
		Components: &Components{SecuritySchemes: schemes},
	}
}

func TestRequiredContext_BearerRequirement(t *testing.T) {
	doc := secureDoc(map[string]SecurityScheme{
		"bearer": {Type: "http", Scheme: "bearer", Description: "API token"},
	}, "bearer")
	op := doc.Operations["op"]

	details := requiredContext(doc, &op, "https://api.example.com", nil)
	if details == nil {
		t.Fatal("expected CONTEXT_REQUIRED details, got nil")
	}
	if details.Key != "api.example.com" {
		t.Errorf("Key = %q, want api.example.com", details.Key)
	}
	if len(details.Alternatives) != 1 || len(details.Alternatives[0].Requirements) != 1 {
		t.Fatalf("alternatives = %+v", details.Alternatives)
	}
	req := details.Alternatives[0].Requirements[0]
	if req.Type != "auth.bearer" {
		t.Errorf("Type = %q, want auth.bearer", req.Type)
	}
	if req.Description != "API token" {
		t.Errorf("Description = %q, want %q", req.Description, "API token")
	}

	// Context carrying the bearer token satisfies the requirement.
	if got := requiredContext(doc, &op, "https://api.example.com", map[string]any{"bearerToken": "t"}); got != nil {
		t.Errorf("expected nil when context satisfies, got %+v", got)
	}
}

func TestRequiredContext_AlternativesAnyOneSuffices(t *testing.T) {
	doc := secureDoc(map[string]SecurityScheme{
		"key":   {Type: "apiKey", In: "header", Name: "X-Key"},
		"basic": {Type: "userPassword"},
	}, "key", "basic")
	op := doc.Operations["op"]

	details := requiredContext(doc, &op, "https://api.example.com", nil)
	if details == nil || len(details.Alternatives) != 2 {
		t.Fatalf("expected 2 alternatives, got %+v", details)
	}
	types := map[string]bool{}
	for _, alt := range details.Alternatives {
		types[alt.Requirements[0].Type] = true
	}
	if !types["auth.apiKey"] || !types["auth.basic"] {
		t.Errorf("alternative types = %v, want auth.apiKey and auth.basic", types)
	}

	// Satisfying any one alternative suffices.
	if got := requiredContext(doc, &op, "https://api.example.com", map[string]any{"apiKey": "k-123"}); got != nil {
		t.Errorf("apiKey alone should satisfy, got %+v", got)
	}
}

func TestRequiredContext_OperationOverridesServer(t *testing.T) {
	doc := secureDoc(map[string]SecurityScheme{
		"bearer": {Type: "http", Scheme: "bearer"},
		"key":    {Type: "httpApiKey", In: "query", Name: "k"},
	}, "bearer")
	op := doc.Operations["op"]
	op.Security = []map[string][]string{{"key": {}}}

	details := requiredContext(doc, &op, "https://api.example.com", nil)
	if details == nil || len(details.Alternatives) != 1 {
		t.Fatalf("expected 1 alternative, got %+v", details)
	}
	if details.Alternatives[0].Requirements[0].Type != "auth.apiKey" {
		t.Errorf("operation-level security must win, got %+v", details.Alternatives[0])
	}
}

func TestRequiredContext_NoDeclaredSecurity(t *testing.T) {
	doc := &Document{
		Servers:    map[string]Server{"prod": {Host: "api.example.com", Protocol: "https"}},
		Operations: map[string]Operation{"op": {Action: "send"}},
	}
	op := doc.Operations["op"]
	if got := requiredContext(doc, &op, "https://api.example.com", nil); got != nil {
		t.Errorf("expected nil without declared security, got %+v", got)
	}
}

func TestRequiredContext_UnknownSchemeNotEnforced(t *testing.T) {
	doc := secureDoc(map[string]SecurityScheme{
		"custom": {Type: "scramSha256"},
	}, "custom")
	op := doc.Operations["op"]
	if got := requiredContext(doc, &op, "https://api.example.com", nil); got != nil {
		t.Errorf("unknown scheme families are not checkable, got %+v", got)
	}
}

func TestRequirementType_Families(t *testing.T) {
	cases := []struct {
		scheme SecurityScheme
		want   string
	}{
		{SecurityScheme{Type: "http", Scheme: "bearer"}, "auth.bearer"},
		{SecurityScheme{Type: "http", Scheme: "Bearer"}, "auth.bearer"},
		{SecurityScheme{Type: "http", Scheme: "basic"}, "auth.basic"},
		{SecurityScheme{Type: "http", Scheme: "digest"}, ""},
		{SecurityScheme{Type: "httpBearer"}, "auth.bearer"},
		{SecurityScheme{Type: "userPassword"}, "auth.basic"},
		{SecurityScheme{Type: "apiKey"}, "auth.apiKey"},
		{SecurityScheme{Type: "httpApiKey"}, "auth.apiKey"},
		{SecurityScheme{Type: "oauth2"}, "auth.oauth2"},
		{SecurityScheme{Type: "scramSha256"}, ""},
	}
	for _, tc := range cases {
		if got := requirementType(tc.scheme); got != tc.want {
			t.Errorf("requirementType(%+v) = %q, want %q", tc.scheme, got, tc.want)
		}
	}
}
