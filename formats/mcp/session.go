package mcp

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// defaultIdleTimeout is how long a session stays alive after its last active
// stream ends before the session is closed and removed from the pool.
const defaultIdleTimeout = 30 * time.Second

// sessionPool manages pooled MCP sessions keyed by server URL + auth headers.
// Multiple concurrent streams to the same server reuse a single MCP session
// (and therefore a single initialize handshake). A session stays alive as long
// as any stream references it. When the last stream releases its reference,
// an idle timer starts; if no new stream arrives before it fires, the session
// is closed and evicted.
type sessionPool struct {
	mu          sync.Mutex
	sessions    map[string]*mcpSession
	creating    map[string]chan struct{} // closed when session creation finishes
	idleTimeout time.Duration
}

func newSessionPool() *sessionPool {
	return &sessionPool{
		sessions:    make(map[string]*mcpSession),
		creating:    make(map[string]chan struct{}),
		idleTimeout: defaultIdleTimeout,
	}
}

// progressRegistration holds a per-call progress handler.
type progressRegistration struct {
	handler func(context.Context, *gomcp.ProgressNotificationClientRequest)
}

// mcpSession is a pooled MCP client session with reference counting and
// idle-timer lifecycle management.
type mcpSession struct {
	session *gomcp.ClientSession

	// progressHandlers maps progress tokens to per-call registrations.
	// The session-level progress handler demuxes incoming notifications to
	// the registration for the matching token. Access is protected by
	// progressMu.
	progressMu       sync.Mutex
	progressHandlers map[string]*progressRegistration

	// refCount tracks the number of active streams using this session.
	// Only manipulated under the pool's mu lock.
	refCount int32

	// idleTimer fires after the idle timeout to close and evict the session.
	// Only set when refCount drops to 0. Cleared when a new stream acquires
	// the session. Protected by the pool's mu lock.
	idleTimer *time.Timer

	url    string
	key    string
	pool   *sessionPool
	closed atomic.Bool
}

// sessionKey builds a cache key from the server URL and auth headers. Two
// calls with different Authorization headers must not share a session because
// the transport's header injector is per-session.
func sessionKey(url string, headers map[string]string) string {
	key := url
	if auth, ok := headers["Authorization"]; ok {
		key += "\x00auth=" + auth
	}
	if cookie, ok := headers["Cookie"]; ok {
		key += "\x00cookie=" + cookie
	}
	// Include any other headers sorted for stable keys.
	var extras []string
	for k, v := range headers {
		if k == "Authorization" || k == "Cookie" {
			continue
		}
		extras = append(extras, k+"="+v)
	}
	if len(extras) > 0 {
		sort.Strings(extras)
		key += "\x00" + strings.Join(extras, "\x00")
	}
	return key
}

// acquire returns a pooled session for the given URL and headers, creating one
// if none exists. The returned session has its ref count incremented; the
// caller must call release() when done.
//
// If multiple goroutines call acquire for the same key concurrently, only one
// creates the session while the others wait. This ensures concurrent calls
// share a single session and a single initialize handshake.
func (p *sessionPool) acquire(ctx context.Context, clientVersion string, url string, headers map[string]string) (*mcpSession, error) {
	key := sessionKey(url, headers)

	for {
		p.mu.Lock()

		// Fast path: reuse an existing session.
		if s, ok := p.sessions[key]; ok && !s.closed.Load() {
			if s.idleTimer != nil {
				s.idleTimer.Stop()
				s.idleTimer = nil
			}
			s.refCount++
			p.mu.Unlock()
			return s, nil
		}

		// Check if another goroutine is already creating a session for this
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
		// a session and release the lock for the slow connect operation.
		waitCh := make(chan struct{})
		p.creating[key] = waitCh
		p.mu.Unlock()

		s, err := p.createSession(ctx, clientVersion, url, headers, key)

		p.mu.Lock()
		delete(p.creating, key)

		if err != nil {
			p.mu.Unlock()
			close(waitCh)
			return nil, err
		}

		s.refCount = 1
		p.sessions[key] = s
		p.mu.Unlock()
		close(waitCh) // wake all waiters so they can acquire the session
		return s, nil
	}
}

// createSession establishes a new MCP session with a demuxing progress handler.
func (p *sessionPool) createSession(ctx context.Context, clientVersion string, url string, headers map[string]string, key string) (*mcpSession, error) {
	if !startsWithHTTP(url) {
		return nil, fmt.Errorf("MCP source location must be an HTTP or HTTPS URL, got %q", url)
	}

	s := &mcpSession{
		progressHandlers: make(map[string]*progressRegistration),
		url:              url,
		key:              key,
		pool:             p,
	}

	transport := &gomcp.StreamableClientTransport{Endpoint: url}
	if len(headers) > 0 {
		transport.HTTPClient = &http.Client{
			Transport: &headerTransport{
				base:    http.DefaultTransport,
				headers: headers,
			},
		}
	}

	opts := &gomcp.ClientOptions{
		ProgressNotificationHandler: s.demuxProgress,
	}
	client := gomcp.NewClient(clientInfo(clientVersion), opts)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, err
	}
	s.session = session
	return s, nil
}

// demuxProgress routes a progress notification to the handler registered for
// its progress token. If no handler is registered (e.g., the stream already
// completed), the notification is silently dropped.
func (s *mcpSession) demuxProgress(ctx context.Context, req *gomcp.ProgressNotificationClientRequest) {
	if req == nil || req.Params == nil {
		return
	}
	token := progressTokenString(req.Params.ProgressToken)
	if token == "" {
		return
	}
	s.progressMu.Lock()
	reg := s.progressHandlers[token]
	s.progressMu.Unlock()
	if reg != nil {
		reg.handler(ctx, req)
	}
}

// registerProgress associates a progress token with a per-call handler.
// The session-level demux handler routes incoming notifications to the
// registered handler for the matching token.
func (s *mcpSession) registerProgress(token string, handler func(context.Context, *gomcp.ProgressNotificationClientRequest)) {
	reg := &progressRegistration{handler: handler}
	s.progressMu.Lock()
	s.progressHandlers[token] = reg
	s.progressMu.Unlock()
}

// unregisterProgress removes the handler for a progress token. Any in-flight
// handler calls that started before unregistration may still be executing; the
// trySend mechanism in the handler guards against sending on a closed channel.
func (s *mcpSession) unregisterProgress(token string) {
	s.progressMu.Lock()
	delete(s.progressHandlers, token)
	s.progressMu.Unlock()
}

// release decrements the ref count and starts the idle timer if no streams
// remain active.
func (s *mcpSession) release() {
	s.pool.mu.Lock()
	defer s.pool.mu.Unlock()

	s.refCount--
	if s.refCount > 0 {
		return
	}

	// Last stream ended. Start the idle timer.
	timeout := s.pool.idleTimeout
	if timeout <= 0 {
		timeout = defaultIdleTimeout
	}
	s.idleTimer = time.AfterFunc(timeout, func() {
		s.pool.evict(s)
	})
}

// evict closes and removes a session from the pool. Called by the idle timer
// or on auth failure.
func (p *sessionPool) evict(s *mcpSession) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Only evict if the session is still the one in the map (it may have been
	// replaced by a new session for the same key).
	if current, ok := p.sessions[s.key]; ok && current == s {
		delete(p.sessions, s.key)
	}
	if !s.closed.Swap(true) {
		_ = s.session.Close()
	}
}

// invalidate forcefully removes a specific session from the pool (e.g., on
// auth failure) so the next acquire() creates a fresh one.
func (p *sessionPool) invalidate(s *mcpSession) {
	p.mu.Lock()
	if s.idleTimer != nil {
		s.idleTimer.Stop()
		s.idleTimer = nil
	}
	p.mu.Unlock()
	p.evict(s)
}

// closeAll closes all pooled sessions. Used by Executor.Close().
func (p *sessionPool) closeAll() {
	p.mu.Lock()
	sessions := make([]*mcpSession, 0, len(p.sessions))
	for _, s := range p.sessions {
		sessions = append(sessions, s)
	}
	p.sessions = make(map[string]*mcpSession)
	p.mu.Unlock()

	for _, s := range sessions {
		if s.idleTimer != nil {
			s.idleTimer.Stop()
		}
		if !s.closed.Swap(true) {
			_ = s.session.Close()
		}
	}
}

// progressTokenString extracts a string from a progress token, which may be
// a string or a json.Number.
func progressTokenString(token any) string {
	switch v := token.(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	default:
		if token != nil {
			return fmt.Sprintf("%v", token)
		}
		return ""
	}
}
