package openapi

import (
	"context"
	"encoding/base64"
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
		Source: openbindings.InvocationSource{BindingSpec: BindingSpec, Content: openbindings.TextContent(string(specBytes))},
	})
	// OAPI-P-04: a binary-signaled part's bytes come from the caller's
	// STRING value, Base64-decoded (3.0.x signals binary via format: binary
	// and declares no encoding, so the boundary encoding applies).
	_, ierr := driveSingle(t, call, map[string]any{
		"file":        base64.StdEncoding.EncodeToString([]byte("binary-content-here")),
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
			{BindingSpec: BindingSpec, Location: specURL},
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
					"responses": map[string]any{"200": map[string]any{"description": "OK", "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{}}}}},
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
					"responses": map[string]any{"201": map[string]any{"description": "Created", "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{}}}}},
				},
			},
			"/session": map[string]any{
				"get": map[string]any{
					"operationId": "getSession",
					"parameters": []map[string]any{
						{"name": "session_id", "in": "cookie", "schema": map[string]any{"type": "string"}},
					},
					"responses": map[string]any{"200": map[string]any{"description": "OK", "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{}}}}},
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
	source := openbindings.InvocationSource{BindingSpec: BindingSpec, Content: openbindings.TextContent(widgetSpec(srv.URL))}
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

// TestIntegration_BareCloseDispatchesWhenArtifactPermits verifies the
// permissive half of bare-close adjudication (C2): a caller with nothing to
// say says so by closing, and the ARTIFACT decides what that means. Here
// /session declares only an optional cookie parameter the OBI never expressed,
// so §9.1's required-declaration rule is satisfied and the call dispatches with
// empty input. The operation's absent `input` member plays no part: it is not a
// cardinality signal (core §6.2), and this same document dispatches identically
// whether or not the OBI expressed a contract.
func TestIntegration_BareCloseDispatchesWhenArtifactPermits(t *testing.T) {
	srv, requests := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	binv := NewInvoker()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	call := binv.InvokeBinding(ctx, &openbindings.BindingInvocationArgs{
		Source:  openbindings.InvocationSource{BindingSpec: BindingSpec, Content: openbindings.TextContent(widgetSpec(srv.URL))},
		Ref:     "#/paths/~1session/get",
		Binding: &openbindings.BindingEntry{Operation: "getSession", Source: "api", Ref: "#/paths/~1session/get"},
		// InputSchema nil — the document makes no claim at this boundary.
	})
	// The caller speaks: nothing to write, so it closes. Bounded by ctx so a
	// parked binding fails the test instead of hanging it.
	if err := call.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	out, err := openbindings.Single(ctx, call.Outputs())
	if err != nil {
		t.Fatalf("bare close should dispatch when the artifact permits it: %v", err)
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
		Source:  openbindings.InvocationSource{BindingSpec: BindingSpec, Content: openbindings.TextContent(withDeclaredJSONResponses(t, string(specBytes)))},
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

// TestIntegration_TwoANDedAPIKeysDistinguishedByName verifies the R2.d
// ruling end to end: an operation whose single security alternative ANDs two
// apiKey schemes (header + query, different securitySchemes keys) resolves
// each credential independently from context.apiKeys[name] — the two keys
// would otherwise be indistinguishable under the old single "apiKey"
// convenience field, and each must land in its own declared wire location.
func TestIntegration_TwoANDedAPIKeysDistinguishedByName(t *testing.T) {
	var gotHeader, gotQuery string
	srv, _ := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Header-Key")
		gotQuery = r.URL.Query().Get("api_key")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})

	spec := map[string]any{
		"openapi": "3.0.3",
		"info":    map[string]any{"title": "Two Key API", "version": "1.0.0"},
		"servers": []map[string]any{{"url": srv.URL}},
		"paths": map[string]any{
			"/secure": map[string]any{
				"get": map[string]any{
					"operationId": "secure",
					"security":    []map[string]any{{"headerKey": []any{}, "queryKey": []any{}}},
					"responses":   map[string]any{"200": map[string]any{"description": "OK"}},
				},
			},
		},
		"components": map[string]any{
			"securitySchemes": map[string]any{
				"headerKey": map[string]any{"type": "apiKey", "in": "header", "name": "X-Header-Key"},
				"queryKey":  map[string]any{"type": "apiKey", "in": "query", "name": "api_key"},
			},
		},
	}
	specBytes, _ := json.Marshal(spec)

	binv := NewInvoker()
	source := openbindings.InvocationSource{BindingSpec: BindingSpec, Content: openbindings.TextContent(withDeclaredJSONResponses(t, string(specBytes)))}

	// Preflight: the AND'd alternative challenges, each requirement carrying
	// its own securitySchemes key as Name (R2.a ruling).
	call := binv.InvokeBinding(context.Background(), &openbindings.BindingInvocationArgs{
		Source: source,
		Ref:    "#/paths/~1secure/get",
	})
	_, ierr := driveSingle(t, call, nil)
	if ierr == nil || ierr.Code != openbindings.ErrCodeContextRequired {
		t.Fatalf("expected CONTEXT_REQUIRED, got %v", ierr)
	}
	details, _ := ierr.Details.(*openbindings.ContextRequiredDetails)
	if details == nil || len(details.Alternatives) != 1 || len(details.Alternatives[0].Requirements) != 2 {
		t.Fatalf("unexpected challenge shape: %+v", ierr.Details)
	}
	names := map[string]bool{}
	for _, req := range details.Alternatives[0].Requirements {
		names[req.Name] = true
	}
	if !names["headerKey"] || !names["queryKey"] {
		t.Fatalf("requirement Names = %v, want {headerKey, queryKey}", names)
	}

	// Resolve with scheme-scoped apiKeys: both credentials must reach their
	// own declared wire location, distinguishably.
	call2 := binv.InvokeBinding(context.Background(), &openbindings.BindingInvocationArgs{
		Source: source,
		Ref:    "#/paths/~1secure/get",
		Context: map[string]any{
			"apiKeys": map[string]any{"headerKey": "hdr-secret", "queryKey": "qry-secret"},
		},
	})
	if _, ierr := driveSingle(t, call2, nil); ierr != nil {
		t.Fatalf("unexpected error: %s: %s", ierr.Code, ierr.Message)
	}
	if gotHeader != "hdr-secret" {
		t.Errorf("header key = %q, want %q", gotHeader, "hdr-secret")
	}
	if gotQuery != "qry-secret" {
		t.Errorf("query key = %q, want %q", gotQuery, "qry-secret")
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
		Source: openbindings.InvocationSource{BindingSpec: BindingSpec, Content: openbindings.TextContent(string(spec))},
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
								"application/json": map[string]any{
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
		Source: openbindings.InvocationSource{BindingSpec: BindingSpec, Content: openbindings.TextContent(sseSpec(srv.URL))},
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
		Source: openbindings.InvocationSource{BindingSpec: BindingSpec, Content: openbindings.TextContent(sseSpec(srv.URL))},
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

// A mid-stream lifetime DEADLINE on an SSE (server-streaming) response:
// already-emitted events stand, then exactly one terminal ERR_TIMEOUT
// (transient / possible). This must agree with the gRPC server-stream deadline
// terminal (cross-format consistency, audit finding 2.3): both formats classify
// a mid-stream deadline as ERR_TIMEOUT, never ERR_CANCELLED.
func TestIntegration_SSEResponse_MidStreamDeadlineIsTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: {\"id\":\"1\",\"msg\":\"first\"}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		// Hold the stream open until the client's lifetime deadline tears it
		// down (mirrors gRPC WatchItems).
		<-r.Context().Done()
	}))
	defer srv.Close()

	// The invocation LIFETIME carries the deadline; reads use a separate ctx.
	lifeCtx, cancelLife := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancelLife()
	call := NewInvoker().InvokeBinding(lifeCtx, &openbindings.BindingInvocationArgs{
		Ref:    "#/paths/~1events/get",
		Source: openbindings.InvocationSource{BindingSpec: BindingSpec, Content: openbindings.TextContent(sseSpec(srv.URL))},
	})
	_ = call.Close()

	readCtx, cancelRead := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelRead()
	out := call.Outputs()

	first, err := out.Read(readCtx)
	if err != nil {
		t.Fatalf("first event must stand, got %v", err)
	}
	if first == nil {
		t.Fatal("first event must be non-nil (an output flowed before the deadline)")
	}

	_, err = out.Read(readCtx)
	var terr *openbindings.InvocationError
	if !errors.As(err, &terr) {
		t.Fatalf("expected *InvocationError, got %T: %v", err, err)
	}
	if terr.Code != openbindings.ErrCodeTimeout {
		t.Fatalf("code = %q, want ERR_TIMEOUT", terr.Code)
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
		Source: openbindings.InvocationSource{BindingSpec: BindingSpec, Content: openbindings.TextContent(sseSpec("http://example.test"))},
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
		Source: openbindings.InvocationSource{BindingSpec: BindingSpec, Content: openbindings.TextContent(sseSpec(srv.URL))},
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

// Request bodies declared via $ref are the production norm; the
// synthesized contract must describe the FLAT caller shape and the invoker
// must send it verbatim. Regression: the flatten decision ran before ref
// inlining, so a phantom "body" wrapper entered the contract AND went
// literally onto the wire, while required-ness ([]any after the JSON
// round-trip) was silently dropped.
func TestSynthesizeInterface_RefRequestBodyRoundTrip(t *testing.T) {
	spec := `{
	  "openapi": "3.0.3",
	  "info": {"title": "Pets", "version": "1.0.0"},
	  "paths": {"/pets": {"post": {
	    "operationId": "createPet",
	    "requestBody": {"required": true, "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Pet"}}}},
	    "responses": {"201": {"description": "created", "content": {"application/json": {"schema": {}}}}}
	  }}},
	  "components": {"schemas": {"Pet": {
	    "type": "object",
	    "properties": {"name": {"type": "string"}, "tag": {"type": "string"}},
	    "required": ["name"]
	  }}}
	}`

	var receivedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	iface, err := NewSynthesizer().SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{BindingSpec: BindingSpec, Content: openbindings.TextContent(strings.ReplaceAll(spec, "PLACEHOLDER", srv.URL))}},
	})
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}

	op := iface.Operations["createPet"]
	props, _ := op.Input.(map[string]any)["properties"].(map[string]any)
	if _, hasBody := props["body"]; hasBody {
		t.Errorf("contract carries a phantom body wrapper: %v", op.Input)
	}
	if _, hasName := props["name"]; !hasName {
		t.Fatalf("contract must describe the flat caller shape, got properties %v", props)
	}
	req, _ := op.Input.(map[string]any)["required"].([]any)
	found := false
	for _, r := range req {
		if r == "name" {
			found = true
		}
	}
	if !found {
		t.Errorf("required-ness of body fields dropped: required = %v", op.Input.(map[string]any)["required"])
	}

	// The contract's shape goes onto the wire verbatim.
	call := NewInvoker().InvokeBinding(context.Background(), &openbindings.BindingInvocationArgs{
		Source:  openbindings.InvocationSource{BindingSpec: BindingSpec, Content: openbindings.TextContent(strings.ReplaceAll(spec, `"paths"`, `"servers": [{"url": "`+srv.URL+`"}], "paths"`))},
		Ref:     "#/paths/~1pets/post",
		Context: nil,
	})
	if _, ierr := driveSingle(t, call, map[string]any{"name": "rex"}); ierr != nil {
		t.Fatalf("invoke: %s: %s", ierr.Code, ierr.Message)
	}
	var wire map[string]any
	if err := json.Unmarshal(receivedBody, &wire); err != nil {
		t.Fatalf("wire body: %v (%s)", err, receivedBody)
	}
	if wire["name"] != "rex" {
		t.Errorf("wire body should be the flat shape, got %s", receivedBody)
	}
	if _, wrapped := wire["body"]; wrapped {
		t.Errorf("phantom body wrapper reached the wire: %s", receivedBody)
	}
}

// TestIntegration_RefParametersRouteCorrectly pins that $ref'd parameters
// (the components/parameters indirection, a production norm) are resolved at
// document load, so a $ref'd path/query parameter routes to its declared wire
// location instead of silently falling into the body (TS is aligned TO this).
func TestIntegration_RefParametersRouteCorrectly(t *testing.T) {
	var gotPath, gotQuery string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query().Get("verbose")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	spec := fmt.Sprintf(`{
	  "openapi": "3.0.3",
	  "info": {"title": "t", "version": "1"},
	  "servers": [{"url": %q}],
	  "paths": {
	    "/users/{id}": {
	      "get": {
	        "operationId": "getUser",
	        "parameters": [
	          {"$ref": "#/components/parameters/IdParam"},
	          {"$ref": "#/components/parameters/VerboseParam"}
	        ],
	        "responses": {"200": {"description": "ok", "content": {"application/json": {"schema": {}}}}}
	      }
	    }
	  },
	  "components": {
	    "parameters": {
	      "IdParam": {"name": "id", "in": "path", "required": true, "schema": {"type": "string"}},
	      "VerboseParam": {"name": "verbose", "in": "query", "schema": {"type": "boolean"}}
	    }
	  }
	}`, srv.URL)

	call := NewInvoker().InvokeBinding(context.Background(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{BindingSpec: BindingSpec, Content: openbindings.TextContent(spec)},
		Ref:    "#/paths/~1users~1{id}/get",
	})
	if _, ierr := driveSingle(t, call, map[string]any{"id": "u1", "verbose": true}); ierr != nil {
		t.Fatalf("invoke: %s: %s", ierr.Code, ierr.Message)
	}
	if gotPath != "/users/u1" {
		t.Errorf("path = %q, want /users/u1 ($ref'd path param must route to the path)", gotPath)
	}
	if gotQuery != "true" {
		t.Errorf("query verbose = %q, want true ($ref'd query param must route to the query)", gotQuery)
	}
	if len(gotBody) > 0 {
		t.Errorf("no field should fall to the body of a GET without requestBody, got %s", gotBody)
	}
}

// TestPrepareBinding_UsesCachePrimedFromContent pins the content+location
// cache rule: an invocation with BOTH content and location primes the
// location-keyed document cache, so a later location-only prepareBinding
// (which never fetches) can report requirements from the warm cache
// (TS is aligned TO this).
func TestPrepareBinding_UsesCachePrimedFromContent(t *testing.T) {
	spec, _ := json.Marshal(makeOpenAPISpec("https://api.example.com"))
	location := "https://example.test/openapi.json"

	binv := NewInvoker()
	// Content+location invocation: no fetch happens (content is authoritative),
	// but the parse must land in the location-keyed cache.
	call := binv.InvokeBinding(context.Background(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{BindingSpec: BindingSpec, Location: location, Content: openbindings.TextContent(string(spec))},
		Ref:    "#/paths/~1items/get",
	})
	_, ierr := driveOutputs(context.Background(), call, nil)
	if ierr == nil || ierr.Code != openbindings.ErrCodeContextRequired {
		t.Fatalf("expected CONTEXT_REQUIRED, got %v", ierr)
	}

	// Location-only preflight: must be served from the warm cache.
	details, err := binv.PrepareBinding(context.Background(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{BindingSpec: BindingSpec, Location: location},
		Ref:    "#/paths/~1items/get",
	})
	if err != nil {
		t.Fatalf("prepareBinding: %v", err)
	}
	if details == nil {
		t.Fatal("prepareBinding must see the cached document primed from content")
	}
	if details.Target != "https://api.example.com" {
		t.Errorf("target = %q, want https://api.example.com", details.Target)
	}
}

// A parameter/body collision makes that request-media candidate
// inadmissible. One caller value is never duplicated into independent wire
// declarations.
func TestIntegration_CollisionRefusesBeforeDispatch(t *testing.T) {
	var requests atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "must not dispatch", http.StatusTeapot)
	}))
	defer ts.Close()

	spec := fmt.Sprintf(`{
	  "openapi": "3.0.0",
	  "info": {"title": "t", "version": "1"},
	  "servers": [{"url": %q}],
	  "paths": {
	    "/users/{id}": {
	      "put": {
	        "operationId": "updateUser",
	        "parameters": [{"name": "id", "in": "path", "required": true, "schema": {"type": "string"}}],
	        "requestBody": {"required": true, "content": {"application/json": {"schema": {
	          "type": "object",
	          "properties": {"id": {"type": "string"}, "name": {"type": "string"}},
	          "required": ["id", "name"]
	        }}}},
	        "responses": {"200": {"description": "ok"}}
	      }
	    }
	  }
	}`, ts.URL)

	inv := NewInvoker()
	call := inv.InvokeBinding(context.Background(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{BindingSpec: BindingSpec, Content: openbindings.TextContent(spec)},
		Ref:    "#/paths/~1users~1{id}/put",
	})
	if err := call.Write(context.Background(), map[string]any{"id": "u1", "name": "Ada"}); err != nil {
		t.Fatal(err)
	}
	if _, err := openbindings.Single(context.Background(), call.Outputs()); err == nil {
		t.Fatal("collision must refuse")
	}
	if requests.Load() != 0 {
		t.Fatalf("collision refusal must precede dispatch, got %d requests", requests.Load())
	}
}

// TestConformance_G1_AbsentInputIsNotNoInput is the executable form of the
// core's §5.1 claim, and it FAILS today by design (conformance loop rev C1,
// gap G-1).
//
// The core states that an absent `input` means no portable contract is
// specified for that boundary — explicitly NOT that the interaction carries
// zero values, because interaction shape is binding-specification-defined.
// An author who omits `input` because the contract is unknown is making no
// cardinality claim at all.
//
// Both reference implementations nonetheless read schema absence as a
// no-input signal (see invoke.go's "Operation-layer no-input convention",
// and the TypeScript mirror). The consequence is this test: an operation
// whose artifact REQUIRES a request body, invoked through the operation
// layer with `input` omitted, has its input side closed before the caller
// can write — so a value the caller supplies is discarded and the service
// receives an empty body.
//
// The correct signal is one line below the defect in the same switch:
// `len(params) == 0 && !hasRequestBody(op)`, derived from the artifact,
// which is where the spec says interaction shape lives.
func TestConformance_G1_AbsentInputIsNotNoInput(t *testing.T) {
	var body atomic.Value
	body.Store("")
	srv, _ := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body.Store(string(raw))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"created": true})
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// createWidget declares `requestBody: {required: true}`. The OBI omits
	// `input`: no portable contract, no cardinality claim.
	call := NewInvoker().InvokeBinding(ctx, &openbindings.BindingInvocationArgs{
		Source:  openbindings.InvocationSource{BindingSpec: BindingSpec, Content: openbindings.TextContent(widgetSpec(srv.URL))},
		Ref:     "#/paths/~1widgets/post",
		Binding: &openbindings.BindingEntry{Operation: "createWidget", Source: "api", Ref: "#/paths/~1widgets/post"},
		// InputSchema nil — the document makes no claim at this boundary.
	})

	writeErr := call.Write(ctx, map[string]any{"name": "gadget"})
	closeErr := call.Close()
	_, readErr := openbindings.Single(ctx, call.Outputs())

	sent, _ := body.Load().(string)
	if !strings.Contains(sent, "gadget") {
		t.Errorf("G-1: the caller's value never reached the service.\n"+
			"  body received = %q\n"+
			"  write err = %v, close err = %v, read err = %v\n"+
			"  Absent `input` was read as a no-input cardinality signal; core §5.1 "+
			"says absence means no CONTRACT is specified, not that the interaction "+
			"carries zero values. Interaction shape is binding-specification-defined "+
			"and must come from the artifact (here: requestBody.required), never from "+
			"the document's schema slot.", sent, writeErr, closeErr, readErr)
	}
}
