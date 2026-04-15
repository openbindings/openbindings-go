package asyncapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	openbindings "github.com/openbindings/openbindings-go"
	"nhooyr.io/websocket"
)

// TestWSPool_MultipleSendsShareOneConnection verifies that multiple send
// operations to the same channel reuse a single WebSocket connection rather
// than opening a new one each time.
func TestWSPool_MultipleSendsShareOneConnection(t *testing.T) {
	var upgradeCount int32

	srv := wsTestServer(t, func(ctx context.Context, conn *websocket.Conn, r *http.Request) {
		atomic.AddInt32(&upgradeCount, 1)

		// Read messages until the connection closes.
		for {
			readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			_, msg, err := conn.Read(readCtx)
			cancel()
			if err != nil {
				return
			}
			// Echo back each message as a reply.
			var payload map[string]any
			if json.Unmarshal(msg, &payload) == nil {
				_ = writeWSJSON(ctx, conn, map[string]any{"echo": payload})
			}
		}
	})
	defer srv.Close()

	doc := makeWSAsyncAPISpec(srv.URL, SecurityScheme{Type: "http", Scheme: "bearer"})
	// Add Reply so sendWS reads one response per send.
	pubOp := doc.Operations["publish"]
	pubOp.Reply = &OperationReply{}
	doc.Operations["publish"] = pubOp

	exec := NewExecutor()
	defer exec.Close()

	// Send 5 messages sequentially. All should share one connection.
	for i := 0; i < 5; i++ {
		ch, err := exec.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
			Source: openbindings.BindingExecutionSource{
				Format:  FormatToken,
				Content: doc,
			},
			Ref:     "#/operations/publish",
			Input:   map[string]any{"seq": i},
			Context: map[string]any{"bearerToken": "tok"},
		})
		if err != nil {
			t.Fatalf("send %d: ExecuteBinding error: %v", i, err)
		}
		events := drainStream(ch)
		if len(events) != 1 {
			t.Fatalf("send %d: expected 1 event, got %d", i, len(events))
		}
		if events[0].Error != nil {
			t.Fatalf("send %d: unexpected error: %s", i, events[0].Error.Message)
		}
	}

	count := atomic.LoadInt32(&upgradeCount)
	if count != 1 {
		t.Errorf("expected 1 WebSocket upgrade, got %d", count)
	}
}

// TestWSPool_ConcurrentSendsShareOneConnection verifies that concurrent send
// operations to the same channel all share one WebSocket connection. The
// creating-wait mechanism ensures only one goroutine dials.
func TestWSPool_ConcurrentSendsShareOneConnection(t *testing.T) {
	var upgradeCount int32

	srv := wsTestServer(t, func(ctx context.Context, conn *websocket.Conn, r *http.Request) {
		atomic.AddInt32(&upgradeCount, 1)

		for {
			readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			_, msg, err := conn.Read(readCtx)
			cancel()
			if err != nil {
				return
			}
			var payload map[string]any
			if json.Unmarshal(msg, &payload) == nil {
				_ = writeWSJSON(ctx, conn, map[string]any{"echo": payload})
			}
		}
	})
	defer srv.Close()

	doc := makeWSAsyncAPISpec(srv.URL, SecurityScheme{Type: "http", Scheme: "bearer"})
	pubOp := doc.Operations["publish"]
	pubOp.Reply = &OperationReply{}
	doc.Operations["publish"] = pubOp

	exec := NewExecutor()
	defer exec.Close()

	const concurrency = 10
	var wg sync.WaitGroup
	errors := make(chan error, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(seq int) {
			defer wg.Done()
			ch, err := exec.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
				Source: openbindings.BindingExecutionSource{
					Format:  FormatToken,
					Content: doc,
				},
				Ref:     "#/operations/publish",
				Input:   map[string]any{"seq": seq},
				Context: map[string]any{"bearerToken": "tok"},
			})
			if err != nil {
				errors <- err
				return
			}
			events := drainStream(ch)
			if len(events) != 1 {
				errors <- fmt.Errorf("send %d: expected 1 event, got %d", seq, len(events))
				return
			}
			if events[0].Error != nil {
				errors <- fmt.Errorf("send %d: error: %s", seq, events[0].Error.Message)
			}
		}(i)
	}

	wg.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}

	count := atomic.LoadInt32(&upgradeCount)
	if count != 1 {
		t.Errorf("expected 1 WebSocket upgrade for %d concurrent sends, got %d", concurrency, count)
	}
}

// TestWSPool_IdleTimeoutEviction verifies that a pooled WebSocket connection
// is closed and evicted after the idle timeout fires when no operations hold
// a reference.
func TestWSPool_IdleTimeoutEviction(t *testing.T) {
	var upgradeCount int32

	srv := wsTestServer(t, func(ctx context.Context, conn *websocket.Conn, r *http.Request) {
		atomic.AddInt32(&upgradeCount, 1)

		for {
			readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			_, msg, err := conn.Read(readCtx)
			cancel()
			if err != nil {
				return
			}
			var payload map[string]any
			if json.Unmarshal(msg, &payload) == nil {
				_ = writeWSJSON(ctx, conn, map[string]any{"echo": payload})
			}
		}
	})
	defer srv.Close()

	doc := makeWSAsyncAPISpec(srv.URL, SecurityScheme{Type: "http", Scheme: "bearer"})
	pubOp := doc.Operations["publish"]
	pubOp.Reply = &OperationReply{}
	doc.Operations["publish"] = pubOp

	exec := NewExecutor()
	defer exec.Close()

	// Use a very short idle timeout for testing.
	exec.wsPool.idleTimeout = 50 * time.Millisecond

	// First send: creates the connection.
	ch, err := exec.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{
			Format:  FormatToken,
			Content: doc,
		},
		Ref:     "#/operations/publish",
		Input:   map[string]any{"msg": "first"},
		Context: map[string]any{"bearerToken": "tok"},
	})
	if err != nil {
		t.Fatalf("first send error: %v", err)
	}
	events := drainStream(ch)
	if len(events) != 1 || events[0].Error != nil {
		t.Fatalf("first send: unexpected result: events=%d, err=%v", len(events), events)
	}

	if c := atomic.LoadInt32(&upgradeCount); c != 1 {
		t.Fatalf("expected 1 upgrade after first send, got %d", c)
	}

	// Wait for the idle timeout to fire and evict the connection.
	time.Sleep(200 * time.Millisecond)

	// Verify the pool is empty.
	exec.wsPool.mu.Lock()
	poolSize := len(exec.wsPool.conns)
	exec.wsPool.mu.Unlock()
	if poolSize != 0 {
		t.Errorf("expected empty pool after idle timeout, got %d connections", poolSize)
	}

	// Second send: should create a new connection.
	ch, err = exec.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{
			Format:  FormatToken,
			Content: doc,
		},
		Ref:     "#/operations/publish",
		Input:   map[string]any{"msg": "second"},
		Context: map[string]any{"bearerToken": "tok"},
	})
	if err != nil {
		t.Fatalf("second send error: %v", err)
	}
	events = drainStream(ch)
	if len(events) != 1 || events[0].Error != nil {
		t.Fatalf("second send: unexpected result: events=%d, err=%v", len(events), events)
	}

	if c := atomic.LoadInt32(&upgradeCount); c != 2 {
		t.Errorf("expected 2 upgrades (one per connection), got %d", c)
	}
}

// TestWSPool_FireAndForgetSend verifies that a send operation without a Reply
// defined returns an immediately-closed channel (fire-and-forget). The message
// is still sent to the server.
func TestWSPool_FireAndForgetSend(t *testing.T) {
	received := make(chan map[string]any, 1)

	srv := wsTestServer(t, func(ctx context.Context, conn *websocket.Conn, r *http.Request) {
		first, err := readWSJSON(ctx, conn)
		if err != nil {
			return
		}
		received <- first
	})
	defer srv.Close()

	doc := makeWSAsyncAPISpec(srv.URL, SecurityScheme{Type: "http", Scheme: "bearer"})
	// No Reply on the publish operation -- fire-and-forget.

	exec := NewExecutor()
	defer exec.Close()

	ch, err := exec.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{
			Format:  FormatToken,
			Content: doc,
		},
		Ref:     "#/operations/publish",
		Input:   map[string]any{"command": "fire"},
		Context: map[string]any{"bearerToken": "tok"},
	})
	if err != nil {
		t.Fatalf("ExecuteBinding error: %v", err)
	}

	// Channel should close immediately with zero events.
	events := drainStream(ch)
	if len(events) != 0 {
		t.Errorf("fire-and-forget send should yield 0 events, got %d", len(events))
	}

	// Server should still receive the message.
	select {
	case msg := <-received:
		if msg["command"] != "fire" {
			t.Errorf("server received command = %v, want %q", msg["command"], "fire")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not receive the fire-and-forget message within 3s")
	}
}

// TestWSPool_ReceiveStillUsesDedicatedConnection verifies that receive-action
// operations still use subscribeWS (dedicated connection per subscription),
// not the connection pool.
func TestWSPool_ReceiveStillUsesDedicatedConnection(t *testing.T) {
	var upgradeCount int32

	srv := wsTestServer(t, func(ctx context.Context, conn *websocket.Conn, r *http.Request) {
		atomic.AddInt32(&upgradeCount, 1)
		// Drain the initial message from subscribeWS.
		_, _ = readWSJSON(ctx, conn)
		// Send one event and close.
		_ = writeWSJSON(ctx, conn, map[string]any{"event": "hello"})
	})
	defer srv.Close()

	doc := makeWSAsyncAPISpec(srv.URL, SecurityScheme{Type: "http", Scheme: "bearer"})

	exec := NewExecutor()
	defer exec.Close()

	// Two receive subscriptions should each open their own connection.
	for i := 0; i < 2; i++ {
		ch, err := exec.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
			Source: openbindings.BindingExecutionSource{
				Format:  FormatToken,
				Content: doc,
			},
			Ref:     "#/operations/subscribe",
			Context: map[string]any{"bearerToken": "tok"},
		})
		if err != nil {
			t.Fatalf("receive %d error: %v", i, err)
		}
		events := drainStream(ch)
		if len(events) == 0 {
			t.Fatalf("receive %d: expected at least 1 event", i)
		}
	}

	count := atomic.LoadInt32(&upgradeCount)
	if count != 2 {
		t.Errorf("expected 2 separate WebSocket upgrades for 2 receive subscriptions, got %d", count)
	}
}

// TestWSPool_CloseClosesAllConnections verifies that Executor.Close() closes
// all pooled WebSocket connections.
func TestWSPool_CloseClosesAllConnections(t *testing.T) {
	srv := wsTestServer(t, func(ctx context.Context, conn *websocket.Conn, r *http.Request) {
		for {
			readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			_, msg, err := conn.Read(readCtx)
			cancel()
			if err != nil {
				return
			}
			var payload map[string]any
			if json.Unmarshal(msg, &payload) == nil {
				_ = writeWSJSON(ctx, conn, map[string]any{"echo": payload})
			}
		}
	})
	defer srv.Close()

	doc := makeWSAsyncAPISpec(srv.URL, SecurityScheme{Type: "http", Scheme: "bearer"})
	pubOp := doc.Operations["publish"]
	pubOp.Reply = &OperationReply{}
	doc.Operations["publish"] = pubOp

	exec := NewExecutor()

	// Send a message to create a pooled connection.
	ch, err := exec.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{
			Format:  FormatToken,
			Content: doc,
		},
		Ref:     "#/operations/publish",
		Input:   map[string]any{"msg": "hi"},
		Context: map[string]any{"bearerToken": "tok"},
	})
	if err != nil {
		t.Fatalf("ExecuteBinding error: %v", err)
	}
	_ = drainStream(ch)

	// Pool should have one connection.
	exec.wsPool.mu.Lock()
	poolSize := len(exec.wsPool.conns)
	exec.wsPool.mu.Unlock()
	if poolSize != 1 {
		t.Fatalf("expected 1 pooled connection, got %d", poolSize)
	}

	// Close the executor.
	exec.Close()

	// Pool should be empty.
	exec.wsPool.mu.Lock()
	poolSize = len(exec.wsPool.conns)
	exec.wsPool.mu.Unlock()
	if poolSize != 0 {
		t.Errorf("expected 0 pooled connections after Close(), got %d", poolSize)
	}
}
