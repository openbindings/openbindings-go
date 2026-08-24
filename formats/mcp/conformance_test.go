package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/openbindings/openbindings-go/invoke"
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

func pinArgs(url, selector string, pin any, bindContext map[string]any) *invoke.BindingInvocationArgs {
	args := invocationArgs(url, selector, bindContext)
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
			if codeOf(t, err) != invoke.ErrCodeSourceLoadFailed {
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
	value, err := invoke.Single(shortCtx(t), call.Outputs())
	if err != nil {
		t.Fatal(err)
	}
	if got := value.(map[string]any)["value"]; got != "echo: hello" {
		t.Fatalf("output = %#v", value)
	}
}

func TestPinResolutionRefusalsAreOffline(t *testing.T) {
	for _, test := range []struct {
		name     string
		selector string
		pin      any
		code     string
	}{
		{"missing", "tools/missing", map[string]any{"tools": []any{applicationTool("probe")}}, invoke.ErrCodeSelectorNotFound},
		{"ambiguous", "tools/probe", map[string]any{"tools": []any{applicationTool("probe"), applicationTool("probe")}}, invoke.ErrCodeSelectorNotFound},
		{"missing output schema", "tools/probe", map[string]any{"tools": []any{map[string]any{"name": "probe"}}}, invoke.ErrCodeInvalidSelector},
		{"required task", "tools/probe", map[string]any{"tools": []any{map[string]any{"name": "probe", "outputSchema": map[string]any{}, "execution": map[string]any{"taskSupport": "required"}}}}, invoke.ErrCodeInvalidSelector},
		{"resource", "resources/app://x", map[string]any{"resources": []any{map[string]any{"uri": "app://x"}}}, invoke.ErrCodeInvalidSelector},
		{"prompt", "prompts/review", map[string]any{"prompts": []any{map[string]any{"name": "review"}}}, invoke.ErrCodeInvalidSelector},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, requests := deadEndServer(t)
			call := NewInvoker().InvokeBinding(bg(), pinArgs(server.URL, test.selector, test.pin, nil))
			_, err := drainOutputs(t, call)
			if codeOf(t, err) != test.code {
				t.Fatalf("error = %v", err)
			}
			if ie := invoke.AsInvocationError(err); ie.HasData() {
				t.Fatalf("offline resolution diagnostics crossed as abstract data: %#v", ie.Data)
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
	if codeOf(t, err) != invoke.ErrCodeSourceConfigError {
		t.Fatal(err)
	}
	if requests.Load() != 0 {
		t.Fatalf("non-exact identifier made %d requests", requests.Load())
	}
}
