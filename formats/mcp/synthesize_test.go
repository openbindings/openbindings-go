package mcp

import (
	"testing"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestConvertToInterface_CopiesServerInfo(t *testing.T) {
	disc := &discovery{
		ServerInfo: &gomcp.Implementation{Name: "test-server", Version: "1.0.0", Title: "Test Server"},
	}

	iface, err := convertToInterface(disc, "https://mcp.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if iface.Name != "test-server" {
		t.Errorf("Name = %q, want test-server", iface.Name)
	}
	if iface.Version != "1.0.0" {
		t.Errorf("Version = %q, want 1.0.0", iface.Version)
	}
	if iface.Description != "Test Server" {
		t.Errorf("Description = %q, want Test Server", iface.Description)
	}
}

func TestConvertToInterface_CreatesToolOperations(t *testing.T) {
	disc := &discovery{
		Tools: []*gomcp.Tool{
			{Name: "get_weather", Description: "Get weather", InputSchema: map[string]any{"type": "object"}},
			{Name: "search", Description: "Search things"},
		},
	}

	iface, err := convertToInterface(disc, "https://mcp.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(iface.Operations) != 2 {
		t.Fatalf("expected 2 operations, got %d", len(iface.Operations))
	}
	if _, ok := iface.Operations["get_weather"]; !ok {
		t.Error("expected operation 'get_weather'")
	}
	if _, ok := iface.Operations["search"]; !ok {
		t.Error("expected operation 'search'")
	}
}

func TestConvertToInterface_ToolBindingRefs(t *testing.T) {
	disc := &discovery{
		Tools: []*gomcp.Tool{
			{Name: "get_weather"},
		},
	}

	iface, err := convertToInterface(disc, "https://mcp.example.com")
	if err != nil {
		t.Fatal(err)
	}
	key := "get_weather." + DefaultSourceName
	binding, ok := iface.Bindings[key]
	if !ok {
		t.Fatalf("expected binding %q", key)
	}
	if binding.Ref != "tools/get_weather" {
		t.Errorf("ref = %q, want tools/get_weather", binding.Ref)
	}
}

func TestConvertToInterface_ToolInputOutputSchemas(t *testing.T) {
	disc := &discovery{
		Tools: []*gomcp.Tool{
			{
				Name:         "calc",
				InputSchema:  map[string]any{"type": "object", "properties": map[string]any{"x": map[string]any{"type": "number"}}},
				OutputSchema: map[string]any{"type": "object", "properties": map[string]any{"result": map[string]any{"type": "number"}}},
			},
		},
	}

	iface, err := convertToInterface(disc, "")
	if err != nil {
		t.Fatal(err)
	}
	op := iface.Operations["calc"]
	if op.Input == nil {
		t.Fatal("expected input schema")
	}
	if op.Input["type"] != "object" {
		t.Errorf("input type = %v, want object", op.Input["type"])
	}
	if op.Output == nil {
		t.Fatal("expected output schema")
	}
	if op.Output["type"] != "object" {
		t.Errorf("output type = %v, want object", op.Output["type"])
	}
}

func TestConvertToInterface_ResourceOperations(t *testing.T) {
	disc := &discovery{
		Resources: []*gomcp.Resource{
			{Name: "config", URI: "file:///etc/config.json", Description: "Config file"},
		},
	}

	iface, err := convertToInterface(disc, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(iface.Operations) != 1 {
		t.Fatalf("expected 1 operation, got %d", len(iface.Operations))
	}
	op := iface.Operations["config"]
	// Resource input should have const URI
	props, ok := op.Input["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties in input schema")
	}
	uriSchema, ok := props["uri"].(map[string]any)
	if !ok {
		t.Fatal("expected uri property")
	}
	if uriSchema["const"] != "file:///etc/config.json" {
		t.Errorf("const = %v, want file:///etc/config.json", uriSchema["const"])
	}

	binding := iface.Bindings["config."+DefaultSourceName]
	if binding.Ref != "resources/file:///etc/config.json" {
		t.Errorf("ref = %q, want resources/file:///etc/config.json", binding.Ref)
	}
}

func TestConvertToInterface_ResourceTemplateOperations(t *testing.T) {
	disc := &discovery{
		ResourceTemplates: []*gomcp.ResourceTemplate{
			{Name: "user_profile", URITemplate: "users/{userId}/profile", Description: "User profile"},
		},
	}

	iface, err := convertToInterface(disc, "")
	if err != nil {
		t.Fatal(err)
	}
	op := iface.Operations["user_profile"]
	props := op.Input["properties"].(map[string]any)
	tmplSchema := props["uriTemplate"].(map[string]any)
	if tmplSchema["const"] != "users/{userId}/profile" {
		t.Errorf("const = %v, want users/{userId}/profile", tmplSchema["const"])
	}
}

func TestConvertToInterface_PromptOperations(t *testing.T) {
	disc := &discovery{
		Prompts: []*gomcp.Prompt{
			{
				Name:        "code_review",
				Description: "Review code",
				Arguments: []*gomcp.PromptArgument{
					{Name: "language", Description: "Programming language", Required: true},
					{Name: "style", Description: "Review style"},
				},
			},
		},
	}

	iface, err := convertToInterface(disc, "")
	if err != nil {
		t.Fatal(err)
	}
	op := iface.Operations["code_review"]
	if op.Input == nil {
		t.Fatal("expected input schema for prompt")
	}
	props := op.Input["properties"].(map[string]any)
	if _, ok := props["language"]; !ok {
		t.Error("expected language property")
	}
	if _, ok := props["style"]; !ok {
		t.Error("expected style property")
	}
	req, ok := op.Input["required"].([]string)
	if !ok {
		t.Fatal("expected required array")
	}
	if len(req) != 1 || req[0] != "language" {
		t.Errorf("required = %v, want [language]", req)
	}

	binding := iface.Bindings["code_review."+DefaultSourceName]
	if binding.Ref != "prompts/code_review" {
		t.Errorf("ref = %q, want prompts/code_review", binding.Ref)
	}
}

func TestConvertToInterface_PromptHasOutputSchema(t *testing.T) {
	disc := &discovery{
		Prompts: []*gomcp.Prompt{
			{Name: "review", Description: "Review code"},
		},
	}

	iface, err := convertToInterface(disc, "")
	if err != nil {
		t.Fatal(err)
	}
	op := iface.Operations["review"]
	if op.Output == nil {
		t.Fatal("expected output schema for prompt operation")
	}
	if op.Output["type"] != "object" {
		t.Errorf("output type = %v, want object", op.Output["type"])
	}
	props, ok := op.Output["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties in output schema")
	}
	if _, ok := props["messages"]; !ok {
		t.Error("expected messages property in output schema")
	}
	req, ok := op.Output["required"].([]string)
	if !ok {
		t.Fatal("expected required array in output schema")
	}
	if len(req) != 1 || req[0] != "messages" {
		t.Errorf("required = %v, want [messages]", req)
	}
}

func TestConvertToInterface_SourceEntry(t *testing.T) {
	disc := &discovery{}
	iface, err := convertToInterface(disc, "https://mcp.example.com/sse")
	if err != nil {
		t.Fatal(err)
	}
	src := iface.Sources[DefaultSourceName]
	if src.Format != FormatToken {
		t.Errorf("format = %q, want %q", src.Format, FormatToken)
	}
	if src.Location != "https://mcp.example.com/sse" {
		t.Errorf("location = %q, want https://mcp.example.com/sse", src.Location)
	}
}

func TestConvertToInterface_NilDiscovery(t *testing.T) {
	_, err := convertToInterface(nil, "")
	if err == nil {
		t.Error("expected error for nil discovery")
	}
}

func TestConvertToInterface_SortedOutput(t *testing.T) {
	disc := &discovery{
		Tools: []*gomcp.Tool{
			{Name: "zulu_tool"},
			{Name: "alpha_tool"},
		},
		Resources: []*gomcp.Resource{
			{Name: "zulu_resource", URI: "z://z"},
			{Name: "alpha_resource", URI: "a://a"},
		},
	}

	iface, err := convertToInterface(disc, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(iface.Operations) != 4 {
		t.Fatalf("expected 4 operations, got %d", len(iface.Operations))
	}
	for _, name := range []string{"alpha_tool", "zulu_tool", "alpha_resource", "zulu_resource"} {
		if _, ok := iface.Operations[name]; !ok {
			t.Errorf("expected operation %q", name)
		}
	}
}

func TestConvertToInterface_ToolFallsBackToTitle(t *testing.T) {
	disc := &discovery{
		Tools: []*gomcp.Tool{
			{Name: "my_tool", Title: "My Tool Title"},
		},
	}

	iface, err := convertToInterface(disc, "")
	if err != nil {
		t.Fatal(err)
	}
	if iface.Operations["my_tool"].Description != "My Tool Title" {
		t.Errorf("description = %q, want My Tool Title", iface.Operations["my_tool"].Description)
	}
}
