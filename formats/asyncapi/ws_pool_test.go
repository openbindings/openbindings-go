package asyncapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	openbindings "github.com/openbindings/openbindings-go"
	"nhooyr.io/websocket"
)

// sendOnce drives one ws publish invocation: write one message, close input,
// and assert the fire-and-forget completion (zero outputs, clean EOF).
func sendOnce(t *testing.T, binv *Invoker, source openbindings.InvocationSource, bindCtx map[string]any, msg map[string]any) {
	t.Helper()
	call := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source:  source,
		Ref:     "#/operations/publish",
		Context: bindCtx,
	})
	if err := call.Write(bg(), msg); err != nil {
		t.Fatal(err)
	}
	if err := call.Close(); err != nil {
		t.Fatal(err)
	}
	vals, err := drainOutputs(t, call)
	if err != nil {
		t.Fatalf("publish failed: %v", err)
	}
	if len(vals) != 0 {
		t.Fatalf("a ws publish yields no outputs, got %v", vals)
	}
}

// frameCollector is a ws exchange that records every frame it reads.
func frameCollector(upgrades *atomic.Int32, frames chan<- map[string]any) func(ctx context.Context, conn *websocket.Conn, r *http.Request) {
	return func(ctx context.Context, conn *websocket.Conn, r *http.Request) {
		upgrades.Add(1)
		for {
			msg, err := readWSJSON(ctx, conn)
			if err != nil {
				return
			}
			select {
			case frames <- msg:
			default:
			}
		}
	}
}

func collectFrames(t *testing.T, frames <-chan map[string]any, n int) []map[string]any {
	t.Helper()
	out := make([]map[string]any, 0, n)
	timeout := time.After(5 * time.Second)
	for len(out) < n {
		select {
		case f := <-frames:
			out = append(out, f)
		case <-timeout:
			t.Fatalf("expected %d frames, got %d", n, len(out))
		}
	}
	return out
}

// TestWSPool_SequentialSendsShareOneConnection verifies that consecutive
// publish invocations to the same channel reuse a single WebSocket
// connection rather than dialing per invocation.
func TestWSPool_SequentialSendsShareOneConnection(t *testing.T) {
	var upgrades atomic.Int32
	frames := make(chan map[string]any, 16)
	srv := wsTestServer(t, frameCollector(&upgrades, frames))

	binv := NewInvoker()
	defer binv.Close()
	source := wsSource(srv, &SecurityScheme{Type: "http", Scheme: "bearer"})

	for i := 0; i < 5; i++ {
		sendOnce(t, binv, source, map[string]any{"bearerToken": "tok"}, map[string]any{"seq": i})
	}

	got := collectFrames(t, frames, 5)
	for i, f := range got {
		if seq, _ := f["seq"].(float64); int(seq) != i {
			t.Errorf("frame %d: seq = %v, want %d", i, f["seq"], i)
		}
	}
	if c := upgrades.Load(); c != 1 {
		t.Errorf("expected 1 WebSocket upgrade for 5 sequential sends, got %d", c)
	}
}

// TestWSPool_ConcurrentSendsShareOneConnection verifies that concurrent
// publish invocations all share one connection: the creating-wait mechanism
// ensures only one goroutine dials.
func TestWSPool_ConcurrentSendsShareOneConnection(t *testing.T) {
	var upgrades atomic.Int32
	frames := make(chan map[string]any, 32)
	srv := wsTestServer(t, frameCollector(&upgrades, frames))

	binv := NewInvoker()
	defer binv.Close()
	source := wsSource(srv, &SecurityScheme{Type: "http", Scheme: "bearer"})

	const concurrency = 10
	var wg sync.WaitGroup
	errs := make(chan error, concurrency)
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(seq int) {
			defer wg.Done()
			call := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
				Source:  source,
				Ref:     "#/operations/publish",
				Context: map[string]any{"bearerToken": "tok"},
			})
			if err := call.Write(bg(), map[string]any{"seq": seq}); err != nil {
				errs <- err
				return
			}
			if err := call.Close(); err != nil {
				errs <- err
				return
			}
			out := call.Outputs()
			if _, err := out.Read(shortCtx(t)); err != io.EOF {
				errs <- fmt.Errorf("send %d: expected EOF, got %v", seq, err)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	collectFrames(t, frames, concurrency)
	if c := upgrades.Load(); c != 1 {
		t.Errorf("expected 1 WebSocket upgrade for %d concurrent sends, got %d", concurrency, c)
	}
}

// TestWSPool_ClientStreamingFrames verifies the send mapping: every input is
// one frame in write order, auth rides the upgrade request (never the
// message body), and closing input completes the call with zero outputs.
func TestWSPool_ClientStreamingFrames(t *testing.T) {
	var upgrades atomic.Int32
	var authMu sync.Mutex
	var upgradeAuth string
	frames := make(chan map[string]any, 16)
	srv := wsTestServer(t, func(ctx context.Context, conn *websocket.Conn, r *http.Request) {
		upgrades.Add(1)
		authMu.Lock()
		upgradeAuth = r.Header.Get("Authorization")
		authMu.Unlock()
		for {
			msg, err := readWSJSON(ctx, conn)
			if err != nil {
				return
			}
			frames <- msg
		}
	})

	binv := NewInvoker()
	defer binv.Close()

	call := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source:  wsSource(srv, &SecurityScheme{Type: "http", Scheme: "bearer"}),
		Ref:     "#/operations/publish",
		Context: map[string]any{"bearerToken": "tok"},
	})
	for i := 1; i <= 3; i++ {
		if err := call.Write(bg(), map[string]any{"chunk": i}); err != nil {
			t.Fatal(err)
		}
	}
	if err := call.Close(); err != nil {
		t.Fatal(err)
	}
	vals, err := drainOutputs(t, call)
	if err != nil || len(vals) != 0 {
		t.Fatalf("expected clean zero-output completion, got %v err=%v", vals, err)
	}

	got := collectFrames(t, frames, 3)
	for i, f := range got {
		if chunk, _ := f["chunk"].(float64); int(chunk) != i+1 {
			t.Errorf("frame %d: chunk = %v, want %d", i, f["chunk"], i+1)
		}
		if _, hasToken := f["bearerToken"]; hasToken {
			t.Error("publish frames must not carry bearerToken in the body")
		}
	}
	authMu.Lock()
	defer authMu.Unlock()
	if upgradeAuth != "Bearer tok" {
		t.Errorf("upgrade Authorization = %q, want %q", upgradeAuth, "Bearer tok")
	}
	if c := upgrades.Load(); c != 1 {
		t.Errorf("expected 1 upgrade, got %d", c)
	}
}

// TestWSPool_DifferentCredentialsNeverShareConnection verifies the pool key
// includes credential identity: invocations with different bearer tokens must
// not share an authenticated socket (cross-tenant credential leak), while a
// repeat invocation with an already-seen token reuses its socket.
func TestWSPool_DifferentCredentialsNeverShareConnection(t *testing.T) {
	var upgrades atomic.Int32
	frames := make(chan map[string]any, 16)
	srv := wsTestServer(t, frameCollector(&upgrades, frames))

	binv := NewInvoker()
	defer binv.Close()
	source := wsSource(srv, &SecurityScheme{Type: "http", Scheme: "bearer"})

	sendOnce(t, binv, source, map[string]any{"bearerToken": "tenant-a"}, map[string]any{"seq": 0})
	sendOnce(t, binv, source, map[string]any{"bearerToken": "tenant-b"}, map[string]any{"seq": 1})
	if c := upgrades.Load(); c != 2 {
		t.Fatalf("different bearer tokens must not share a socket: got %d upgrades, want 2", c)
	}

	// Same token as the first call: must reuse tenant-a's socket.
	sendOnce(t, binv, source, map[string]any{"bearerToken": "tenant-a"}, map[string]any{"seq": 2})
	if c := upgrades.Load(); c != 2 {
		t.Errorf("same bearer token must share a socket: got %d upgrades, want 2", c)
	}
	collectFrames(t, frames, 3)
}

// TestWSPool_SendAndReceiveShareConnection verifies the AsyncAPI
// two-operation pattern the pool exists for: a receive subscription and a
// send publish on the same channel share one socket, so server state keyed
// by connection (e.g. subscriptions) is visible to both.
func TestWSPool_SendAndReceiveShareConnection(t *testing.T) {
	var upgrades atomic.Int32
	srv := wsTestServer(t, func(ctx context.Context, conn *websocket.Conn, r *http.Request) {
		upgrades.Add(1)
		// Announce, then echo every frame back as a data event.
		_ = writeWSJSON(ctx, conn, map[string]any{"hello": true})
		for {
			msg, err := readWSJSON(ctx, conn)
			if err != nil {
				return
			}
			_ = writeWSJSON(ctx, conn, msg)
		}
	})

	binv := NewInvoker()
	defer binv.Close()
	source := wsSource(srv, nil)

	sub := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source: source,
		Ref:    "#/operations/subscribe",
	})
	out := sub.Outputs()

	// The hello event proves the subscription is live (socket dialed and
	// listener registered) before the publish goes out.
	hello, err := out.Read(shortCtx(t))
	if err != nil {
		t.Fatal(err)
	}
	if hello.(map[string]any)["hello"] != true {
		t.Fatalf("got %v", hello)
	}

	sendOnce(t, binv, source, nil, map[string]any{"n": float64(1)})

	echoed, err := out.Read(shortCtx(t))
	if err != nil {
		t.Fatal(err)
	}
	if echoed.(map[string]any)["n"] != float64(1) {
		t.Fatalf("subscription must observe the publish echoed on the shared socket, got %v", echoed)
	}

	if c := upgrades.Load(); c != 1 {
		t.Errorf("expected send and receive to share 1 upgrade, got %d", c)
	}
	out.Stop()
}

// TestWSPool_IdleTimeoutEviction verifies that a pooled connection is closed
// and evicted after the idle timeout fires with no operations holding a
// reference, and that the next invocation dials a fresh one.
func TestWSPool_IdleTimeoutEviction(t *testing.T) {
	var upgrades atomic.Int32
	frames := make(chan map[string]any, 16)
	srv := wsTestServer(t, frameCollector(&upgrades, frames))

	binv := NewInvoker()
	defer binv.Close()
	binv.wsPool.idleTimeout = 50 * time.Millisecond
	source := wsSource(srv, &SecurityScheme{Type: "http", Scheme: "bearer"})

	sendOnce(t, binv, source, map[string]any{"bearerToken": "tok"}, map[string]any{"msg": "first"})
	if c := upgrades.Load(); c != 1 {
		t.Fatalf("expected 1 upgrade after first send, got %d", c)
	}

	// Wait for the idle timeout to fire and evict the connection.
	deadline := time.After(3 * time.Second)
	for {
		binv.wsPool.mu.Lock()
		size := len(binv.wsPool.conns)
		binv.wsPool.mu.Unlock()
		if size == 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("pool not emptied by idle timeout: %d connections", size)
		case <-time.After(10 * time.Millisecond):
		}
	}

	sendOnce(t, binv, source, map[string]any{"bearerToken": "tok"}, map[string]any{"msg": "second"})
	if c := upgrades.Load(); c != 2 {
		t.Errorf("expected 2 upgrades (one per connection), got %d", c)
	}
}

// TestWSPool_CloseClosesAllConnections verifies that Invoker.Close() closes
// and evicts all pooled WebSocket connections.
func TestWSPool_CloseClosesAllConnections(t *testing.T) {
	var upgrades atomic.Int32
	frames := make(chan map[string]any, 16)
	srv := wsTestServer(t, frameCollector(&upgrades, frames))

	binv := NewInvoker()
	source := wsSource(srv, &SecurityScheme{Type: "http", Scheme: "bearer"})

	sendOnce(t, binv, source, map[string]any{"bearerToken": "tok"}, map[string]any{"msg": "hi"})

	binv.wsPool.mu.Lock()
	size := len(binv.wsPool.conns)
	binv.wsPool.mu.Unlock()
	if size != 1 {
		t.Fatalf("expected 1 pooled connection, got %d", size)
	}

	binv.Close()

	binv.wsPool.mu.Lock()
	size = len(binv.wsPool.conns)
	binv.wsPool.mu.Unlock()
	if size != 0 {
		t.Errorf("expected 0 pooled connections after Close(), got %d", size)
	}
}
