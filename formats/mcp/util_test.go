package mcp

import (
	"encoding/base64"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func TestParseRef_Tool(t *testing.T) {
	entityType, name, err := parseRef("tools/get_weather")
	if err != nil {
		t.Fatal(err)
	}
	if entityType != "tools" {
		t.Errorf("entityType = %q, want tools", entityType)
	}
	if name != "get_weather" {
		t.Errorf("name = %q, want get_weather", name)
	}
}

func TestParseRef_Resource(t *testing.T) {
	entityType, name, err := parseRef("resources/file:///src/main.rs")
	if err != nil {
		t.Fatal(err)
	}
	if entityType != "resources" {
		t.Errorf("entityType = %q, want resources", entityType)
	}
	if name != "file:///src/main.rs" {
		t.Errorf("name = %q, want file:///src/main.rs", name)
	}
}

func TestParseRef_Prompt(t *testing.T) {
	entityType, name, err := parseRef("prompts/code_review")
	if err != nil {
		t.Fatal(err)
	}
	if entityType != "prompts" {
		t.Errorf("entityType = %q, want prompts", entityType)
	}
	if name != "code_review" {
		t.Errorf("name = %q, want code_review", name)
	}
}

func TestParseRef_Empty(t *testing.T) {
	_, _, err := parseRef("")
	if err == nil {
		t.Error("expected error for empty ref")
	}
}

func TestParseRef_NoPrefix(t *testing.T) {
	_, _, err := parseRef("get_weather")
	if err == nil {
		t.Error("expected error for ref without prefix")
	}
}

func TestParseRef_EmptyName(t *testing.T) {
	_, _, err := parseRef("tools/")
	if err == nil {
		t.Error("expected error for empty name")
	}
}

func TestNormalizeEndpoint_Full(t *testing.T) {
	got := normalizeEndpoint("https://mcp.example.com/sse")
	if got != "mcp.example.com" {
		t.Errorf("got %q, want %q", got, "mcp.example.com")
	}
}

func TestNormalizeEndpoint_WithPort(t *testing.T) {
	got := normalizeEndpoint("http://localhost:8080/mcp")
	if got != "localhost:8080" {
		t.Errorf("got %q, want %q", got, "localhost:8080")
	}
}

func TestNormalizeEndpoint_Empty(t *testing.T) {
	got := normalizeEndpoint("")
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestBuildHTTPHeaders_BearerToken(t *testing.T) {
	h := buildHTTPHeaders(map[string]any{"bearerToken": "tok_123"}, nil)
	if h["Authorization"] != "Bearer tok_123" {
		t.Errorf("Authorization = %q, want Bearer tok_123", h["Authorization"])
	}
}

func TestBuildHTTPHeaders_APIKey(t *testing.T) {
	h := buildHTTPHeaders(map[string]any{"apiKey": "key_abc"}, nil)
	if h["Authorization"] != "ApiKey key_abc" {
		t.Errorf("Authorization = %q, want ApiKey key_abc", h["Authorization"])
	}
}

func TestBuildHTTPHeaders_BasicAuth(t *testing.T) {
	h := buildHTTPHeaders(map[string]any{"basic": map[string]any{"username": "user", "password": "pass"}}, nil)
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("user:pass"))
	if h["Authorization"] != want {
		t.Errorf("Authorization = %q, want %q", h["Authorization"], want)
	}
}

func TestBuildHTTPHeaders_BearerTakesPriority(t *testing.T) {
	h := buildHTTPHeaders(map[string]any{"bearerToken": "tok", "apiKey": "key"}, nil)
	if h["Authorization"] != "Bearer tok" {
		t.Errorf("Authorization = %q, want Bearer tok", h["Authorization"])
	}
}

func TestBuildHTTPHeaders_NoCredentials(t *testing.T) {
	h := buildHTTPHeaders(nil, nil)
	if h != nil {
		t.Errorf("expected nil headers, got %v", h)
	}
}

func TestBuildHTTPHeaders_ExecutionOptionsHeaders(t *testing.T) {
	opts := &openbindings.ExecutionOptions{
		Headers: map[string]string{"X-Custom": "value"},
	}
	h := buildHTTPHeaders(nil, opts)
	if h["X-Custom"] != "value" {
		t.Errorf("X-Custom = %q, want value", h["X-Custom"])
	}
}
