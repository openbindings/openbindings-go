package asyncapi

import (
	"context"
	"net/http"
	"sync"
	"time"

	"nhooyr.io/websocket"
)

// WebSocket connection pool for the AsyncAPI invoker.
//
// Multiple operations on the same channel (same server + address) share a
// single WebSocket connection. This is load-bearing for the AsyncAPI
// two-operation pattern where a `receive` operation opens a long-lived
// stream and `send` operations push messages on the same channel: without
// pooling, each invocation opens a separate socket and the server tracks
// subscriptions per-socket, so send and receive never share state.
//
// The pool is transport-only: it delivers raw frames to subscribed listeners
// and never interprets payloads. A single reader goroutine per connection
// broadcasts every incoming frame to all listeners in arrival order.
//
// Lifecycle:
//   - First acquire() for a key dials the WebSocket.
//   - Subsequent acquire() calls for the same key reuse it.
//   - Each acquire() increments a ref count; release() decrements.
//   - When refCount hits 0, an idle timer starts (default 30s).
//   - If no new acquire() arrives before the timer fires, the socket is
//     closed and evicted.

// defaultWSIdleTimeout is how long a pooled WebSocket stays alive after its
// last active operation releases it before the connection is closed and
// removed from the pool.
const defaultWSIdleTimeout = 30 * time.Second

// wsListener receives broadcast frames from a pooled connection. onClose is
// called exactly once when the socket closes: with nil for a clean close and
// with the read error otherwise.
type wsListener struct {
	onFrame func(data []byte)
	onClose func(err error)
}

// wsPool manages pooled WebSocket connections keyed by "serverURL|address".
type wsPool struct {
	mu          sync.Mutex
	conns       map[string]*pooledWS
	creating    map[string]chan struct{} // closed when connection creation finishes
	idleTimeout time.Duration
}

// pooledWS is a pooled WebSocket connection with reference counting,
// idle-timer lifecycle management, and frame broadcast to listeners.
type pooledWS struct {
	conn *websocket.Conn
	key  string
	pool *wsPool

	// refCount and idleTimer are guarded by the pool's mu.
	refCount  int
	idleTimer *time.Timer

	// writeMu serializes frame writes from concurrent operations.
	writeMu sync.Mutex

	// Listener registry and close state, guarded by lmu.
	lmu        sync.Mutex
	listeners  map[int]*wsListener
	nextID     int
	closed     bool
	closeErr   error // nil for a clean close
	localClose bool  // set before the pool closes the socket itself
}

func newWSPool() *wsPool {
	return &wsPool{
		conns:       make(map[string]*pooledWS),
		creating:    make(map[string]chan struct{}),
		idleTimeout: defaultWSIdleTimeout,
	}
}

// wsPoolKey builds a cache key from the server URL and channel address.
func wsPoolKey(serverURL, address string) string {
	return serverURL + "|" + address
}

// acquire returns a pooled WebSocket connection for the given server URL and
// address, creating one if none exists. The returned connection has its ref
// count incremented; the caller must call release() when done.
//
// When l is non-nil it is registered as a frame listener: on a fresh dial it
// is registered BEFORE the reader goroutine starts, so no frame the server
// pushes right after the handshake can be lost. The returned remove function
// unregisters it (no-op when l is nil).
//
// If multiple goroutines call acquire for the same key concurrently, only one
// creates the connection while the others wait.
func (p *wsPool) acquire(ctx context.Context, serverURL, address string, doc *Document, asyncOp *Operation, bindCtx map[string]any, l *wsListener) (*pooledWS, func(), error) {
	key := wsPoolKey(serverURL, address)

	for {
		p.mu.Lock()

		// Fast path: reuse an existing connection. A subscriber joining an
		// existing broadcast sees frames from registration on.
		if pw, ok := p.conns[key]; ok {
			if pw.idleTimer != nil {
				pw.idleTimer.Stop()
				pw.idleTimer = nil
			}
			pw.refCount++
			p.mu.Unlock()
			remove := func() {}
			if l != nil {
				remove = pw.subscribe(l)
			}
			return pw, remove, nil
		}

		// Check if another goroutine is already creating a connection for this
		// key. If so, wait for it to finish and loop to try acquire again.
		if ch, ok := p.creating[key]; ok {
			p.mu.Unlock()
			select {
			case <-ch:
				continue // retry acquire
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
		}

		// We're the first goroutine for this key. Mark that we're creating
		// a connection and release the lock for the slow dial operation.
		waitCh := make(chan struct{})
		p.creating[key] = waitCh
		p.mu.Unlock()

		pw, remove, err := p.createConn(ctx, serverURL, address, key, doc, asyncOp, bindCtx, l)

		p.mu.Lock()
		delete(p.creating, key)

		if err != nil {
			p.mu.Unlock()
			close(waitCh)
			return nil, nil, err
		}

		pw.refCount = 1
		p.conns[key] = pw
		p.mu.Unlock()
		close(waitCh) // wake all waiters so they can acquire the connection
		return pw, remove, nil
	}
}

// createConn establishes a new WebSocket connection with auth applied via
// applyHTTPContext on the upgrade request (headers AND query parameters:
// spec-driven apiKey credentials placed in the query must reach the dialed
// URL, because browsers cannot set custom WebSocket upgrade headers), then
// starts the broadcast reader goroutine.
func (p *wsPool) createConn(ctx context.Context, serverURL, address, key string, doc *Document, asyncOp *Operation, bindCtx map[string]any, l *wsListener) (*pooledWS, func(), error) {
	wsURL := serverURL + "/" + trimLeadingSlash(address)

	upgradeReq, err := http.NewRequestWithContext(ctx, http.MethodGet, wsURL, nil)
	if err != nil {
		return nil, nil, err
	}
	applyHTTPContext(upgradeReq, doc, asyncOp, bindCtx)

	dialOpts := &websocket.DialOptions{
		HTTPHeader: upgradeReq.Header,
	}

	// Dial with the request's reconstructed URL rather than the original
	// wsURL so query-param credentials appended by applyHTTPContext reach
	// the server.
	conn, _, err := websocket.Dial(ctx, upgradeReq.URL.String(), dialOpts)
	if err != nil {
		return nil, nil, err
	}

	pw := &pooledWS{
		conn:      conn,
		key:       key,
		pool:      p,
		listeners: make(map[int]*wsListener),
	}
	remove := func() {}
	if l != nil {
		remove = pw.subscribe(l)
	}
	go pw.readLoop()
	return pw, remove, nil
}

// readLoop is the single reader goroutine for the connection: it broadcasts
// every frame to the registered listeners in arrival order, and on socket
// close notifies them exactly once (clean vs error) and removes the
// connection from the pool.
func (pw *pooledWS) readLoop() {
	for {
		_, msg, err := pw.conn.Read(context.Background())
		if err != nil {
			pw.finish(err)
			return
		}
		pw.lmu.Lock()
		ls := make([]*wsListener, 0, len(pw.listeners))
		for _, l := range pw.listeners {
			ls = append(ls, l)
		}
		pw.lmu.Unlock()
		for _, l := range ls {
			if l.onFrame != nil {
				l.onFrame(msg)
			}
		}
	}
}

// finish marks the connection closed, evicts it from the pool, and notifies
// listeners. A close the pool initiated itself (idle timeout, closeAll) and a
// remote normal closure are clean; everything else carries the read error.
func (pw *pooledWS) finish(readErr error) {
	pw.lmu.Lock()
	if pw.closed {
		pw.lmu.Unlock()
		return
	}
	var closeErr error
	if status := websocket.CloseStatus(readErr); !pw.localClose &&
		status != websocket.StatusNormalClosure && status != websocket.StatusGoingAway {
		closeErr = readErr
	}
	pw.closed = true
	pw.closeErr = closeErr
	ls := make([]*wsListener, 0, len(pw.listeners))
	for _, l := range pw.listeners {
		ls = append(ls, l)
	}
	pw.listeners = nil
	pw.lmu.Unlock()

	pw.pool.remove(pw)
	for _, l := range ls {
		if l.onClose != nil {
			l.onClose(closeErr)
		}
	}
}

// subscribe registers a listener for broadcast frames and close notification.
// If the connection has already closed, onClose fires immediately. Returns a
// removal function (idempotent).
func (pw *pooledWS) subscribe(l *wsListener) (remove func()) {
	pw.lmu.Lock()
	if pw.closed {
		err := pw.closeErr
		pw.lmu.Unlock()
		if l.onClose != nil {
			l.onClose(err)
		}
		return func() {}
	}
	id := pw.nextID
	pw.nextID++
	pw.listeners[id] = l
	pw.lmu.Unlock()
	return func() {
		pw.lmu.Lock()
		if pw.listeners != nil {
			delete(pw.listeners, id)
		}
		pw.lmu.Unlock()
	}
}

// send writes one text frame on the shared socket. Writes from concurrent
// operations are serialized.
func (pw *pooledWS) send(ctx context.Context, data []byte) error {
	pw.writeMu.Lock()
	defer pw.writeMu.Unlock()
	return pw.conn.Write(ctx, websocket.MessageText, data)
}

// closeConn closes the underlying socket, marking the close as
// pool-initiated so listeners observe a clean close.
func (pw *pooledWS) closeConn(reason string) {
	pw.lmu.Lock()
	pw.localClose = true
	pw.lmu.Unlock()
	_ = pw.conn.Close(websocket.StatusNormalClosure, reason)
}

// release decrements the ref count and starts the idle timer if no
// operations remain active.
func (pw *pooledWS) release() {
	pw.pool.mu.Lock()
	defer pw.pool.mu.Unlock()

	pw.refCount--
	if pw.refCount > 0 {
		return
	}

	// Last operation ended. Start the idle timer.
	timeout := pw.pool.idleTimeout
	if timeout <= 0 {
		timeout = defaultWSIdleTimeout
	}
	pw.idleTimer = time.AfterFunc(timeout, func() {
		pw.pool.evictIdle(pw)
	})
}

// evictIdle closes and removes a connection from the pool when it is still
// unreferenced (an acquire may have raced the timer).
func (p *wsPool) evictIdle(pw *pooledWS) {
	p.mu.Lock()
	if current, ok := p.conns[pw.key]; !ok || current != pw || pw.refCount > 0 {
		p.mu.Unlock()
		return
	}
	delete(p.conns, pw.key)
	p.mu.Unlock()
	pw.closeConn("idle timeout")
}

// evict closes and removes a broken connection from the pool so the next
// acquire dials a fresh one.
func (p *wsPool) evict(pw *pooledWS) {
	p.remove(pw)
	pw.closeConn("evicted")
}

// remove deletes the connection from the pool map (if it is still the
// current entry for its key) without closing it.
func (p *wsPool) remove(pw *pooledWS) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if current, ok := p.conns[pw.key]; ok && current == pw {
		delete(p.conns, pw.key)
	}
	if pw.idleTimer != nil {
		pw.idleTimer.Stop()
		pw.idleTimer = nil
	}
}

// closeAll closes all pooled WebSocket connections. Used by Invoker.Close().
func (p *wsPool) closeAll() {
	p.mu.Lock()
	conns := make([]*pooledWS, 0, len(p.conns))
	for _, pw := range p.conns {
		if pw.idleTimer != nil {
			pw.idleTimer.Stop()
			pw.idleTimer = nil
		}
		conns = append(conns, pw)
	}
	p.conns = make(map[string]*pooledWS)
	p.mu.Unlock()

	for _, pw := range conns {
		pw.closeConn("pool closed")
	}
}

// trimLeadingSlash trims a leading slash from a string if present.
func trimLeadingSlash(s string) string {
	if len(s) > 0 && s[0] == '/' {
		return s[1:]
	}
	return s
}
