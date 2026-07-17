package connect

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	openbindings "github.com/openbindings/openbindings-go"
)

const testProto = `
syntax = "proto3";
package testpkg;

message GetItemRequest { string id = 1; }
message GetItemResponse { string id = 1; string name = 2; }

service TestService {
  rpc GetItem(GetItemRequest) returns (GetItemResponse);
}
`

// emptyRequestProto declares a method whose request message has no fields, so
// a bare input close (no message written) is a valid empty request.
const emptyRequestProto = `
syntax = "proto3";
package testpkg;

message Empty {}
message PingResponse { string status = 1; }

service PingService {
  rpc Ping(Empty) returns (PingResponse);
}
`

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func unaryArgs(location string, content any, ref string) *openbindings.BindingInvocationArgs {
	// nil means ABSENT content (descriptorless mode), never a present null;
	// a string is .proto source text (the family's string carriage).
	var raw json.RawMessage
	switch c := content.(type) {
	case nil:
	case string:
		raw = openbindings.TextContent(c)
	case json.RawMessage:
		raw = c
	default:
		raw = mustContent(content)
	}
	return &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{
			BindingSpec: BindingSpec,
			Location:    location,
			Content:     raw,
		},
		Ref: ref,
	}
}

// invokeWith starts an invocation, writes the given inputs through the
// handle, and closes the input side.
func invokeWith(t *testing.T, ctx context.Context, invoker *Invoker, args *openbindings.BindingInvocationArgs, inputs ...any) openbindings.Invocation[any, any] {
	t.Helper()
	inv := invoker.InvokeBinding(ctx, args)
	for _, in := range inputs {
		if err := inv.Write(ctx, in); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if err := inv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return inv
}

// collectOutputs reads the output stream to its end and returns the outputs
// plus the terminal error (nil on clean io.EOF close).
func collectOutputs(t *testing.T, ctx context.Context, inv openbindings.Invocation[any, any]) ([]any, *openbindings.InvocationError) {
	t.Helper()
	out := inv.Outputs()
	var outputs []any
	for {
		v, err := out.Read(ctx)
		if err == io.EOF {
			return outputs, nil
		}
		if err != nil {
			ierr, ok := err.(*openbindings.InvocationError)
			if !ok {
				t.Fatalf("Read returned a non-InvocationError: %v", err)
			}
			return outputs, ierr
		}
		outputs = append(outputs, v)
	}
}

// mustTerminalError drains the stream expecting zero outputs and a terminal
// error with the given code.
func mustTerminalError(t *testing.T, ctx context.Context, inv openbindings.Invocation[any, any], wantCode string) *openbindings.InvocationError {
	t.Helper()
	outputs, ierr := collectOutputs(t, ctx, inv)
	if len(outputs) != 0 {
		t.Fatalf("expected no outputs, got %d: %+v", len(outputs), outputs)
	}
	if ierr == nil {
		t.Fatalf("expected terminal error %q, got clean close", wantCode)
	}
	if ierr.Code != wantCode {
		t.Fatalf("error code = %q, want %q (message: %q)", ierr.Code, wantCode, ierr.Message)
	}
	return ierr
}

// unreachableServer returns an httptest.Server whose handler fails the test:
// it asserts the invoker errored before any network side effect.
func unreachableServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("server should not be reached: the invoker must fail before any network I/O")
		http.Error(w, "should not be reached", http.StatusTeapot)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fakeConnectServer returns an httptest.Server that responds to Connect-protocol
// POSTs with a canned JSON response. The handler is invoked once per request.
func fakeConnectServer(t *testing.T, statusCode int, responseBody string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}
		if got := r.Header.Get("Connect-Protocol-Version"); got != "1" {
			t.Errorf("Connect-Protocol-Version = %q, want 1", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		_, _ = io.WriteString(w, responseBody)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestInvokeBinding_UnarySuccess(t *testing.T) {
	ctx := testContext(t)
	srv := fakeConnectServer(t, http.StatusOK, `{"id":"abc","name":"hello"}`)

	inv := invokeWith(t, ctx, NewInvoker(), unaryArgs(srv.URL, testProto, "testpkg.TestService/GetItem"),
		map[string]any{"id": "abc"})

	got, err := openbindings.Single[any](ctx, inv.Outputs())
	if err != nil {
		t.Fatalf("Single: %v", err)
	}
	data, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("expected map output, got %T", got)
	}
	if data["id"] != "abc" || data["name"] != "hello" {
		t.Errorf("unexpected response data: %+v", data)
	}

	// Leading metadata is the HTTP response headers.
	md, err := inv.Header(ctx)
	if err != nil {
		t.Fatalf("Header: %v", err)
	}
	if got := md["Content-Type"]; len(got) != 1 || got[0] != "application/json" {
		t.Errorf("header Content-Type = %v, want [application/json]", got)
	}
	if got := inv.Trailer(); len(got) != 0 {
		t.Errorf("Trailer = %v, want empty", got)
	}
}

func TestInvokeBinding_UnaryTrailer(t *testing.T) {
	ctx := testContext(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Connect unary trailing metadata travels as Trailer- prefixed headers.
		w.Header().Set("Trailer-X-After", "done")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"abc","name":"hello"}`)
	}))
	t.Cleanup(srv.Close)

	inv := invokeWith(t, ctx, NewInvoker(), unaryArgs(srv.URL, testProto, "testpkg.TestService/GetItem"),
		map[string]any{"id": "abc"})

	if _, err := openbindings.Single[any](ctx, inv.Outputs()); err != nil {
		t.Fatalf("Single: %v", err)
	}

	md, err := inv.Header(ctx)
	if err != nil {
		t.Fatalf("Header: %v", err)
	}
	if _, present := md["Trailer-X-After"]; present {
		t.Error("Trailer-X-After must not appear in leading metadata")
	}
	trailer := inv.Trailer()
	if got := trailer["X-After"]; len(got) != 1 || got[0] != "done" {
		t.Errorf("trailer X-After = %v, want [done]", got)
	}
}

func TestInvokeBinding_HTTPError(t *testing.T) {
	ctx := testContext(t)
	srv := fakeConnectServer(t, http.StatusInternalServerError, `{"code":"internal","message":"boom"}`)

	inv := invokeWith(t, ctx, NewInvoker(), unaryArgs(srv.URL, testProto, "testpkg.TestService/GetItem"),
		map[string]any{"id": "abc"})

	ierr := mustTerminalError(t, ctx, inv, openbindings.ErrCodeExecutionFailed)
	if ierr.Message != "boom" {
		t.Errorf("error message = %q, want %q", ierr.Message, "boom")
	}
}

func TestInvokeBinding_ConnectErrorCodeMapping(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     string
		wantCode string
	}{
		{"unauthenticated", http.StatusUnauthorized, `{"code":"unauthenticated","message":"need token"}`, openbindings.ErrCodeAuthRequired},
		{"permission_denied", http.StatusForbidden, `{"code":"permission_denied","message":"nope"}`, openbindings.ErrCodePermissionDenied},
		{"unavailable", http.StatusServiceUnavailable, `{"code":"unavailable","message":"down"}`, openbindings.ErrCodeConnectFailed},
		{"deadline_exceeded", http.StatusGatewayTimeout, `{"code":"deadline_exceeded","message":"too slow"}`, openbindings.ErrCodeTimeout},
		{"internal", http.StatusInternalServerError, `{"code":"internal","message":"broke"}`, openbindings.ErrCodeExecutionFailed},
		{"plain 401 no connect body", http.StatusUnauthorized, ``, openbindings.ErrCodeAuthRequired},
		{"plain 403 no connect body", http.StatusForbidden, ``, openbindings.ErrCodePermissionDenied},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := testContext(t)
			srv := fakeConnectServer(t, tc.status, tc.body)

			inv := invokeWith(t, ctx, NewInvoker(), unaryArgs(srv.URL, testProto, "testpkg.TestService/GetItem"),
				map[string]any{"id": "abc"})
			mustTerminalError(t, ctx, inv, tc.wantCode)
		})
	}
}

func TestInvokeBinding_AuthRequired(t *testing.T) {
	ctx := testContext(t)
	srv := fakeConnectServer(t, http.StatusUnauthorized, `{"code":"unauthenticated","message":"need token"}`)

	inv := invokeWith(t, ctx, NewInvoker(), unaryArgs(srv.URL, testProto, "testpkg.TestService/GetItem"),
		map[string]any{"id": "abc"})

	ierr := mustTerminalError(t, ctx, inv, openbindings.ErrCodeAuthRequired)
	if ierr.Message != "need token" {
		t.Errorf("error message = %q, want %q", ierr.Message, "need token")
	}
}

func TestInvokeBinding_BearerTokenSent(t *testing.T) {
	ctx := testContext(t)
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"abc","name":"hello"}`)
	}))
	t.Cleanup(srv.Close)

	args := unaryArgs(srv.URL, testProto, "testpkg.TestService/GetItem")
	args.Context = map[string]any{"bearerToken": "secret-123"}
	inv := invokeWith(t, ctx, NewInvoker(), args, map[string]any{"id": "abc"})

	if _, err := openbindings.Single[any](ctx, inv.Outputs()); err != nil {
		t.Fatalf("Single: %v", err)
	}
	if gotAuth != "Bearer secret-123" {
		t.Errorf("Authorization header = %q, want %q", gotAuth, "Bearer secret-123")
	}
}

func TestInvokeBinding_InvalidRef(t *testing.T) {
	ctx := testContext(t)
	srv := unreachableServer(t)

	// No input is written: a malformed ref must terminate the invocation
	// before any input is consumed and before any network I/O.
	inv := NewInvoker().InvokeBinding(ctx, unaryArgs(srv.URL, testProto, "not-a-valid-ref"))
	mustTerminalError(t, ctx, inv, openbindings.ErrCodeInvalidRef)
}

func TestInvokeBinding_MissingBaseURL(t *testing.T) {
	ctx := testContext(t)
	inv := NewInvoker().InvokeBinding(ctx, unaryArgs("", testProto, "testpkg.TestService/GetItem"))
	mustTerminalError(t, ctx, inv, openbindings.ErrCodeSourceConfigError)
}

// An embed-mode Connect source carries the SCHEMA as content; the base URL
// resolves from location, else the target configuration point (§9.1,
// context configuration.target — the same ruled point the grpc invoker
// honors), else a remedy-naming refusal (a content-only source is not
// conformant, CONN-D-02).
func TestInvokeBinding_EmbedModeBaseURLResolution(t *testing.T) {
	proto := `syntax = "proto3";
package tiny;
service Tiny { rpc Ping(PingMsg) returns (PingMsg); }
message PingMsg { string msg = 1; }
`
	ctx := testContext(t)
	invoker := NewInvoker()

	// No location, no configured target: refuse, naming both remedies. The
	// message must be the embedded-content-aware variant, distinct from the
	// no-content-at-all message.
	h := invoker.InvokeBinding(ctx, &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{BindingSpec: BindingSpec, Content: openbindings.TextContent(proto)},
		Ref:    "tiny.Tiny/Ping",
	})
	ierr := mustTerminalError(t, ctx, h, openbindings.ErrCodeSourceConfigError)
	for _, want := range []string{"embedded content", "location", "configuration.target"} {
		if !strings.Contains(ierr.Message, want) {
			t.Errorf("refusal must mention %q, got: %s", want, ierr.Message)
		}
	}

	// configuration.target supplies the base URL: the invocation proceeds
	// past the config gate (failing later at the unreachable endpoint, not
	// at configuration).
	h2 := invokeWith(t, ctx, invoker, &openbindings.BindingInvocationArgs{
		Source:  openbindings.InvocationSource{BindingSpec: BindingSpec, Content: openbindings.TextContent(proto)},
		Ref:     "tiny.Tiny/Ping",
		Context: map[string]any{"configuration": map[string]any{"target": "http://127.0.0.1:1"}},
	}, map[string]any{"msg": "hi"})

	_, ierr2 := collectOutputs(t, ctx, h2)
	if ierr2 == nil {
		t.Fatal("dial of an unreachable base URL must fail")
	}
	if ierr2.Code == openbindings.ErrCodeSourceConfigError {
		t.Fatalf("configuration.target must satisfy the config gate; still got config error: %s", ierr2.Message)
	}
}

// A location-empty source with NO embedded content gets the plain (not
// embedded-content-aware) refusal message: there is no schema to point at,
// so the remedy is only the location/baseURL pair, not a mention of content.
func TestInvokeBinding_MissingBaseURL_NoContentMessage(t *testing.T) {
	ctx := testContext(t)
	inv := NewInvoker().InvokeBinding(ctx, unaryArgs("", nil, "testpkg.TestService/GetItem"))
	ierr := mustTerminalError(t, ctx, inv, openbindings.ErrCodeSourceConfigError)
	if strings.Contains(ierr.Message, "embedded content") {
		t.Errorf("no-content refusal should not claim embedded content, got: %s", ierr.Message)
	}
	for _, want := range []string{"location"} {
		if !strings.Contains(ierr.Message, want) {
			t.Errorf("refusal must mention %q, got: %s", want, ierr.Message)
		}
	}
}

func TestInvokeBinding_BadProtoContent(t *testing.T) {
	ctx := testContext(t)
	srv := unreachableServer(t)

	inv := NewInvoker().InvokeBinding(ctx, unaryArgs(srv.URL, "this is not proto {", "testpkg.TestService/GetItem"))
	mustTerminalError(t, ctx, inv, openbindings.ErrCodeSourceLoadFailed)
}

// clientStreamingProto declares a client-streaming method alongside a
// regular unary one, so the invoker's cardinality check can be proven
// against a real resolved descriptor.
const clientStreamingProto = `
syntax = "proto3";
package testpkg;

message Chunk { bytes data = 1; }
message UploadSummary { int32 bytesReceived = 1; }

service UploadService {
  rpc Upload(stream Chunk) returns (UploadSummary);
}
`

// With a resolved descriptor (embedded proto content), a client-streaming
// method must be refused loudly pre-dispatch — never silently mis-dispatched
// as a broken unary request. The refusal must fire before any network I/O
// (unreachableServer fails the test if the server is ever reached) and
// before any input is required, mirroring formats/grpc's equivalent refusal.
func TestInvokeBinding_ClientStreamingRefused(t *testing.T) {
	ctx := testContext(t)
	srv := unreachableServer(t)

	inv := NewInvoker().InvokeBinding(ctx, unaryArgs(srv.URL, clientStreamingProto, "testpkg.UploadService/Upload"))

	ierr := mustTerminalError(t, ctx, inv, openbindings.ErrCodeExecutionFailed)
	for _, want := range []string{"UploadService/Upload", "client-streaming"} {
		if !strings.Contains(ierr.Message, want) {
			t.Errorf("refusal must mention %q, got: %s", want, ierr.Message)
		}
	}
}

func TestInvokeBinding_AbsentInputDispatchesEmptyMessage(t *testing.T) {
	ctx := testContext(t)
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"","name":"ok"}`)
	}))
	t.Cleanup(srv.Close)

	// A close-without-write is the ABSENT input value (§9.2 via GRPC-P-03):
	// it marshals as the empty request message even for a fielded request
	// type. This module no longer produces ERR_MISSING_INPUT.
	inv := invokeWith(t, ctx, NewInvoker(), unaryArgs(srv.URL, testProto, "testpkg.TestService/GetItem"))

	if _, err := openbindings.Single[any](ctx, inv.Outputs()); err != nil {
		t.Fatalf("absent input must dispatch the empty request message (CONN-P-02): %v", err)
	}
	if gotBody != "{}" {
		t.Errorf("request body = %q, want {}", gotBody)
	}
}

func TestInvokeBinding_EmptyRequestMessage(t *testing.T) {
	ctx := testContext(t)
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	}))
	t.Cleanup(srv.Close)

	// Empty request message: a bare input close is acceptable and dispatches {}.
	inv := invokeWith(t, ctx, NewInvoker(), unaryArgs(srv.URL, emptyRequestProto, "testpkg.PingService/Ping"))

	got, err := openbindings.Single[any](ctx, inv.Outputs())
	if err != nil {
		t.Fatalf("Single: %v", err)
	}
	if data, ok := got.(map[string]any); !ok || data["status"] != "ok" {
		t.Errorf("unexpected output: %+v", got)
	}
	if gotBody != "{}" {
		t.Errorf("request body = %q, want {}", gotBody)
	}
}

func TestInvokeBinding_NonCanonicalInput(t *testing.T) {
	ctx := testContext(t)
	srv := unreachableServer(t)

	// GetItemRequest's canonical JSON form is an object; a bare string is
	// refused pre-dispatch (CONN-P-02 via GRPC-P-03: the accepted shape is
	// the request type's canonical JSON form).
	inv := invokeWith(t, ctx, NewInvoker(), unaryArgs(srv.URL, testProto, "testpkg.TestService/GetItem"),
		"just a string")
	ierr := mustTerminalError(t, ctx, inv, openbindings.ErrCodeValidationFailed)
	if !strings.Contains(ierr.Message, "canonical JSON form") {
		t.Errorf("error message = %q, want to mention the canonical JSON form", ierr.Message)
	}
}

func TestInvokeBinding_RequestBodyMatchesInput(t *testing.T) {
	ctx := testContext(t)
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"abc","name":"hello"}`)
	}))
	t.Cleanup(srv.Close)

	inv := invokeWith(t, ctx, NewInvoker(), unaryArgs(srv.URL, testProto, "testpkg.TestService/GetItem"),
		map[string]any{"id": "xyz"})
	if _, err := openbindings.Single[any](ctx, inv.Outputs()); err != nil {
		t.Fatalf("Single: %v", err)
	}

	var sent map[string]any
	if err := json.Unmarshal([]byte(gotBody), &sent); err != nil {
		t.Fatalf("server received non-JSON body %q: %v", gotBody, err)
	}
	if sent["id"] != "xyz" {
		t.Errorf("body id = %v, want xyz", sent["id"])
	}
}

func TestInvokeBinding_NoProtoContent_GenericJSON(t *testing.T) {
	ctx := testContext(t)
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"abc"}`)
	}))
	t.Cleanup(srv.Close)

	// Without content the invoker operates in descriptorless mode
	// (CONN-P-01): unary by definition, verbatim JSON values (§9.3).
	inv := invokeWith(t, ctx, NewInvoker(), unaryArgs(srv.URL, nil, "testpkg.TestService/GetItem"),
		map[string]any{"id": "abc"})
	if _, err := openbindings.Single[any](ctx, inv.Outputs()); err != nil {
		t.Fatalf("Single: %v", err)
	}

	var sent map[string]any
	if err := json.Unmarshal([]byte(gotBody), &sent); err != nil {
		t.Fatalf("server received non-JSON body %q: %v", gotBody, err)
	}
	if sent["id"] != "abc" {
		t.Errorf("body id = %v, want abc", sent["id"])
	}
}

func TestInvokeBinding_ConnectFailed(t *testing.T) {
	ctx := testContext(t)
	// Use an unreachable address; 127.0.0.1:1 is reserved and should refuse
	// connections on most systems.
	inv := invokeWith(t, ctx, NewInvoker(), unaryArgs("http://127.0.0.1:1", testProto, "testpkg.TestService/GetItem"),
		map[string]any{"id": "abc"})
	mustTerminalError(t, ctx, inv, openbindings.ErrCodeConnectFailed)
}

func TestInvokeBinding_RedirectLimit(t *testing.T) {
	ctx := testContext(t)
	// Build a server that always redirects to itself, to verify the
	// CheckRedirect cap kicks in instead of looping forever.
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(srv.Close)

	inv := invokeWith(t, ctx, NewInvoker(), unaryArgs(srv.URL, testProto, "testpkg.TestService/GetItem"),
		map[string]any{"id": "abc"})
	ierr := mustTerminalError(t, ctx, inv, openbindings.ErrCodeConnectFailed)
	if !strings.Contains(ierr.Message, "redirect") {
		t.Errorf("error message = %q, expected to mention redirect", ierr.Message)
	}
}

func TestInvokeBinding_CancelledContext(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	srv := unreachableServer(t)

	inv := NewInvoker().InvokeBinding(cancelled, unaryArgs(srv.URL, testProto, "testpkg.TestService/GetItem"))
	mustTerminalError(t, testContext(t), inv, openbindings.ErrCodeCancelled)
}

// streamingProto declares a server-streaming method so the invoker takes the
// streaming dispatch path. The method returns multiple LogEntry messages.
const streamingProto = `
syntax = "proto3";
package testpkg;

message TailLogsRequest { string source = 1; }
message LogEntry { string message = 1; int64 timestamp = 2; }

service LogsService {
  rpc TailLogs(TailLogsRequest) returns (stream LogEntry);
}
`

func streamingArgs(location string) *openbindings.BindingInvocationArgs {
	return unaryArgs(location, streamingProto, "testpkg.LogsService/TailLogs")
}

// fakeConnectStreamingServer returns an httptest.Server that responds to
// Connect streaming requests with `application/connect+json` and writes a
// fixed sequence of envelope-framed messages followed by an end-stream
// envelope. The number of data envelopes and the end-stream payload are
// configurable; an empty endStreamJSON sends `{}`.
func fakeConnectStreamingServer(t *testing.T, dataMessages []string, endStreamJSON string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if got := r.Header.Get("Content-Type"); got != "application/connect+json" {
			t.Errorf("Content-Type = %q, want application/connect+json", got)
		}
		if got := r.Header.Get("Connect-Protocol-Version"); got != "1" {
			t.Errorf("Connect-Protocol-Version = %q, want 1", got)
		}

		// The request body should be a single envelope containing the
		// request message; we don't bother decoding it for this test.
		_, _ = io.ReadAll(r.Body)

		w.Header().Set("Content-Type", "application/connect+json")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)

		for _, msg := range dataMessages {
			if err := writeConnectEnvelope(w, 0, []byte(msg)); err != nil {
				t.Errorf("write data envelope: %v", err)
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		// End-stream envelope.
		endPayload := []byte("{}")
		if endStreamJSON != "" {
			endPayload = []byte(endStreamJSON)
		}
		if err := writeConnectEnvelope(w, connectFlagEndStream, endPayload); err != nil {
			t.Errorf("write end-stream envelope: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestInvokeBinding_ServerStreaming_Success(t *testing.T) {
	ctx := testContext(t)
	srv := fakeConnectStreamingServer(t, []string{
		`{"message":"line 1","timestamp":1}`,
		`{"message":"line 2","timestamp":2}`,
		`{"message":"line 3","timestamp":3}`,
	}, "")

	inv := invokeWith(t, ctx, NewInvoker(), streamingArgs(srv.URL), map[string]any{"source": "test"})

	outputs, ierr := collectOutputs(t, ctx, inv)
	if ierr != nil {
		t.Fatalf("unexpected terminal error: %+v", ierr)
	}
	if len(outputs) != 3 {
		t.Fatalf("expected 3 outputs, got %d", len(outputs))
	}
	for i, out := range outputs {
		data, ok := out.(map[string]any)
		if !ok {
			t.Fatalf("output %d: expected map, got %T", i, out)
		}
		wantMessage := []string{"line 1", "line 2", "line 3"}[i]
		if data["message"] != wantMessage {
			t.Errorf("output %d: message = %v, want %s", i, data["message"], wantMessage)
		}
	}

	md, err := inv.Header(ctx)
	if err != nil {
		t.Fatalf("Header: %v", err)
	}
	if got := md["Content-Type"]; len(got) != 1 || got[0] != "application/connect+json" {
		t.Errorf("header Content-Type = %v, want [application/connect+json]", got)
	}
	if got := inv.Trailer(); len(got) != 0 {
		t.Errorf("Trailer = %v, want empty", got)
	}
}

func TestInvokeBinding_ServerStreaming_EmptyStream(t *testing.T) {
	ctx := testContext(t)
	srv := fakeConnectStreamingServer(t, nil, "")

	inv := invokeWith(t, ctx, NewInvoker(), streamingArgs(srv.URL), map[string]any{"source": "test"})

	outputs, ierr := collectOutputs(t, ctx, inv)
	if ierr != nil {
		t.Fatalf("unexpected terminal error: %+v", ierr)
	}
	if len(outputs) != 0 {
		t.Fatalf("expected 0 outputs from empty stream, got %d", len(outputs))
	}
}

func TestInvokeBinding_ServerStreaming_EndStreamError(t *testing.T) {
	ctx := testContext(t)
	// Server sends one data envelope then ends the stream with an error.
	srv := fakeConnectStreamingServer(
		t,
		[]string{`{"message":"first","timestamp":1}`},
		`{"error":{"code":"internal","message":"backend exploded"}}`,
	)

	inv := invokeWith(t, ctx, NewInvoker(), streamingArgs(srv.URL), map[string]any{"source": "test"})

	outputs, ierr := collectOutputs(t, ctx, inv)
	if len(outputs) != 1 {
		t.Fatalf("expected 1 output before the terminal error, got %d", len(outputs))
	}
	if ierr == nil {
		t.Fatal("expected a terminal error from the end-stream envelope")
	}
	if ierr.Code != openbindings.ErrCodeExecutionFailed {
		t.Errorf("error code = %q, want %q", ierr.Code, openbindings.ErrCodeExecutionFailed)
	}
	if !strings.Contains(ierr.Message, "backend exploded") {
		t.Errorf("error message = %q, want to contain 'backend exploded'", ierr.Message)
	}
}

func TestInvokeBinding_ServerStreaming_EndStreamErrorCodeMapping(t *testing.T) {
	ctx := testContext(t)
	srv := fakeConnectStreamingServer(t, nil,
		`{"error":{"code":"unauthenticated","message":"token expired"}}`)

	inv := invokeWith(t, ctx, NewInvoker(), streamingArgs(srv.URL), map[string]any{"source": "test"})
	mustTerminalError(t, ctx, inv, openbindings.ErrCodeAuthRequired)
}

func TestInvokeBinding_ServerStreaming_EndStreamMetadata(t *testing.T) {
	ctx := testContext(t)
	srv := fakeConnectStreamingServer(
		t,
		[]string{`{"message":"only","timestamp":1}`},
		`{"metadata":{"x-after":["done"]}}`,
	)

	inv := invokeWith(t, ctx, NewInvoker(), streamingArgs(srv.URL), map[string]any{"source": "test"})

	outputs, ierr := collectOutputs(t, ctx, inv)
	if ierr != nil {
		t.Fatalf("unexpected terminal error: %+v", ierr)
	}
	if len(outputs) != 1 {
		t.Fatalf("expected 1 output, got %d", len(outputs))
	}
	trailer := inv.Trailer()
	if got := trailer["x-after"]; len(got) != 1 || got[0] != "done" {
		t.Errorf("trailer x-after = %v, want [done]", got)
	}
}

func TestInvokeBinding_ServerStreaming_Stop(t *testing.T) {
	ctx := testContext(t)
	handlerDone := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerDone)
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/connect+json")
		w.WriteHeader(http.StatusOK)
		if err := writeConnectEnvelope(w, 0, []byte(`{"message":"first","timestamp":1}`)); err != nil {
			t.Errorf("write data envelope: %v", err)
			return
		}
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		// Block until the client tears the request down.
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	inv := invokeWith(t, ctx, NewInvoker(), streamingArgs(srv.URL), map[string]any{"source": "test"})

	out := inv.Outputs()
	first, err := out.Read(ctx)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if data, ok := first.(map[string]any); !ok || data["message"] != "first" {
		t.Fatalf("unexpected first output: %+v", first)
	}

	// Abandoning the stream cancels the invocation and tears down the
	// in-flight HTTP request.
	out.Stop()
	if _, err := out.Read(ctx); err == nil || openbindings.AsInvocationError(err).Code != openbindings.ErrCodeCancelled {
		t.Fatalf("Read after Stop = %v, want %s", err, openbindings.ErrCodeCancelled)
	}

	select {
	case <-handlerDone:
	case <-ctx.Done():
		t.Fatal("server handler was not released after Stop")
	}
}

func TestEnvelopeRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	payloads := [][]byte{
		[]byte(`{"a":1}`),
		[]byte(`{"b":[1,2,3]}`),
		[]byte{},
	}
	for _, p := range payloads {
		if err := writeConnectEnvelope(&buf, 0, p); err != nil {
			t.Fatalf("write envelope: %v", err)
		}
	}
	if err := writeConnectEnvelope(&buf, connectFlagEndStream, []byte(`{}`)); err != nil {
		t.Fatalf("write end-stream envelope: %v", err)
	}

	for i, want := range payloads {
		flags, got, err := readConnectEnvelope(&buf, 1024)
		if err != nil {
			t.Fatalf("read envelope %d: %v", i, err)
		}
		if flags != 0 {
			t.Errorf("envelope %d: flags = 0x%x, want 0", i, flags)
		}
		if string(got) != string(want) {
			t.Errorf("envelope %d: got %q, want %q", i, got, want)
		}
	}
	flags, payload, err := readConnectEnvelope(&buf, 1024)
	if err != nil {
		t.Fatalf("read end-stream envelope: %v", err)
	}
	if flags&connectFlagEndStream == 0 {
		t.Errorf("end-stream envelope: flags = 0x%x, want END_STREAM bit set", flags)
	}
	if string(payload) != `{}` {
		t.Errorf("end-stream payload = %q, want {}", payload)
	}
}

// TestWriteCompressedEnvelopeRejected verifies that writeConnectEnvelope
// refuses to emit a frame with the COMPRESSED flag set, since the invoker
// does not support compression in v0.1.
func TestWriteCompressedEnvelopeRejected(t *testing.T) {
	var buf bytes.Buffer
	err := writeConnectEnvelope(&buf, connectFlagCompressed, []byte(`{"a":1}`))
	if err == nil {
		t.Fatal("expected error when writing compressed envelope")
	}
	if !strings.Contains(err.Error(), "compression") {
		t.Errorf("error message %q should mention compression", err.Error())
	}
}

// TestReadCompressedEnvelopeRejected verifies that readConnectEnvelope
// refuses to decode a frame with the COMPRESSED flag set, mirroring the
// write-side rejection.
func TestReadCompressedEnvelopeRejected(t *testing.T) {
	// Construct an envelope by hand: 1 byte flags (0x01 = compressed) +
	// 4 bytes BE length + payload.
	header := []byte{connectFlagCompressed, 0, 0, 0, 7}
	payload := []byte(`{"a":1}`)
	buf := bytes.NewBuffer(append(header, payload...))

	_, _, err := readConnectEnvelope(buf, 1024)
	if err == nil {
		t.Fatal("expected error when reading compressed envelope")
	}
	if !strings.Contains(err.Error(), "compress") {
		t.Errorf("error message %q should mention compression", err.Error())
	}
}

// TestServerStreamingCompressedFrameRejected exercises the invoker end-to-end
// against a server that sends a compressed frame, verifying that the streaming
// invoker surfaces the rejection as a terminal stream error.
func TestServerStreamingCompressedFrameRejected(t *testing.T) {
	ctx := testContext(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/connect+json")
		w.WriteHeader(http.StatusOK)
		// Manually write a compressed envelope. The invoker must reject it.
		header := []byte{connectFlagCompressed, 0, 0, 0, 7}
		_, _ = w.Write(header)
		_, _ = w.Write([]byte(`{"a":1}`))
		// End-stream envelope (won't be reached because the read fails).
		_ = writeConnectEnvelope(w, connectFlagEndStream, []byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	inv := invokeWith(t, ctx, NewInvoker(), streamingArgs(srv.URL), map[string]any{"source": "test"})

	ierr := mustTerminalError(t, ctx, inv, openbindings.ErrCodeStreamError)
	if !strings.Contains(ierr.Message, "compress") {
		t.Errorf("error message = %q, want to mention compression", ierr.Message)
	}
}

// TestInvokeBinding_NoInputConvention verifies that an operation-layer call
// for an operation declaring no input (Binding set, InputSchema nil)
// dispatches an empty request without waiting for a write. The caller writes
// nothing and never closes; a missing convention would park forever.
func TestInvokeBinding_NoInputConvention(t *testing.T) {
	ctx := testContext(t)
	srv := fakeConnectServer(t, http.StatusOK, `{"id":"","name":"ok"}`)

	args := unaryArgs(srv.URL, testProto, "testpkg.TestService/GetItem")
	args.Binding = &openbindings.BindingEntry{Operation: "getItem", Source: "s", Ref: "testpkg.TestService/GetItem"}
	// InputSchema deliberately nil → no-input operation.

	inv := NewInvoker().InvokeBinding(ctx, args)
	got, err := openbindings.Single[any](ctx, inv.Outputs())
	if err != nil {
		t.Fatalf("no-input convention should dispatch without a write: %v", err)
	}
	if data, ok := got.(map[string]any); !ok || data["name"] != "ok" {
		t.Fatalf("unexpected output: %v", got)
	}
}

// TestNewInvokerWithClient verifies that an Invoker created with a custom
// HTTP client uses that client for outbound requests.
func TestNewInvokerWithClient(t *testing.T) {
	ctx := testContext(t)
	var requestCount int
	custom := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		requestCount++
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"abc","name":"hello"}`)),
			Request:    req,
		}, nil
	})}

	inv := invokeWith(t, ctx, NewInvokerWithClient(custom),
		unaryArgs("http://example.test", testProto, "testpkg.TestService/GetItem"),
		map[string]any{"id": "abc"})
	got, err := openbindings.Single[any](ctx, inv.Outputs())
	if err != nil {
		t.Fatalf("Single: %v", err)
	}
	if requestCount != 1 {
		t.Errorf("expected custom transport to be called exactly once, got %d", requestCount)
	}
	if data, ok := got.(map[string]any); !ok || data["id"] != "abc" {
		t.Errorf("unexpected response through custom client: %+v", got)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
