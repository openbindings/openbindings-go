package openapi

import (
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
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
// resolveServerKey
// ---------------------------------------------------------------------------

func TestResolveServerKey_AbsoluteURL(t *testing.T) {
	doc := &openapi3.T{
		Servers: openapi3.Servers{
			{URL: "https://api.example.com/v1"},
		},
	}
	key, err := resolveServerKey(doc, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "api.example.com" {
		t.Errorf("key = %q, want %q", key, "api.example.com")
	}
}

func TestResolveServerKey_RelativeURLWithSourceLocation(t *testing.T) {
	doc := &openapi3.T{
		Servers: openapi3.Servers{
			{URL: "/api/v1"},
		},
	}
	key, err := resolveServerKey(doc, "https://example.com/openapi.json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "example.com" {
		t.Errorf("key = %q, want %q", key, "example.com")
	}
}

func TestResolveServerKey_NoServers(t *testing.T) {
	doc := &openapi3.T{}
	_, err := resolveServerKey(doc, "")
	if err == nil {
		t.Fatal("expected error for empty servers, got nil")
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

	resolvedPath, query, headers, body := classifyInput(params, input, "/items/{id}")

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

func TestClassifyInput_UnknownParamsGoToBody(t *testing.T) {
	params := openapi3.Parameters{}
	input := map[string]any{"foo": "bar", "baz": 1}

	_, _, _, body := classifyInput(params, input, "/test")

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
