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

// setupDecodeServer registers the tool/resource shapes the decode-lane
// ruling pins: JSON-as-text without structuredContent, structuredContent
// with its compatibility text shadow, and resources with declared MIME
// types (json valid, json malformed, plain text that looks like JSON).
func setupDecodeServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := gomcp.NewServer(&gomcp.Implementation{Name: "decode-test", Version: "1.0.0"}, nil)

	type emptyInput struct{}
	gomcp.AddTool(server, &gomcp.Tool{
		Name: "jsonAsText", Description: "returns JSON serialized into a text block, no structuredContent",
	}, func(ctx context.Context, req *gomcp.CallToolRequest, args emptyInput) (*gomcp.CallToolResult, any, error) {
		return &gomcp.CallToolResult{
			Content: []gomcp.Content{&gomcp.TextContent{Text: `{"temperature": 22.5}`}},
		}, nil, nil
	})
	gomcp.AddTool(server, &gomcp.Tool{
		Name: "structured", Description: "returns structuredContent plus its compatibility text shadow",
	}, func(ctx context.Context, req *gomcp.CallToolRequest, args emptyInput) (*gomcp.CallToolResult, any, error) {
		return &gomcp.CallToolResult{
			Content:           []gomcp.Content{&gomcp.TextContent{Text: `{"temperature": 22.5}`}},
			StructuredContent: map[string]any{"temperature": 22.5},
		}, nil, nil
	})

	addResource := func(name, uri, mime, text string) {
		server.AddResource(&gomcp.Resource{Name: name, URI: uri}, func(ctx context.Context, req *gomcp.ReadResourceRequest) (*gomcp.ReadResourceResult, error) {
			return &gomcp.ReadResourceResult{
				Contents: []*gomcp.ResourceContents{{URI: uri, MIMEType: mime, Text: text}},
			}, nil
		})
	}
	addResource("goodJSON", "app://good", "application/json", `{"ok":true}`)
	addResource("badJSON", "app://bad", "application/json", `{not json`)
	addResource("plainText", "app://plain", "text/plain", `{"looks":"like json"}`)

	// Multi-item, blob, and empty resources exercise §9.3's always-array
	// resource rule (MCP-P-05): the output value is uniformly the array of
	// decoded contents items, whatever the count, and blob items decode
	// structurally before any mimeType consideration.
	server.AddResource(&gomcp.Resource{Name: "multi", URI: "app://multi"}, func(ctx context.Context, req *gomcp.ReadResourceRequest) (*gomcp.ReadResourceResult, error) {
		return &gomcp.ReadResourceResult{
			Contents: []*gomcp.ResourceContents{
				{URI: "app://multi", MIMEType: "application/json", Text: `{"n":1}`},
				{URI: "app://multi", MIMEType: "text/plain", Text: "second"},
			},
		}, nil
	})
	server.AddResource(&gomcp.Resource{Name: "blob", URI: "app://blob"}, func(ctx context.Context, req *gomcp.ReadResourceRequest) (*gomcp.ReadResourceResult, error) {
		return &gomcp.ReadResourceResult{
			Contents: []*gomcp.ResourceContents{
				// Declared-JSON mimeType on a BLOB item: the blob member is
				// structural and wins, whatever mimeType declares.
				{URI: "app://blob", MIMEType: "application/json", Blob: []byte("hello world")},
			},
		}, nil
	})
	server.AddResource(&gomcp.Resource{Name: "empty", URI: "app://empty"}, func(ctx context.Context, req *gomcp.ReadResourceRequest) (*gomcp.ReadResourceResult, error) {
		return &gomcp.ReadResourceResult{Contents: []*gomcp.ResourceContents{}}, nil
	})

	handler := gomcp.NewStreamableHTTPHandler(func(r *http.Request) *gomcp.Server { return server }, nil)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts
}

func invokeAndRead(t *testing.T, inv *Invoker, url, ref string, hooks *openbindings.InvokeHooks) (any, error) {
	t.Helper()
	args := invocationArgs(url, ref, nil)
	args.Hooks = hooks
	call := inv.InvokeBinding(bg(), args)
	if strings.HasPrefix(ref, "tools/") {
		if err := call.Write(bg(), map[string]any{}); err != nil {
			return nil, err
		}
	}
	return openbindings.Single(shortCtx(t), call.Outputs())
}

// The ruling's core pin: JSON-in-text is, per MCP 2025-11-25, the
// backwards-compatibility SHADOW of structuredContent — a client never
// sniffs it. Text content is a string.
func TestDecode_JSONAsTextIsAString(t *testing.T) {
	ts := setupDecodeServer(t)
	inv := NewInvoker()
	defer inv.Close()

	v, err := invokeAndRead(t, inv, ts.URL, "tools/jsonAsText", nil)
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := v.(string); !ok || s != `{"temperature": 22.5}` {
		t.Fatalf("text content must be the string verbatim, got %T: %v", v, v)
	}
}

// structuredContent is the declared lane and wins over its text shadow.
func TestDecode_StructuredContentPreferred(t *testing.T) {
	ts := setupDecodeServer(t)
	inv := NewInvoker()
	defer inv.Close()

	v, err := invokeAndRead(t, inv, ts.URL, "tools/structured", nil)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := v.(map[string]any)
	if !ok || m["temperature"] != 22.5 {
		t.Fatalf("structuredContent must win, got %T: %v", v, v)
	}
}

// singleItem asserts the always-array resource rule (openbindings.mcp@1
// §9.3, MCP-P-05) and returns the lone decoded item. The resource decode
// tests previously pinned the non-conformant single-item unwrap.
func singleItem(t *testing.T, v any) any {
	t.Helper()
	items, ok := v.([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("resource output must be the array of decoded contents items, got %T: %v", v, v)
	}
	return items[0]
}

// Resources decode by their DECLARED mimeType: json parses...
func TestDecode_ResourceDeclaredJSONParses(t *testing.T) {
	ts := setupDecodeServer(t)
	inv := NewInvoker()
	defer inv.Close()

	v, err := invokeAndRead(t, inv, ts.URL, "resources/app://good", nil)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := singleItem(t, v).(map[string]any)
	if !ok || m["ok"] != true {
		t.Fatalf("declared application/json must parse, got %T: %v", v, v)
	}
}

// ...malformed declared-JSON is a LOUD error (never a silent string)...
func TestDecode_ResourceDeclaredJSONMalformedIsLoud(t *testing.T) {
	ts := setupDecodeServer(t)
	inv := NewInvoker()
	defer inv.Close()

	_, err := invokeAndRead(t, inv, ts.URL, "resources/app://bad", nil)
	if err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("declared-JSON parse failure must be loud, got %v", err)
	}
}

// ...and JSON-shaped text under a non-JSON declaration stays a string
// (the anti-sniff pin: the payload's shape never picks the lane).
func TestDecode_ResourcePlainTextNeverSniffed(t *testing.T) {
	ts := setupDecodeServer(t)
	inv := NewInvoker()
	defer inv.Close()

	v, err := invokeAndRead(t, inv, ts.URL, "resources/app://plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := singleItem(t, v).(string); !ok || s != `{"looks":"like json"}` {
		t.Fatalf("text/plain must stay a string regardless of shape, got %T: %v", v, v)
	}
}

// The output value is ALWAYS the array of decoded contents items, in order
// (§9.3, MCP-P-05): a two-item result decodes item-by-item.
func TestDecode_ResourceMultipleItemsInOrder(t *testing.T) {
	ts := setupDecodeServer(t)
	inv := NewInvoker()
	defer inv.Close()

	v, err := invokeAndRead(t, inv, ts.URL, "resources/app://multi", nil)
	if err != nil {
		t.Fatal(err)
	}
	items, ok := v.([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("expected 2 decoded items, got %T: %v", v, v)
	}
	first, ok := items[0].(map[string]any)
	if !ok || first["n"] != 1.0 {
		t.Errorf("item 0 must be the parsed JSON, got %T: %v", items[0], items[0])
	}
	if items[1] != "second" {
		t.Errorf("item 1 must be the text string, got %v", items[1])
	}
}

// A blob item decodes STRUCTURALLY first (§9.3): the blob member is the
// item's Base64 string as MCP carries it, whatever mimeType it declares —
// even application/json.
func TestDecode_ResourceBlobIsBase64String(t *testing.T) {
	ts := setupDecodeServer(t)
	inv := NewInvoker()
	defer inv.Close()

	v, err := invokeAndRead(t, inv, ts.URL, "resources/app://blob", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := base64.StdEncoding.EncodeToString([]byte("hello world"))
	if s, ok := singleItem(t, v).(string); !ok || s != want {
		t.Fatalf("blob item must pass as its Base64 string, got %T: %v", v, v)
	}
}

// contents: [] yields [] — the shape never depends on the item count (§9.3).
func TestDecode_ResourceEmptyContentsIsEmptyArray(t *testing.T) {
	ts := setupDecodeServer(t)
	inv := NewInvoker()
	defer inv.Close()

	v, err := invokeAndRead(t, inv, ts.URL, "resources/app://empty", nil)
	if err != nil {
		t.Fatal(err)
	}
	items, ok := v.([]any)
	if !ok || len(items) != 0 {
		t.Fatalf("empty contents must yield an empty array, got %T: %v", v, v)
	}
}

// A consumer stuck with a JSON-as-text server opts in through the decode
// seam — per-invocation hook, the completeness spectrum working as designed.
func TestDecode_HookOptInParsesText(t *testing.T) {
	ts := setupDecodeServer(t)
	inv := NewInvoker()
	defer inv.Close()

	hooks := openbindings.NewOperationInvoker().SnapshotHooks(
		func(_ openbindings.InvokeSite, raw openbindings.RawResult) (any, error) {
			var parsed any
			if err := json.Unmarshal(raw.Body, &parsed); err != nil {
				return nil, err
			}
			return parsed, nil
		}, nil, nil)

	v, err := invokeAndRead(t, inv, ts.URL, "tools/jsonAsText", hooks)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := v.(map[string]any)
	if !ok || m["temperature"] != 22.5 {
		t.Fatalf("decode hook must be able to opt into JSON, got %T: %v", v, v)
	}
}
