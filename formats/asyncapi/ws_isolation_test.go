package asyncapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/coder/websocket"
)

// TestWSPool_WriteCancelDoesNotTearDownSiblings pins C5f: a pooled socket's
// write is DECOUPLED from any per-invocation context, so one invocation
// cancelling mid-frame can never tear down a sibling subscription sharing the
// connection. nhooyr closes the whole connection when a write's context is
// cancelled; before the fix, `send` rode the invocation ctx, so a cancel
// during an in-flight write closed the shared socket and fired a spurious
// ERR_STREAM_ERROR on the innocent sibling.
//
// The server is gated: it announces (proving the subscription is live), then
// holds its reads until released. A multi-megabyte frame guarantees the
// write is genuinely in-flight (blocked on a full send buffer) when the
// caller's context is cancelled — exactly the window the bug needs.
func TestWSPool_WriteCancelDoesNotTearDownSiblings(t *testing.T) {
	release := make(chan struct{})
	srv := wsTestServer(t, func(ctx context.Context, conn *websocket.Conn, r *http.Request) {
		conn.SetReadLimit(32 << 20)
		// Announce so the subscriber proves the socket is live before any
		// publish write goes out.
		if err := writeWSJSON(ctx, conn, map[string]any{"hello": true}); err != nil {
			return
		}
		<-release // hold reads: the pending write stays in-flight
		// Drain the large in-flight frame and discard it (never echoed).
		if _, err := readWSJSON(ctx, conn); err != nil {
			return
		}
		// Echo everything after, so the sibling can observe post-cancel life.
		for {
			msg, err := readWSJSON(ctx, conn)
			if err != nil {
				return
			}
			if err := writeWSJSON(ctx, conn, msg); err != nil {
				return
			}
		}
	})

	binv := NewInvoker()
	defer binv.Close()
	source := wsSource(srv, nil)

	// The sibling subscription (listener A) that must survive.
	sub := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source: source,
		Ref:    "#/operations/subscribe",
	})
	out := sub.Outputs()
	defer out.Stop()

	hello, err := out.Read(shortCtx(t))
	if err != nil {
		t.Fatalf("subscription must go live: %v", err)
	}
	if hello.(map[string]any)["hello"] != true {
		t.Fatalf("expected the announce frame, got %v", hello)
	}

	// Grab the shared pooled connection the subscription dialed.
	pw := onlyPooledConn(t, binv)

	// A large frame blocks the write (server is gated, buffers fill), so the
	// write is genuinely in-flight when we cancel.
	big, _ := json.Marshal(map[string]any{"pad": strings.Repeat("x", 8<<20), "marker": "publish"})

	writeDone := make(chan error, 1)
	pubCtx, cancelPub := context.WithCancel(context.Background())
	go func() { writeDone <- pw.send(pubCtx, big) }()

	// Let the write reach the socket and register with nhooyr's timeout loop,
	// then cancel the publishing invocation mid-write. Before the fix, this
	// closes the shared connection.
	time.Sleep(150 * time.Millisecond)
	cancelPub()
	time.Sleep(100 * time.Millisecond)

	// Release the server: it drains the large frame (completing the detached
	// write) and resumes echoing.
	close(release)
	select {
	case <-writeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the detached write never completed after release")
	}

	// The sibling MUST still be alive: a fresh publish on the shared socket is
	// echoed back to it. If the cancel had torn down the connection, this read
	// returns ERR_STREAM_ERROR instead.
	if err := pw.send(context.Background(), mustJSON(map[string]any{"n": float64(7)})); err != nil {
		t.Fatalf("post-cancel publish on the shared socket failed: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := out.Read(ctx)
	if err != nil {
		t.Fatalf("sibling subscription was torn down by a sibling's cancel (C5f): %v", err)
	}
	if got.(map[string]any)["n"] != float64(7) {
		t.Fatalf("sibling received %v, want the post-cancel echo {n:7}", got)
	}
}

func onlyPooledConn(t *testing.T, binv *Invoker) *pooledWS {
	t.Helper()
	binv.wsPool.mu.Lock()
	defer binv.wsPool.mu.Unlock()
	for _, pw := range binv.wsPool.conns {
		return pw
	}
	t.Fatal("no pooled connection was dialed")
	return nil
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
