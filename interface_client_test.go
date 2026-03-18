package openbindings

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func makeTestInterface(name string, ops ...string) *Interface {
	iface := &Interface{
		OpenBindings: "0.1.0",
		Name:         name,
		Operations:   map[string]Operation{},
	}
	for _, op := range ops {
		iface.Operations[op] = Operation{Kind: OperationKindMethod}
	}
	return iface
}

func serveOBI(t *testing.T, iface *Interface) *httptest.Server {
	t.Helper()
	data, err := json.Marshal(iface)
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	}))
}

func TestInterfaceClient_ResolveDirectOBI(t *testing.T) {
	provided := makeTestInterface("test-service", "getStatus")
	srv := serveOBI(t, provided)
	defer srv.Close()

	required := makeTestInterface("my-component", "getStatus")
	exec := NewOperationExecutor()
	client := NewInterfaceClient(required, exec)

	if client.State() != StateIdle {
		t.Fatalf("expected idle, got %s", client.State())
	}

	err := client.Resolve(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if client.State() != StateBound {
		t.Fatalf("expected bound, got %s", client.State())
	}
	if client.Resolved() == nil {
		t.Fatal("resolved should not be nil")
	}
	if client.Synthesized() {
		t.Fatal("should not be synthesized when fetched directly")
	}
	if len(client.Issues()) != 0 {
		t.Fatalf("expected no issues, got %+v", client.Issues())
	}
}

func TestInterfaceClient_WellKnownDiscovery(t *testing.T) {
	provided := makeTestInterface("test-service", "getStatus")
	data, _ := json.Marshal(provided)

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openbindings", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	required := makeTestInterface("my-component", "getStatus")
	exec := NewOperationExecutor()
	client := NewInterfaceClient(required, exec)

	err := client.Resolve(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client.State() != StateBound {
		t.Fatalf("expected bound via well-known, got %s", client.State())
	}
	if client.Synthesized() {
		t.Fatal("well-known discovery should not be marked synthesized")
	}
}

func TestInterfaceClient_IncompatibleInterface(t *testing.T) {
	provided := makeTestInterface("test-service", "somethingElse")
	srv := serveOBI(t, provided)
	defer srv.Close()

	required := makeTestInterface("my-component", "getStatus")
	exec := NewOperationExecutor()
	client := NewInterfaceClient(required, exec)

	err := client.Resolve(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if client.State() != StateIncompatible {
		t.Fatalf("expected incompatible, got %s", client.State())
	}
	if len(client.Issues()) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(client.Issues()))
	}
	if client.Issues()[0].Kind != CompatibilityMissing {
		t.Fatalf("expected missing, got %s", client.Issues()[0].Kind)
	}
}

func TestInterfaceClient_ResolveInterface(t *testing.T) {
	required := makeTestInterface("my-component", "getStatus")
	provided := makeTestInterface("test-service", "getStatus")

	exec := NewOperationExecutor()
	client := NewInterfaceClient(required, exec)

	client.ResolveInterface(provided)

	if client.State() != StateBound {
		t.Fatalf("expected bound, got %s", client.State())
	}
	if client.Synthesized() {
		t.Fatal("direct resolve should not be synthesized")
	}
}

func TestInterfaceClient_Refresh(t *testing.T) {
	provided := makeTestInterface("test-service", "getStatus")
	srv := serveOBI(t, provided)
	defer srv.Close()

	required := makeTestInterface("my-component", "getStatus")
	exec := NewOperationExecutor()
	client := NewInterfaceClient(required, exec)

	err := client.Resolve(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("unexpected resolve: %v", err)
	}
	if client.State() != StateBound {
		t.Fatalf("expected bound, got %s", client.State())
	}

	err = client.Refresh(context.Background())
	if err != nil {
		t.Fatalf("unexpected refresh error: %v", err)
	}
	if client.State() != StateBound {
		t.Fatalf("expected bound after refresh, got %s", client.State())
	}
}

func TestInterfaceClient_RefreshNoTarget(t *testing.T) {
	required := makeTestInterface("my-component", "getStatus")
	exec := NewOperationExecutor()
	client := NewInterfaceClient(required, exec)

	err := client.Refresh(context.Background())
	if err != nil {
		t.Fatalf("refresh with no target should be a no-op, got: %v", err)
	}
	if client.State() != StateIdle {
		t.Fatalf("expected idle, got %s", client.State())
	}
}

func TestInterfaceClient_ResolveEmpty(t *testing.T) {
	required := makeTestInterface("my-component", "getStatus")
	exec := NewOperationExecutor()
	client := NewInterfaceClient(required, exec)

	err := client.Resolve(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client.State() != StateIdle {
		t.Fatalf("expected idle after empty resolve, got %s", client.State())
	}
}

func TestInterfaceClient_CancelledContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	required := makeTestInterface("my-component", "getStatus")
	exec := NewOperationExecutor()
	client := NewInterfaceClient(required, exec)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := client.Resolve(ctx, srv.URL)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if client.State() != StateError {
		t.Fatalf("expected error state, got %s", client.State())
	}
	if client.ErrorMessage() == "" {
		t.Fatal("expected error message")
	}
}

func TestInterfaceClient_ExecuteWithoutBind(t *testing.T) {
	required := makeTestInterface("my-component", "getStatus")
	exec := NewOperationExecutor()
	client := NewInterfaceClient(required, exec)

	_, err := client.Execute(context.Background(), "getStatus", nil)
	if err == nil {
		t.Fatal("expected error when not bound")
	}
}

func TestInterfaceClient_SkipWellKnownForSpecURLs(t *testing.T) {
	if !shouldSkipWellKnownDiscovery("https://example.com/openapi.json") {
		t.Fatal("should skip .json URLs")
	}
	if !shouldSkipWellKnownDiscovery("https://example.com/v2/swagger") {
		t.Fatal("should skip swagger paths")
	}
	if !shouldSkipWellKnownDiscovery("https://example.com/.well-known/openbindings") {
		t.Fatal("should skip well-known path itself")
	}
	if shouldSkipWellKnownDiscovery("https://example.com") {
		t.Fatal("should not skip bare domain")
	}
	if shouldSkipWellKnownDiscovery("https://example.com/api/v2") {
		t.Fatal("should not skip generic API paths")
	}
}
