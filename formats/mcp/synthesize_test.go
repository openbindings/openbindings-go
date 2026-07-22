package mcp

import (
	"context"
	"strings"
	"testing"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"
	openbindings "github.com/openbindings/openbindings-go"
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
	if op.Input.(map[string]any)["type"] != "object" {
		t.Errorf("input type = %v, want object", op.Input.(map[string]any)["type"])
	}
	if op.Output == nil {
		t.Fatal("expected output schema")
	}
	if op.Output.(map[string]any)["type"] != "object" {
		t.Errorf("output type = %v, want object", op.Output.(map[string]any)["type"])
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
	// Updated for openbindings.mcp@1 §8/§9.1: a static resource takes no
	// input value — this test previously pinned a const-uri input schema,
	// an input the conformant invoker refuses. The URI is the binding's
	// ref, not caller input.
	if op.Input != nil {
		t.Fatalf("static resource operation must declare no input, got %v", op.Input)
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
	// Updated for openbindings.mcp@1 §8/§9.1: a template operation's input
	// is the object of its RFC 6570 variables (string-typed, none required)
	// — this test previously pinned a const-uriTemplate input schema, whose
	// only member the conformant invoker refuses as an undeclared variable.
	if op.Input == nil {
		t.Fatal("expected an input schema derived from the template's variables")
	}
	props, ok := op.Input.(map[string]any)["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties in input schema")
	}
	varSchema, ok := props["userId"].(map[string]any)
	if !ok {
		t.Fatalf("expected userId variable property, got %v", props)
	}
	if varSchema["type"] != "string" {
		t.Errorf("userId type = %v, want string (template variables are string-typed)", varSchema["type"])
	}
	if _, hasTemplate := props["uriTemplate"]; hasTemplate {
		t.Error("uriTemplate must not appear as an input property")
	}
	if op.Input.(map[string]any)["additionalProperties"] != false {
		t.Errorf("additionalProperties = %v, want false (undeclared variables are refused)", op.Input.(map[string]any)["additionalProperties"])
	}
	if _, hasRequired := op.Input.(map[string]any)["required"]; hasRequired {
		t.Error("no variable is required: unsupplied variables follow RFC 6570 undefined-value expansion")
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
	props := op.Input.(map[string]any)["properties"].(map[string]any)
	if _, ok := props["language"]; !ok {
		t.Error("expected language property")
	}
	if _, ok := props["style"]; !ok {
		t.Error("expected style property")
	}
	req, ok := op.Input.(map[string]any)["required"].([]string)
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
	if op.Output.(map[string]any)["type"] != "object" {
		t.Errorf("output type = %v, want object", op.Output.(map[string]any)["type"])
	}
	props, ok := op.Output.(map[string]any)["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties in output schema")
	}
	if _, ok := props["messages"]; !ok {
		t.Error("expected messages property in output schema")
	}
	req, ok := op.Output.(map[string]any)["required"].([]string)
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
	if src.BindingSpec != BindingSpec {
		t.Errorf("format = %q, want %q", src.BindingSpec, BindingSpec)
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

// Cross-SDK pin, mirroring the TS SDK's collision-assignment fixture: both
// names sanitize to "a_b", and byte-wise name order (" " 0x20 < "_" 0x5F)
// decides which tool is processed first and wins the bare key. The TS SDK
// matches via codePointCompare (UTF-8 byte order is code point order); its
// former localeCompare made this assignment depend on the host locale.
func TestConvertToInterface_CollisionAssignmentFollowsByteOrder(t *testing.T) {
	disc := &discovery{
		Tools: []*gomcp.Tool{
			{Name: "a_b", Description: "underscore tool"},
			{Name: "a b", Description: "space tool"},
		},
	}

	iface, err := convertToInterface(disc, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := iface.Operations["a_b"].Description; got != "space tool" {
		t.Errorf(`Operations["a_b"].Description = %q, want "space tool"`, got)
	}
	if got := iface.Operations["tool_a_b"].Description; got != "underscore tool" {
		t.Errorf(`Operations["tool_a_b"].Description = %q, want "underscore tool"`, got)
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

// ---------------------------------------------------------------------------
// Pinned-listing synthesis (MCP-D-01, §6 content primacy)
// ---------------------------------------------------------------------------

// A source carrying content is a pinned listing: synthesis reads the pin
// OFFLINE — the server is never dialed (the dead-end counter proves it) —
// and the emitted OBI matches what the same live listing would produce.
func TestSynthesizeInterface_PinnedListingIsOffline(t *testing.T) {
	ts, requests := deadEndServer(t)

	pin := map[string]any{
		"tools": []any{map[string]any{
			"name":        "get_weather",
			"description": "Get weather",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{"city": map[string]any{"type": "string"}},
			},
		}},
		"resources": []any{map[string]any{
			"uri": "app://status", "name": "status", "description": "Application status",
		}},
		"resourceTemplates": []any{map[string]any{
			"uriTemplate": "file:///{path}", "name": "file",
		}},
		"prompts": []any{map[string]any{
			"name":      "greet",
			"arguments": []any{map[string]any{"name": "name", "required": true}},
		}},
	}

	synth := NewSynthesizer()
	iface, err := synth.SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{
			BindingSpec: BindingSpec,
			Location:    ts.URL,
			Content:     mustContent(pin),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	wantRefs := map[string]string{
		"get_weather." + DefaultSourceName: "tools/get_weather",
		"status." + DefaultSourceName:      "resources/app://status",
		"file." + DefaultSourceName:        "resourceTemplates/file:///{path}",
		"greet." + DefaultSourceName:       "prompts/greet",
	}
	if len(iface.Operations) != 4 {
		t.Fatalf("expected 4 operations, got %d (%v)", len(iface.Operations), iface.Operations)
	}
	for key, ref := range wantRefs {
		b, ok := iface.Bindings[key]
		if !ok {
			t.Errorf("expected binding %q", key)
			continue
		}
		if b.Ref != ref {
			t.Errorf("binding %q ref = %q, want %q", key, b.Ref, ref)
		}
	}

	if in, ok := iface.Operations["get_weather"].Input.(map[string]any); !ok || in["type"] != "object" {
		t.Errorf("get_weather input schema not decoded from the pin: %v", iface.Operations["get_weather"].Input)
	}
	greetIn, ok := iface.Operations["greet"].Input.(map[string]any)
	if !ok {
		t.Fatalf("greet input schema not derived from pinned prompt arguments: %v", iface.Operations["greet"].Input)
	}
	if req, _ := greetIn["required"].([]string); len(req) != 1 || req[0] != "name" {
		t.Errorf("greet required = %v, want [name]", greetIn["required"])
	}
	if src := iface.Sources[DefaultSourceName]; src.Location != ts.URL {
		t.Errorf("source location = %q, want %q", src.Location, ts.URL)
	}

	if n := requests.Load(); n != 0 {
		t.Errorf("pinned synthesis must be offline; server saw %d requests", n)
	}
}

// An invalid pin is refused loudly before any I/O: the pin grammar
// (stray members included) and the 2025-11-25 entity shapes both gate the
// synthesis lane exactly as they gate invocation.
func TestSynthesizeInterface_InvalidPinRefusedLoudly(t *testing.T) {
	ts, requests := deadEndServer(t)

	synth := NewSynthesizer()
	cases := []struct {
		name string
		pin  any
	}{
		{"stray nextCursor", map[string]any{
			"tools":      []any{map[string]any{"name": "probe"}},
			"nextCursor": "page2",
		}},
		{"non-object content", "not a listing"},
		{"entry missing identity", map[string]any{"tools": []any{map[string]any{"description": "no name"}}}},
		{"entity shape mismatch", map[string]any{"tools": []any{map[string]any{"name": "probe", "description": 5}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := synth.SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
				Sources: []openbindings.SynthesizeSource{{
					BindingSpec: BindingSpec,
					Location:    ts.URL,
					Content:     mustContent(tc.pin),
				}},
			})
			if err == nil {
				t.Fatal("expected an invalid-pin refusal")
			}
			if !strings.Contains(err.Error(), "MCP-D-01") {
				t.Errorf("refusal should cite MCP-D-01, got: %v", err)
			}
		})
	}

	if n := requests.Load(); n != 0 {
		t.Errorf("invalid pins must be refused before network I/O; server saw %d requests", n)
	}
}

// Content does not waive MCP-D-02: a pinned source still requires the
// location, refused offline.
func TestSynthesizeInterface_PinWithoutLocationRefused(t *testing.T) {
	synth := NewSynthesizer()
	_, err := synth.SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{
			BindingSpec: BindingSpec,
			Content:     mustContent(map[string]any{"tools": []any{map[string]any{"name": "probe"}}}),
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "MCP-D-02") {
		t.Fatalf("expected an MCP-D-02 refusal, got: %v", err)
	}
}

// Without content there is no pin: synthesis still dials the live server.
func TestSynthesizeInterface_AbsentContentDialsLive(t *testing.T) {
	ts, requests := deadEndServer(t)

	synth := NewSynthesizer()
	_, err := synth.SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{
			BindingSpec: BindingSpec,
			Location:    ts.URL,
		}},
	})
	if err == nil {
		t.Fatal("expected discovery against a dead-end server to fail")
	}
	if n := requests.Load(); n == 0 {
		t.Error("absent content must dial the live server; server saw no requests")
	}
}
