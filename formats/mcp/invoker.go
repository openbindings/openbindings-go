// Package mcp implements the MCP (Model Context Protocol) binding format
// for OpenBindings.
//
// The package handles:
//   - Discovering tools, resources, and prompts from MCP servers
//   - Converting MCP entities to OpenBindings interfaces
//   - Invoking bindings via the MCP JSON-RPC protocol
//
// Only the Streamable HTTP transport is supported. Source locations must be
// HTTP or HTTPS URLs pointing to an MCP-capable endpoint.
package mcp

import (
	"context"
	"encoding/base64"
	"fmt"
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

// Invoker handles binding invocation for MCP sources.
//
// The Invoker pools MCP sessions by server URL and auth headers. Multiple
// InvokeBinding calls to the same server reuse a single MCP session (one
// initialize handshake). A session stays alive as long as any invocation on it
// is active. When the last invocation ends, the session remains idle for up to
// 30 seconds before being closed. New invocations arriving during the idle
// window reuse the warm session without re-handshaking.
//
// Call Close to shut down all pooled sessions when the Invoker is no longer
// needed.
type Invoker struct {
	clientVersion string
	idleTimeout   time.Duration
	pool          *sessionPool
}

// InvokerOption configures an Invoker.
type InvokerOption func(*Invoker)

// WithClientVersion sets the client version reported to MCP servers.
func WithClientVersion(v string) InvokerOption {
	return func(e *Invoker) {
		e.clientVersion = v
	}
}

// WithIdleTimeout overrides the default 30-second idle timeout for pooled
// sessions. After the last active invocation on a session ends, the session is
// kept alive for this duration before being closed. A new invocation arriving
// during the idle window reuses the warm session.
func WithIdleTimeout(d time.Duration) InvokerOption {
	return func(e *Invoker) {
		e.idleTimeout = d
	}
}

// NewInvoker creates a new MCP binding invoker with session pooling enabled.
func NewInvoker(opts ...InvokerOption) *Invoker {
	e := &Invoker{
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

// Close shuts down all pooled MCP sessions. After Close returns, the Invoker
// should not be used for new invocations.
func (e *Invoker) Close() {
	e.pool.closeAll()
}

// Formats returns the source formats supported by the MCP invoker.
func (e *Invoker) Formats() []openbindings.FormatInfo {
	return []openbindings.FormatInfo{{Token: FormatToken, Description: "Model Context Protocol"}}
}

// InvokeBinding invokes an MCP binding and returns the Invocation handle
// synchronously; the MCP session work is scheduled on its own goroutine.
//
// Tool and prompt arguments arrive as the operation's single input message
// through the handle's Write channel; resource reads take no input (the
// binding closes the input side on entry). For tool invocations, any
// `notifications/progress` events the server sends during the call are
// emitted as outputs ahead of the final tool result. Pre-dispatch failures
// (bad ref, missing endpoint, non-object input) terminate the handle before
// any network side effect.
//
// Credentials are applied from args.Context (bearerToken, apiKey, basic,
// headers, cookies) as HTTP headers; an HTTP 401 from the server surfaces as
// a terminal ERR_AUTH_REQUIRED.
func (e *Invoker) InvokeBinding(ctx context.Context, args *openbindings.BindingInvocationArgs) openbindings.Invocation[any, any] {
	inv := openbindings.NewInvocationImpl[any, any](ctx)
	go e.run(ctx, args, inv)
	return inv
}

// Synthesizer handles interface synthesis from MCP servers.
type Synthesizer struct {
	clientVersion string
}

// SynthesizerOption configures a Synthesizer.
type SynthesizerOption func(*Synthesizer)

// WithSynthesizerClientVersion sets the client version reported to MCP servers.
func WithSynthesizerClientVersion(v string) SynthesizerOption {
	return func(c *Synthesizer) {
		c.clientVersion = v
	}
}

// NewSynthesizer creates a new MCP interface synthesizer.
func NewSynthesizer(opts ...SynthesizerOption) *Synthesizer {
	c := &Synthesizer{
		clientVersion: "0.0.0",
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Formats returns the source formats supported by the MCP synthesizer.
func (c *Synthesizer) Formats() []openbindings.FormatInfo {
	return []openbindings.FormatInfo{{Token: FormatToken, Description: "Model Context Protocol"}}
}

// SynthesizeInterface discovers an MCP server's capabilities and converts to an OpenBindings interface.
func (c *Synthesizer) SynthesizeInterface(ctx context.Context, in *openbindings.SynthesizeInput) (*openbindings.Interface, error) {
	if len(in.Sources) == 0 {
		return nil, openbindings.ErrNoSources
	}
	if len(in.Sources) > 1 {
		return nil, openbindings.ErrMultipleSources
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

// buildHTTPHeaders constructs HTTP headers from invocation context
// credentials for the MCP Streamable HTTP transport.
func buildHTTPHeaders(bindCtx map[string]any) map[string]string {
	headers := map[string]string{}

	if token := openbindings.ContextBearerToken(bindCtx); token != "" {
		headers["Authorization"] = "Bearer " + token
	} else if key := openbindings.ContextAPIKey(bindCtx); key != "" {
		headers["Authorization"] = "ApiKey " + key
	} else if u, p, ok := openbindings.ContextBasicAuth(bindCtx); ok {
		encoded := base64.StdEncoding.EncodeToString([]byte(u + ":" + p))
		headers["Authorization"] = "Basic " + encoded
	}

	for k, v := range openbindings.ContextHeaders(bindCtx) {
		headers[k] = v
	}
	if cookies := openbindings.ContextCookies(bindCtx); len(cookies) > 0 {
		var parts []string
		for name, value := range cookies {
			parts = append(parts, name+"="+value)
		}
		sort.Strings(parts)
		headers["Cookie"] = strings.Join(parts, "; ")
	}

	if len(headers) == 0 {
		return nil
	}
	return headers
}
