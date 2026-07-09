package operationgraph

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

// Creation is inert: InvokeBinding returns the handle synchronously and
// preflight failures (here: an unloadable source) surface as a terminal
// error THROUGH the handle, never as a synchronous load before the handle
// exists (the load may be a network fetch).
func TestInvokeBinding_PreflightErrorsThroughHandle(t *testing.T) {
	inv := NewInvoker(openbindings.NewOperationInvoker()).InvokeBinding(context.Background(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{Format: FormatToken, Location: filepath.Join(t.TempDir(), "missing.json")},
		Ref:    "#/graphs/g",
	})
	if inv == nil {
		t.Fatal("expected a handle")
	}
	_, err := openbindings.Single[any](context.Background(), inv.Outputs())
	if err == nil {
		t.Fatal("expected the preflight failure as a terminal error")
	}
	ierr := openbindings.AsInvocationError(err)
	if ierr == nil || ierr.Code != openbindings.ErrCodeSourceLoadFailed {
		t.Fatalf("want %s through the handle, got %v", openbindings.ErrCodeSourceLoadFailed, err)
	}
}

// TestNewInvokerWithClient verifies that a location-fed graph document is
// fetched through the injected client (and thus under the invocation ctx).
// The graph is a bare input->output pass-through, so the engine needs no
// operations and the caller's one write flows straight to the output.
func TestNewInvokerWithClient(t *testing.T) {
	graphDoc := `{"graphs":{"g":{
		"openbindings.operation-graph":"0.2.0",
		"nodes":{"in":{"type":"input"},"out":{"type":"output"}},
		"edges":[{"from":"in","to":"out"}]
	}}}`
	var requestCount int
	custom := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		requestCount++
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(graphDoc)),
			Request:    req,
		}, nil
	})}

	ctx := context.Background()
	inv := NewInvokerWithClient(openbindings.NewOperationInvoker(), custom).InvokeBinding(ctx, &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{Format: FormatToken, Location: "http://example.test/graph.json"},
		Ref:    "#/graphs/g",
	})
	if err := inv.Write(ctx, map[string]any{"n": 1}); err != nil {
		t.Fatal(err)
	}
	_ = inv.Close()
	out, err := openbindings.Single[any](ctx, inv.Outputs())
	if err != nil {
		t.Fatalf("Single: %v", err)
	}
	if requestCount != 1 {
		t.Errorf("expected custom transport to be called exactly once, got %d", requestCount)
	}
	// The engine passes the written value through as-is (no JSON round-trip),
	// so the int survives.
	if m, ok := out.(map[string]any); !ok || m["n"] != 1 {
		t.Errorf("unexpected pass-through output: %#v", out)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
