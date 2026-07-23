package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"
	openbindings "github.com/openbindings/openbindings-go"
)

// setupDecodeServer deliberately returns result objects with several legal
// shapes. Revision 1 carries each complete MCP result object; it does not
// project structuredContent, unwrap content, or reinterpret resource text.
func setupDecodeServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := gomcp.NewServer(&gomcp.Implementation{Name: "decode-test", Version: "1.0.0"}, nil)
	type emptyInput struct{}
	addTool := func(name string, result *gomcp.CallToolResult) {
		gomcp.AddTool(server, &gomcp.Tool{Name: name}, func(context.Context, *gomcp.CallToolRequest, emptyInput) (*gomcp.CallToolResult, any, error) {
			return result, nil, nil
		})
	}
	addTool("jsonAsText", &gomcp.CallToolResult{Content: []gomcp.Content{&gomcp.TextContent{Text: `{"temperature": 22.5}`}}})
	addTool("multiText", &gomcp.CallToolResult{Content: []gomcp.Content{
		&gomcp.TextContent{Text: "note:"},
		&gomcp.TextContent{Text: `{"a":1}`},
	}})
	addTool("structured", &gomcp.CallToolResult{
		Content:           []gomcp.Content{&gomcp.TextContent{Text: `{"temperature": 22.5}`}},
		StructuredContent: map[string]any{"temperature": 22.5},
	})

	addResource := func(name, uri, mime, text string) {
		server.AddResource(&gomcp.Resource{Name: name, URI: uri}, func(context.Context, *gomcp.ReadResourceRequest) (*gomcp.ReadResourceResult, error) {
			return &gomcp.ReadResourceResult{Contents: []*gomcp.ResourceContents{{URI: uri, MIMEType: mime, Text: text}}}, nil
		})
	}
	addResource("goodJSON", "app://good", "application/json", `{"ok":true}`)
	addResource("badJSON", "app://bad", "application/json", `{not json`)
	addResource("plainText", "app://plain", "text/plain", `{"looks":"like json"}`)
	server.AddResource(&gomcp.Resource{Name: "multi", URI: "app://multi"}, func(context.Context, *gomcp.ReadResourceRequest) (*gomcp.ReadResourceResult, error) {
		return &gomcp.ReadResourceResult{Contents: []*gomcp.ResourceContents{
			{URI: "app://multi", MIMEType: "application/json", Text: `{"n":1}`},
			{URI: "app://multi", MIMEType: "text/plain", Text: "second"},
		}}, nil
	})
	server.AddResource(&gomcp.Resource{Name: "blob", URI: "app://blob"}, func(context.Context, *gomcp.ReadResourceRequest) (*gomcp.ReadResourceResult, error) {
		return &gomcp.ReadResourceResult{Contents: []*gomcp.ResourceContents{{
			URI: "app://blob", MIMEType: "application/json", Blob: []byte("hello world"),
		}}}, nil
	})
	server.AddResource(&gomcp.Resource{Name: "empty", URI: "app://empty"}, func(context.Context, *gomcp.ReadResourceRequest) (*gomcp.ReadResourceResult, error) {
		return &gomcp.ReadResourceResult{Contents: []*gomcp.ResourceContents{}}, nil
	})

	ts := httptest.NewServer(gomcp.NewStreamableHTTPHandler(func(*http.Request) *gomcp.Server { return server }, nil))
	t.Cleanup(ts.Close)
	return ts
}

func invokeAndRead(t *testing.T, inv *Invoker, url, ref string, _ ...*openbindings.InvokeHooks) (any, error) {
	t.Helper()
	call := inv.InvokeBinding(bg(), invocationArgs(url, ref, nil))
	if strings.HasPrefix(ref, "tools/") {
		if err := call.Write(bg(), map[string]any{}); err != nil {
			return nil, err
		}
	}
	return openbindings.Single(shortCtx(t), call.Outputs())
}

func resultObject(t *testing.T, value any) map[string]any {
	t.Helper()
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("MCP output must be the complete result object, got %T: %v", value, value)
	}
	return result
}

func arrayMember(t *testing.T, result map[string]any, name string) []any {
	t.Helper()
	items, ok := result[name].([]any)
	if !ok {
		t.Fatalf("result.%s must be an array, got %T: %v", name, result[name], result[name])
	}
	return items
}

func TestDecode_ToolResultPreservesTextContent(t *testing.T) {
	ts := setupDecodeServer(t)
	inv := NewInvoker()
	defer inv.Close()
	v, err := invokeAndRead(t, inv, ts.URL, "tools/jsonAsText")
	if err != nil {
		t.Fatal(err)
	}
	items := arrayMember(t, resultObject(t, v), "content")
	block := items[0].(map[string]any)
	if block["type"] != "text" || block["text"] != `{"temperature": 22.5}` {
		t.Fatalf("content block was not preserved: %v", block)
	}
}

func TestDecode_ToolResultPreservesMultipleBlocks(t *testing.T) {
	ts := setupDecodeServer(t)
	inv := NewInvoker()
	defer inv.Close()
	v, err := invokeAndRead(t, inv, ts.URL, "tools/multiText")
	if err != nil {
		t.Fatal(err)
	}
	items := arrayMember(t, resultObject(t, v), "content")
	if len(items) != 2 || items[0].(map[string]any)["text"] != "note:" || items[1].(map[string]any)["text"] != `{"a":1}` {
		t.Fatalf("content array was not preserved: %v", items)
	}
}

func TestDecode_ToolResultPreservesStructuredAndCompatibilityContent(t *testing.T) {
	ts := setupDecodeServer(t)
	inv := NewInvoker()
	defer inv.Close()
	v, err := invokeAndRead(t, inv, ts.URL, "tools/structured")
	if err != nil {
		t.Fatal(err)
	}
	result := resultObject(t, v)
	structured, ok := result["structuredContent"].(map[string]any)
	if !ok || structured["temperature"] != json.Number("22.5") || len(arrayMember(t, result, "content")) != 1 {
		t.Fatalf("complete tool result was not preserved: %v", result)
	}
}

func TestDecode_ResourceResultPreservesDeclaredText(t *testing.T) {
	ts := setupDecodeServer(t)
	inv := NewInvoker()
	defer inv.Close()
	for _, tc := range []struct{ ref, text string }{
		{"resources/app://good", `{"ok":true}`},
		{"resources/app://bad", `{not json`},
		{"resources/app://plain", `{"looks":"like json"}`},
	} {
		v, err := invokeAndRead(t, inv, ts.URL, tc.ref)
		if err != nil {
			t.Fatalf("%s: %v", tc.ref, err)
		}
		item := arrayMember(t, resultObject(t, v), "contents")[0].(map[string]any)
		if item["text"] != tc.text {
			t.Errorf("%s text = %v, want %q", tc.ref, item["text"], tc.text)
		}
	}
}

func TestDecode_ResourceResultPreservesOrderBlobAndEmpty(t *testing.T) {
	ts := setupDecodeServer(t)
	inv := NewInvoker()
	defer inv.Close()
	multi, err := invokeAndRead(t, inv, ts.URL, "resources/app://multi")
	if err != nil {
		t.Fatal(err)
	}
	items := arrayMember(t, resultObject(t, multi), "contents")
	if len(items) != 2 || items[0].(map[string]any)["text"] != `{"n":1}` || items[1].(map[string]any)["text"] != "second" {
		t.Fatalf("resource order or items changed: %v", items)
	}
	blob, err := invokeAndRead(t, inv, ts.URL, "resources/app://blob")
	if err != nil {
		t.Fatal(err)
	}
	if got := arrayMember(t, resultObject(t, blob), "contents")[0].(map[string]any)["blob"]; got != base64.StdEncoding.EncodeToString([]byte("hello world")) {
		t.Fatalf("blob = %v", got)
	}
	empty, err := invokeAndRead(t, inv, ts.URL, "resources/app://empty")
	if err != nil {
		t.Fatal(err)
	}
	if got := arrayMember(t, resultObject(t, empty), "contents"); len(got) != 0 {
		t.Fatalf("empty contents changed: %v", got)
	}
}
