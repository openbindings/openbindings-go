package openapi

import (
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	openbindings "github.com/openbindings/openbindings-go"
)

// ---------------------------------------------------------------------------
// parseRef
// ---------------------------------------------------------------------------

func TestParseRef_StandardJSONPointer(t *testing.T) {
	path, method, err := parseRef("#/paths/~1users/get")
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

func TestParseRef_WithoutLeadingHashSlash(t *testing.T) {
	path, method, err := parseRef("paths/~1users~1{id}/delete")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != "/users/{id}" {
		t.Errorf("path = %q, want %q", path, "/users/{id}")
	}
	if method != "delete" {
		t.Errorf("method = %q, want %q", method, "delete")
	}
}

func TestParseRef_TildeEscaping(t *testing.T) {
	path, method, err := parseRef("#/paths/~1a~0b~1c/post")
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

func TestParseRef_MethodLowercasing(t *testing.T) {
	_, method, err := parseRef("#/paths/~1users/GET")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if method != "get" {
		t.Errorf("method = %q, want %q", method, "get")
	}
}

func TestParseRef_ErrorTooFewParts(t *testing.T) {
	_, _, err := parseRef("#/paths")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "must be in format") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "must be in format")
	}
}

func TestParseRef_ErrorNonPathsPrefix(t *testing.T) {
	_, _, err := parseRef("#/components/schemas/get")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "must be in format") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "must be in format")
	}
}

func TestParseRef_ErrorInvalidMethod(t *testing.T) {
	_, _, err := parseRef("#/paths/~1users/connect")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "invalid HTTP method") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "invalid HTTP method")
	}
}

// ---------------------------------------------------------------------------
// buildJSONPointerRef
// ---------------------------------------------------------------------------

func TestBuildJSONPointerRef_Simple(t *testing.T) {
	ref := buildJSONPointerRef("/users", "get")
	if ref != "#/paths/~1users/get" {
		t.Errorf("ref = %q, want %q", ref, "#/paths/~1users/get")
	}
}

func TestBuildJSONPointerRef_NestedPaths(t *testing.T) {
	ref := buildJSONPointerRef("/users/{id}/posts", "post")
	if ref != "#/paths/~1users~1{id}~1posts/post" {
		t.Errorf("ref = %q, want %q", ref, "#/paths/~1users~1{id}~1posts/post")
	}
}

func TestBuildJSONPointerRef_RoundTrip(t *testing.T) {
	originalPath := "/users/{id}/posts"
	originalMethod := "put"

	ref := buildJSONPointerRef(originalPath, originalMethod)
	path, method, err := parseRef(ref)
	if err != nil {
		t.Fatalf("round-trip parseRef failed: %v", err)
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
	base, err := resolveBaseURLWithLocation(doc, nil, location)
	if err != nil {
		return "", err
	}
	return openbindings.NormalizeContextKey(base), nil
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

	if d := requiredContext(doc, op, nil, "https://api.example.com"); d == nil {
		t.Fatal("expected a challenge when no token is present")
	} else if len(d.Alternatives) != 1 || d.Alternatives[0].Requirements[0].Type != "auth.bearer" {
		t.Errorf("unexpected challenge shape: %+v", d)
	} else if d.Target != "https://api.example.com" {
		t.Errorf("challenge target = %q, want https://api.example.com", d.Target)
	}

	if d := requiredContext(doc, op, map[string]any{"bearerToken": "tok"}, "https://api.example.com"); d != nil {
		t.Errorf("expected no challenge when token is present, got %+v", d)
	}

	noSecOp := &openapi3.Operation{}
	if d := requiredContext(doc, noSecOp, nil, "https://api.example.com"); d != nil {
		t.Errorf("expected no challenge for an op without security, got %+v", d)
	}
}

// TestSchemeToRequirementType verifies the scheme→requirement mapping:
// openIdConnect stays auth.oauth2 (TS is aligned TO this), while http schemes
// other than bearer/basic (e.g. digest) are inexpressible — the alternative
// is skipped rather than degraded to auth.bearer.
func TestSchemeToRequirementType(t *testing.T) {
	cases := []struct {
		scheme *openapi3.SecurityScheme
		want   string
	}{
		{&openapi3.SecurityScheme{Type: "http", Scheme: "bearer"}, "auth.bearer"},
		{&openapi3.SecurityScheme{Type: "http", Scheme: "Bearer"}, "auth.bearer"},
		{&openapi3.SecurityScheme{Type: "http", Scheme: "basic"}, "auth.basic"},
		{&openapi3.SecurityScheme{Type: "http", Scheme: "digest"}, ""},
		{&openapi3.SecurityScheme{Type: "http", Scheme: "negotiate"}, ""},
		{&openapi3.SecurityScheme{Type: "apiKey"}, "auth.apiKey"},
		{&openapi3.SecurityScheme{Type: "oauth2"}, "auth.oauth2"},
		{&openapi3.SecurityScheme{Type: "openIdConnect"}, "auth.oauth2"},
		{&openapi3.SecurityScheme{Type: "mutualTLS"}, ""},
	}
	for _, tc := range cases {
		req, ok := schemeToRequirement(tc.scheme, "https://api.example.com")
		got := ""
		if ok {
			got = req.Type
		}
		if got != tc.want {
			t.Errorf("schemeToRequirement(%s/%s) = %q, want %q", tc.scheme.Type, tc.scheme.Scheme, got, tc.want)
		}
	}
}

// TestOAuth2Requirement_CarriesFlowFields verifies an oauth2 authorization-code
// scheme carries the flow's authorize/token URLs (relative ones absolutized)
// and scopes into the requirement's Extra fields, so a resolver can drive the
// flow without out-of-band knowledge.
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
	req, ok := schemeToRequirement(scheme, "https://api.example.com")
	if !ok || req.Type != "auth.oauth2" {
		t.Fatalf("expected auth.oauth2, got (%+v, %v)", req, ok)
	}
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

	// openIdConnect carries its discovery URL.
	oidc := &openapi3.SecurityScheme{Type: "openIdConnect", OpenIdConnectUrl: "https://auth.example.com/.well-known/openid-configuration"}
	r2, _ := schemeToRequirement(oidc, "https://api.example.com")
	if r2.Extra["openIdConnectUrl"] != "https://auth.example.com/.well-known/openid-configuration" {
		t.Errorf("openIdConnectUrl not carried: %+v", r2.Extra)
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
	req, ok := schemeToRequirement(scheme, "https://api.example.com")
	if !ok || req.Type != "auth.oauth2" {
		t.Fatalf("expected auth.oauth2, got (%+v, %v)", req, ok)
	}
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
	req, ok := schemeToRequirement(scheme, "https://api.example.com")
	if !ok || req.Type != "auth.oauth2" {
		t.Fatalf("expected auth.oauth2, got (%+v, %v)", req, ok)
	}
	if got := req.Extra["tokenUrl"]; got != "https://auth.example.com/oauth/token" {
		t.Errorf("tokenUrl = %v", got)
	}
	scopes, _ := req.Extra["scopes"].([]string)
	if len(scopes) != 1 || scopes[0] != "profile" {
		t.Errorf("scopes = %v, want [profile]", req.Extra["scopes"])
	}
}

// TestRequiredContext_DigestOnlyAlternativeSkipped verifies an operation
// whose only security alternative is inexpressible (http digest) raises no
// challenge at all.
func TestRequiredContext_DigestOnlyAlternativeSkipped(t *testing.T) {
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
	if d := requiredContext(doc, op, nil, "https://api.example.com"); d != nil {
		t.Errorf("an inexpressible-only alternative must yield no challenge, got %+v", d)
	}
}

// ---------------------------------------------------------------------------
// classifyInput
// ---------------------------------------------------------------------------

func TestClassifyInput_PathQueryHeaderBody(t *testing.T) {
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

	resolvedPath, query, headers, body := classifyInput(params, input, "/items/{id}", nil)

	if resolvedPath != "/items/42" {
		t.Errorf("resolvedPath = %q, want %q", resolvedPath, "/items/42")
	}
	if query["page"] != 2 {
		t.Errorf("query[page] = %v, want 2", query["page"])
	}
	if headers["X-Request-Id"] != "abc" {
		t.Errorf("headers[X-Request-Id] = %v, want %q", headers["X-Request-Id"], "abc")
	}
	if body["title"] != "hello" {
		t.Errorf("body[title] = %v, want %q", body["title"], "hello")
	}
	if _, ok := body["id"]; ok {
		t.Error("body should not contain path parameter 'id'")
	}
}

// TestClassifyInput_CookieParamsGoToCookieHeader verifies declared cookie
// params travel in a Cookie header (sorted, "; "-joined), never in the body.
func TestClassifyInput_CookieParamsGoToCookieHeader(t *testing.T) {
	params := openapi3.Parameters{
		&openapi3.ParameterRef{Value: &openapi3.Parameter{Name: "session_id", In: "cookie"}},
		&openapi3.ParameterRef{Value: &openapi3.Parameter{Name: "csrf", In: "cookie"}},
	}
	input := map[string]any{
		"session_id": "s-1",
		"csrf":       "c-2",
		"title":      "hello",
	}

	_, _, headers, body := classifyInput(params, input, "/session", nil)

	if got := headers["Cookie"]; got != "csrf=c-2; session_id=s-1" {
		t.Errorf("headers[Cookie] = %v, want %q", got, "csrf=c-2; session_id=s-1")
	}
	if _, ok := body["session_id"]; ok {
		t.Error("body should not contain cookie parameter 'session_id'")
	}
	if _, ok := body["csrf"]; ok {
		t.Error("body should not contain cookie parameter 'csrf'")
	}
	if body["title"] != "hello" {
		t.Errorf("body[title] = %v, want %q", body["title"], "hello")
	}
}

// TestClassifyInput_PathValuesPercentEncoded pins the cross-SDK URL rule
// (mirrors the TS invoker.test.ts pin): path parameter values are
// percent-encoded with the encodeURIComponent byte set, so a value carrying
// `/`, `?`, or `#` cannot corrupt the request URL's path/query structure.
func TestClassifyInput_PathValuesPercentEncoded(t *testing.T) {
	params := openapi3.Parameters{
		&openapi3.ParameterRef{Value: &openapi3.Parameter{Name: "id", In: "path"}},
	}
	input := map[string]any{"id": "a/b?c#d"}

	resolvedPath, _, _, _ := classifyInput(params, input, "/users/{id}", nil)

	if resolvedPath != "/users/a%2Fb%3Fc%23d" {
		t.Errorf("resolvedPath = %q, want %q", resolvedPath, "/users/a%2Fb%3Fc%23d")
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

func TestClassifyInput_UnknownParamsGoToBody(t *testing.T) {
	params := openapi3.Parameters{}
	input := map[string]any{"foo": "bar", "baz": 1}

	_, _, _, body := classifyInput(params, input, "/test", nil)

	if len(body) != 2 {
		t.Errorf("body has %d entries, want 2", len(body))
	}
	if body["foo"] != "bar" {
		t.Errorf("body[foo] = %v, want %q", body["foo"], "bar")
	}
}

// ---------------------------------------------------------------------------
// isMultipartFormData
// ---------------------------------------------------------------------------

func TestIsMultipartFormData_MultipartOnly(t *testing.T) {
	op := &openapi3.Operation{
		RequestBody: &openapi3.RequestBodyRef{
			Value: &openapi3.RequestBody{
				Content: openapi3.Content{
					"multipart/form-data": &openapi3.MediaType{},
				},
			},
		},
	}
	if !isMultipartFormData(op) {
		t.Error("expected true for multipart/form-data only content")
	}
}

func TestIsMultipartFormData_PrefersJSON(t *testing.T) {
	op := &openapi3.Operation{
		RequestBody: &openapi3.RequestBodyRef{
			Value: &openapi3.RequestBody{
				Content: openapi3.Content{
					"application/json":    &openapi3.MediaType{},
					"multipart/form-data": &openapi3.MediaType{},
				},
			},
		},
	}
	if isMultipartFormData(op) {
		t.Error("expected false when application/json is also available")
	}
}

func TestIsMultipartFormData_JSONOnly(t *testing.T) {
	op := &openapi3.Operation{
		RequestBody: &openapi3.RequestBodyRef{
			Value: &openapi3.RequestBody{
				Content: openapi3.Content{
					"application/json": &openapi3.MediaType{},
				},
			},
		},
	}
	if isMultipartFormData(op) {
		t.Error("expected false for application/json only")
	}
}

func TestIsMultipartFormData_NoRequestBody(t *testing.T) {
	op := &openapi3.Operation{}
	if isMultipartFormData(op) {
		t.Error("expected false for nil request body")
	}
}

// ---------------------------------------------------------------------------
// buildMultipartBody
// ---------------------------------------------------------------------------

func TestBuildMultipartBody_StringFields(t *testing.T) {
	op := &openapi3.Operation{
		RequestBody: &openapi3.RequestBodyRef{
			Value: &openapi3.RequestBody{
				Content: openapi3.Content{
					"multipart/form-data": &openapi3.MediaType{
						Schema: &openapi3.SchemaRef{
							Value: &openapi3.Schema{
								Type: &openapi3.Types{"object"},
								Properties: openapi3.Schemas{
									"name": &openapi3.SchemaRef{
										Value: &openapi3.Schema{Type: &openapi3.Types{"string"}},
									},
									"count": &openapi3.SchemaRef{
										Value: &openapi3.Schema{Type: &openapi3.Types{"integer"}},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	fields := map[string]any{"name": "test", "count": 42}
	buf, ct, err := buildMultipartBody(op, fields)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(ct, "multipart/form-data") {
		t.Errorf("content type = %q, want multipart/form-data", ct)
	}
	if buf.Len() == 0 {
		t.Error("expected non-empty body")
	}
	body := buf.String()
	if !strings.Contains(body, "name") || !strings.Contains(body, "test") {
		t.Error("body should contain the name field")
	}
	if !strings.Contains(body, "count") || !strings.Contains(body, "42") {
		t.Error("body should contain the count field")
	}
}

func TestBuildMultipartBody_BinaryField(t *testing.T) {
	op := &openapi3.Operation{
		RequestBody: &openapi3.RequestBodyRef{
			Value: &openapi3.RequestBody{
				Content: openapi3.Content{
					"multipart/form-data": &openapi3.MediaType{
						Schema: &openapi3.SchemaRef{
							Value: &openapi3.Schema{
								Type: &openapi3.Types{"object"},
								Properties: openapi3.Schemas{
									"file": &openapi3.SchemaRef{
										Value: &openapi3.Schema{
											Type:   &openapi3.Types{"string"},
											Format: "binary",
										},
									},
									"description": &openapi3.SchemaRef{
										Value: &openapi3.Schema{Type: &openapi3.Types{"string"}},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	fileContent := []byte("hello world")
	fields := map[string]any{"file": fileContent, "description": "a test file"}
	buf, ct, err := buildMultipartBody(op, fields)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(ct, "multipart/form-data") {
		t.Errorf("content type = %q, want multipart/form-data", ct)
	}
	body := buf.String()
	if !strings.Contains(body, "hello world") {
		t.Error("body should contain the binary file content")
	}
	if !strings.Contains(body, "a test file") {
		t.Error("body should contain the description field")
	}
}

func TestBuildMultipartBody_BinaryFieldWrongType(t *testing.T) {
	op := &openapi3.Operation{
		RequestBody: &openapi3.RequestBodyRef{
			Value: &openapi3.RequestBody{
				Content: openapi3.Content{
					"multipart/form-data": &openapi3.MediaType{
						Schema: &openapi3.SchemaRef{
							Value: &openapi3.Schema{
								Type: &openapi3.Types{"object"},
								Properties: openapi3.Schemas{
									"file": &openapi3.SchemaRef{
										Value: &openapi3.Schema{
											Type:   &openapi3.Types{"string"},
											Format: "binary",
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	fields := map[string]any{"file": "not-bytes"}
	_, _, err := buildMultipartBody(op, fields)
	if err == nil {
		t.Fatal("expected error for non-[]byte binary field, got nil")
	}
	if !strings.Contains(err.Error(), "expected []byte") {
		t.Errorf("error = %q, want it to mention []byte", err.Error())
	}
}

// TestClassifyInput_CollisionDeliversToBothLocations pins the
// field-collision rule: a field declared as a parameter AND a body
// property carries ONE value delivered to every declared wire location.
func TestClassifyInput_CollisionDeliversToBothLocations(t *testing.T) {
	params := openapi3.Parameters{
		{Value: &openapi3.Parameter{Name: "id", In: "path", Required: true}},
	}
	input := map[string]any{"id": "u1", "name": "Ada"}
	path, _, _, body := classifyInput(params, input, "/users/{id}", map[string]bool{"id": true, "name": true})
	if path != "/users/u1" {
		t.Fatalf("path = %q, want /users/u1", path)
	}
	if body["id"] != "u1" {
		t.Fatalf("body must keep the colliding field, got %v", body)
	}
	if body["name"] != "Ada" {
		t.Fatalf("body missing name: %v", body)
	}
}

// TestBodyPropertyNames_MediaTypeParameters pins that media-type parameters
// ("; charset=utf-8") never change whether a request body is JSON: the
// collision rule still sees the body's property names (TS parity — TS
// stripped parameters, Go compared the full content-type key and missed).
func TestBodyPropertyNames_MediaTypeParameters(t *testing.T) {
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
	names := bodyPropertyNames(op)
	if !names["id"] || !names["name"] {
		t.Fatalf("bodyPropertyNames must see through media-type parameters, got %v", names)
	}
}

// Without a body declaration the parameter stays exclusive (unchanged).
func TestClassifyInput_ParamOnlyStaysExclusive(t *testing.T) {
	params := openapi3.Parameters{
		{Value: &openapi3.Parameter{Name: "id", In: "path", Required: true}},
	}
	input := map[string]any{"id": "u1"}
	_, _, _, body := classifyInput(params, input, "/users/{id}", nil)
	if _, ok := body["id"]; ok {
		t.Fatalf("param-only field must not leak into the body: %v", body)
	}
}
