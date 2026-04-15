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
		iface.Operations[op] = Operation{}
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

// ---------------------------------------------------------------------------
// Discovery mode
// ---------------------------------------------------------------------------

func TestInterfaceClient_DiscoveryMode_NilInterface(t *testing.T) {
	provided := makeTestInterface("test-service", "getStatus", "search")
	srv := serveOBI(t, provided)
	defer srv.Close()

	exec := NewOperationExecutor()
	client := NewInterfaceClient(nil, exec)

	if client.Interface() != nil {
		t.Fatal("expected nil interface in discovery mode")
	}
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
	if len(client.Issues()) != 0 {
		t.Fatalf("expected no issues, got %+v", client.Issues())
	}
}

func TestInterfaceClient_DiscoveryMode_NewUnboundClient(t *testing.T) {
	provided := makeTestInterface("test-service", "getStatus")
	srv := serveOBI(t, provided)
	defer srv.Close()

	exec := NewOperationExecutor()
	client := NewUnboundClient(exec)

	if client.Interface() != nil {
		t.Fatal("expected nil interface from NewUnboundClient")
	}

	err := client.Resolve(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client.State() != StateBound {
		t.Fatalf("expected bound, got %s", client.State())
	}
}

func TestInterfaceClient_DiscoveryMode_ResolveInterface(t *testing.T) {
	provided := makeTestInterface("test-service", "getStatus", "search")
	exec := NewOperationExecutor()
	client := NewInterfaceClient(nil, exec)

	client.ResolveInterface(provided)
	if client.State() != StateBound {
		t.Fatalf("expected bound, got %s", client.State())
	}
}

// ---------------------------------------------------------------------------
// Conforms()
// ---------------------------------------------------------------------------

func TestInterfaceClient_Conforms_PassWhenCompatible(t *testing.T) {
	provided := &Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"listWorkspaces": {Output: JSONSchema{"type": "object"}},
			"getWorkspace":   {Output: JSONSchema{"type": "object"}},
			"search":         {},
		},
	}

	exec := NewOperationExecutor()
	client := NewInterfaceClient(nil, exec)
	client.ResolveInterface(provided)

	required := makeTestInterface("ws-manager", "listWorkspaces", "getWorkspace")
	issues, err := client.Conforms(required)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("expected no issues, got %d: %+v", len(issues), issues)
	}
}

func TestInterfaceClient_Conforms_FailWhenMissing(t *testing.T) {
	provided := makeTestInterface("test-service", "search")
	exec := NewOperationExecutor()
	client := NewInterfaceClient(nil, exec)
	client.ResolveInterface(provided)

	required := makeTestInterface("ws-manager", "listWorkspaces", "getWorkspace")
	issues, err := client.Conforms(required)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("expected 2 issues, got %d: %+v", len(issues), issues)
	}
}

func TestInterfaceClient_Conforms_WithSatisfiesMatch(t *testing.T) {
	provided := &Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"myList": {
				Satisfies: []Satisfies{
					{Role: "openbindings.workspace-manager", Operation: "listWorkspaces"},
				},
			},
		},
	}

	exec := NewOperationExecutor()
	client := NewInterfaceClient(nil, exec)
	client.ResolveInterface(provided)

	required := makeTestInterface("ws-iface", "listWorkspaces")
	issues, err := client.Conforms(required, "openbindings.workspace-manager")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("expected no issues with satisfies match, got %d: %+v", len(issues), issues)
	}
}

func TestInterfaceClient_Conforms_ErrorBeforeResolution(t *testing.T) {
	exec := NewOperationExecutor()
	client := NewInterfaceClient(nil, exec)

	required := makeTestInterface("ws-manager", "listWorkspaces")
	_, err := client.Conforms(required)
	if err == nil {
		t.Fatal("expected error when calling Conforms before resolution")
	}
}

func TestInterfaceClient_Conforms_WorksInDemandMode(t *testing.T) {
	provided := &Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"getInfo": {Output: JSONSchema{"type": "object"}},
			"search":  {},
		},
	}

	required := makeTestInterface("narrow", "getInfo")
	exec := NewOperationExecutor()
	client := NewInterfaceClient(required, exec)
	client.ResolveInterface(provided)

	if client.State() != StateBound {
		t.Fatalf("expected bound, got %s", client.State())
	}

	searchIface := makeTestInterface("searcher", "search")
	issues, err := client.Conforms(searchIface)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("expected search to conform, got %d: %+v", len(issues), issues)
	}

	exoticIface := makeTestInterface("exotic", "doSomethingExotic")
	issues, err = client.Conforms(exoticIface)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}
}

// ---------------------------------------------------------------------------
// WithInterfaceID — satisfies matching during resolution
// ---------------------------------------------------------------------------

func TestInterfaceClient_WithInterfaceID_SatisfiesResolution(t *testing.T) {
	provided := &Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"myList": {
				Satisfies: []Satisfies{
					{Role: "openbindings.workspace-manager", Operation: "listWorkspaces"},
				},
			},
		},
	}
	srv := serveOBI(t, provided)
	defer srv.Close()

	required := makeTestInterface("ws-manager", "listWorkspaces")
	exec := NewOperationExecutor()
	client := NewInterfaceClient(required, exec, WithInterfaceID("openbindings.workspace-manager"))

	err := client.Resolve(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client.State() != StateBound {
		t.Fatalf("expected bound via satisfies, got %s", client.State())
	}
}

func TestInterfaceClient_WithoutInterfaceID_SatisfiesFails(t *testing.T) {
	provided := &Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"myList": {
				Satisfies: []Satisfies{
					{Role: "openbindings.workspace-manager", Operation: "listWorkspaces"},
				},
			},
		},
	}
	srv := serveOBI(t, provided)
	defer srv.Close()

	required := makeTestInterface("ws-manager", "listWorkspaces")
	exec := NewOperationExecutor()
	client := NewInterfaceClient(required, exec)

	err := client.Resolve(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client.State() != StateIncompatible {
		t.Fatalf("expected incompatible without interface ID, got %s", client.State())
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
