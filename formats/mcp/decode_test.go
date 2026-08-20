package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openbindings/openbindings-go/invoke"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func setupDecodeServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := gomcp.NewServer(&gomcp.Implementation{Name: "decode", Version: "1"}, nil)
	inputSchema := map[string]any{"type": "object"}
	outputSchema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"value": map[string]any{"type": "string"}},
		"required":   []any{"value"},
	}
	add := func(name string, result *gomcp.CallToolResult) {
		server.AddTool(&gomcp.Tool{Name: name, InputSchema: inputSchema, OutputSchema: outputSchema},
			func(context.Context, *gomcp.CallToolRequest) (*gomcp.CallToolResult, error) { return result, nil })
	}
	add("structured", &gomcp.CallToolResult{
		Content:           []gomcp.Content{&gomcp.TextContent{Text: "native shadow"}},
		StructuredContent: map[string]any{"value": "application"},
	})
	add("missing", &gomcp.CallToolResult{Content: []gomcp.Content{&gomcp.TextContent{Text: "only native"}}})
	add("invalid", &gomcp.CallToolResult{Content: []gomcp.Content{}, StructuredContent: map[string]any{"value": 7}})
	testServer := httptest.NewServer(gomcp.NewStreamableHTTPHandler(func(*http.Request) *gomcp.Server { return server }, nil))
	t.Cleanup(testServer.Close)
	return testServer
}

func TestDecodeProjectsStructuredApplicationValueOnly(t *testing.T) {
	server := setupDecodeServer(t)
	invoker := NewInvoker()
	defer invoker.Close()
	call := invoker.InvokeBinding(bg(), invocationArgs(server.URL, "tools/structured", nil))
	_ = call.Write(bg(), map[string]any{})
	value, err := invoke.Single(shortCtx(t), call.Outputs())
	if err != nil {
		t.Fatal(err)
	}
	output := value.(map[string]any)
	if output["value"] != "application" || output["content"] != nil {
		t.Fatalf("output = %#v", output)
	}
}

func TestDecodeMissingOrInvalidStructuredContentFails(t *testing.T) {
	server := setupDecodeServer(t)
	invoker := NewInvoker()
	defer invoker.Close()
	for _, name := range []string{"missing", "invalid"} {
		call := invoker.InvokeBinding(bg(), invocationArgs(server.URL, "tools/"+name, nil))
		_ = call.Write(bg(), map[string]any{})
		values, err := drainOutputs(t, call)
		if len(values) != 0 || codeOf(t, err) != invoke.ErrCodeResponseError {
			t.Fatalf("%s: values=%#v err=%v", name, values, err)
		}
	}
}
