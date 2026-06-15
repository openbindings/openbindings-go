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
	if details.Target != "https://api.example.com" {
		t.Errorf("Target = %q, want https://api.example.com", details.Target)
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

// TestRequiredContext_ConjunctionMapsToOneAlternative verifies the AND shape:
// a requirement OBJECT naming several schemes is one alternative carrying ALL
// of them as requirements (the document model represents multi-scheme
// conjunctions).
func TestRequiredContext_ConjunctionMapsToOneAlternative(t *testing.T) {
	doc := &Document{
		Servers: map[string]Server{
			"prod": {Host: "api.example.com", Protocol: "https", Security: []map[string][]string{
				{"bearer": {}, "key": {}}, // one object: bearer AND key
			}},
		},
		Operations: map[string]Operation{"op": {Action: "send"}},
		Components: &Components{SecuritySchemes: map[string]SecurityScheme{
			"bearer": {Type: "http", Scheme: "bearer"},
			"key":    {Type: "apiKey", In: "header", Name: "X-Key"},
		}},
	}
	op := doc.Operations["op"]

	details := requiredContext(doc, &op, "https://api.example.com", nil)
	if details == nil || len(details.Alternatives) != 1 {
		t.Fatalf("expected 1 alternative, got %+v", details)
	}
	reqs := details.Alternatives[0].Requirements
	if len(reqs) != 2 {
		t.Fatalf("expected 2 requirements in the conjunction, got %+v", reqs)
	}
	types := map[string]bool{reqs[0].Type: true, reqs[1].Type: true}
	if !types["auth.bearer"] || !types["auth.apiKey"] {
		t.Errorf("requirement types = %v, want auth.bearer and auth.apiKey", types)
	}

	// One credential alone must NOT satisfy the conjunction.
	if got := requiredContext(doc, &op, "https://api.example.com", map[string]any{"bearerToken": "t"}); got == nil {
		t.Error("bearer alone must not satisfy a bearer-AND-apiKey conjunction")
	}
	// Both together do.
	if got := requiredContext(doc, &op, "https://api.example.com", map[string]any{"bearerToken": "t", "apiKey": "k"}); got != nil {
		t.Errorf("bearer+apiKey should satisfy, got %+v", got)
	}
}

// TestRequiredContext_EmptyRequirementObjectAllowsAnonymous verifies the
// openapi-mirroring rule: an empty requirement object means anonymous access
// is allowed, so no challenge is warranted at all.
func TestRequiredContext_EmptyRequirementObjectAllowsAnonymous(t *testing.T) {
	doc := secureDoc(map[string]SecurityScheme{
		"bearer": {Type: "http", Scheme: "bearer"},
	}, "bearer")
	server := doc.Servers["prod"]
	server.Security = append(server.Security, map[string][]string{}) // anonymous alternative
	doc.Servers["prod"] = server
	op := doc.Operations["op"]

	if got := requiredContext(doc, &op, "https://api.example.com", nil); got != nil {
		t.Errorf("an empty requirement object allows anonymous access, got %+v", got)
	}
}

// TestRequiredContext_InexpressibleSchemeSkipsWholeAlternative verifies that
// a conjunction containing an unmappable scheme is dropped entirely rather
// than degraded into a weaker requirement.
func TestRequiredContext_InexpressibleSchemeSkipsWholeAlternative(t *testing.T) {
	doc := &Document{
		Servers: map[string]Server{
			"prod": {Host: "api.example.com", Protocol: "https", Security: []map[string][]string{
				{"bearer": {}, "custom": {}},
			}},
		},
		Operations: map[string]Operation{"op": {Action: "send"}},
		Components: &Components{SecuritySchemes: map[string]SecurityScheme{
			"bearer": {Type: "http", Scheme: "bearer"},
			"custom": {Type: "scramSha256"},
		}},
	}
	op := doc.Operations["op"]
	if got := requiredContext(doc, &op, "https://api.example.com", nil); got != nil {
		t.Errorf("an alternative with an inexpressible scheme must be skipped, got %+v", got)
	}
}

// TestRequiredContext_DerivesFromConnectionServer verifies the requirements
// come from the SAME server the connection targets (first sorted supported
// server), never from another server that happens to declare security.
func TestRequiredContext_DerivesFromConnectionServer(t *testing.T) {
	doc := &Document{
		Servers: map[string]Server{
			// "a" sorts first and is the connection target: no security.
			"a": {Host: "open.example.com", Protocol: "https"},
			// "b" declares security but is NOT the server dialed.
			"b": {Host: "secure.example.com", Protocol: "https", Security: []map[string][]string{{"bearer": {}}}},
		},
		Operations: map[string]Operation{"op": {Action: "send"}},
		Components: &Components{SecuritySchemes: map[string]SecurityScheme{
			"bearer": {Type: "http", Scheme: "bearer"},
		}},
	}
	op := doc.Operations["op"]
	if got := requiredContext(doc, &op, "https://open.example.com", nil); got != nil {
		t.Errorf("requirements must derive from the connection's server (no security), got %+v", got)
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
