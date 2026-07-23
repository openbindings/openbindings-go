package asyncapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	openbindings "github.com/openbindings/openbindings-go"
)

// WS slow-consumer backpressure (spec/binding-specs/asyncapi/openbindings.asyncapi.md, "WS slow-consumer
// backpressure" open point, settled 2026-07-11): the receive path bounds
// undelivered frames between the shared socket reader and one subscription's
// consumer at maxWSBufferedFrames frames or maxWSBufferedBytes in-flight
// bytes, whichever trips first; overflow fails that subscription loudly
// rather than buffering unboundedly, and never touches sibling subscriptions
// sharing the same pooled connection.

// setWSBackpressureBoundsForTest lowers the WS receive backpressure bounds
// for one test, restoring the reference-package defaults on cleanup.
// maxWSBufferedFrames/maxWSBufferedBytes are unexported package vars (not
// part of the public API) so a test can trip a bound deterministically
// without pushing the full frame count / byte volume through a real socket.
func setWSBackpressureBoundsForTest(t *testing.T, frames, bytes int) {
	t.Helper()
	prevFrames, prevBytes := maxWSBufferedFrames, maxWSBufferedBytes
	maxWSBufferedFrames, maxWSBufferedBytes = frames, bytes
	t.Cleanup(func() {
		maxWSBufferedFrames, maxWSBufferedBytes = prevFrames, prevBytes
	})
}

// waitForSoleConnListenerCount polls (never a blind sleep) until the pool's
// one pooled connection has at least n registered listeners, or fails the
// test after a timeout. Every test in this file dials exactly one
// server/credential combination, so there is always at most one pooled
// connection to find.
func waitForSoleConnListenerCount(t *testing.T, pool *wsPool, n int) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		pool.mu.Lock()
		var pw *pooledWS
		for _, v := range pool.conns {
			pw = v
			break
		}
		connCount := len(pool.conns)
		pool.mu.Unlock()

		if pw != nil {
			pw.lmu.Lock()
			count := len(pw.listeners)
			pw.lmu.Unlock()
			if count >= n {
				return
			}
		}
		select {
		case <-deadline:
			t.Fatalf("listener count never reached %d (pool has %d connections)", n, connCount)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// TestWSReceiveBackpressure_FrameCountOverflowFailsSubscription proves the
// frame-count bound: a subscriber that never drains eventually fails loudly
// instead of buffering the flood unboundedly. Written failing-first: against
// the pre-fix unbounded wsSubscription this test does not pass (it times out
// waiting for an *InvocationError that unbounded code never produces) — see
// the FINAL REPORT for the empirical before/after run.
func TestWSReceiveBackpressure_FrameCountOverflowFailsSubscription(t *testing.T) {
	setWSBackpressureBoundsForTest(t, 64, 64*1024*1024)
	floodCount := maxWSBufferedFrames + 200
	floodDone := make(chan struct{})
	srv := wsTestServer(t, func(ctx context.Context, conn *websocket.Conn, r *http.Request) {
		for i := 0; i < floodCount; i++ {
			if err := writeWSJSON(ctx, conn, map[string]any{"n": i}); err != nil {
				return
			}
		}
		close(floodDone)
		<-ctx.Done() // keep the connection open; the client drives the terminal
	})

	binv := NewInvoker()
	defer binv.Close()

	call := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source: wsSource(srv, nil),
		Ref:    "#/operations/subscribe",
	})

	// Let the flood land in the subscription's buffer before this test
	// ever calls Read: the handle's own output buffer is only 4 deep
	// (EmitOutput parks past that), so it's the SUBSCRIPTION's buffer —
	// exactly what's under test — that has to absorb the rest.
	select {
	case <-floodDone:
	case <-time.After(10 * time.Second):
		t.Fatal("server never finished flooding")
	}

	vals, err := drainOutputs(t, call)
	if err == nil {
		t.Fatalf("expected a backpressure overflow, got a clean EOF with %d values", len(vals))
	}
	if codeOf(t, err) != openbindings.ErrCodeStreamError {
		t.Fatalf("expected ERR_STREAM_ERROR, got %v", err)
	}
	wantMsg := fmt.Sprintf("backpressure overflow: more than %d undelivered frames", maxWSBufferedFrames)
	if err.Error() != wantMsg {
		t.Fatalf("error message = %q, want %q", err.Error(), wantMsg)
	}
	// Drain-before-terminal: some already-buffered frames were delivered
	// ahead of the terminal error (the exact count is timing-sensitive —
	// the handle's own 4-deep output buffer plus whatever the consumer
	// loop had already dequeued before this test started reading both
	// land outside the subscription's own bound), but never everything
	// the flood sent: some frames were genuinely dropped by the overflow.
	if len(vals) == 0 {
		t.Fatal("expected some frames to drain before the terminal error")
	}
	if len(vals) >= floodCount {
		t.Fatalf("delivered all %d flooded frames (%d), expected the overflow to drop some", floodCount, len(vals))
	}
}

// TestWSReceiveBackpressure_ByteBudgetOverflowFailsSubscription proves the
// byte bound independently of the frame-count bound: the frame count is
// raised out of reach and the byte budget is lowered (via
// setWSBackpressureBoundsForTest) so only the byte bound can trip, without
// pushing 64 MiB through a test socket.
func TestWSReceiveBackpressure_ByteBudgetOverflowFailsSubscription(t *testing.T) {
	const byteBudget = 2048
	setWSBackpressureBoundsForTest(t, 1_000_000, byteBudget)

	// ~54 bytes of JSON per frame; 200 frames is comfortably more than
	// byteBudget in total while each individual frame is far under it (an
	// accumulation trip, not a single-oversized-frame trip).
	const frameCount = 200
	payload := strings.Repeat("x", 32)
	floodDone := make(chan struct{})
	srv := wsTestServer(t, func(ctx context.Context, conn *websocket.Conn, r *http.Request) {
		for i := 0; i < frameCount; i++ {
			if err := writeWSJSON(ctx, conn, map[string]any{"n": i, "pad": payload}); err != nil {
				return
			}
		}
		close(floodDone)
		<-ctx.Done()
	})

	binv := NewInvoker()
	defer binv.Close()

	call := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source: wsSource(srv, nil),
		Ref:    "#/operations/subscribe",
	})

	select {
	case <-floodDone:
	case <-time.After(10 * time.Second):
		t.Fatal("server never finished flooding")
	}

	vals, err := drainOutputs(t, call)
	if err == nil {
		t.Fatalf("expected a byte-budget overflow, got a clean EOF with %d values", len(vals))
	}
	if codeOf(t, err) != openbindings.ErrCodeStreamError {
		t.Fatalf("expected ERR_STREAM_ERROR, got %v", err)
	}
	wantMsg := fmt.Sprintf("backpressure overflow: more than %d undelivered bytes", byteBudget)
	if err.Error() != wantMsg {
		t.Fatalf("error message = %q, want %q", err.Error(), wantMsg)
	}
	if len(vals) == 0 {
		t.Fatal("expected some frames to drain before the terminal error")
	}
}

// TestWSReceiveBackpressure_OverflowIsolatesToOneSubscription proves the
// isolation guarantee: two subscriptions share one pooled connection (the
// AsyncAPI two-operation pooling pattern), one never drains and overflows,
// and the other — actively draining throughout — keeps receiving on the
// same shared socket the whole time. The overflowing subscription's push()
// simply stops accepting frames for itself; nothing closes the pooled
// connection or touches the sibling's listener registration.
func TestWSReceiveBackpressure_OverflowIsolatesToOneSubscription(t *testing.T) {
	setWSBackpressureBoundsForTest(t, 64, 64*1024*1024)
	floodCount := maxWSBufferedFrames + 300
	startFlood := make(chan struct{})
	floodDone := make(chan struct{})
	var upgrades atomic.Int32
	srv := wsTestServer(t, func(ctx context.Context, conn *websocket.Conn, r *http.Request) {
		upgrades.Add(1)
		<-startFlood
		for i := 0; i < floodCount; i++ {
			if err := writeWSJSON(ctx, conn, map[string]any{"n": i}); err != nil {
				return
			}
		}
		close(floodDone)
		<-ctx.Done()
	})

	binv := NewInvoker()
	defer binv.Close()
	source := wsSource(srv, nil)

	callA := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{Source: source, Ref: "#/operations/subscribe"})
	callB := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{Source: source, Ref: "#/operations/subscribe"})
	outB := callB.Outputs()

	// Both subscribers must actually be registered on the ONE shared
	// pooled connection before the flood begins — a subscriber only sees
	// frames from its registration point on, so a late join could miss
	// the whole flood and this test would prove nothing.
	waitForSoleConnListenerCount(t, binv.wsPool, 2)

	readCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var gotB atomic.Int64
	doneB := make(chan struct{})
	go func() {
		defer close(doneB)
		for {
			_, err := outB.Read(readCtx)
			if err != nil {
				return
			}
			gotB.Add(1)
		}
	}()

	close(startFlood)

	// A never drains until after the flood has fully landed (mirrors
	// TestWSReceiveBackpressure_FrameCountOverflowFailsSubscription): it
	// overflows. B, meanwhile, has been actively draining since before the
	// flood started, so it is unaffected.
	select {
	case <-floodDone:
	case <-time.After(10 * time.Second):
		t.Fatal("server never finished flooding")
	}
	_, errA := drainOutputs(t, callA)
	if codeOf(t, errA) != openbindings.ErrCodeStreamError {
		t.Fatalf("subscriber A: expected ERR_STREAM_ERROR, got %v", errA)
	}
	wantMsg := fmt.Sprintf("backpressure overflow: more than %d undelivered frames", maxWSBufferedFrames)
	if errA.Error() != wantMsg {
		t.Fatalf("subscriber A error = %q, want %q", errA.Error(), wantMsg)
	}

	// B must still be receiving off the SAME pooled connection: A's
	// overflow must not have torn down the shared socket.
	outB.Stop()
	<-doneB
	if got := gotB.Load(); got == 0 {
		t.Fatal("subscriber B received nothing; A's overflow broke the shared connection")
	}

	if c := upgrades.Load(); c != 1 {
		t.Errorf("expected A and B to share 1 upgrade, got %d", c)
	}
}

// TestWSReceiveBackpressure_NormalDrainNeverTrips proves the bound is about
// undelivered/in-flight frames, not a cumulative lifetime cap: a consumer
// that keeps draining receives comfortably more than maxWSBufferedFrames
// over the connection's life without ever tripping either bound (mirrors
// the SSE per-event-not-cumulative cap, TestSSEReceiveCapIsPerEvent).
func TestWSReceiveBackpressure_NormalDrainNeverTrips(t *testing.T) {
	total := maxWSBufferedFrames + maxWSBufferedFrames/2

	srv := wsTestServer(t, func(ctx context.Context, conn *websocket.Conn, r *http.Request) {
		for i := 0; i < total; i++ {
			if err := writeWSJSON(ctx, conn, map[string]any{"n": i}); err != nil {
				return
			}
		}
	})

	binv := NewInvoker()
	defer binv.Close()

	call := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source: wsSource(srv, nil),
		Ref:    "#/operations/subscribe",
	})
	vals, err := drainOutputs(t, call)
	if err != nil {
		t.Fatalf("a normally-draining consumer must never trip the backpressure bound, got: %v", err)
	}
	if len(vals) != total {
		t.Fatalf("expected %d values, got %d", total, len(vals))
	}
}
