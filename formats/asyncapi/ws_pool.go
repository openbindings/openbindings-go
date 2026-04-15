package asyncapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	openbindings "github.com/openbindings/openbindings-go"
	"nhooyr.io/websocket"
)

// defaultWSIdleTimeout is how long a pooled WebSocket stays alive after its
// last active operation releases it before the connection is closed and
// removed from the pool.
const defaultWSIdleTimeout = 30 * time.Second

// wsPool manages pooled WebSocket connections keyed by "serverURL|address".
// Multiple concurrent send operations to the same channel reuse a single
// WebSocket connection. A connection stays alive as long as any operation
// holds a reference. When the last operation releases, an idle timer starts;
// if no new operation arrives before it fires, the connection is closed and
// evicted.
type wsPool struct {
	mu          sync.Mutex
	conns       map[string]*pooledWS
	creating    map[string]chan struct{} // closed when connection creation finishes
	idleTimeout time.Duration
}

// pooledWS is a pooled WebSocket connection with reference counting and
// idle-timer lifecycle management.
type pooledWS struct {
	conn *websocket.Conn

	// refCount tracks the number of active operations using this connection.
	// Only manipulated under the pool's mu lock.
	refCount int32

	// idleTimer fires after the idle timeout to close and evict the connection.
	// Only set when refCount drops to 0. Cleared when a new operation acquires
	// the connection. Protected by the pool's mu lock.
	idleTimer *time.Timer

	// opMu serializes send+reply cycles on the connection. The
	// nhooyr.io/websocket library requires that only one goroutine reads
	// and one goroutine writes at a time. For send-with-reply operations
	// (write then read), the entire cycle must be serialized.
	opMu sync.Mutex

	key  string
	pool *wsPool
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
// If multiple goroutines call acquire for the same key concurrently, only one
// creates the connection while the others wait.
func (p *wsPool) acquire(ctx context.Context, serverURL, address string, doc *Document, asyncOp *Operation, bindCtx map[string]any, opts *openbindings.ExecutionOptions) (*pooledWS, error) {
	key := wsPoolKey(serverURL, address)

	for {
		p.mu.Lock()

		// Fast path: reuse an existing connection.
		if pw, ok := p.conns[key]; ok {
			if pw.idleTimer != nil {
				pw.idleTimer.Stop()
				pw.idleTimer = nil
			}
			pw.refCount++
			p.mu.Unlock()
			return pw, nil
		}

		// Check if another goroutine is already creating a connection for this
		// key. If so, wait for it to finish and loop to try acquire again.
		if ch, ok := p.creating[key]; ok {
			p.mu.Unlock()
			select {
			case <-ch:
				continue // retry acquire
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		// We're the first goroutine for this key. Mark that we're creating
		// a connection and release the lock for the slow dial operation.
		waitCh := make(chan struct{})
		p.creating[key] = waitCh
		p.mu.Unlock()

		pw, err := p.createConn(ctx, serverURL, address, key, doc, asyncOp, bindCtx, opts)

		p.mu.Lock()
		delete(p.creating, key)

		if err != nil {
			p.mu.Unlock()
			close(waitCh)
			return nil, err
		}

		pw.refCount = 1
		p.conns[key] = pw
		p.mu.Unlock()
		close(waitCh) // wake all waiters so they can acquire the connection
		return pw, nil
	}
}

// createConn establishes a new WebSocket connection with auth headers applied
// via applyHTTPContext on the upgrade request.
func (p *wsPool) createConn(ctx context.Context, serverURL, address, key string, doc *Document, asyncOp *Operation, bindCtx map[string]any, opts *openbindings.ExecutionOptions) (*pooledWS, error) {
	wsURL := serverURL + "/" + trimLeadingSlash(address)

	upgradeReq, err := http.NewRequestWithContext(ctx, "GET", wsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("building upgrade request: %w", err)
	}
	applyHTTPContext(upgradeReq, doc, asyncOp, bindCtx, opts)

	dialOpts := &websocket.DialOptions{
		HTTPHeader: upgradeReq.Header,
	}

	conn, _, err := websocket.Dial(ctx, upgradeReq.URL.String(), dialOpts)
	if err != nil {
		return nil, err
	}

	pw := &pooledWS{
		conn: conn,
		key:  key,
		pool: p,
	}
	return pw, nil
}

// release decrements the ref count and starts the idle timer if no operations
// remain active.
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
		pw.pool.evict(pw)
	})
}

// evict closes and removes a connection from the pool.
func (p *wsPool) evict(pw *pooledWS) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Only evict if the connection is still the one in the map.
	if current, ok := p.conns[pw.key]; ok && current == pw {
		delete(p.conns, pw.key)
	}
	pw.conn.Close(websocket.StatusNormalClosure, "idle timeout")
}

// closeAll closes all pooled WebSocket connections. Used by Executor.Close().
func (p *wsPool) closeAll() {
	p.mu.Lock()
	conns := make([]*pooledWS, 0, len(p.conns))
	for _, pw := range p.conns {
		conns = append(conns, pw)
	}
	p.conns = make(map[string]*pooledWS)
	p.mu.Unlock()

	for _, pw := range conns {
		if pw.idleTimer != nil {
			pw.idleTimer.Stop()
		}
		pw.conn.Close(websocket.StatusNormalClosure, "pool closed")
	}
}

// sendWS acquires a pooled WebSocket connection (or opens a new one), sends
// the message, optionally reads one reply, then releases the connection back
// to the pool.
//
// If the operation has a Reply defined, one response message is read and
// returned as a single stream event. Otherwise the send is fire-and-forget
// and an empty (immediately closed) channel is returned.
//
// The entire send+reply cycle is serialized through the connection's opMu to
// prevent concurrent read corruption. Multiple goroutines will queue on the
// mutex while still sharing the same underlying connection.
func sendWS(ctx context.Context, pool *wsPool, serverURL, address string, input *openbindings.BindingExecutionInput, doc *Document, asyncOp *Operation) (<-chan openbindings.StreamEvent, error) {
	pw, err := pool.acquire(ctx, serverURL, address, doc, asyncOp, input.Context, input.Options)
	if err != nil {
		return openbindings.SingleEventChannel(openbindings.FailedOutput(time.Now(), openbindings.ErrCodeConnectFailed, err.Error())), nil
	}

	// Build the message payload from input fields (no bearerToken in body for
	// pooled sends; auth is on the upgrade request headers).
	payload := make(map[string]any)
	if input.Input != nil {
		if m, ok := input.Input.(map[string]any); ok {
			for k, v := range m {
				payload[k] = v
			}
		}
	}

	msgBytes, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		pw.release()
		return openbindings.SingleEventChannel(openbindings.FailedOutput(time.Now(), openbindings.ErrCodeExecutionFailed, marshalErr.Error())), nil
	}

	wantReply := asyncOp.Reply != nil

	ch := make(chan openbindings.StreamEvent, 1)
	go func() {
		defer close(ch)
		defer pw.release()

		// Serialize the entire send(+reply) cycle so concurrent callers
		// don't interleave reads/writes on the same WebSocket.
		pw.opMu.Lock()
		defer pw.opMu.Unlock()

		writeErr := pw.conn.Write(ctx, websocket.MessageText, msgBytes)
		if writeErr != nil {
			// Connection is broken. Evict it so the next caller gets a fresh one.
			pool.evict(pw)
			select {
			case ch <- openbindings.StreamEvent{Error: &openbindings.ExecuteError{
				Code:    openbindings.ErrCodeExecutionFailed,
				Message: writeErr.Error(),
			}}:
			case <-ctx.Done():
			}
			return
		}

		if !wantReply {
			return
		}

		_, msg, readErr := pw.conn.Read(ctx)
		if readErr != nil {
			if ctx.Err() != nil {
				return
			}
			select {
			case ch <- openbindings.StreamEvent{Error: &openbindings.ExecuteError{
				Code:    openbindings.ErrCodeStreamError,
				Message: readErr.Error(),
			}}:
			case <-ctx.Done():
			}
			return
		}

		var parsed any
		if json.Unmarshal(msg, &parsed) == nil {
			select {
			case ch <- openbindings.StreamEvent{Data: parsed}:
			case <-ctx.Done():
			}
		} else {
			select {
			case ch <- openbindings.StreamEvent{Data: string(msg)}:
			case <-ctx.Done():
			}
		}
	}()
	return ch, nil
}

// trimLeadingSlash trims a leading slash from a string if present.
func trimLeadingSlash(s string) string {
	if len(s) > 0 && s[0] == '/' {
		return s[1:]
	}
	return s
}
