package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func deadEndServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	requests := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, "network trap", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	return server, requests
}

func mustContent(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}

func pinArgs(url, ref string, pin any, bindContext map[string]any) *openbindings.BindingInvocationArgs {
	args := invocationArgs(url, ref, bindContext)
	args.Source.Content = mustContent(pin)
	return args
}

func applicationTool(name string) map[string]any {
	return map[string]any{
		"name":         name,
		"inputSchema":  map[string]any{"type": "object"},
		"outputSchema": map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}}, "required": []any{"value"}},
	}
}

func TestPinGrammarRefusesInvalidContentBeforeIO(t *testing.T) {
	for name, pin := range map[string]any{
		"non-object":       "bad",
		"array":            []any{},
		"pagination":       map[string]any{"nextCursor": "later"},
		"non-array family": map[string]any{"tools": map[string]any{}},
		"missing identity": map[string]any{"tools": []any{map[string]any{"outputSchema": map[string]any{}}}},
	} {
		t.Run(name, func(t *testing.T) {
			server, requests := deadEndServer(t)
			call := NewInvoker().InvokeBinding(bg(), pinArgs(server.URL, "tools/probe", pin, nil))
			_, err := drainOutputs(t, call)
			if codeOf(t, err) != openbindings.ErrCodeSourceLoadFailed {
				t.Fatal(err)
			}
			if requests.Load() != 0 {
				t.Fatalf("invalid pin made %d requests", requests.Load())
			}
		})
	}
}

func TestPinDisplacesLiveListingButRetainsInvocationTarget(t *testing.T) {
	server, _ := setupMCPServer(t)
	call := NewInvoker().InvokeBinding(bg(), pinArgs(server.URL, "tools/echo", map[string]any{
		"tools": []any{applicationTool("echo")},
	}, nil))
	_ = call.Write(bg(), map[string]any{"message": "hello"})
	value, err := openbindings.Single(shortCtx(t), call.Outputs())
	if err != nil {
		t.Fatal(err)
	}
	if got := value.(map[string]any)["value"]; got != "echo: hello" {
		t.Fatalf("output = %#v", value)
	}
}

func TestPinResolutionRefusalsAreOffline(t *testing.T) {
	for _, test := range []struct {
		name string
		ref  string
		pin  any
		want string
		code string
	}{
		{"missing", "tools/missing", map[string]any{"tools": []any{applicationTool("probe")}}, "matches no", openbindings.ErrCodeRefNotFound},
		{"ambiguous", "tools/probe", map[string]any{"tools": []any{applicationTool("probe"), applicationTool("probe")}}, "ambiguous", openbindings.ErrCodeRefNotFound},
		{"missing output schema", "tools/probe", map[string]any{"tools": []any{map[string]any{"name": "probe"}}}, "outputSchema", openbindings.ErrCodeInvalidRef},
		{"required task", "tools/probe", map[string]any{"tools": []any{map[string]any{"name": "probe", "outputSchema": map[string]any{}, "execution": map[string]any{"taskSupport": "required"}}}}, "task", openbindings.ErrCodeInvalidRef},
		{"resource", "resources/app://x", map[string]any{"resources": []any{map[string]any{"uri": "app://x"}}}, "excluded", openbindings.ErrCodeInvalidRef},
		{"prompt", "prompts/review", map[string]any{"prompts": []any{map[string]any{"name": "review"}}}, "excluded", openbindings.ErrCodeInvalidRef},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, requests := deadEndServer(t)
			call := NewInvoker().InvokeBinding(bg(), pinArgs(server.URL, test.ref, test.pin, nil))
			_, err := drainOutputs(t, call)
			if codeOf(t, err) != test.code || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v", err)
			}
			if requests.Load() != 0 {
				t.Fatalf("offline refusal made %d requests", requests.Load())
			}
		})
	}
}

func TestNonExactBindingIdentifierRefusesBeforeIO(t *testing.T) {
	server, requests := deadEndServer(t)
	args := pinArgs(server.URL, "tools/probe", map[string]any{"tools": []any{applicationTool("probe")}}, nil)
	args.Source.BindingSpec = "openbindings.mcp@2"
	call := NewInvoker().InvokeBinding(context.Background(), args)
	_, err := drainOutputs(t, call)
	if codeOf(t, err) != openbindings.ErrCodeSourceConfigError {
		t.Fatal(err)
	}
	if requests.Load() != 0 {
		t.Fatalf("non-exact identifier made %d requests", requests.Load())
	}
}
