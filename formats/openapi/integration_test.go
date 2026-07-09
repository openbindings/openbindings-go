package openapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	openbindings "github.com/openbindings/openbindings-go"
)

const secret = "test-token-123"

// testStore is a minimal in-memory ContextStore for exercising the
// store-backed resolver. The SDK no longer ships a built-in store; consuming
// tools own storage, so tests supply their own.
type testStore map[string]map[string]any

func (s testStore) Get(_ context.Context, key string) (map[string]any, error) {
	return s[key], nil
}

func (s testStore) Set(_ context.Context, key string, value map[string]any) error {
	if value == nil {
		delete(s, key)
		return nil
	}
	s[key] = value
	return nil
}

func (s testStore) Delete(_ context.Context, key string) error {
	delete(s, key)
	return nil
}

// ---------------------------------------------------------------------------
// Handle-driving helpers
// ---------------------------------------------------------------------------

// driveOutputs writes input (when non-nil), closes the input side, and
// collects every output until clean close or a terminal error. The terminal
// *InvocationError (or nil) is returned alongside the outputs received before
// it.
func driveOutputs(ctx context.Context, call openbindings.Invocation[any, any], input any) ([]any, *openbindings.InvocationError) {
	if input != nil {
		_ = call.Write(ctx, input)
	}
	_ = call.Close()
	out := call.Outputs()
	var vals []any
	for {
		v, err := out.Read(ctx)
		if errors.Is(err, io.EOF) {
			return vals, nil
		}
		if err != nil {
			var ie *openbindings.InvocationError
			errors.As(err, &ie)
			return vals, ie
		}
		vals = append(vals, v)
	}
}

// driveSingle is the canonical unary pattern (write, close, Single): unlike
// draining, Single asserts exactly-one, so a zero- or multi-output unary
// invocation fails the test instead of passing silently.
func driveSingle(t *testing.T, call openbindings.Invocation[any, any], input any) (any, *openbindings.InvocationError) {
	t.Helper()
	ctx := context.Background()
	if input != nil {
		_ = call.Write(ctx, input)
	}
	_ = call.Close()
	out, err := openbindings.Single(ctx, call.Outputs())
	if err != nil {
		var ie *openbindings.InvocationError
		if !errors.As(err, &ie) {
			t.Fatalf("expected *InvocationError, got %T: %v", err, err)
		}
		return nil, ie
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Multipart/form-data integration test
// ---------------------------------------------------------------------------

func TestIntegration_MultipartFormData(t *testing.T) {
	var receivedFile []byte
	var receivedDesc string
	var receivedContentType string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/upload" && r.Method == "POST" {
			receivedContentType = r.Header.Get("Content-Type")
			if err := r.ParseMultipartForm(10 << 20); err != nil {
				w.WriteHeader(400)
				json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
				return
			}
			receivedDesc = r.FormValue("description")
			file, _, err := r.FormFile("file")
			if err != nil {
				w.WriteHeader(400)
				json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
				return
			}
			defer file.Close()
			receivedFile, _ = io.ReadAll(file)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	spec := map[string]any{
		"openapi": "3.0.3",
		"info":    map[string]any{"title": "Upload API", "version": "1.0.0"},
		"servers": []map[string]any{{"url": srv.URL}},
		"paths": map[string]any{
			"/upload": map[string]any{
				"post": map[string]any{
					"operationId": "uploadFile",
					"requestBody": map[string]any{
						"required": true,
						"content": map[string]any{
							"multipart/form-data": map[string]any{
								"schema": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"file": map[string]any{
											"type":   "string",
											"format": "binary",
										},
										"description": map[string]any{
											"type": "string",
										},
									},
								},
							},
						},
					},
					"responses": map[string]any{
						"200": map[string]any{
							"description": "OK",
							"content": map[string]any{
								"application/json": map[string]any{
									"schema": map[string]any{
										"type": "object",
										"properties": map[string]any{
											"status": map[string]any{"type": "string"},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	binv := NewInvoker()
	ctx := context.Background()
	specBytes, _ := json.Marshal(spec)

	call := binv.InvokeBinding(ctx, &openbindings.BindingInvocationArgs{
		Ref:    "#/paths/~1upload/post",
		Source: openbindings.InvocationSource{Format: FormatToken, Content: string(specBytes)},
	})
	_, ierr := driveSingle(t, call, map[string]any{
		"file":        []byte("binary-content-here"),
		"description": "my upload",
	})
	if ierr != nil {
		t.Fatalf("unexpected error: %s: %s", ierr.Code, ierr.Message)
	}

	if !strings.Contains(receivedContentType, "multipart/form-data") {
		t.Errorf("server received Content-Type = %q, want multipart/form-data", receivedContentType)
	}
	if string(receivedFile) != "binary-content-here" {
		t.Errorf("server received file = %q, want %q", string(receivedFile), "binary-content-here")
	}
	if receivedDesc != "my upload" {
		t.Errorf("server received description = %q, want %q", receivedDesc, "my upload")
	}
}

var items = []map[string]any{
	{"id": float64(1), "name": "Alpha"},
	{"id": float64(2), "name": "Bravo"},
}

func makeOpenAPISpec(baseURL string) map[string]any {
	return map[string]any{
		"openapi": "3.0.3",
		"info":    map[string]any{"title": "Test API", "version": "1.0.0"},
		"servers": []map[string]any{{"url": baseURL}},
		"paths": map[string]any{
			"/items": map[string]any{
				"get": map[string]any{
					"operationId": "listItems",
					"summary":     "List all items",
					"security":    []map[string]any{{"bearerAuth": []any{}}},
					"responses": map[string]any{
						"200": map[string]any{
							"description": "OK",
							"content": map[string]any{
								"application/json": map[string]any{
									"schema": map[string]any{
										"type": "array",
										"items": map[string]any{
											"type": "object",
											"properties": map[string]any{
												"id":   map[string]any{"type": "integer"},
												"name": map[string]any{"type": "string"},
											},
										},
									},
								},
							},
						},
					},
				},
			},
			"/items/{id}": map[string]any{
				"get": map[string]any{
					"operationId": "getItem",
					"summary":     "Get a single item",
					"security":    []map[string]any{{"bearerAuth": []any{}}},
					"parameters": []map[string]any{
						{"name": "id", "in": "path", "required": true, "schema": map[string]any{"type": "integer"}},
					},
					"responses": map[string]any{
						"200": map[string]any{
							"description": "OK",
							"content": map[string]any{
								"application/json": map[string]any{
									"schema": map[string]any{
										"type": "object",
										"properties": map[string]any{
											"id":   map[string]any{"type": "integer"},
											"name": map[string]any{"type": "string"},
										},
									},
								},
							},
						},
					},
				},
			},
		},
		"components": map[string]any{
			"securitySchemes": map[string]any{
				"bearerAuth": map[string]any{"type": "http", "scheme": "bearer"},
			},
		},
	}
}

var itemIDPattern = regexp.MustCompile(`^/items/(\d+)$`)

func testHandler(baseURL string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/openapi.json" && r.Method == "GET" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(makeOpenAPISpec(baseURL))
			return
		}

		if r.URL.Path == "/items" && r.Method == "GET" {
			if r.Header.Get("Authorization") != "Bearer "+secret {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(401)
				json.NewEncoder(w).Encode(map[string]any{"error": "unauthorized"})
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(items)
			return
		}

		matches := itemIDPattern.FindStringSubmatch(r.URL.Path)
		if matches != nil && r.Method == "GET" {
			if r.Header.Get("Authorization") != "Bearer "+secret {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(401)
				json.NewEncoder(w).Encode(map[string]any{"error": "unauthorized"})
				return
			}
			for _, item := range items {
				if fmt.Sprintf("%v", item["id"]) == matches[1] {
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(item)
					return
				}
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(404)
			json.NewEncoder(w).Encode(map[string]any{"error": "not found"})
			return
		}

		w.WriteHeader(404)
	}
}

func setupServer() (*httptest.Server, string) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		testHandler(srv.URL)(w, r)
	}))
	specURL := srv.URL + "/openapi.json"
	return srv, specURL
}

// synthesizeOBI creates an OBI from the OpenAPI spec served at specURL.
func synthesizeOBI(t *testing.T, specURL string) *openbindings.Interface {
	t.Helper()
	synthesizer := NewSynthesizer()
	ctx := context.Background()
	iface, err := synthesizer.SynthesizeInterface(ctx, &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{
			{Format: FormatToken, Location: specURL},
		},
	})
	if err != nil {
		t.Fatalf("synthesizeOBI failed: %v", err)
	}
	return iface
}

// TestIntegration_NoCredentialsChallenge verifies that an operation declaring
// a security requirement, invoked with no resolvable credentials, terminates
// with CONTEXT_REQUIRED — raised before any network dispatch — rather than a
// server-side 401.
func TestIntegration_NoCredentialsChallenge(t *testing.T) {
	srv, specURL := setupServer()
	defer srv.Close()

	store := testStore{}
	binv := NewInvoker()
	invoker := openbindings.NewOperationInvoker(binv).WithRuntime(openbindings.StoreContextResolver(store))

	iface := synthesizeOBI(t, specURL)

	call := openbindings.Invoke(context.Background(), invoker, iface,
		openbindings.NewOperationSignature[any, any]("listItems"))
	_, ierr := driveSingle(t, call, nil)
	if ierr == nil {
		t.Fatal("expected a terminal error, got success")
	}
	if ierr.Code != openbindings.ErrCodeContextRequired {
		t.Errorf("error code = %q, want %q", ierr.Code, openbindings.ErrCodeContextRequired)
	}
}

func TestIntegration_PreStoredCredentialsSucceed(t *testing.T) {
	srv, specURL := setupServer()
	defer srv.Close()

	store := testStore{}
	ctx := context.Background()

	// Pre-store credentials under the normalized server key.
	contextKey := openbindings.NormalizeContextKey(srv.URL)
	if err := store.Set(ctx, contextKey, map[string]any{"bearerToken": secret}); err != nil {
		t.Fatalf("store.Set failed: %v", err)
	}

	binv := NewInvoker()
	invoker := openbindings.NewOperationInvoker(binv).WithRuntime(openbindings.StoreContextResolver(store))
	iface := synthesizeOBI(t, specURL)

	// First call: listItems should succeed via resolve-and-retry.
	call := openbindings.Invoke(ctx, invoker, iface, openbindings.NewOperationSignature[any, any]("listItems"))
	out, ierr := driveSingle(t, call, nil)
	if ierr != nil {
		t.Fatalf("unexpected error: %s: %s", ierr.Code, ierr.Message)
	}
	data, _ := json.Marshal(out)
	var got []map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("failed to unmarshal response as array: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("got %d items, want 2", len(got))
	}

	// Second call reuses the credential (now via preflight against the warm doc cache).
	call2 := openbindings.Invoke(ctx, invoker, iface, openbindings.NewOperationSignature[any, any]("listItems"))
	if _, ierr := driveSingle(t, call2, nil); ierr != nil {
		t.Fatalf("second listItems call should succeed: %v", ierr)
	}

	// Different operation: getItem with a path parameter.
	call3 := openbindings.Invoke(ctx, invoker, iface,
		openbindings.NewOperationSignature[any, any]("getItem"))
	item3, ierr := driveSingle(t, call3, map[string]any{"id": 1})
	if ierr != nil {
		t.Fatalf("getItem unexpected error: %s: %s", ierr.Code, ierr.Message)
	}
	itemData, _ := json.Marshal(item3)
	var item map[string]any
	if err := json.Unmarshal(itemData, &item); err != nil {
		t.Fatalf("failed to unmarshal getItem response: %v", err)
	}
	if item["name"] != "Alpha" {
		t.Errorf("item name = %v, want %q", item["name"], "Alpha")
	}
}

func TestIntegration_IsolatedStoresDontShareCredentials(t *testing.T) {
	srv, specURL := setupServer()
	defer srv.Close()

	ctx := context.Background()
	store1 := testStore{}
	store2 := testStore{}

	contextKey := openbindings.NormalizeContextKey(srv.URL)
	if err := store1.Set(ctx, contextKey, map[string]any{"bearerToken": secret}); err != nil {
		t.Fatalf("store1.Set failed: %v", err)
	}

	iface := synthesizeOBI(t, specURL)

	opExec1 := openbindings.NewOperationInvoker(NewInvoker()).WithRuntime(openbindings.StoreContextResolver(store1))
	opExec2 := openbindings.NewOperationInvoker(NewInvoker()).WithRuntime(openbindings.StoreContextResolver(store2))

	// Invoker 1 succeeds (has credentials).
	call1 := openbindings.Invoke(ctx, opExec1, iface, openbindings.NewOperationSignature[any, any]("listItems"))
	if _, ierr := driveSingle(t, call1, nil); ierr != nil {
		t.Fatalf("opExec1 should succeed with stored credentials: %v", ierr)
	}

	// Invoker 2 gets CONTEXT_REQUIRED (no credentials, declined by the empty store).
	call2 := openbindings.Invoke(ctx, opExec2, iface, openbindings.NewOperationSignature[any, any]("listItems"))
	_, ierr := driveSingle(t, call2, nil)
	if ierr == nil || ierr.Code != openbindings.ErrCodeContextRequired {
		t.Fatalf("client2 should fail with CONTEXT_REQUIRED, got %v", ierr)
	}
}

// countingServer wraps a handler with a request counter for zero-I/O
// assertions.
func countingServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &requests
}

// widgetSpec declares ops with required inputs and a cookie param, without
// security (so input handling is exercised, not the CONTEXT_REQUIRED gate).
func widgetSpec(baseURL string) string {
	spec := map[string]any{
		"openapi": "3.0.3",
		"info":    map[string]any{"title": "Widget API", "version": "1.0.0"},
		"servers": []map[string]any{{"url": baseURL}},
		"paths": map[string]any{
			"/widgets/{id}": map[string]any{
				"get": map[string]any{
					"operationId": "getWidget",
					"parameters": []map[string]any{
						{"name": "id", "in": "path", "required": true, "schema": map[string]any{"type": "integer"}},
					},
					"responses": map[string]any{"200": map[string]any{"description": "OK"}},
				},
			},
			"/widgets": map[string]any{
				"post": map[string]any{
					"operationId": "createWidget",
					"requestBody": map[string]any{
						"required": true,
						"content": map[string]any{
							"application/json": map[string]any{"schema": map[string]any{"type": "object"}},
						},
					},
					"responses": map[string]any{"201": map[string]any{"description": "Created"}},
				},
			},
			"/session": map[string]any{
				"get": map[string]any{
					"operationId": "getSession",
					"parameters": []map[string]any{
						{"name": "session_id", "in": "cookie", "schema": map[string]any{"type": "string"}},
					},
					"responses": map[string]any{"200": map[string]any{"description": "OK"}},
				},
			},
		},
	}
	b, _ := json.Marshal(spec)
	return string(b)
}

// TestIntegration_MissingRequiredInput_NoDispatch verifies cross-SDK parity:
// a bare input close on an operation with a required parameter (or required
// requestBody) fires ERR_MISSING_INPUT BEFORE dispatch — the server sees
// zero requests.
func TestIntegration_MissingRequiredInput_NoDispatch(t *testing.T) {
	srv, requests := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})
	source := openbindings.InvocationSource{Format: FormatToken, Content: widgetSpec(srv.URL)}
	binv := NewInvoker()

	cases := []struct {
		name string
		ref  string
	}{
		{"required path parameter", "#/paths/~1widgets~1{id}/get"},
		{"required request body", "#/paths/~1widgets/post"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := requests.Load()
			call := binv.InvokeBinding(context.Background(), &openbindings.BindingInvocationArgs{
				Source: source,
				Ref:    tc.ref,
			})
			// Bare close: no input written.
			_, ierr := driveOutputs(context.Background(), call, nil)
			if ierr == nil || ierr.Code != openbindings.ErrCodeMissingInput {
				t.Fatalf("expected ERR_MISSING_INPUT, got %v", ierr)
			}
			if got := requests.Load(); got != before {
				t.Errorf("missing input must fire before dispatch: %d requests hit the server", got-before)
			}
		})
	}
}

// TestIntegration_NoInputOperationConvention verifies the operation-layer
// no-input convention: a call carrying Binding != nil and InputSchema == nil
// dispatches with empty input without waiting for a write — even when the
// OpenAPI doc declares params (here a cookie-only param the OBI did not
// express). The caller never writes nor closes.
func TestIntegration_NoInputOperationConvention(t *testing.T) {
	srv, requests := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	binv := NewInvoker()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	call := binv.InvokeBinding(ctx, &openbindings.BindingInvocationArgs{
		Source:  openbindings.InvocationSource{Format: FormatToken, Content: widgetSpec(srv.URL)},
		Ref:     "#/paths/~1session/get",
		Binding: &openbindings.BindingEntry{Operation: "getSession", Source: "api", Ref: "#/paths/~1session/get"},
		// InputSchema nil → no-input operation; the binding closes input itself.
	})
	// No Write, no Close: read directly (bounded by ctx so a parked binding
	// fails the test instead of hanging it).
	out, err := openbindings.Single(ctx, call.Outputs())
	if err != nil {
		t.Fatalf("no-input convention failed: %v", err)
	}
	if m, _ := out.(map[string]any); m["ok"] != true {
		t.Fatalf("got %v", out)
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("expected exactly 1 dispatch, got %d", got)
	}
}

// TestIntegration_OAuth2CredentialsApplied verifies oauth2/openIdConnect
// schemes place the accessToken (falling back to bearerToken) as
// Authorization: Bearer.
func TestIntegration_OAuth2CredentialsApplied(t *testing.T) {
	var gotAuth string
	srv, _ := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})

	spec := map[string]any{
		"openapi": "3.0.3",
		"info":    map[string]any{"title": "OAuth API", "version": "1.0.0"},
		"servers": []map[string]any{{"url": srv.URL}},
		"paths": map[string]any{
			"/me": map[string]any{
				"get": map[string]any{
					"operationId": "me",
					"security":    []map[string]any{{"oidc": []any{}}},
					"responses":   map[string]any{"200": map[string]any{"description": "OK"}},
				},
			},
		},
		"components": map[string]any{
			"securitySchemes": map[string]any{
				"oidc": map[string]any{"type": "oauth2", "flows": map[string]any{
					"clientCredentials": map[string]any{"tokenUrl": srv.URL + "/token", "scopes": map[string]any{}},
				}},
			},
		},
	}
	specBytes, _ := json.Marshal(spec)

	binv := NewInvoker()
	call := binv.InvokeBinding(context.Background(), &openbindings.BindingInvocationArgs{
		Source:  openbindings.InvocationSource{Format: FormatToken, Content: string(specBytes)},
		Ref:     "#/paths/~1me/get",
		Context: map[string]any{"accessToken": "at-123"},
	})
	if _, ierr := driveSingle(t, call, nil); ierr != nil {
		t.Fatalf("unexpected error: %s: %s", ierr.Code, ierr.Message)
	}
	if gotAuth != "Bearer at-123" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer at-123")
	}
}

// TestIntegration_ContextRequired_ZeroIO verifies the CONTEXT_REQUIRED
// challenge is raised pre-dispatch: the request-counting server sees zero
// requests (parity with the asyncapi/mcp zero-I/O tests).
func TestIntegration_ContextRequired_ZeroIO(t *testing.T) {
	srv, requests := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})
	spec, _ := json.Marshal(makeOpenAPISpec(srv.URL))

	binv := NewInvoker()
	call := binv.InvokeBinding(context.Background(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{Format: FormatToken, Content: string(spec)},
		Ref:    "#/paths/~1items/get",
	})
	_, ierr := driveOutputs(context.Background(), call, nil)
	if ierr == nil || ierr.Code != openbindings.ErrCodeContextRequired {
		t.Fatalf("expected CONTEXT_REQUIRED, got %v", ierr)
	}
	if got := requests.Load(); got != 0 {
		t.Errorf("challenge must precede any I/O: %d requests dispatched", got)
	}
}

// ---------------------------------------------------------------------------
// Server-Sent Events (SSE) integration tests
// ---------------------------------------------------------------------------

func sseSpec(serverURL string) string {
	spec := map[string]any{
		"openapi": "3.0.3",
		"info":    map[string]any{"title": "SSE API", "version": "1.0.0"},
		"servers": []map[string]any{{"url": serverURL}},
		"paths": map[string]any{
			"/events": map[string]any{
				"get": map[string]any{
					"operationId": "subscribeEvents",
					"responses": map[string]any{
						"200": map[string]any{
							"description": "Stream of events",
							"content": map[string]any{
								"text/event-stream": map[string]any{
									"schema": map[string]any{
										"type": "object",
										"properties": map[string]any{
											"id":  map[string]any{"type": "string"},
											"msg": map[string]any{"type": "string"},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
	bytes, _ := json.Marshal(spec)
	return string(bytes)
}

func sseCall(srv *httptest.Server) openbindings.Invocation[any, any] {
	return NewInvoker().InvokeBinding(context.Background(), &openbindings.BindingInvocationArgs{
		Ref:    "#/paths/~1events/get",
		Source: openbindings.InvocationSource{Format: FormatToken, Content: sseSpec(srv.URL)},
	})
}

// sseCallHooked drives the same op with the documented consumer pattern
// for JSON SSE streams: an OutputDecoder that parses each event's data and
// reconstructs the framing wrap from the per-unit Meta (x-sse-event/id).
func sseCallHooked(srv *httptest.Server) openbindings.Invocation[any, any] {
	op := openbindings.NewOperationInvoker(NewInvoker())
	op.OutputDecoder = func(_ openbindings.InvokeSite, raw openbindings.RawResult) (any, error) {
		var data any
		if err := json.Unmarshal(raw.Body, &data); err != nil {
			return nil, err
		}
		ev, id := "", ""
		if vs := raw.Meta["x-sse-event"]; len(vs) > 0 {
			ev = vs[0]
		}
		if vs := raw.Meta["x-sse-id"]; len(vs) > 0 {
			id = vs[0]
		}
		if ev == "" && id == "" {
			return data, nil
		}
		wrapped := map[string]any{"data": data}
		if ev != "" {
			wrapped["event"] = ev
		}
		if id != "" {
			wrapped["id"] = id
		}
		return wrapped, nil
	}
	return op.InvokeBinding(context.Background(), &openbindings.BindingInvocationArgs{
		Ref:    "#/paths/~1events/get",
		Source: openbindings.InvocationSource{Format: FormatToken, Content: sseSpec(srv.URL)},
	})
}

func TestIntegration_SSEResponse_StreamsEvents(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if accept := r.Header.Get("Accept"); !strings.Contains(accept, "text/event-stream") {
			t.Errorf("Accept header = %q, expected to contain text/event-stream", accept)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: {\"id\":\"1\",\"msg\":\"first\"}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = io.WriteString(w, "data: {\"id\":\"2\",\"msg\":\"second\"}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = io.WriteString(w, "event: progress\nid: 42\ndata: {\"id\":\"3\",\"msg\":\"third\"}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = io.WriteString(w, ": this is a comment, should be ignored\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer srv.Close()

	events, ierr := driveOutputs(context.Background(), sseCallHooked(srv), nil)
	if ierr != nil {
		t.Fatalf("unexpected stream error: %v", ierr)
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 SSE events, got %d", len(events))
	}

	first, ok := events[0].(map[string]any)
	if !ok || first["id"] != "1" || first["msg"] != "first" {
		t.Errorf("event 0 = %+v, want id=1 msg=first", events[0])
	}
	second, ok := events[1].(map[string]any)
	if !ok || second["id"] != "2" || second["msg"] != "second" {
		t.Errorf("event 1 = %+v, want id=2 msg=second", events[1])
	}
	third, ok := events[2].(map[string]any)
	if !ok {
		t.Fatalf("event 2: expected wrapped map, got %T", events[2])
	}
	if third["event"] != "progress" || third["id"] != "42" {
		t.Errorf("event 2 metadata = %+v, want event=progress id=42", third)
	}
	innerData, ok := third["data"].(map[string]any)
	if !ok || innerData["msg"] != "third" {
		t.Errorf("event 2 inner data = %+v, want msg=third", third["data"])
	}
}

func TestIntegration_SSEResponse_NotSSE_StaysUnary(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"only","msg":"hi"}`)
	}))
	defer srv.Close()

	events, ierr := driveOutputs(context.Background(), sseCall(srv), nil)
	if ierr != nil {
		t.Fatalf("unexpected error: %v", ierr)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 unary event, got %d", len(events))
	}
	data, ok := events[0].(map[string]any)
	if !ok || data["msg"] != "hi" {
		t.Errorf("data = %+v, want msg=hi", events[0])
	}
}

func TestIntegration_SSEResponse_MultilineData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: line one\ndata: line two\ndata: line three\n\n")
	}))
	defer srv.Close()

	events, ierr := driveOutputs(context.Background(), sseCall(srv), nil)
	if ierr != nil {
		t.Fatalf("unexpected error: %v", ierr)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event from multiline data, got %d", len(events))
	}
	str, ok := events[0].(string)
	if !ok || str != "line one\nline two\nline three" {
		t.Errorf("data = %q, want joined multiline", events[0])
	}
}

// TestIntegration_SSEResponse_MidStreamClose verifies that an abrupt
// server-side connection drop after some events surfaces the events received
// before the close and terminates the invocation cleanly (no hang, no leak).
func TestIntegration_SSEResponse_MidStreamClose(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: {\"id\":\"1\"}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = io.WriteString(w, "data: {\"id\":\"2\"}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			return
		}
		_ = conn.Close()
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	events, _ := driveOutputs(ctx, sseCall(srv), nil)
	if len(events) < 2 {
		t.Fatalf("expected at least 2 events before drop, got %d", len(events))
	}
}

// TestIntegration_SSEResponse_MalformedLines verifies that malformed SSE lines
// are handled leniently per the W3C spec and only valid data events surface.
func TestIntegration_SSEResponse_MalformedLines(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, ": this is a comment\n")
		_, _ = io.WriteString(w, "garbage-line-no-colon\n")
		_, _ = io.WriteString(w, "unknown-field: value\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"survivor\"}\n\n")
		_, _ = io.WriteString(w, ": another comment\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"second\"}\n\n")
	}))
	defer srv.Close()

	events, ierr := driveOutputs(context.Background(), sseCallHooked(srv), nil)
	if ierr != nil {
		t.Fatalf("unexpected error: %v", ierr)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 valid events from malformed stream, got %d", len(events))
	}
	first, _ := events[0].(map[string]any)
	if first["id"] != "survivor" {
		t.Errorf("first event id = %v, want survivor", first["id"])
	}
}

// TestNewInvokerWithClient verifies that an Invoker created with a custom HTTP
// client uses that client for outbound requests.
func TestNewInvokerWithClient(t *testing.T) {
	var requestCount int
	customTransport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		requestCount++
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"custom":"client"}`)),
			Request:    req,
		}, nil
	})
	invoker := NewInvokerWithClient(&http.Client{Transport: customTransport})

	call := invoker.InvokeBinding(context.Background(), &openbindings.BindingInvocationArgs{
		Ref:    "#/paths/~1events/get",
		Source: openbindings.InvocationSource{Format: FormatToken, Content: sseSpec("http://example.test")},
	})
	out, ierr := driveSingle(t, call, nil)
	if ierr != nil {
		t.Fatalf("unexpected error: %v", ierr)
	}
	if requestCount != 1 {
		t.Errorf("expected custom transport to be called exactly once, got %d", requestCount)
	}
	data, ok := out.(map[string]any)
	if !ok || data["custom"] != "client" {
		t.Errorf("expected response from custom client, got %+v", out)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

// TestIntegration_SSEResponse_Cancellation verifies that cancelling the
// invocation tears down the SSE stream cleanly; the output stream terminates
// promptly with ERR_CANCELLED.
func TestIntegration_SSEResponse_Cancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: {\"id\":\"first\"}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	call := NewInvoker().InvokeBinding(ctx, &openbindings.BindingInvocationArgs{
		Ref:    "#/paths/~1events/get",
		Source: openbindings.InvocationSource{Format: FormatToken, Content: sseSpec(srv.URL)},
	})

	out := call.Outputs()
	first, err := out.Read(context.Background())
	if err != nil {
		t.Fatalf("first event error: %v", err)
	}
	// Text lane under the header rule: the event arrives as its data
	// string (decode behavior is covered by the StreamsEvents tests).
	if fs, ok := first.(string); !ok || fs == "" {
		t.Fatalf("expected first event data string, got %T %v", first, first)
	}

	cancel()
	done := make(chan error, 1)
	go func() {
		_, err := out.Read(context.Background())
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, io.EOF) {
			var ie *openbindings.InvocationError
			if !errors.As(err, &ie) || ie.Code != openbindings.ErrCodeCancelled {
				t.Fatalf("expected ERR_CANCELLED or EOF after cancel, got %v", err)
			}
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SSE stream did not terminate within 3s of cancellation")
	}
}
