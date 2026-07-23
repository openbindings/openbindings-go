package mcp

import "testing"

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

func TestBuildHTTPHeaders_GenericCredentialsDoNotInventCarriage(t *testing.T) {
	for name, context := range map[string]map[string]any{
		"bearer": {"bearerToken": "tok_123"},
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
