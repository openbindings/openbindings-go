package asyncapi

import (
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
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
	doc := &document{
		Servers: map[string]server{
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
	doc := &document{
		Servers: map[string]server{
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
	doc := &document{}
	_, _, err := resolveServer(doc, nil)
	if err == nil {
		t.Error("expected error for doc with no servers")
	}
}

// secureDoc builds a doc whose server declares the named security schemes.
// Each name becomes its own $ref list entry — the AsyncAPI 3.0 shape, where
// `security` is a flat list of Security Scheme Objects or Reference
// Objects, never OpenAPI/2.x-style requirement maps.
func secureDoc(schemes map[string]securityScheme, names ...string) *document {
	reqs := make([]securityRequirement, 0, len(names))
	for _, n := range names {
		reqs = append(reqs, securityRequirement{Ref: "#/components/securitySchemes/" + n})
	}
	return &document{
		Servers: map[string]server{
			"prod": {Host: "api.example.com", Protocol: "https", Security: reqs},
		},
		Operations: map[string]asyncOperation{
			"op": {Action: "send", Channel: channelRef{Ref: "#/channels/ch"}},
		},
		Components: &components{SecuritySchemes: schemes},
	}
}

func TestRequiredContext_BearerRequirement(t *testing.T) {
	doc := secureDoc(map[string]securityScheme{
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
	doc := secureDoc(map[string]securityScheme{
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

// TestRequiredContext_ServerAndOperationAreConjunctive verifies the
// ASYNC-P-07 derivation: the targeted server's security applies AND the
// operation's security, when declared, applies in addition — the operation
// never displaces the server. Within each list one entry suffices, so the
// challenge is the cross product: one server entry paired with one
// operation entry per alternative.
func TestRequiredContext_ServerAndOperationAreConjunctive(t *testing.T) {
	doc := secureDoc(map[string]securityScheme{
		"bearer": {Type: "http", Scheme: "bearer"},
		"key":    {Type: "httpApiKey", In: "query", Name: "k"},
	}, "bearer")
	op := doc.Operations["op"]
	op.Security = []securityRequirement{{Ref: "#/components/securitySchemes/key"}}

	details := requiredContext(doc, &op, "https://api.example.com", nil)
	if details == nil || len(details.Alternatives) != 1 {
		t.Fatalf("expected 1 alternative (1 server entry x 1 op entry), got %+v", details)
	}
	reqs := details.Alternatives[0].Requirements
	if len(reqs) != 2 || reqs[0].Type != "auth.bearer" || reqs[1].Type != "auth.apiKey" {
		t.Fatalf("expected conjunctive [auth.bearer auth.apiKey], got %+v", reqs)
	}

	// Either credential alone must NOT satisfy the conjunction.
	if got := requiredContext(doc, &op, "https://api.example.com", map[string]any{"bearerToken": "t"}); got == nil {
		t.Error("bearer alone must not satisfy server AND operation security")
	}
	if got := requiredContext(doc, &op, "https://api.example.com", map[string]any{"apiKeys": map[string]any{"key": "k"}}); got == nil {
		t.Error("apiKey alone must not satisfy server AND operation security")
	}
	// Both together satisfy.
	both := map[string]any{"bearerToken": "t", "apiKeys": map[string]any{"key": "k"}}
	if got := requiredContext(doc, &op, "https://api.example.com", both); got != nil {
		t.Errorf("bearer+apiKey should satisfy the conjunction, got %+v", got)
	}
}

// TestRequiredContext_SameSchemeBothLevelsIsOneRequirement verifies a scheme
// declared on both the server and the operation is one requirement in the
// paired alternative, never a duplicated conjunct.
func TestRequiredContext_SameSchemeBothLevelsIsOneRequirement(t *testing.T) {
	doc := secureDoc(map[string]securityScheme{
		"bearer": {Type: "http", Scheme: "bearer"},
	}, "bearer")
	op := doc.Operations["op"]
	op.Security = []securityRequirement{{Ref: "#/components/securitySchemes/bearer"}}

	details := requiredContext(doc, &op, "https://api.example.com", nil)
	if details == nil || len(details.Alternatives) != 1 {
		t.Fatalf("expected 1 alternative, got %+v", details)
	}
	if reqs := details.Alternatives[0].Requirements; len(reqs) != 1 || reqs[0].Type != "auth.bearer" {
		t.Fatalf("expected the shared scheme deduplicated to one requirement, got %+v", reqs)
	}
	if got := requiredContext(doc, &op, "https://api.example.com", map[string]any{"bearerToken": "t"}); got != nil {
		t.Errorf("the one bearer credential should satisfy, got %+v", got)
	}
}

// TestRequiredContext_MultipleEntriesAreIndependentAlternatives verifies the
// AsyncAPI 3.0 shape: `security` is a flat list, and each entry is its own
// standalone alternative (pure OR). There is no OpenAPI/2.x-style
// requirement-object that groups several scheme names into one AND'd
// alternative — a list of two entries always means "satisfy either", never
// "satisfy both".
func TestRequiredContext_MultipleEntriesAreIndependentAlternatives(t *testing.T) {
	doc := &document{
		Servers: map[string]server{
			"prod": {Host: "api.example.com", Protocol: "https", Security: []securityRequirement{
				{Ref: "#/components/securitySchemes/bearer"},
				{Ref: "#/components/securitySchemes/key"},
			}},
		},
		Operations: map[string]asyncOperation{"op": {Action: "send"}},
		Components: &components{SecuritySchemes: map[string]securityScheme{
			"bearer": {Type: "http", Scheme: "bearer"},
			"key":    {Type: "apiKey", In: "header", Name: "X-Key"},
		}},
	}
	op := doc.Operations["op"]

	details := requiredContext(doc, &op, "https://api.example.com", nil)
	if details == nil || len(details.Alternatives) != 2 {
		t.Fatalf("expected 2 independent alternatives, got %+v", details)
	}
	for _, alt := range details.Alternatives {
		if len(alt.Requirements) != 1 {
			t.Errorf("each AsyncAPI 3.0 security entry is a single-scheme alternative, got %+v", alt)
		}
	}

	// Either credential alone suffices (pure OR, never a conjunction).
	if got := requiredContext(doc, &op, "https://api.example.com", map[string]any{"bearerToken": "t"}); got != nil {
		t.Errorf("bearer alone should satisfy one of the alternatives, got %+v", got)
	}
	if got := requiredContext(doc, &op, "https://api.example.com", map[string]any{"apiKey": "k"}); got != nil {
		t.Errorf("apiKey alone should satisfy one of the alternatives, got %+v", got)
	}
}

// TestRequiredContext_InlineSchemeObject verifies that a `security` list
// entry MAY be an inline Security Scheme Object (not just a $ref to
// components.securitySchemes) — both forms are valid per the AsyncAPI 3.0
// spec, and an inline entry needs no components.securitySchemes section at
// all to be enforced.
func TestRequiredContext_InlineSchemeObject(t *testing.T) {
	doc := &document{
		Servers: map[string]server{
			"prod": {Host: "api.example.com", Protocol: "https", Security: []securityRequirement{
				{securityScheme: securityScheme{Type: "http", Scheme: "bearer", Description: "inline"}},
			}},
		},
		Operations: map[string]asyncOperation{"op": {Action: "send"}},
		// Deliberately no Components: the scheme is inline, not a $ref.
	}
	op := doc.Operations["op"]

	details := requiredContext(doc, &op, "https://api.example.com", nil)
	if details == nil || len(details.Alternatives) != 1 {
		t.Fatalf("expected 1 alternative from the inline scheme, got %+v", details)
	}
	req := details.Alternatives[0].Requirements[0]
	if req.Type != "auth.bearer" || req.Description != "inline" {
		t.Errorf("requirement = %+v, want auth.bearer/inline", req)
	}
}

// TestRequiredContext_UnresolvableRefSkipsEntry verifies that a $ref which
// cannot be resolved (dangling, or components.securitySchemes absent) is
// dropped rather than degraded into a weaker requirement — mirroring the
// "inexpressible scheme" rule, now expressed per-entry since AsyncAPI 3.0
// entries are standalone.
func TestRequiredContext_UnresolvableRefSkipsEntry(t *testing.T) {
	doc := &document{
		Servers: map[string]server{
			"prod": {Host: "api.example.com", Protocol: "https", Security: []securityRequirement{
				{Ref: "#/components/securitySchemes/bearer"},
				{Ref: "#/components/securitySchemes/missing"},
			}},
		},
		Operations: map[string]asyncOperation{"op": {Action: "send"}},
		Components: &components{SecuritySchemes: map[string]securityScheme{
			"bearer": {Type: "http", Scheme: "bearer"},
		}},
	}
	op := doc.Operations["op"]
	details := requiredContext(doc, &op, "https://api.example.com", nil)
	if details == nil || len(details.Alternatives) != 1 {
		t.Fatalf("expected exactly 1 alternative (the resolvable bearer entry), got %+v", details)
	}
	if details.Alternatives[0].Requirements[0].Type != "auth.bearer" {
		t.Errorf("expected the resolvable entry to survive, got %+v", details.Alternatives[0])
	}
}

// TestRequiredContext_DerivesFromConnectionServer verifies the requirements
// come from the SAME server the connection targets (first sorted supported
// server), never from another server that happens to declare security.
func TestRequiredContext_DerivesFromConnectionServer(t *testing.T) {
	doc := &document{
		Servers: map[string]server{
			// "a" sorts first and is the connection target: no security.
			"a": {Host: "open.example.com", Protocol: "https"},
			// "b" declares security but is NOT the server dialed.
			"b": {Host: "secure.example.com", Protocol: "https", Security: []securityRequirement{{Ref: "#/components/securitySchemes/bearer"}}},
		},
		Operations: map[string]asyncOperation{"op": {Action: "send"}},
		Components: &components{SecuritySchemes: map[string]securityScheme{
			"bearer": {Type: "http", Scheme: "bearer"},
		}},
	}
	op := doc.Operations["op"]
	if got := requiredContext(doc, &op, "https://open.example.com", nil); got != nil {
		t.Errorf("requirements must derive from the connection's server (no security), got %+v", got)
	}
}

func TestRequiredContext_NoDeclaredSecurity(t *testing.T) {
	doc := &document{
		Servers:    map[string]server{"prod": {Host: "api.example.com", Protocol: "https"}},
		Operations: map[string]asyncOperation{"op": {Action: "send"}},
	}
	op := doc.Operations["op"]
	if got := requiredContext(doc, &op, "https://api.example.com", nil); got != nil {
		t.Errorf("expected nil without declared security, got %+v", got)
	}
}

// TestRequiredContext_UnknownSchemeSurfaced verifies the R2.c ruling
// (flipped from TestRequiredContext_UnknownSchemeNotEnforced, which pinned
// the old drop behavior): an unmapped scheme family (e.g. "scramSha256") is
// now SURFACED as "auth."+type with its components.securitySchemes key as
// Name (R2.a), instead of silently dropped — a document whose every
// alternative is unmapped now produces a challenge rather than dispatching
// unauthenticated.
func TestRequiredContext_UnknownSchemeSurfaced(t *testing.T) {
	doc := secureDoc(map[string]securityScheme{
		"custom": {Type: "scramSha256"},
	}, "custom")
	op := doc.Operations["op"]
	got := requiredContext(doc, &op, "https://api.example.com", nil)
	if got == nil {
		t.Fatal("expected a challenge surfacing the unmapped scheme, got nil")
	}
	if len(got.Alternatives) != 1 || len(got.Alternatives[0].Requirements) != 1 {
		t.Fatalf("unexpected challenge shape: %+v", got)
	}
	req := got.Alternatives[0].Requirements[0]
	if req.Type != "auth.scramSha256" {
		t.Errorf("Type = %q, want auth.scramSha256", req.Type)
	}
	if req.Name != "custom" {
		t.Errorf("Name = %q, want the securitySchemes key %q", req.Name, "custom")
	}
	// Unresolvable by the built-in satisfaction check (rule 10: no resolver
	// at this layer invented for an unmapped family).
	if openbindings.ContextSatisfies(map[string]any{"bearerToken": "t"}, got) {
		t.Error("an unmapped requirement must never be satisfiable by the built-in check")
	}
}

// TestRequiredContext_UnmappedHTTPSchemeSurfaced verifies the R2.c ruling's
// http-scheme naming: an unmapped "http" scheme (e.g. digest) surfaces as
// "auth.http.<scheme>", mirroring openapi's naming exactly.
func TestRequiredContext_UnmappedHTTPSchemeSurfaced(t *testing.T) {
	doc := secureDoc(map[string]securityScheme{
		"digestAuth": {Type: "http", Scheme: "digest"},
	}, "digestAuth")
	op := doc.Operations["op"]
	got := requiredContext(doc, &op, "https://api.example.com", nil)
	if got == nil || len(got.Alternatives) != 1 {
		t.Fatalf("expected a surfaced challenge, got %+v", got)
	}
	req := got.Alternatives[0].Requirements[0]
	if req.Type != "auth.http.digest" {
		t.Errorf("Type = %q, want auth.http.digest", req.Type)
	}
	if req.Name != "digestAuth" {
		t.Errorf("Name = %q, want %q", req.Name, "digestAuth")
	}
}

// TestRequiredContext_NameFromRefVsInline verifies the R2.a ruling's naming
// rule for AsyncAPI: a $ref entry carries the components.securitySchemes key
// it resolved through as Name; an inline scheme object has no addressable
// name and carries an empty Name.
func TestRequiredContext_NameFromRefVsInline(t *testing.T) {
	doc := secureDoc(map[string]securityScheme{
		"bearer": {Type: "http", Scheme: "bearer"},
	}, "bearer")
	op := doc.Operations["op"]
	got := requiredContext(doc, &op, "https://api.example.com", nil)
	if got == nil || len(got.Alternatives) != 1 {
		t.Fatalf("expected 1 alternative, got %+v", got)
	}
	if name := got.Alternatives[0].Requirements[0].Name; name != "bearer" {
		t.Errorf("$ref entry Name = %q, want %q", name, "bearer")
	}

	inlineDoc := &document{
		Servers: map[string]server{
			"prod": {Host: "api.example.com", Protocol: "https", Security: []securityRequirement{
				{securityScheme: securityScheme{Type: "http", Scheme: "bearer"}},
			}},
		},
		Operations: map[string]asyncOperation{"op": {Action: "send"}},
	}
	inlineOp := inlineDoc.Operations["op"]
	gotInline := requiredContext(inlineDoc, &inlineOp, "https://api.example.com", nil)
	if gotInline == nil || len(gotInline.Alternatives) != 1 {
		t.Fatalf("expected 1 alternative, got %+v", gotInline)
	}
	if name := gotInline.Alternatives[0].Requirements[0].Name; name != "" {
		t.Errorf("inline scheme Name = %q, want empty (no addressable name)", name)
	}
}

func TestRequirementType_Families(t *testing.T) {
	cases := []struct {
		scheme securityScheme
		want   string
	}{
		{securityScheme{Type: "http", Scheme: "bearer"}, "auth.bearer"},
		{securityScheme{Type: "http", Scheme: "Bearer"}, "auth.bearer"},
		{securityScheme{Type: "http", Scheme: "basic"}, "auth.basic"},
		{securityScheme{Type: "http", Scheme: "digest"}, ""},
		{securityScheme{Type: "httpBearer"}, "auth.bearer"},
		{securityScheme{Type: "userPassword"}, "auth.basic"},
		{securityScheme{Type: "apiKey"}, "auth.apiKey"},
		{securityScheme{Type: "httpApiKey"}, "auth.apiKey"},
		{securityScheme{Type: "oauth2"}, "auth.oauth2"},
		{securityScheme{Type: "scramSha256"}, ""},
	}
	for _, tc := range cases {
		if got := requirementType(tc.scheme); got != tc.want {
			t.Errorf("requirementType(%+v) = %q, want %q", tc.scheme, got, tc.want)
		}
	}
}

// TestUnmappedRequirementType verifies the R2.c naming rule mirrors openapi
// exactly: an unmapped "http" scheme surfaces as "auth.http.<scheme>";
// anything else surfaces as "auth."+type verbatim.
func TestUnmappedRequirementType(t *testing.T) {
	cases := []struct {
		scheme securityScheme
		want   string
	}{
		{securityScheme{Type: "http", Scheme: "digest"}, "auth.http.digest"},
		{securityScheme{Type: "http", Scheme: "negotiate"}, "auth.http.negotiate"},
		{securityScheme{Type: "scramSha256"}, "auth.scramSha256"},
		{securityScheme{Type: "X509"}, "auth.X509"},
	}
	for _, tc := range cases {
		if got := unmappedRequirementType(tc.scheme); got != tc.want {
			t.Errorf("unmappedRequirementType(%+v) = %q, want %q", tc.scheme, got, tc.want)
		}
	}
}

// TestOAuth2Requirement_GrantTypeSurfacesSelectedFlow mirrors openapi's
// oauth2 grantType test (R2.b ruling): grantType names exactly the flow the
// fixed priority selects (authorizationCode > implicit > password >
// clientCredentials), alongside authorizeUrl/tokenUrl/scopes.
func TestOAuth2Requirement_GrantTypeSurfacesSelectedFlow(t *testing.T) {
	cases := []struct {
		name  string
		flows *oauthFlows
		want  string
	}{
		{
			"authorizationCode",
			&oauthFlows{AuthorizationCode: &oauthFlow{AuthorizationURL: "https://a.example.com/authorize", TokenURL: "https://a.example.com/token"}},
			"authorization_code",
		},
		{
			"implicit",
			&oauthFlows{Implicit: &oauthFlow{AuthorizationURL: "https://a.example.com/authorize"}},
			"implicit",
		},
		{
			"password",
			&oauthFlows{Password: &oauthFlow{TokenURL: "https://a.example.com/token"}},
			"password",
		},
		{
			"clientCredentials",
			&oauthFlows{ClientCredentials: &oauthFlow{TokenURL: "https://a.example.com/token"}},
			"client_credentials",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scheme := securityScheme{Type: "oauth2", Flows: tc.flows}
			req := oauth2Requirement(scheme, "https://api.example.com")
			if req.Type != "auth.oauth2" {
				t.Fatalf("Type = %q, want auth.oauth2", req.Type)
			}
			if got := req.Extra["grantType"]; got != tc.want {
				t.Errorf("grantType = %v, want %q", got, tc.want)
			}
		})
	}
}

// TestOAuth2Requirement_CarriesFlowFields verifies authorizeUrl/tokenUrl
// (relative ones absolutized against the server URL) and scopes ride
// alongside grantType — parity with openapi's oauth2Requirement.
func TestOAuth2Requirement_CarriesFlowFields(t *testing.T) {
	scheme := securityScheme{
		Type: "oauth2",
		Flows: &oauthFlows{
			AuthorizationCode: &oauthFlow{
				AuthorizationURL: "/oauth/authorize", // relative -> absolutized
				TokenURL:         "https://auth.example.com/oauth/token",
				Scopes:           map[string]string{"read": "Read", "write": "Write"},
			},
		},
	}
	req := oauth2Requirement(scheme, "https://api.example.com")
	if got := req.Extra["authorizeUrl"]; got != "https://api.example.com/oauth/authorize" {
		t.Errorf("authorizeUrl = %v, want absolutized host URL", got)
	}
	if got := req.Extra["tokenUrl"]; got != "https://auth.example.com/oauth/token" {
		t.Errorf("tokenUrl = %v", got)
	}
	scopes, _ := req.Extra["scopes"].([]string)
	if len(scopes) != 2 || scopes[0] != "read" || scopes[1] != "write" {
		t.Errorf("scopes = %v, want [read write]", req.Extra["scopes"])
	}
}

// TestOAuth2Requirement_NoFlows verifies a bare oauth2 scheme (no `flows`)
// carries only the type — no grantType, no Extra.
func TestOAuth2Requirement_NoFlows(t *testing.T) {
	req := oauth2Requirement(securityScheme{Type: "oauth2"}, "https://api.example.com")
	if req.Type != "auth.oauth2" || req.Extra != nil {
		t.Errorf("expected bare auth.oauth2 with no Extra, got %+v", req)
	}
}

// AsyncAPI 3.0: a message that omits contentType takes the document's
// defaultContentType — still the declared lane, never payload sniffing.
// Regression: the model didn't parse defaultContentType, so JSON events
// from documents relying on it decoded as strings and failed OBI-T-08.
func TestMessageContentType_FallsBackToDocumentDefault(t *testing.T) {
	doc := &document{
		AsyncAPI:           "3.0.0",
		DefaultContentType: "application/json",
		Operations: map[string]asyncOperation{
			"sub": {Action: "send", Messages: []messageRef{{Ref: "#/components/messages/plain"}}},
		},
		Components: &components{
			Messages: map[string]message{"plain": {Name: "plain"}}, // no contentType
		},
	}
	op := doc.Operations["sub"]
	if got := messageContentType(doc, &op); got != "application/json" {
		t.Errorf("messageContentType = %q, want the document default", got)
	}
	// A per-message declaration still wins over the default.
	doc.Components.Messages["plain"] = message{Name: "plain", ContentType: "text/plain"}
	if got := messageContentType(doc, &op); got != "text/plain" {
		t.Errorf("messageContentType = %q, want the per-message declaration", got)
	}
}

// Direction-correct decode (ASYNC-P-05): a publish invocation's output (the
// response) decodes by the REPLY-side declarations, never the operation's
// own request-side messages.
func TestReplyContentType_UsesReplySideDeclarations(t *testing.T) {
	doc := &document{
		AsyncAPI: "3.0.0",
		Operations: map[string]asyncOperation{
			"pub": {
				Action:   "receive",
				Messages: []messageRef{{Ref: "#/components/messages/req"}},
				Reply:    &operationReply{Messages: []messageRef{{Ref: "#/components/messages/rep"}}},
			},
		},
		Components: &components{
			Messages: map[string]message{
				"req": {Name: "req", ContentType: "text/plain"},
				"rep": {Name: "rep", ContentType: "application/json"},
			},
		},
	}
	op := doc.Operations["pub"]
	if got := replyContentType(doc, &op); got != "application/json" {
		t.Errorf("replyContentType = %q, want the reply-side declaration", got)
	}
	if got := messageContentType(doc, &op); got != "text/plain" {
		t.Errorf("messageContentType = %q, want the request-side declaration", got)
	}
}
