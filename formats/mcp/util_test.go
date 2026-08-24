package mcp

import "testing"

func TestParseSelector_Tool(t *testing.T) {
	entityType, name, err := parseSelector("tools/get_weather")
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

func TestParseSelector_Resource(t *testing.T) {
	entityType, name, err := parseSelector("resources/file:///src/main.rs")
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

func TestParseSelector_Prompt(t *testing.T) {
	entityType, name, err := parseSelector("prompts/code_review")
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

func TestParseSelector_Empty(t *testing.T) {
	_, _, err := parseSelector("")
	if err == nil {
		t.Error("expected error for empty selector")
	}
}

func TestParseSelector_NoPrefix(t *testing.T) {
	_, _, err := parseSelector("get_weather")
	if err == nil {
		t.Error("expected error for selector without prefix")
	}
}

func TestParseSelector_EmptyName(t *testing.T) {
	_, _, err := parseSelector("tools/")
	if err == nil {
		t.Error("expected error for empty name")
	}
}

func TestBuildHTTPHeaders_Carriage(t *testing.T) {
	if got := buildHTTPHeaders(map[string]any{"bearerToken": "tok_123"})["Authorization"]; got != "Bearer tok_123" {
		t.Fatalf("bearer token carriage = %q, want Authorization: Bearer", got)
	}
	for name, context := range map[string]map[string]any{
		"apiKey": {"apiKey": "key_abc"},
		"basic":  {"basic": map[string]any{"username": "user", "password": "pass"}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := buildHTTPHeaders(context); got != nil {
				t.Fatalf("generic credential must not be assigned an invented HTTP header: %v", got)
			}
		})
	}
}

func TestBuildHTTPHeaders_NoCredentials(t *testing.T) {
	h := buildHTTPHeaders(nil)
	if h != nil {
		t.Errorf("expected nil headers, got %v", h)
	}
}

func TestBuildHTTPHeaders_ContextHeaders(t *testing.T) {
	bindCtx := map[string]any{
		"headers": map[string]any{"X-Custom": "value", "Authorization": "Bearer explicit"},
	}
	h := buildHTTPHeaders(bindCtx)
	if h["X-Custom"] != "value" {
		t.Errorf("X-Custom = %q, want value", h["X-Custom"])
	}
	if h["Authorization"] != "Bearer explicit" {
		t.Errorf("Authorization = %q, want explicitly supplied header", h["Authorization"])
	}
}
