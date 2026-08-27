package openapi

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/openbindings/openbindings-go/invoke"

	"github.com/getkin/kin-openapi/openapi3"
)

// BindingSpec keeps the predominantly OpenAPI 3.0 historical white-box tests
// concise while production code exposes only explicit family constants.
// OpenAPI 3.1 tests name BindingSpecOpenAPI31 directly.
const BindingSpec = BindingSpecOpenAPI30

func bindingSpecForTestDocument(content any) string {
	var text string
	switch value := content.(type) {
	case string:
		text = value
	case []byte:
		text = string(value)
	case json.RawMessage:
		text = string(value)
	default:
		encoded, _ := json.Marshal(value)
		text = string(encoded)
	}
	if strings.Contains(text, `"openapi":"3.1`) ||
		strings.Contains(text, `"openapi": "3.1`) ||
		strings.Contains(text, "openapi: 3.1") {
		return BindingSpecOpenAPI31
	}
	return BindingSpecOpenAPI30
}

// ---------------------------------------------------------------------------
// parseSelector
// ---------------------------------------------------------------------------

func TestParseSelector_StandardJSONPointer(t *testing.T) {
	path, method, err := parseSelector("#/paths/~1users/get")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != "/users" {
		t.Errorf("path = %q, want %q", path, "/users")
	}
	if method != "get" {
		t.Errorf("method = %q, want %q", method, "get")
	}
}

// OAPI-D-03: the selector MUST be a JSON Pointer of the exact form
// #/paths/<escaped-path>/<method>. A prefix-less spelling was previously
// accepted leniently; that acceptance was non-conformant.
func TestParseSelector_RefusesWithoutLeadingHashSlash(t *testing.T) {
	_, _, err := parseSelector("paths/~1users~1{id}/delete")
	if err == nil {
		t.Fatal("expected refusal for a selector without the #/paths/ prefix (OAPI-D-03)")
	}
}

// OAPI-D-03: the path segment carries RFC 6901 escaping, so a conformant selector
// has exactly one path token. Unescaped multi-token spellings were
// previously accepted leniently; that acceptance was non-conformant.
func TestParseSelector_RefusesUnescapedPathTokens(t *testing.T) {
	_, _, err := parseSelector("#/paths/users/posts/get")
	if err == nil {
		t.Fatal("expected refusal for a selector with unescaped path tokens (OAPI-D-03)")
	}
}

func TestParseSelector_TildeEscaping(t *testing.T) {
	path, method, err := parseSelector("#/paths/~1a~0b~1c/post")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != "/a~b/c" {
		t.Errorf("path = %q, want %q", path, "/a~b/c")
	}
	if method != "post" {
		t.Errorf("method = %q, want %q", method, "post")
	}
}

// OAPI-D-03: the method is lowercase exactly as the artifact spells it —
// acceptance never case-folds. (This flips the previous lenient
// upper-casing pin, which was non-conformant.)
func TestParseSelector_RefusesUppercaseMethod(t *testing.T) {
	_, _, err := parseSelector("#/paths/~1users/GET")
	if err == nil {
		t.Fatal("expected refusal for an uppercase method (OAPI-D-03: no case folding)")
	}
	if !strings.Contains(err.Error(), "lowercase") {
		t.Errorf("error = %q, want it to explain the lowercase-exactly rule", err.Error())
	}
}

func TestParseSelector_ErrorTooFewParts(t *testing.T) {
	_, _, err := parseSelector("#/paths")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "must be a JSON Pointer") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "must be a JSON Pointer")
	}
}

func TestParseSelector_ErrorNonPathsPrefix(t *testing.T) {
	_, _, err := parseSelector("#/components/schemas/get")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "must be a JSON Pointer") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "must be a JSON Pointer")
	}
}

func TestParseSelector_ErrorInvalidMethod(t *testing.T) {
	_, _, err := parseSelector("#/paths/~1users/connect")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "invalid HTTP method") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "invalid HTTP method")
	}
}

// ---------------------------------------------------------------------------
// buildJSONPointerSelector
// ---------------------------------------------------------------------------

func TestBuildJSONPointerSelector_Simple(t *testing.T) {
	selector := buildJSONPointerSelector("/users", "get")
	if selector != "#/paths/~1users/get" {
		t.Errorf("selector = %q, want %q", selector, "#/paths/~1users/get")
	}
}

func TestBuildJSONPointerSelector_NestedPaths(t *testing.T) {
	selector := buildJSONPointerSelector("/users/{id}/posts", "post")
	if selector != "#/paths/~1users~1{id}~1posts/post" {
		t.Errorf("selector = %q, want %q", selector, "#/paths/~1users~1{id}~1posts/post")
	}
}

func TestBuildJSONPointerSelector_RoundTrip(t *testing.T) {
	originalPath := "/users/{id}/posts"
	originalMethod := "put"

	selector := buildJSONPointerSelector(originalPath, originalMethod)
	path, method, err := parseSelector(selector)
	if err != nil {
		t.Fatalf("round-trip parseSelector failed: %v", err)
	}
	if path != originalPath {
		t.Errorf("round-trip path = %q, want %q", path, originalPath)
	}
	if method != originalMethod {
		t.Errorf("round-trip method = %q, want %q", method, originalMethod)
	}
}

// ---------------------------------------------------------------------------
// Context key derivation (base URL → normalized context-store key). This is
// the Key the CONTEXT_REQUIRED challenge carries; auth is negotiated context,
// not an OBI security field.
// ---------------------------------------------------------------------------

func contextKey(doc *openapi3.T, location string) (string, error) {
	base, err := resolveServer(doc, nil, nil, nil, location)
	if err != nil {
		return "", err
	}
	return invoke.NormalizeContextKey(base), nil
}

func TestContextKey_AbsoluteURL(t *testing.T) {
	doc := &openapi3.T{
		Servers: openapi3.Servers{
			{URL: "https://api.example.com/v1"},
		},
	}
	key, err := contextKey(doc, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "api.example.com" {
		t.Errorf("key = %q, want %q", key, "api.example.com")
	}
}

func TestContextKey_RelativeURLWithSourceLocation(t *testing.T) {
	doc := &openapi3.T{
		Servers: openapi3.Servers{
			{URL: "/api/v1"},
		},
	}
	key, err := contextKey(doc, "https://example.com/openapi.json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "example.com" {
		t.Errorf("key = %q, want %q", key, "example.com")
	}
}

func TestContextKey_NoServers(t *testing.T) {
	doc := &openapi3.T{}
	_, err := contextKey(doc, "")
	if err == nil {
		t.Fatal("expected error for empty servers, got nil")
	}
}

// TestRequiredContext verifies the security-requirement derivation that drives
// CONTEXT_REQUIRED: an op needing bearer auth challenges when context lacks a
// token, is satisfied once a token is present, and never challenges when the
// op declares no security.
func TestRequiredContext(t *testing.T) {
	doc := &openapi3.T{
		Components: &openapi3.Components{
			SecuritySchemes: openapi3.SecuritySchemes{
				"bearerAuth": &openapi3.SecuritySchemeRef{
					Value: &openapi3.SecurityScheme{Type: "http", Scheme: "bearer"},
				},
			},
		},
	}
	op := &openapi3.Operation{
		Security: openapi3.NewSecurityRequirements().With(openapi3.NewSecurityRequirement().Authenticate("bearerAuth")),
	}

	if d := requiredContext(doc, op, nil, "https://api.example.com", nil); d == nil {
		t.Fatal("expected a challenge when no token is present")
	} else if len(d.Alternatives) != 1 || d.Alternatives[0].Requirements[0].Type != "auth.bearer" {
		t.Errorf("unexpected challenge shape: %+v", d)
	} else if d.Target != "https://api.example.com" {
		t.Errorf("challenge target = %q, want https://api.example.com", d.Target)
	} else if got := d.Alternatives[0].Requirements[0].Name; got != "bearerAuth" {
		// R2.a ruling: every requirement carries the securitySchemes key as Name.
		t.Errorf("Name = %q, want the securitySchemes key %q", got, "bearerAuth")
	}

	if d := requiredContext(doc, op, map[string]any{"bearerToken": "tok"}, "https://api.example.com", nil); d != nil {
		t.Errorf("expected no challenge when token is present, got %+v", d)
	}

	noSecOp := &openapi3.Operation{}
	if d := requiredContext(doc, noSecOp, nil, "https://api.example.com", nil); d != nil {
		t.Errorf("expected no challenge for an op without security, got %+v", d)
	}
}

// TestRequiredContext_NameDistinguishesANDedSchemes verifies the R2.a ruling
// end to end for the AND case: a single alternative requiring two schemes of
// the same family (two apiKey schemes) carries a distinct Name per
// requirement, the securitySchemes key each was declared under — otherwise
// they would be indistinguishable.
func TestRequiredContext_NameDistinguishesANDedSchemes(t *testing.T) {
	doc := &openapi3.T{
		Components: &openapi3.Components{
			SecuritySchemes: openapi3.SecuritySchemes{
				"headerKey": &openapi3.SecuritySchemeRef{
					Value: &openapi3.SecurityScheme{Type: "apiKey", In: "header", Name: "X-Header-Key"},
				},
				"queryKey": &openapi3.SecuritySchemeRef{
					Value: &openapi3.SecurityScheme{Type: "apiKey", In: "query", Name: "api_key"},
				},
			},
		},
	}
	op := &openapi3.Operation{
		Security: openapi3.NewSecurityRequirements().With(
			openapi3.NewSecurityRequirement().Authenticate("headerKey").Authenticate("queryKey"),
		),
	}
	d := requiredContext(doc, op, nil, "https://api.example.com", nil)
	if d == nil || len(d.Alternatives) != 1 || len(d.Alternatives[0].Requirements) != 2 {
		t.Fatalf("expected one AND'd alternative with 2 requirements, got %+v", d)
	}
	names := map[string]bool{}
	for _, req := range d.Alternatives[0].Requirements {
		if req.Type != "auth.apiKey" {
			t.Errorf("requirement type = %q, want auth.apiKey", req.Type)
		}
		names[req.Name] = true
	}
	if !names["headerKey"] || !names["queryKey"] {
		t.Errorf("requirement Names = %v, want {headerKey, queryKey}", names)
	}
}

// TestSchemeToRequirementType verifies the scheme→requirement mapping:
// openIdConnect stays auth.oauth2 (TS is aligned TO this). R2.c ruling
// (flipped from the prior "inexpressible -> skipped" pin): http schemes
// other than bearer/basic (e.g. digest, negotiate) and other unmapped
// artifact types (e.g. mutualTLS) are now SURFACED with a derived type
// ("auth.http.<scheme>" / "auth."+type) instead of dropped, and
// schemeToRequirement always reports ok=true.
func TestSchemeToRequirementType(t *testing.T) {
	cases := []struct {
		scheme *openapi3.SecurityScheme
		want   string
	}{
		{&openapi3.SecurityScheme{Type: "http", Scheme: "bearer"}, "auth.bearer"},
		{&openapi3.SecurityScheme{Type: "http", Scheme: "Bearer"}, "auth.bearer"},
		{&openapi3.SecurityScheme{Type: "http", Scheme: "basic"}, "auth.basic"},
		{&openapi3.SecurityScheme{Type: "http", Scheme: "digest"}, "auth.http.digest"},
		{&openapi3.SecurityScheme{Type: "http", Scheme: "negotiate"}, "auth.http.negotiate"},
		{&openapi3.SecurityScheme{Type: "apiKey"}, "auth.apiKey"},
		{&openapi3.SecurityScheme{Type: "oauth2"}, "auth.oauth2"},
		{&openapi3.SecurityScheme{Type: "openIdConnect"}, "auth.oauth2"},
		{&openapi3.SecurityScheme{Type: "mutualTLS"}, "auth.mutualTLS"},
	}
	for _, tc := range cases {
		req, ok := schemeToRequirement(tc.scheme, "https://api.example.com")
		if !ok {
			t.Errorf("schemeToRequirement(%s/%s) ok = false, want true (every scheme now surfaces, R2.c ruling)", tc.scheme.Type, tc.scheme.Scheme)
			continue
		}
		if req.Type != tc.want {
			t.Errorf("schemeToRequirement(%s/%s) = %q, want %q", tc.scheme.Type, tc.scheme.Scheme, req.Type, tc.want)
		}
	}
}

// TestSchemeToRequirementType_UnmappedCarriesDescription verifies the R2.c
// ruling's description passthrough: an unmapped scheme's artifact
// description rides the surfaced requirement.
func TestSchemeToRequirementType_UnmappedCarriesDescription(t *testing.T) {
	scheme := &openapi3.SecurityScheme{Type: "http", Scheme: "digest", Description: "Digest auth"}
	req, ok := schemeToRequirement(scheme, "https://api.example.com")
	if !ok || req.Type != "auth.http.digest" {
		t.Fatalf("expected auth.http.digest, got (%+v, %v)", req, ok)
	}
	if req.Description != "Digest auth" {
		t.Errorf("Description = %q, want %q", req.Description, "Digest auth")
	}
}

// TestOAuth2Requirement_CarriesFlowFields verifies an oauth2 authorization-code
// scheme carries the flow's authorize/token URLs (relative ones absolutized)
// and the scopes required by the Security Requirement Object, so a resolver
// can drive the flow without out-of-band knowledge.
func TestOAuth2Requirement_CarriesFlowFields(t *testing.T) {
	scheme := &openapi3.SecurityScheme{
		Type: "oauth2",
		Flows: &openapi3.OAuthFlows{
			AuthorizationCode: &openapi3.OAuthFlow{
				AuthorizationURL: "/oauth/authorize", // relative -> absolutized
				TokenURL:         "https://auth.example.com/oauth/token",
				Scopes:           map[string]string{"read": "Read", "write": "Write"},
			},
		},
	}
	requirements := oauth2Requirements(scheme, "https://api.example.com", []string{"write"})
	if len(requirements) != 1 || requirements[0].Type != "auth.oauth2" {
		t.Fatalf("expected one auth.oauth2 requirement, got %+v", requirements)
	}
	req := requirements[0]
	if got := req.Extra["authorizeUrl"]; got != "https://api.example.com/oauth/authorize" {
		t.Errorf("authorizeUrl = %v, want absolutized host URL", got)
	}
	if got := req.Extra["tokenUrl"]; got != "https://auth.example.com/oauth/token" {
		t.Errorf("tokenUrl = %v", got)
	}
	scopes, _ := req.Extra["scopes"].([]string)
	if len(scopes) != 1 || scopes[0] != "write" {
		t.Errorf("scopes = %v, want the required scope [write]", req.Extra["scopes"])
	}
	// R2.b ruling: the selected flow's grantType rides alongside authorizeUrl/tokenUrl/scopes.
	if got := req.Extra["grantType"]; got != "authorization_code" {
		t.Errorf("grantType = %v, want authorization_code", got)
	}

	// openIdConnect carries its discovery URL and NO grantType (R2.b ruling:
	// openIdConnect has no `flows`, so no flow is ever genuinely selected).
	oidc := &openapi3.SecurityScheme{Type: "openIdConnect", OpenIdConnectUrl: "https://auth.example.com/.well-known/openid-configuration"}
	r2 := schemeRequirements(oidc, "https://api.example.com", []string{"openid", "profile"})[0]
	if r2.Extra["openIdConnectUrl"] != "https://auth.example.com/.well-known/openid-configuration" {
		t.Errorf("openIdConnectUrl not carried: %+v", r2.Extra)
	}
	if _, present := r2.Extra["grantType"]; present {
		t.Errorf("openIdConnect must carry no grantType, got %v", r2.Extra["grantType"])
	}
	if got, _ := r2.Extra["scopes"].([]string); !reflect.DeepEqual(got, []string{"openid", "profile"}) {
		t.Errorf("openIdConnect scopes = %v, want [openid profile]", got)
	}
}

// TestOAuth2Requirement_GrantTypeSurfacesSelectedFlow verifies the R2.b
// ruling end to end: grantType names exactly the flow the existing fixed
// priority selects (authorizationCode > implicit > password >
// clientCredentials), never a flow that wasn't actually chosen.
func TestOAuth2Requirement_GrantTypeSurfacesSelectedFlow(t *testing.T) {
	cases := []struct {
		name  string
		flows *openapi3.OAuthFlows
		want  string
	}{
		{
			"authorizationCode",
			&openapi3.OAuthFlows{AuthorizationCode: &openapi3.OAuthFlow{AuthorizationURL: "https://a.example.com/authorize", TokenURL: "https://a.example.com/token"}},
			"authorization_code",
		},
		{
			"implicit",
			&openapi3.OAuthFlows{Implicit: &openapi3.OAuthFlow{AuthorizationURL: "https://a.example.com/authorize"}},
			"implicit",
		},
		{
			"password",
			&openapi3.OAuthFlows{Password: &openapi3.OAuthFlow{TokenURL: "https://a.example.com/token"}},
			"password",
		},
		{
			"clientCredentials",
			&openapi3.OAuthFlows{ClientCredentials: &openapi3.OAuthFlow{TokenURL: "https://a.example.com/token"}},
			"client_credentials",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scheme := &openapi3.SecurityScheme{Type: "oauth2", Flows: tc.flows}
			requirements := oauth2Requirements(scheme, "https://api.example.com", nil)
			if len(requirements) != 1 || requirements[0].Type != "auth.oauth2" {
				t.Fatalf("expected one auth.oauth2 requirement, got %+v", requirements)
			}
			req := requirements[0]
			if got := req.Extra["grantType"]; got != tc.want {
				t.Errorf("grantType = %v, want %q", got, tc.want)
			}
		})
	}
}

// TestOAuth2Requirement_ClientCredentialsOnly verifies that a scheme offering
// only the clientCredentials flow (no authorizationCode/implicit) still carries
// its tokenUrl and scopes. clientCredentials flows define only a tokenUrl in
// OpenAPI 3.x, so the fallback must select on TokenURL, not AuthorizationURL.
func TestOAuth2Requirement_ClientCredentialsOnly(t *testing.T) {
	scheme := &openapi3.SecurityScheme{
		Type: "oauth2",
		Flows: &openapi3.OAuthFlows{
			ClientCredentials: &openapi3.OAuthFlow{
				TokenURL: "/oauth/token", // relative -> absolutized
				Scopes:   map[string]string{"read": "Read", "write": "Write"},
			},
		},
	}
	requirements := oauth2Requirements(scheme, "https://api.example.com", []string{"read", "write"})
	if len(requirements) != 1 || requirements[0].Type != "auth.oauth2" {
		t.Fatalf("expected one auth.oauth2 requirement, got %+v", requirements)
	}
	req := requirements[0]
	if got := req.Extra["tokenUrl"]; got != "https://api.example.com/oauth/token" {
		t.Errorf("tokenUrl = %v, want absolutized host URL", got)
	}
	if _, present := req.Extra["authorizeUrl"]; present {
		t.Errorf("clientCredentials flow has no authorizeUrl, got %v", req.Extra["authorizeUrl"])
	}
	scopes, _ := req.Extra["scopes"].([]string)
	if len(scopes) != 2 || scopes[0] != "read" || scopes[1] != "write" {
		t.Errorf("scopes = %v, want [read write]", req.Extra["scopes"])
	}
}

// TestOAuth2Requirement_PasswordOnly verifies the password flow (also tokenUrl-only)
// likewise carries its tokenUrl through the fallback selection.
func TestOAuth2Requirement_PasswordOnly(t *testing.T) {
	scheme := &openapi3.SecurityScheme{
		Type: "oauth2",
		Flows: &openapi3.OAuthFlows{
			Password: &openapi3.OAuthFlow{
				TokenURL: "https://auth.example.com/oauth/token",
				Scopes:   map[string]string{"profile": "Profile"},
			},
		},
	}
	requirements := oauth2Requirements(scheme, "https://api.example.com", []string{"profile"})
	if len(requirements) != 1 || requirements[0].Type != "auth.oauth2" {
		t.Fatalf("expected one auth.oauth2 requirement, got %+v", requirements)
	}
	req := requirements[0]
	if got := req.Extra["tokenUrl"]; got != "https://auth.example.com/oauth/token" {
		t.Errorf("tokenUrl = %v", got)
	}
	scopes, _ := req.Extra["scopes"].([]string)
	if len(scopes) != 1 || scopes[0] != "profile" {
		t.Errorf("scopes = %v, want [profile]", req.Extra["scopes"])
	}
}

// TestOAuth2Requirements_PreservesPasswordAndClientCredentials verifies that
// declared OAuth flows remain distinct runtime alternatives. Canonical order
// is deterministic but does not collapse them into an invented preference.
func TestOAuth2Requirements_PreservesPasswordAndClientCredentials(t *testing.T) {
	scheme := &openapi3.SecurityScheme{
		Type: "oauth2",
		Flows: &openapi3.OAuthFlows{
			ClientCredentials: &openapi3.OAuthFlow{
				TokenURL: "https://auth.example.com/client-credentials/token",
				Scopes:   map[string]string{"cc": "ClientCredentials"},
			},
			Password: &openapi3.OAuthFlow{
				TokenURL: "https://auth.example.com/password/token",
				Scopes:   map[string]string{"pw": "Password"},
			},
		},
	}
	requirements := oauth2Requirements(scheme, "https://api.example.com", nil)
	if len(requirements) != 2 {
		t.Fatalf("requirements = %+v, want password and client_credentials alternatives", requirements)
	}
	if got := requirements[0].Extra["grantType"]; got != "password" {
		t.Errorf("first grantType = %v, want password", got)
	}
	if got := requirements[1].Extra["grantType"]; got != "client_credentials" {
		t.Errorf("second grantType = %v, want client_credentials", got)
	}
	for _, req := range requirements {
		if scopes, _ := req.Extra["scopes"].([]string); len(scopes) != 0 {
			t.Errorf("scopes = %v, want the empty required-scope set", scopes)
		}
	}
}

// TestOAuth2Requirements_PreservesEveryUsableFlow verifies that every usable
// declared flow is surfaced, including when authorizationCode is present.
func TestOAuth2Requirements_PreservesEveryUsableFlow(t *testing.T) {
	scheme := &openapi3.SecurityScheme{
		Type: "oauth2",
		Flows: &openapi3.OAuthFlows{
			ClientCredentials: &openapi3.OAuthFlow{
				TokenURL: "https://auth.example.com/client-credentials/token",
			},
			Password: &openapi3.OAuthFlow{
				TokenURL: "https://auth.example.com/password/token",
			},
			Implicit: &openapi3.OAuthFlow{
				AuthorizationURL: "https://auth.example.com/implicit/authorize",
			},
			AuthorizationCode: &openapi3.OAuthFlow{
				AuthorizationURL: "https://auth.example.com/authorize",
				TokenURL:         "https://auth.example.com/authcode/token",
			},
		},
	}
	requirements := oauth2Requirements(scheme, "https://api.example.com", nil)
	if len(requirements) != 4 {
		t.Fatalf("requirements = %+v, want all four declared flow alternatives", requirements)
	}
	want := []string{"authorization_code", "implicit", "password", "client_credentials"}
	for i, req := range requirements {
		if got := req.Extra["grantType"]; got != want[i] {
			t.Errorf("requirement %d grantType = %v, want %s", i, got, want[i])
		}
	}
}

// The Security Requirement Object's scope array is the requested capability.
// A higher-priority-looking flow that cannot grant it is not surfaced; a later
// declared flow that can grant it remains a complete runtime alternative.
func TestRequiredContext_OAuthRequiredScopesSelectCapableFlow(t *testing.T) {
	doc := &openapi3.T{
		Components: &openapi3.Components{SecuritySchemes: openapi3.SecuritySchemes{
			"oauth": &openapi3.SecuritySchemeRef{Value: &openapi3.SecurityScheme{
				Type: "oauth2",
				Flows: &openapi3.OAuthFlows{
					AuthorizationCode: &openapi3.OAuthFlow{
						AuthorizationURL: "https://auth.example.com/authorize",
						TokenURL:         "https://auth.example.com/auth-code/token",
						Scopes:           map[string]string{"read": "Read"},
					},
					ClientCredentials: &openapi3.OAuthFlow{
						TokenURL: "https://auth.example.com/client/token",
						Scopes:   map[string]string{"read": "Read", "write": "Write"},
					},
				},
			}},
		}},
	}
	security := openapi3.SecurityRequirements{
		openapi3.SecurityRequirement{"oauth": []string{"write"}},
	}
	op := &openapi3.Operation{Security: &security}
	details := requiredContext(doc, op, nil, "https://api.example.com", nil)
	if details == nil || len(details.Alternatives) != 1 || len(details.Alternatives[0].Requirements) != 1 {
		t.Fatalf("challenge = %+v, want one capable OAuth flow", details)
	}
	req := details.Alternatives[0].Requirements[0]
	if got := req.Extra["grantType"]; got != "client_credentials" {
		t.Errorf("grantType = %v, want client_credentials", got)
	}
	if got := req.Extra["tokenUrl"]; got != "https://auth.example.com/client/token" {
		t.Errorf("tokenUrl = %v, want capable flow's endpoint", got)
	}
	if got, _ := req.Extra["scopes"].([]string); !reflect.DeepEqual(got, []string{"write"}) {
		t.Errorf("scopes = %v, want authoritative required scope [write]", got)
	}
}

// When no declared flow advertises every required scope, the invoker must not
// claim that an insufficient flow can resolve the challenge. It retains a bare
// scoped OAuth requirement for an externally acquired token and performs no
// unauthenticated dispatch.
func TestRequiredContext_OAuthInsufficientFlowChallengesBareRequiredScopes(t *testing.T) {
	doc := &openapi3.T{
		Components: &openapi3.Components{SecuritySchemes: openapi3.SecuritySchemes{
			"oauth": &openapi3.SecuritySchemeRef{Value: &openapi3.SecurityScheme{
				Type: "oauth2",
				Flows: &openapi3.OAuthFlows{AuthorizationCode: &openapi3.OAuthFlow{
					AuthorizationURL: "https://auth.example.com/authorize",
					TokenURL:         "https://auth.example.com/token",
					Scopes:           map[string]string{"read": "Read"},
				}},
			}},
		}},
	}
	security := openapi3.SecurityRequirements{
		openapi3.SecurityRequirement{"oauth": []string{"write"}},
	}
	details := requiredContext(doc, &openapi3.Operation{Security: &security}, nil, "https://api.example.com", nil)
	if details == nil || len(details.Alternatives) != 1 || len(details.Alternatives[0].Requirements) != 1 {
		t.Fatalf("challenge = %+v, want one bare scoped OAuth requirement", details)
	}
	req := details.Alternatives[0].Requirements[0]
	if _, ok := req.Extra["grantType"]; ok {
		t.Errorf("insufficient declared flow must not be surfaced as usable: %+v", req.Extra)
	}
	if _, ok := req.Extra["authorizeUrl"]; ok {
		t.Errorf("insufficient declared flow endpoint must not be surfaced: %+v", req.Extra)
	}
	if got, _ := req.Extra["scopes"].([]string); !reflect.DeepEqual(got, []string{"write"}) {
		t.Errorf("scopes = %v, want [write]", got)
	}
}

// Raw-Cookie declarations alone do not remove a structured-cookie credential
// alternative. Both credential alternatives remain visible until an
// invocation supplies a raw Cookie value that would collide on the wire.
func TestRequiredContext_PreservesCookieAlternativeUntilInvocation(t *testing.T) {
	doc := &openapi3.T{
		Components: &openapi3.Components{SecuritySchemes: openapi3.SecuritySchemes{
			"cookieKey": &openapi3.SecuritySchemeRef{Value: &openapi3.SecurityScheme{Type: "apiKey", In: "cookie", Name: "session"}},
			"bearer":    &openapi3.SecuritySchemeRef{Value: &openapi3.SecurityScheme{Type: "http", Scheme: "bearer"}},
		}},
	}
	security := openapi3.SecurityRequirements{
		openapi3.SecurityRequirement{"cookieKey": []string{}},
		openapi3.SecurityRequirement{"bearer": []string{}},
	}
	op := &openapi3.Operation{Security: &security}
	params := openapi3.Parameters{
		&openapi3.ParameterRef{Value: &openapi3.Parameter{Name: "Cookie", In: openapi3.ParameterInHeader}},
	}
	details := requiredContext(doc, op, nil, "https://api.example.com", params)
	if details == nil || len(details.Alternatives) != 2 {
		t.Fatalf("challenge = %+v, want both credential alternatives", details)
	}
	for index, want := range []struct{ requirementType, name string }{
		{"auth.apiKey", "cookieKey"},
		{"auth.bearer", "bearer"},
	} {
		if len(details.Alternatives[index].Requirements) != 1 {
			t.Fatalf("alternative %d = %+v, want one requirement", index, details.Alternatives[index])
		}
		requirement := details.Alternatives[index].Requirements[0]
		if requirement.Type != want.requirementType || requirement.Name != want.name {
			t.Errorf("alternative %d requirement = %+v, want %s %q", index, requirement, want.requirementType, want.name)
		}
	}
}

// TestRequiredContext_DigestOnlyAlternativeSurfaced verifies the R2.c ruling
// (flipped from TestRequiredContext_DigestOnlyAlternativeSkipped, which
// pinned the old drop behavior): an operation whose only security
// alternative was previously inexpressible (http digest) now raises a
// CONTEXT_REQUIRED challenge surfacing "auth.http.digest" — carrying the
// securitySchemes key as Name (R2.a) — instead of silently dispatching
// unauthenticated.
func TestRequiredContext_DigestOnlyAlternativeSurfaced(t *testing.T) {
	doc := &openapi3.T{
		Components: &openapi3.Components{
			SecuritySchemes: openapi3.SecuritySchemes{
				"digestAuth": &openapi3.SecuritySchemeRef{
					Value: &openapi3.SecurityScheme{Type: "http", Scheme: "digest"},
				},
			},
		},
	}
	op := &openapi3.Operation{
		Security: openapi3.NewSecurityRequirements().With(openapi3.NewSecurityRequirement().Authenticate("digestAuth")),
	}
	d := requiredContext(doc, op, nil, "https://api.example.com", nil)
	if d == nil {
		t.Fatal("expected a challenge surfacing the unmapped digest scheme, got nil")
	}
	if len(d.Alternatives) != 1 || len(d.Alternatives[0].Requirements) != 1 {
		t.Fatalf("unexpected challenge shape: %+v", d)
	}
	req := d.Alternatives[0].Requirements[0]
	if req.Type != "auth.http.digest" {
		t.Errorf("Type = %q, want auth.http.digest", req.Type)
	}
	if req.Name != "digestAuth" {
		t.Errorf("Name = %q, want the securitySchemes key %q", req.Name, "digestAuth")
	}
	// The surfaced-but-unresolvable requirement is unselectable by the
	// built-in satisfaction check (rule 10: no resolver at this layer) —
	// no context can satisfy it.
	if invoke.ContextSatisfies(map[string]any{"bearerToken": "t"}, d) {
		t.Error("an unmapped requirement must never be satisfiable by the built-in check")
	}
}

func TestSecurityConfigurationErrorConfinesMixedValidAndMissingAlternatives(t *testing.T) {
	doc := &openapi3.T{Components: &openapi3.Components{SecuritySchemes: openapi3.SecuritySchemes{
		"bearer": {Value: &openapi3.SecurityScheme{Type: "http", Scheme: "bearer"}},
	}}}
	security := openapi3.SecurityRequirements{
		openapi3.SecurityRequirement{"bearer": []string{}},
		openapi3.SecurityRequirement{"missing": []string{}},
	}
	op := &openapi3.Operation{Security: &security}
	err := securityConfigurationError(doc, op)
	if err != nil {
		t.Fatalf("a missing scheme must make only its alternative unusable, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// routeInput (§9.1, the flattened model's wire side)
// ---------------------------------------------------------------------------

func TestRouteInput_PathQueryHeaderBody(t *testing.T) {
	params := openapi3.Parameters{
		&openapi3.ParameterRef{Value: &openapi3.Parameter{Name: "id", In: "path"}},
		&openapi3.ParameterRef{Value: &openapi3.Parameter{Name: "page", In: "query"}},
		&openapi3.ParameterRef{Value: &openapi3.Parameter{Name: "X-Request-Id", In: "header"}},
	}

	input := map[string]any{
		"id":           42,
		"page":         2,
		"X-Request-Id": "abc",
		"title":        "hello",
	}

	routed, err := routeInputFor(params, input, "/items/{id}", &bodyPlan{declared: true, family: familyJSON}, BindingSpec)
	if err != nil {
		t.Fatalf("routeInput: %v", err)
	}

	if routed.resolvedPath != "/items/42" {
		t.Errorf("resolvedPath = %q, want %q", routed.resolvedPath, "/items/42")
	}
	if len(routed.queryUnits) != 1 || routed.queryUnits[0] != "page=2" {
		t.Errorf("queryUnits = %v, want [page=2]", routed.queryUnits)
	}
	if len(routed.headers) != 1 || routed.headers[0] != [2]string{"X-Request-Id", "abc"} {
		t.Errorf("headers = %v, want X-Request-Id: abc", routed.headers)
	}
	if routed.bodyFields["title"] != "hello" {
		t.Errorf("bodyFields[title] = %v, want %q", routed.bodyFields["title"], "hello")
	}
	if _, ok := routed.bodyFields["id"]; ok {
		t.Error("body should not contain path parameter 'id'")
	}
}

// TestRouteInput_CookieParamsInDeclarationOrder verifies declared cookie
// params become Cookie-header units in DECLARATION order (OAPI-P-10; the
// previous sorted order was this implementation's own, not the spec's),
// never body fields.
func TestRouteInput_CookieParamsInDeclarationOrder(t *testing.T) {
	params := openapi3.Parameters{
		&openapi3.ParameterRef{Value: &openapi3.Parameter{Name: "session_id", In: "cookie"}},
		&openapi3.ParameterRef{Value: &openapi3.Parameter{Name: "csrf", In: "cookie"}},
	}
	input := map[string]any{
		"session_id": "s-1",
		"csrf":       "c-2",
		"title":      "hello",
	}

	routed, err := routeInputFor(params, input, "/session", &bodyPlan{declared: true, family: familyJSON}, BindingSpec)
	if err != nil {
		t.Fatalf("routeInput: %v", err)
	}
	want := []string{"session_id=s-1", "csrf=c-2"}
	if len(routed.cookieUnits) != 2 || routed.cookieUnits[0] != want[0] || routed.cookieUnits[1] != want[1] {
		t.Errorf("cookieUnits = %v, want %v (declaration order)", routed.cookieUnits, want)
	}
	if _, ok := routed.bodyFields["session_id"]; ok {
		t.Error("body should not contain cookie parameter 'session_id'")
	}
	if routed.bodyFields["title"] != "hello" {
		t.Errorf("bodyFields[title] = %v, want %q", routed.bodyFields["title"], "hello")
	}
}

// TestRouteInput_PathValuesPercentEncoded pins the cross-SDK URL rule
// (mirrors the TS invoker.test.ts pin): path parameter values are
// percent-encoded with the encodeURIComponent byte set, so a value carrying
// `/`, `?`, or `#` cannot corrupt the request URL's path/query structure.
func TestRouteInput_PathValuesPercentEncoded(t *testing.T) {
	params := openapi3.Parameters{
		&openapi3.ParameterRef{Value: &openapi3.Parameter{Name: "id", In: "path"}},
	}
	input := map[string]any{"id": "a/b?c#d"}

	routed, err := routeInputFor(params, input, "/users/{id}", &bodyPlan{}, BindingSpec)
	if err != nil {
		t.Fatalf("routeInput: %v", err)
	}
	if routed.resolvedPath != "/users/a%2Fb%3Fc%23d" {
		t.Errorf("resolvedPath = %q, want %q", routed.resolvedPath, "/users/a%2Fb%3Fc%23d")
	}
}

// A supplied input missing a declared path parameter always refuses before
// dispatch: the URL cannot be built without it (§9.1).
func TestRouteInput_MissingPathParamRefuses(t *testing.T) {
	params := openapi3.Parameters{
		&openapi3.ParameterRef{Value: &openapi3.Parameter{Name: "id", In: "path", Required: true}},
	}
	_, err := routeInputFor(params, map[string]any{"other": 1}, "/users/{id}", &bodyPlan{declared: true, family: familyJSON}, BindingSpec)
	if err == nil {
		t.Fatal("expected refusal for a missing path parameter")
	}
	if !errors.Is(err, errMissingPathParam) {
		t.Errorf("error should be the missing-path-parameter refusal, got %v", err)
	}
}

// TestEncodePathValue_MatchesEncodeURIComponent pins the exact byte set:
// encodeURIComponent leaves ALPHA / DIGIT / - _ . ! ~ * ' ( ) unescaped and
// %XX-escapes everything else (UTF-8 bytewise) — sub-delims like "$&+,:;=@"
// ARE escaped (unlike Go's url.PathEscape).
func TestEncodePathValue_MatchesEncodeURIComponent(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain-1_2.3", "plain-1_2.3"},
		{"!~*'()", "!~*'()"},
		{"a b", "a%20b"},
		{"$&+,:;=@", "%24%26%2B%2C%3A%3B%3D%40"},
		{"héllo", "h%C3%A9llo"},
	}
	for _, tc := range cases {
		if got := encodePathValue(tc.in); got != tc.want {
			t.Errorf("encodePathValue(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A field matching no declared parameter passes through into the body when
// the operation declares a request body (§9.1, evaluation-free routing) …
func TestRouteInput_UnknownFieldsPassThroughWithDeclaredBody(t *testing.T) {
	input := map[string]any{"foo": "bar", "baz": 1}

	routed, err := routeInputFor(openapi3.Parameters{}, input, "/test", &bodyPlan{declared: true, family: familyJSON}, BindingSpec)
	if err != nil {
		t.Fatalf("routeInput: %v", err)
	}
	if len(routed.bodyFields) != 2 {
		t.Errorf("body has %d entries, want 2", len(routed.bodyFields))
	}
	if routed.bodyFields["foo"] != "bar" {
		t.Errorf("bodyFields[foo] = %v, want %q", routed.bodyFields["foo"], "bar")
	}
}

// … and is REFUSED before dispatch — loudly, naming the offenders — when
// the operation declares no request body (§9.1; the previous silent body
// invention was non-conformant).
func TestRouteInput_UnknownFieldsRefusedWithoutBody(t *testing.T) {
	input := map[string]any{"foo": "bar", "baz": 1}

	_, err := routeInputFor(openapi3.Parameters{}, input, "/test", &bodyPlan{}, BindingSpec)
	if err == nil {
		t.Fatal("expected refusal for unmatched fields on an operation without a request body")
	}
	if !strings.Contains(err.Error(), "baz") || !strings.Contains(err.Error(), "foo") {
		t.Errorf("refusal must list the offending field names, got %q", err.Error())
	}
}

// Candidate screening, not routeInput, refuses a body/parameter collision;
// routing never duplicates one caller value into both independent locations.
func TestRouteInput_CollisionIsScreenedWithoutDuplication(t *testing.T) {
	params := openapi3.Parameters{
		{Value: &openapi3.Parameter{Name: "id", In: "path", Required: true}},
	}
	input := map[string]any{"id": "u1", "name": "Ada"}
	plan := &bodyPlan{
		declared: true,
		family:   familyJSON,
		props:    map[string]bool{"id": true, "name": true},
	}
	if !candidateCollides(params, plan) {
		t.Fatal("the media candidate must be inadmissible before routing")
	}
	routed, err := routeInputFor(params, input, "/users/{id}", plan, BindingSpec)
	if err != nil {
		t.Fatalf("routeInput: %v", err)
	}
	if routed.resolvedPath != "/users/u1" {
		t.Fatalf("path = %q, want /users/u1", routed.resolvedPath)
	}
	if _, duplicated := routed.bodyFields["id"]; duplicated {
		t.Fatalf("routing must never duplicate a parameter into the body, got %v", routed.bodyFields)
	}
	if routed.bodyFields["name"] != "Ada" {
		t.Fatalf("body missing name: %v", routed.bodyFields)
	}
}

// TestPlanRequestBody_MediaTypeParameters pins that media-type parameters
// ("; charset=utf-8") never change media matching (§9.2 compares type and
// subtype, ignoring parameters): the collision rule still sees the body's
// property names.
func TestPlanRequestBody_MediaTypeParameters(t *testing.T) {
	op := &openapi3.Operation{
		RequestBody: &openapi3.RequestBodyRef{
			Value: &openapi3.RequestBody{
				Content: openapi3.Content{
					"application/json; charset=utf-8": &openapi3.MediaType{
						Schema: &openapi3.SchemaRef{
							Value: &openapi3.Schema{
								Type: &openapi3.Types{"object"},
								Properties: openapi3.Schemas{
									"id":   &openapi3.SchemaRef{Value: &openapi3.Schema{Type: &openapi3.Types{"string"}}},
									"name": &openapi3.SchemaRef{Value: &openapi3.Schema{Type: &openapi3.Types{"string"}}},
								},
							},
						},
					},
				},
			},
		},
	}
	plan, err := planRequestBody(op, BindingSpec)
	if err != nil {
		t.Fatalf("planRequestBody: %v", err)
	}
	if plan.family != familyJSON || plan.mediaType != "application/json; charset=utf-8" {
		t.Fatalf("selection must preserve media-type parameters, got %+v", plan)
	}
	if !plan.props["id"] || !plan.props["name"] {
		t.Fatalf("body property names must survive parameterized content keys, got %v", plan.props)
	}
}

// Without a body declaration the parameter stays exclusive (unchanged).
func TestRouteInput_ParamOnlyStaysExclusive(t *testing.T) {
	params := openapi3.Parameters{
		{Value: &openapi3.Parameter{Name: "id", In: "path", Required: true}},
	}
	input := map[string]any{"id": "u1"}
	routed, err := routeInputFor(params, input, "/users/{id}", &bodyPlan{}, BindingSpec)
	if err != nil {
		t.Fatalf("routeInput: %v", err)
	}
	if _, ok := routed.bodyFields["id"]; ok {
		t.Fatalf("param-only field must not leak into the body: %v", routed.bodyFields)
	}
}
