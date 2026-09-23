package openapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/invoke"
	"github.com/openbindings/openbindings-go/jsonvalue"
)

func preflightDocument(server string) string {
	return fmt.Sprintf(`{"openapi":"3.1.2","info":{"title":"Preflight","version":"1"},"servers":[{"url":%q}],"paths":{"/run":{"get":{"responses":{"200":{"description":"ok","content":{"application/json":{"schema":{"type":"object"}}}}}}}}}`, server)
}

func preflightInterface(source openbindings.Source) *openbindings.Interface {
	return &openbindings.Interface{
		OpenBindings: "0.2.0",
		Operations:   map[string]openbindings.Operation{"run": {}},
		Sources:      map[string]openbindings.Source{"service": source},
		Bindings: map[string]openbindings.BindingEntry{
			"run.service": {Operation: "run", Source: "service", Selector: openbindings.Present("#/paths/~1run/get")},
		},
	}
}

func TestPreflightRequiredFailuresSurface(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "description unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	for _, tc := range []struct {
		name, content, selector, code string
	}{
		{"retrieval", "", "#/paths/~1run/get", invoke.ErrCodeSourceLoadFailed},
		{"invalid document", "{", "#/paths/~1run/get", invoke.ErrCodeSourceLoadFailed},
		{"unknown selector", preflightDocument(server.URL), "#/paths/~1missing/get", invoke.ErrCodeSelectorNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := invoke.InvocationSource{BindingSpec: BindingSpecOpenAPI31, Location: server.URL + "/description"}
			if tc.content != "" {
				source.Content = jsonvalue.TextContent(tc.content)
			}
			details, err := NewInvoker().PreflightBinding(t.Context(), &invoke.BindingInvocationArgs{Source: source, Selector: openbindings.Present(tc.selector)})
			var invocationErr *invoke.InvocationError
			if details != nil || !errors.As(err, &invocationErr) || invocationErr.Code != tc.code {
				t.Fatalf("prepare = (%v, %v), want %s", details, err, tc.code)
			}
		})
	}
	if requests.Load() != 1 {
		t.Fatalf("expected only the attempted description retrieval, got %d requests", requests.Load())
	}
}

func TestPreflightReusesEmbeddedSourceWithoutExecuting(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer server.Close()
	source := openbindings.Source{BindingSpec: BindingSpecOpenAPI31, Content: jsonvalue.TextContent(preflightDocument(server.URL))}
	iface := preflightInterface(source)
	adapter := NewAdapter()
	invoker := invoke.NewOperationInvoker(adapter)
	args := &invoke.BindingInvocationArgs{Source: invoke.InvocationSource{BindingSpec: source.BindingSpec, Content: source.Content}}
	screenCtx, cancelScreen := context.WithCancel(t.Context())
	for i := 0; i < 3; i++ {
		if details, err := invoker.PreflightOperation(screenCtx, iface, "run"); details != nil || err != nil {
			t.Fatalf("early prepare = (%v, %v)", details, err)
		}
	}
	prepared, ok := adapter.invoker.runtime.cachedNativeClient(args)
	if !ok || requests.Load() != 0 {
		t.Fatalf("preflight failed to retain analysis or executed the operation: retained=%v requests=%d", ok, requests.Load())
	}
	cancelScreen() // Completed reusable analysis does not belong to this context.
	call := invoke.Invoke(t.Context(), invoker, iface, invoke.NewOperationSignature[any, any]("run"))
	defer call.Cancel()
	_ = call.Close()
	if _, err := invoke.Single(t.Context(), call.Outputs()); err != nil {
		t.Fatal(err)
	}
	after, ok := adapter.invoker.runtime.cachedNativeClient(args)
	if !ok || after != prepared || requests.Load() != 1 {
		t.Fatalf("invocation did not reuse prepared analysis: retained=%v same=%v requests=%d", ok, after == prepared, requests.Load())
	}
	// Losing an optional cached analysis must leave normal invocation usable.
	adapter.invoker.runtime.nativeClientsMu.Lock()
	clear(adapter.invoker.runtime.nativeClients)
	adapter.invoker.runtime.nativeClientOrder = nil
	adapter.invoker.runtime.nativeClientsMu.Unlock()
	later := invoke.Invoke(t.Context(), invoker, iface, invoke.NewOperationSignature[any, any]("run"))
	defer later.Cancel()
	_ = later.Close()
	if _, err := invoke.Single(t.Context(), later.Outputs()); err != nil {
		t.Fatalf("optional analysis cache became a prerequisite: %v", err)
	}
	if requests.Load() != 2 {
		t.Fatalf("operation requests=%d, want 2", requests.Load())
	}
}

func TestPreflightCancellationDoesNotCancelConcurrentInvocation(t *testing.T) {
	var documents, operations atomic.Int64
	entered := make(chan struct{})
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/description" {
			if documents.Add(1) == 1 {
				close(entered)
				<-r.Context().Done()
				return
			}
			_, _ = io.WriteString(w, preflightDocument(server.URL))
			return
		}
		operations.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer server.Close()
	iface := preflightInterface(openbindings.Source{BindingSpec: BindingSpecOpenAPI31, Location: openbindings.Present(server.URL + "/description")})
	invoker := invoke.NewOperationInvoker(NewAdapter())
	screenCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		_, err := invoker.PreflightOperation(screenCtx, iface, "run")
		finished <- err
	}()
	<-entered
	call := invoke.Invoke(t.Context(), invoker, iface, invoke.NewOperationSignature[any, any]("run"))
	defer call.Cancel()
	_ = call.Close()
	if _, err := invoke.Single(t.Context(), call.Outputs()); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled preflight = %v", err)
	}
	if operations.Load() != 1 || documents.Load() != 3 {
		t.Fatalf("operation requests=%d description loads=%d; expected one execution and three uncached loads", operations.Load(), documents.Load())
	}
}

func TestPreflightConcurrentContextIsolation(t *testing.T) {
	adapter := NewAdapter()
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1) }))
	defer server.Close()
	// A mutable credential supplied to one call must not satisfy another call.
	artifact, err := json.Marshal(makeOpenAPISpec(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(supplied bool) {
			defer wg.Done()
			args := &invoke.BindingInvocationArgs{
				Source:   invoke.InvocationSource{BindingSpec: BindingSpec, Content: jsonvalue.TextContent(string(artifact))},
				Selector: openbindings.Present("#/paths/~1items/get"),
			}
			if supplied {
				args.Context = map[string]any{"bearerToken": secret}
			}
			details, err := adapter.PreflightBinding(t.Context(), args)
			if err != nil || (details == nil) != supplied {
				t.Errorf("supplied=%v prepare=(%v, %v)", supplied, details, err)
			}
		}(i%2 == 0)
	}
	wg.Wait()
	if calls.Load() != 0 {
		t.Fatal("preflight executed an operation")
	}
}

// BenchmarkOperationPreflight compares the same fresh adapter and embedded
// source, with work either at invocation time or moved before the simulated
// click. The native-setup control deliberately accesses the adapter's private
// loader to measure equivalent reuse without inventing another public API.
func BenchmarkOperationPreflight(b *testing.B) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer server.Close()
	source := openbindings.Source{BindingSpec: BindingSpecOpenAPI31, Content: jsonvalue.TextContent(preflightDocument(server.URL))}
	iface := preflightInterface(source)
	for _, mode := range []string{"cold", "early-client-setup", "early-operation-preflight"} {
		b.Run(mode, func(b *testing.B) {
			var preflighting, invoking time.Duration
			startRequests := requests.Load()
			for i := 0; i < b.N; i++ {
				adapter := NewAdapter()
				invoker := invoke.NewOperationInvoker(adapter)
				start := time.Now()
				switch mode {
				case "early-client-setup":
					_, err := adapter.invoker.runtime.loadNativeClient(b.Context(), &invoke.BindingInvocationArgs{
						Source: invoke.InvocationSource{BindingSpec: source.BindingSpec, Content: source.Content},
					})
					if err != nil {
						b.Fatal(err)
					}
				case "early-operation-preflight":
					if details, err := invoker.PreflightOperation(b.Context(), iface, "run"); err != nil || details != nil {
						b.Fatalf("prepare=(%v, %v)", details, err)
					}
				}
				click := time.Now()
				preflighting += click.Sub(start)
				call := invoke.Invoke(b.Context(), invoker, iface, invoke.NewOperationSignature[any, any]("run"))
				_ = call.Close()
				_, err := invoke.Single(b.Context(), call.Outputs())
				call.Cancel()
				if err != nil {
					b.Fatal(err)
				}
				invoking += time.Since(click)
			}
			b.ReportMetric(float64(preflighting.Nanoseconds())/float64(b.N), "preflight-ns/op")
			b.ReportMetric(float64(invoking.Nanoseconds())/float64(b.N), "click-ns/op")
			b.ReportMetric(float64((preflighting+invoking).Nanoseconds())/float64(b.N), "total-ns/op")
			b.ReportMetric(float64(requests.Load()-startRequests)/float64(b.N), "operation-requests/op")
		})
	}
}
