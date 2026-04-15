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

// fakeConnectServer returns an httptest.Server that responds to Connect-protocol
// POSTs with a canned JSON response. The handler is invoked once per request.
func fakeConnectServer(t *testing.T, statusCode int, responseBody string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
}

func drainOne(t *testing.T, ch <-chan openbindings.StreamEvent) openbindings.StreamEvent {
	t.Helper()
	ev, ok := <-ch
	if !ok {
		t.Fatal("expected one event, got closed channel")
	}
	if extra, ok := <-ch; ok {
		t.Fatalf("expected channel to be closed after one event, got: %+v", extra)
	}
	return ev
}

func TestIntegration_ExecuteBinding_Success(t *testing.T) {
	srv := fakeConnectServer(t, http.StatusOK, `{"id":"abc","name":"hello"}`)
	defer srv.Close()

	exec := NewExecutor()
	ch, err := exec.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{
			Format:   FormatToken,
			Location: srv.URL,
			Content:  testProto,
		},
		Ref:   "testpkg.TestService/GetItem",
		Input: map[string]any{"id": "abc"},
	})
	if err != nil {
		t.Fatalf("ExecuteBinding error: %v", err)
	}

	ev := drainOne(t, ch)
	if ev.Error != nil {
		t.Fatalf("unexpected error: %+v", ev.Error)
	}
	if ev.Status != http.StatusOK {
		t.Errorf("status = %d, want 200", ev.Status)
	}
	data, ok := ev.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map data, got %T", ev.Data)
	}
	if data["id"] != "abc" || data["name"] != "hello" {
		t.Errorf("unexpected response data: %+v", data)
	}
}

func TestIntegration_ExecuteBinding_HTTPError(t *testing.T) {
	srv := fakeConnectServer(t, http.StatusInternalServerError, `{"code":"internal","message":"boom"}`)
	defer srv.Close()

	exec := NewExecutor()
	ch, err := exec.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{
			Format:   FormatToken,
			Location: srv.URL,
			Content:  testProto,
		},
		Ref:   "testpkg.TestService/GetItem",
		Input: map[string]any{"id": "abc"},
	})
	if err != nil {
		t.Fatalf("ExecuteBinding error: %v", err)
	}

	ev := drainOne(t, ch)
	if ev.Error == nil {
		t.Fatal("expected error event for 500 response")
	}
	if ev.Error.Code != openbindings.ErrCodeExecutionFailed {
		t.Errorf("error code = %q, want %q", ev.Error.Code, openbindings.ErrCodeExecutionFailed)
	}
}

func TestIntegration_ExecuteBinding_AuthRequired(t *testing.T) {
	srv := fakeConnectServer(t, http.StatusUnauthorized, `{"code":"unauthenticated","message":"need token"}`)
	defer srv.Close()

	exec := NewExecutor()
	ch, err := exec.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{
			Format:   FormatToken,
			Location: srv.URL,
			Content:  testProto,
		},
		Ref:   "testpkg.TestService/GetItem",
		Input: map[string]any{"id": "abc"},
	})
	if err != nil {
		t.Fatalf("ExecuteBinding error: %v", err)
	}

	ev := drainOne(t, ch)
	if ev.Error == nil {
		t.Fatal("expected error event for 401 response")
	}
	if ev.Error.Code != openbindings.ErrCodeAuthRequired {
		t.Errorf("error code = %q, want %q", ev.Error.Code, openbindings.ErrCodeAuthRequired)
	}
}

func TestIntegration_ExecuteBinding_BearerTokenSent(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"abc","name":"hello"}`)
	}))
	defer srv.Close()

	exec := NewExecutor()
	ch, err := exec.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{
			Format:   FormatToken,
			Location: srv.URL,
			Content:  testProto,
		},
		Ref:     "testpkg.TestService/GetItem",
		Input:   map[string]any{"id": "abc"},
		Context: map[string]any{"bearerToken": "secret-123"},
	})
	if err != nil {
		t.Fatalf("ExecuteBinding error: %v", err)
	}

	_ = drainOne(t, ch)
	if gotAuth != "Bearer secret-123" {
		t.Errorf("Authorization header = %q, want %q", gotAuth, "Bearer secret-123")
	}
}

func TestIntegration_ExecuteBinding_InvalidRef(t *testing.T) {
	exec := NewExecutor()
	ch, err := exec.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{
			Format:   FormatToken,
			Location: "http://localhost:1",
			Content:  testProto,
		},
		Ref:   "not-a-valid-ref",
		Input: map[string]any{},
	})
	if err != nil {
		t.Fatalf("ExecuteBinding error: %v", err)
	}

	ev := drainOne(t, ch)
	if ev.Error == nil {
		t.Fatal("expected error event for malformed ref")
	}
	if ev.Error.Code != openbindings.ErrCodeInvalidRef {
		t.Errorf("error code = %q, want %q", ev.Error.Code, openbindings.ErrCodeInvalidRef)
	}
}

func TestIntegration_ExecuteBinding_RequestBodyMatchesInput(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"abc","name":"hello"}`)
	}))
	defer srv.Close()

	exec := NewExecutor()
	ch, err := exec.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{
			Format:   FormatToken,
			Location: srv.URL,
			Content:  testProto,
		},
		Ref:   "testpkg.TestService/GetItem",
		Input: map[string]any{"id": "xyz"},
	})
	if err != nil {
		t.Fatalf("ExecuteBinding error: %v", err)
	}
	_ = drainOne(t, ch)

	var sent map[string]any
	if err := json.Unmarshal([]byte(gotBody), &sent); err != nil {
		t.Fatalf("server received non-JSON body %q: %v", gotBody, err)
	}
	if sent["id"] != "xyz" {
		t.Errorf("body id = %v, want xyz", sent["id"])
	}
}

func TestIntegration_ExecuteBinding_ConnectFailed(t *testing.T) {
	exec := NewExecutor()
	// Use an unreachable address. Skip if we can't bind a port to test against;
	// localhost:1 is reserved and should refuse connections on most systems.
	ch, err := exec.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{
			Format:   FormatToken,
			Location: "http://127.0.0.1:1",
			Content:  testProto,
		},
		Ref:   "testpkg.TestService/GetItem",
		Input: map[string]any{"id": "abc"},
	})
	if err != nil {
		t.Fatalf("ExecuteBinding error: %v", err)
	}

	ev := drainOne(t, ch)
	if ev.Error == nil {
		t.Fatal("expected error event for unreachable host")
	}
	if ev.Error.Code != openbindings.ErrCodeConnectFailed {
		t.Errorf("error code = %q, want %q (got message: %q)",
			ev.Error.Code, openbindings.ErrCodeConnectFailed, ev.Error.Message)
	}
}

func TestIntegration_ExecuteBinding_RedirectLimit(t *testing.T) {
	// Build a server that always redirects to itself, to verify the
	// CheckRedirect cap kicks in instead of looping forever.
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()

	exec := NewExecutor()
	ch, err := exec.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{
			Format:   FormatToken,
			Location: srv.URL,
			Content:  testProto,
		},
		Ref:   "testpkg.TestService/GetItem",
		Input: map[string]any{"id": "abc"},
	})
	if err != nil {
		t.Fatalf("ExecuteBinding error: %v", err)
	}

	ev := drainOne(t, ch)
	if ev.Error == nil {
		t.Fatal("expected redirect loop to error")
	}
	if !strings.Contains(ev.Error.Message, "redirect") {
		t.Errorf("error message = %q, expected to mention redirect", ev.Error.Message)
	}
}

// streamingProto declares a server-streaming method so the executor takes the
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

// fakeConnectStreamingServer returns an httptest.Server that responds to
// Connect streaming requests with `application/connect+json` and writes a
// fixed sequence of envelope-framed messages followed by an end-stream
// envelope. The number of data envelopes and any error in the end-stream
// envelope are configurable.
func fakeConnectStreamingServer(t *testing.T, dataMessages []string, endStreamErrorJSON string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		if endStreamErrorJSON != "" {
			endPayload = []byte(endStreamErrorJSON)
		}
		if err := writeConnectEnvelope(w, connectFlagEndStream, endPayload); err != nil {
			t.Errorf("write end-stream envelope: %v", err)
		}
	}))
}

func TestIntegration_ExecuteBinding_ServerStreaming_Success(t *testing.T) {
	srv := fakeConnectStreamingServer(t, []string{
		`{"message":"line 1","timestamp":1}`,
		`{"message":"line 2","timestamp":2}`,
		`{"message":"line 3","timestamp":3}`,
	}, "")
	defer srv.Close()

	exec := NewExecutor()
	ch, err := exec.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{
			Format:   FormatToken,
			Location: srv.URL,
			Content:  streamingProto,
		},
		Ref:   "testpkg.LogsService/TailLogs",
		Input: map[string]any{"source": "test"},
	})
	if err != nil {
		t.Fatalf("ExecuteBinding error: %v", err)
	}

	var events []openbindings.StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}

	if len(events) != 3 {
		t.Fatalf("expected 3 stream events, got %d", len(events))
	}
	for i, ev := range events {
		if ev.Error != nil {
			t.Fatalf("event %d: unexpected error: %+v", i, ev.Error)
		}
		data, ok := ev.Data.(map[string]any)
		if !ok {
			t.Fatalf("event %d: expected map data, got %T", i, ev.Data)
		}
		wantMessage := []string{"line 1", "line 2", "line 3"}[i]
		if data["message"] != wantMessage {
			t.Errorf("event %d: message = %v, want %s", i, data["message"], wantMessage)
		}
	}
}

func TestIntegration_ExecuteBinding_ServerStreaming_EmptyStream(t *testing.T) {
	srv := fakeConnectStreamingServer(t, nil, "")
	defer srv.Close()

	exec := NewExecutor()
	ch, err := exec.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{
			Format:   FormatToken,
			Location: srv.URL,
			Content:  streamingProto,
		},
		Ref:   "testpkg.LogsService/TailLogs",
		Input: map[string]any{"source": "test"},
	})
	if err != nil {
		t.Fatalf("ExecuteBinding error: %v", err)
	}

	var events []openbindings.StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}
	if len(events) != 0 {
		t.Fatalf("expected 0 events from empty stream, got %d", len(events))
	}
}

func TestIntegration_ExecuteBinding_ServerStreaming_EndStreamError(t *testing.T) {
	// Server sends one data event then ends the stream with an error.
	srv := fakeConnectStreamingServer(
		t,
		[]string{`{"message":"first","timestamp":1}`},
		`{"error":{"code":"internal","message":"backend exploded"}}`,
	)
	defer srv.Close()

	exec := NewExecutor()
	ch, err := exec.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{
			Format:   FormatToken,
			Location: srv.URL,
			Content:  streamingProto,
		},
		Ref:   "testpkg.LogsService/TailLogs",
		Input: map[string]any{"source": "test"},
	})
	if err != nil {
		t.Fatalf("ExecuteBinding error: %v", err)
	}

	var events []openbindings.StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}

	if len(events) != 2 {
		t.Fatalf("expected 2 events (1 data + 1 error), got %d", len(events))
	}
	if events[0].Error != nil {
		t.Errorf("first event should be data, got error: %+v", events[0].Error)
	}
	if events[1].Error == nil {
		t.Fatal("second event should be an error")
	}
	if !strings.Contains(events[1].Error.Message, "backend exploded") {
		t.Errorf("error message = %q, want to contain 'backend exploded'", events[1].Error.Message)
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
// refuses to emit a frame with the COMPRESSED flag set, since the executor
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

// TestServerStreamingCompressedFrameRejected exercises the executor end-to-end
// against a server that sends a compressed frame, verifying that the streaming
// executor surfaces the rejection as a stream error event.
func TestServerStreamingCompressedFrameRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/connect+json")
		w.WriteHeader(http.StatusOK)
		// Manually write a compressed envelope. The executor must reject it.
		header := []byte{connectFlagCompressed, 0, 0, 0, 7}
		_, _ = w.Write(header)
		_, _ = w.Write([]byte(`{"a":1}`))
		// End-stream envelope (won't be reached because the read fails).
		_ = writeConnectEnvelope(w, connectFlagEndStream, []byte(`{}`))
	}))
	defer srv.Close()

	exec := NewExecutor()
	ch, err := exec.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{
			Format:   FormatToken,
			Location: srv.URL,
			Content:  streamingProto,
		},
		Ref:   "testpkg.LogsService/TailLogs",
		Input: map[string]any{"source": "test"},
	})
	if err != nil {
		t.Fatalf("ExecuteBinding error: %v", err)
	}

	var events []openbindings.StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}

	if len(events) != 1 {
		t.Fatalf("expected exactly 1 event (the rejection error), got %d", len(events))
	}
	if events[0].Error == nil {
		t.Fatal("expected stream error event for compressed frame")
	}
	if !strings.Contains(events[0].Error.Message, "compress") {
		t.Errorf("error message = %q, want to mention compression", events[0].Error.Message)
	}
}
