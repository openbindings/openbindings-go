// Package mcp implements the MCP (Model Context Protocol) binding format
// for OpenBindings.
//
// The package handles:
//   - Discovering tools, resources, and prompts from MCP servers
//   - Converting MCP entities to OpenBindings interfaces
//   - Executing operations via the MCP JSON-RPC protocol
//
// Only the Streamable HTTP transport is supported. Source locations must be
// HTTP or HTTPS URLs pointing to an MCP-capable endpoint.
package mcp

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	openbindings "github.com/openbindings/openbindings-go"
)

// FormatToken is the format identifier for MCP sources.
// Targets the 2025-11-25 MCP spec revision. Supported features:
//   - tools/list, tools/call (incl. structuredContent and outputSchema)
//   - resources/list, resources/read
//   - resources/templates/list
//   - prompts/list, prompts/get
//
// Not yet supported: resource subscriptions, sampling, icons, elicitation.
const FormatToken = "mcp@2025-11-25"

// Executor handles binding execution for MCP sources.
//
// The Executor pools MCP sessions by server URL and auth headers. Multiple
// ExecuteBinding calls to the same server reuse a single MCP session (one
// initialize handshake). A session stays alive as long as any execution on it
// is active. When the last execution ends, the session remains idle for up to
// 30 seconds before being closed. New executions arriving during the idle
// window reuse the warm session without re-handshaking.
//
// Call Close to shut down all pooled sessions when the Executor is no longer
// needed.
type Executor struct {
	clientVersion string
	idleTimeout   time.Duration
	pool          *sessionPool
}

// ExecutorOption configures an Executor.
type ExecutorOption func(*Executor)

// WithClientVersion sets the client version reported to MCP servers.
func WithClientVersion(v string) ExecutorOption {
	return func(e *Executor) {
		e.clientVersion = v
	}
}

// WithIdleTimeout overrides the default 30-second idle timeout for pooled
// sessions. After the last active execution on a session ends, the session is
// kept alive for this duration before being closed. A new execution arriving
// during the idle window reuses the warm session.
func WithIdleTimeout(d time.Duration) ExecutorOption {
	return func(e *Executor) {
		e.idleTimeout = d
	}
}

// NewExecutor creates a new MCP binding executor with session pooling enabled.
func NewExecutor(opts ...ExecutorOption) *Executor {
	e := &Executor{
		clientVersion: "0.0.0",
		pool:          newSessionPool(),
	}
	for _, opt := range opts {
		opt(e)
	}
	if e.idleTimeout > 0 {
		e.pool.idleTimeout = e.idleTimeout
	}
	return e
}

// Close shuts down all pooled MCP sessions. After Close returns, the Executor
// should not be used for new executions.
func (e *Executor) Close() {
	e.pool.closeAll()
}

// Formats returns the source formats supported by the MCP executor.
func (e *Executor) Formats() []openbindings.FormatInfo {
	return []openbindings.FormatInfo{{Token: FormatToken, Description: "Model Context Protocol"}}
}

// ExecuteBinding executes an MCP binding and returns a stream of events. For
// resource and prompt invocations the channel yields a single event. For tool
// invocations the channel may yield zero or more `notifications/progress`
// events as intermediate `Data` events, followed by the final tool result as
// the last event. See `binding-format-conventions.md` for the OBI execution
// model around streaming patterns.
//
// Auth retry uses a peek-and-forward pattern: the executor reads the first
// event from the channel returned by `execute`. If that event is an
// auth-required error and security methods + platform callbacks are
// available, credentials are resolved and the call is retried with a fresh
// channel. Otherwise the first event is prepended back onto the original
// stream and forwarded to the caller. This makes the auth retry compatible
// with both unary and progress-streaming tool calls (where the first event
// might be either an auth error or a progress notification).
func (e *Executor) ExecuteBinding(ctx context.Context, in *openbindings.BindingExecutionInput) (<-chan openbindings.StreamEvent, error) {
	enriched := in
	if in.Store != nil {
		key := normalizeEndpoint(in.Source.Location)
		if key != "" {
			if stored, err := in.Store.Get(ctx, key); err == nil && len(stored) > 0 {
				cp := *in
				if len(in.Context) == 0 {
					cp.Context = stored
				} else {
					merged := make(map[string]any, len(stored)+len(in.Context))
					for k, v := range stored {
						merged[k] = v
					}
					for k, v := range in.Context {
						merged[k] = v
					}
					cp.Context = merged
				}
				enriched = &cp
			}
		}
	}

	return e.executeWithAuthRetry(ctx, in, enriched), nil
}

// executeWithAuthRetry runs `execute` against the enriched input, and if the
// first stream event is an auth_required error, resolves credentials via the
// platform callbacks and retries the entire call with a fresh stream channel.
//
// The peek-and-forward pattern works equally well for unary and streaming
// tool calls: if the first event is a progress notification (not an auth
// error), it's prepended back onto the original stream and forwarded
// unchanged. The auth retry is only triggered when the very first event is
// an auth-required error, which is the only case where retrying is safe.
//
// The original `in` is passed alongside `enriched` so the function can
// detect whether `enriched` is still aliased to `in` (no store-merge
// happened) and copy on first mutation.
func (e *Executor) executeWithAuthRetry(ctx context.Context, in, enriched *openbindings.BindingExecutionInput) <-chan openbindings.StreamEvent {
	headers := buildHTTPHeaders(enriched.Context, enriched.Options)
	stream := execute(ctx, e.pool, e.clientVersion, enriched.Source.Location, enriched.Ref, enriched.Input, headers)

	// Peek at the first event for auth-retry detection. If the channel
	// closes immediately (no events), forward an empty closed channel.
	first, ok := <-stream
	if !ok {
		empty := make(chan openbindings.StreamEvent)
		close(empty)
		return empty
	}

	// Fast path: not an auth error, just forward the stream with the first
	// event prepended back on.
	if first.Error == nil || first.Error.Code != openbindings.ErrCodeAuthRequired ||
		len(enriched.Security) == 0 || enriched.Callbacks == nil {
		return prependEvent(first, stream)
	}

	// Auth retry path: the session that produced the auth error may be stale.
	// Invalidate it so the retry creates a fresh session with new credentials.
	oldKey := sessionKey(enriched.Source.Location, headers)
	e.pool.mu.Lock()
	if s, ok := e.pool.sessions[oldKey]; ok {
		e.pool.mu.Unlock()
		e.pool.invalidate(s)
	} else {
		e.pool.mu.Unlock()
	}

	// Resolve credentials interactively and retry once.
	creds, resolveErr := openbindings.ResolveSecurity(ctx, enriched.Security, enriched.Callbacks, nil)
	if resolveErr != nil || creds == nil {
		// Resolution failed; surface the original auth error.
		return prependEvent(first, stream)
	}

	// Credentials resolved. Build a new enriched input with the merged
	// context and persist the credentials in the store for next time.
	if enriched == in {
		cp := *in
		enriched = &cp
	}
	merged := make(map[string]any, len(enriched.Context)+len(creds))
	for k, v := range enriched.Context {
		merged[k] = v
	}
	for k, v := range creds {
		merged[k] = v
	}
	enriched.Context = merged

	if enriched.Store != nil {
		if storeKey := normalizeEndpoint(enriched.Source.Location); storeKey != "" {
			_ = enriched.Store.Set(ctx, storeKey, enriched.Context)
		}
	}

	// Drain the original stream so its goroutine exits cleanly, then return
	// a fresh stream from the retried call.
	drainStreamAsync(stream)
	headers = buildHTTPHeaders(enriched.Context, enriched.Options)
	return execute(ctx, e.pool, e.clientVersion, enriched.Source.Location, enriched.Ref, enriched.Input, headers)
}

// prependEvent returns a new channel that yields `first` followed by every
// event remaining on `rest`, then closes when `rest` closes.
func prependEvent(first openbindings.StreamEvent, rest <-chan openbindings.StreamEvent) <-chan openbindings.StreamEvent {
	out := make(chan openbindings.StreamEvent, 16)
	go func() {
		defer close(out)
		out <- first
		for ev := range rest {
			out <- ev
		}
	}()
	return out
}

// drainStreamAsync consumes any remaining events on the channel so the
// producing goroutine isn't blocked on a full buffer. Used when an auth retry
// abandons the original stream.
func drainStreamAsync(ch <-chan openbindings.StreamEvent) {
	go func() {
		for range ch {
		}
	}()
}

// Creator handles interface creation from MCP servers.
type Creator struct {
	clientVersion string
}

// CreatorOption configures a Creator.
type CreatorOption func(*Creator)

// WithCreatorClientVersion sets the client version reported to MCP servers.
func WithCreatorClientVersion(v string) CreatorOption {
	return func(c *Creator) {
		c.clientVersion = v
	}
}

// NewCreator creates a new MCP interface creator.
func NewCreator(opts ...CreatorOption) *Creator {
	c := &Creator{
		clientVersion: "0.0.0",
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Formats returns the source formats supported by the MCP creator.
func (c *Creator) Formats() []openbindings.FormatInfo {
	return []openbindings.FormatInfo{{Token: FormatToken, Description: "Model Context Protocol"}}
}

// CreateInterface discovers an MCP server's capabilities and converts to an OpenBindings interface.
func (c *Creator) CreateInterface(ctx context.Context, in *openbindings.CreateInput) (*openbindings.Interface, error) {
	if len(in.Sources) == 0 {
		return nil, openbindings.ErrNoSources
	}
	src := in.Sources[0]

	disc, err := discover(ctx, c.clientVersion, src.Location)
	if err != nil {
		return nil, fmt.Errorf("MCP discovery: %w", err)
	}

	iface, err := convertToInterface(disc, src.Location)
	if err != nil {
		return nil, fmt.Errorf("MCP convert: %w", err)
	}

	if in.Name != "" {
		iface.Name = in.Name
	}
	if in.Version != "" {
		iface.Version = in.Version
	}
	if in.Description != "" {
		iface.Description = in.Description
	}

	return iface, nil
}

// buildHTTPHeaders constructs HTTP headers from binding context credentials
// and execution options for the MCP Streamable HTTP transport.
func buildHTTPHeaders(bindCtx map[string]any, opts *openbindings.ExecutionOptions) map[string]string {
	headers := map[string]string{}

	if token := openbindings.ContextBearerToken(bindCtx); token != "" {
		headers["Authorization"] = "Bearer " + token
	} else if key := openbindings.ContextAPIKey(bindCtx); key != "" {
		headers["Authorization"] = "ApiKey " + key
	} else if u, p, ok := openbindings.ContextBasicAuth(bindCtx); ok {
		encoded := base64.StdEncoding.EncodeToString([]byte(u + ":" + p))
		headers["Authorization"] = "Basic " + encoded
	}

	if opts != nil {
		for k, v := range opts.Headers {
			headers[k] = v
		}
		if len(opts.Cookies) > 0 {
			var parts []string
			for name, value := range opts.Cookies {
				parts = append(parts, name+"="+value)
			}
			sort.Strings(parts)
			headers["Cookie"] = strings.Join(parts, "; ")
		}
	}

	if len(headers) == 0 {
		return nil
	}
	return headers
}

// normalizeEndpoint extracts scheme + host from an MCP endpoint URL
// and normalizes it to a stable context store key.
func normalizeEndpoint(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return ""
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return openbindings.NormalizeContextKey(endpoint)
	}
	return openbindings.NormalizeContextKey(u.Scheme + "://" + u.Host)
}
