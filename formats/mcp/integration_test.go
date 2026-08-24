package mcp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openbindings/openbindings-go/invoke"
	"github.com/openbindings/openbindings-go/synthesize"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type testState struct {
	lastAuth string
}

type toolOutput struct {
	Value string `json:"value"`
}

func setupMCPServer(t *testing.T) (*httptest.Server, *testState) {
	t.Helper()
	server := gomcp.NewServer(&gomcp.Implementation{Name: "test-mcp-server", Version: "1.0.0"}, nil)

	type echoInput struct {
		Message string `json:"message"`
	}
	gomcp.AddTool(server, &gomcp.Tool{Name: "echo", Description: "Echoes the input message"},
		func(_ context.Context, _ *gomcp.CallToolRequest, args echoInput) (*gomcp.CallToolResult, toolOutput, error) {
			return &gomcp.CallToolResult{Content: []gomcp.Content{&gomcp.TextContent{Text: "native shadow"}}}, toolOutput{Value: "echo: " + args.Message}, nil
		})

	type emptyInput struct{}
	gomcp.AddTool(server, &gomcp.Tool{Name: "alwaysFails", Description: "Always fails"},
		func(context.Context, *gomcp.CallToolRequest, emptyInput) (*gomcp.CallToolResult, toolOutput, error) {
			return &gomcp.CallToolResult{IsError: true, Content: []gomcp.Content{&gomcp.TextContent{Text: "the tool exploded"}}}, toolOutput{}, nil
		})
	gomcp.AddTool(server, &gomcp.Tool{Name: "slow", Description: "Waits for cancellation"},
		func(ctx context.Context, _ *gomcp.CallToolRequest, _ emptyInput) (*gomcp.CallToolResult, toolOutput, error) {
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second):
			}
			return &gomcp.CallToolResult{}, toolOutput{Value: "done"}, nil
		})

	// Inventory that the first candidate deliberately does not bind.
	server.AddResource(&gomcp.Resource{Name: "status", URI: "app://status"},
		func(context.Context, *gomcp.ReadResourceRequest) (*gomcp.ReadResourceResult, error) {
			return &gomcp.ReadResourceResult{Contents: []*gomcp.ResourceContents{}}, nil
		})
	server.AddPrompt(&gomcp.Prompt{Name: "greet"},
		func(context.Context, *gomcp.GetPromptRequest) (*gomcp.GetPromptResult, error) {
			return &gomcp.GetPromptResult{Messages: []*gomcp.PromptMessage{}}, nil
		})

	state := &testState{}
	handler := gomcp.NewStreamableHTTPHandler(func(r *http.Request) *gomcp.Server {
		state.lastAuth = r.Header.Get("Authorization")
		return server
	}, &gomcp.StreamableHTTPOptions{Stateless: true})
	testServer := httptest.NewServer(handler)
	t.Cleanup(testServer.Close)
	return testServer, state
}

func bg() context.Context { return context.Background() }

func shortCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func invocationArgs(url, selector string, bindCtx map[string]any) *invoke.BindingInvocationArgs {
	return &invoke.BindingInvocationArgs{
		Source:   invoke.InvocationSource{BindingSpec: BindingSpec, Location: url},
		Selector: selector,
		Context:  bindCtx,
	}
}

func invokeAndRead(t *testing.T, invoker *Invoker, url, selector string, _ ...*invoke.InvokeHooks) (any, error) {
	t.Helper()
	call := invoker.InvokeBinding(bg(), invocationArgs(url, selector, nil))
	if len(selector) >= len("tools/") && selector[:len("tools/")] == "tools/" {
		if err := call.Write(bg(), map[string]any{}); err != nil {
			return nil, err
		}
	}
	return invoke.Single(shortCtx(t), call.Outputs())
}

func drainOutputs(t *testing.T, call invoke.Invocation[any, any]) ([]any, error) {
	t.Helper()
	reader := call.Outputs()
	var values []any
	for {
		value, err := reader.Read(shortCtx(t))
		if errors.Is(err, io.EOF) {
			return values, nil
		}
		if err != nil {
			return values, err
		}
		values = append(values, value)
	}
}

func codeOf(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("expected a terminal error")
	}
	var invocationErr *invoke.InvocationError
	if !errors.As(err, &invocationErr) {
		t.Fatalf("expected InvocationError, got %T: %v", err, err)
	}
	return invocationErr.Code
}

func TestIntegrationSynthesisExposesOnlyApplicationContractTools(t *testing.T) {
	testServer, _ := setupMCPServer(t)
	iface, err := NewSynthesizer().SynthesizeInterface(bg(), &synthesize.SynthesizeInput{Sources: []synthesize.SynthesizeSource{{
		BindingSpec: BindingSpec,
		Location:    testServer.URL,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(iface.Operations) != 3 {
		t.Fatalf("operations = %v, want the three output-schema tools", iface.Operations)
	}
	for _, excluded := range []string{"status", "greet"} {
		if _, exists := iface.Operations[excluded]; exists {
			t.Fatalf("native MCP inventory %q leaked into OBI operations", excluded)
		}
	}
}

func TestIntegrationToolInvocationEmitsStructuredApplicationValue(t *testing.T) {
	testServer, _ := setupMCPServer(t)
	invoker := NewInvoker()
	defer invoker.Close()
	call := invoker.InvokeBinding(bg(), invocationArgs(testServer.URL, "tools/echo", nil))
	if err := call.Write(bg(), map[string]any{"message": "hello"}); err != nil {
		t.Fatal(err)
	}
	value, err := invoke.Single(shortCtx(t), call.Outputs())
	if err != nil {
		t.Fatal(err)
	}
	output, ok := value.(map[string]any)
	if !ok || output["value"] != "echo: hello" {
		t.Fatalf("output = %#v", value)
	}
	if _, leaked := output["content"]; leaked {
		t.Fatalf("native MCP result leaked into application output: %#v", output)
	}
}

func TestIntegrationExplicitBearerCredential(t *testing.T) {
	testServer, state := setupMCPServer(t)
	invoker := NewInvoker()
	defer invoker.Close()
	args := invocationArgs(testServer.URL, "tools/echo", map[string]any{"bearerToken": "secret"})
	call := invoker.InvokeBinding(bg(), args)
	_ = call.Write(bg(), map[string]any{"message": "hello"})
	if _, err := invoke.Single(shortCtx(t), call.Outputs()); err != nil {
		t.Fatal(err)
	}
	if state.lastAuth != "Bearer secret" {
		t.Fatalf("authorization = %q", state.lastAuth)
	}
}

func TestIntegrationToolFailureIsNotAnOutput(t *testing.T) {
	testServer, _ := setupMCPServer(t)
	invoker := NewInvoker()
	defer invoker.Close()
	call := invoker.InvokeBinding(bg(), invocationArgs(testServer.URL, "tools/alwaysFails", nil))
	_ = call.Write(bg(), map[string]any{})
	values, err := drainOutputs(t, call)
	if len(values) != 0 || codeOf(t, err) != invoke.ErrCodeExecutionFailed {
		t.Fatalf("values = %#v, err = %v", values, err)
	}
}

func TestIntegrationExcludedInventoryCannotDispatch(t *testing.T) {
	testServer, _ := setupMCPServer(t)
	invoker := NewInvoker()
	defer invoker.Close()
	for _, selector := range []string{"resources/app://status", "prompts/greet"} {
		call := invoker.InvokeBinding(bg(), invocationArgs(testServer.URL, selector, nil))
		_ = call.Close()
		_, err := drainOutputs(t, call)
		if codeOf(t, err) != invoke.ErrCodeInvalidSelector {
			t.Fatalf("%s: %v", selector, err)
		}
	}
}

func TestIntegrationNonObjectInputRefusesBeforeDispatch(t *testing.T) {
	testServer, _ := setupMCPServer(t)
	invoker := NewInvoker()
	defer invoker.Close()
	call := invoker.InvokeBinding(bg(), invocationArgs(testServer.URL, "tools/echo", nil))
	_ = call.Write(bg(), "not an object")
	_, err := drainOutputs(t, call)
	if codeOf(t, err) != invoke.ErrCodeValidationFailed {
		t.Fatal(err)
	}
}

func TestIntegrationCancel(t *testing.T) {
	testServer, _ := setupMCPServer(t)
	invoker := NewInvoker()
	defer invoker.Close()
	call := invoker.InvokeBinding(bg(), invocationArgs(testServer.URL, "tools/slow", nil))
	_ = call.Write(bg(), map[string]any{})
	time.AfterFunc(100*time.Millisecond, call.Cancel)
	_, err := drainOutputs(t, call)
	if codeOf(t, err) != invoke.ErrCodeCancelled {
		t.Fatal(err)
	}
}
