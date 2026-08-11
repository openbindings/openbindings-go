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
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	openbindings "github.com/openbindings/openbindings-go"
)

// BindingSpec identifies the unreleased first MCP binding candidate. It
// exposes artifact-declared application values for tools with outputSchema.
const BindingSpec = "openbindings.mcp@1"

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
	httpClient    *http.Client
	pool          *sessionPool
	// solicitProgress is the consumer-level value of the family's `solicit`
	// configuration point (openbindings.mcp@1 §9.3). nil declines, falling
	// through to the default: not solicited.
	solicitProgress *bool
}

var _ openbindings.BindingInvoker = (*Invoker)(nil)

// InvokerOption configures an Invoker.
type InvokerOption func(*Invoker)

// WithClientVersion sets the client version reported to MCP servers.
func WithClientVersion(v string) InvokerOption {
	return func(e *Invoker) {
		e.clientVersion = v
	}
}

// WithHTTPClient injects the base *http.Client for every pooled session's
// Streamable HTTP transport. Its Transport becomes the round-trip base beneath
// the MCP header injector, and its Timeout, redirect policy, and cookie jar are
// preserved. Use this for a corporate proxy, mTLS client certificates, or a
// custom CA pool. A nil client (or an unset option) means stdlib defaults. This
// is the MCP counterpart to the openapi invoker's NewInvokerWithClient.
func WithHTTPClient(c *http.Client) InvokerOption {
	return func(e *Invoker) {
		e.httpClient = c
	}
}

// WithSolicitProgress sets the consumer-level value of this family's
// `solicit` configuration point (openbindings.mcp@1 §9.3): whether a
// tools/call carries a progressToken so the server's correlated progress
// notifications stream as output values ahead of the result (§9.2). The
// consultation order is per-invocation context.configuration["solicit"] →
// this consumer-level setting → the default, which is NOT solicited: without
// an opt-in the output stream is the result value alone. Solicitation
// applies to tool invocations only.
func WithSolicitProgress(v bool) InvokerOption {
	return func(e *Invoker) {
		e.solicitProgress = &v
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
	e.pool.baseClient = e.httpClient
	return e
}

// Close shuts down all pooled MCP sessions (io.Closer). After Close returns,
// the Invoker should not be used for new invocations.
func (e *Invoker) Close() error {
	e.pool.closeAll()
	return nil
}

// BindingSpecs returns the binding-spec identifiers supported by the MCP invoker.
func (e *Invoker) BindingSpecs() []openbindings.BindingSpecInfo {
	return mcpBindingSpecInfos()
}

func mcpBindingSpecInfos() []openbindings.BindingSpecInfo {
	return []openbindings.BindingSpecInfo{
		{BindingSpec: BindingSpec, Description: "Model Context Protocol application-contract tools"},
	}
}

// InvokeBinding invokes an MCP binding and returns the Invocation handle
// synchronously; the MCP session work is scheduled on its own goroutine.
//
// The ref resolves against the listing before dispatch (openbindings.mcp@1
// §7): offline against a pinned listing when the source carries content,
// otherwise against the live capability-gated, pagination-exhausted listing.
// Tool arguments, prompt arguments, and a resource template's variables
// arrive as the operation's single input message through the handle's Write
// channel; static resource reads take no input (the binding closes the
// input side once resolution says the ref names a static resource). For
// tool invocations, `notifications/progress` events stream as outputs ahead
// of the final result only when solicited (§9.3's `solicit` configuration
// point — per-invocation context.configuration.solicit, then
// WithSolicitProgress, default off). Pre-dispatch failures (bad ref,
// missing endpoint, invalid pin, invalid input, unresolvable ref) terminate
// the handle before the entity request is sent.
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
	httpClient    *http.Client
}

var _ openbindings.InterfaceSynthesizer = (*Synthesizer)(nil)
var _ openbindings.CoverageSynthesizer = (*Synthesizer)(nil)
var _ openbindings.SourceInspector = (*Synthesizer)(nil)

// SynthesizerOption configures a Synthesizer.
type SynthesizerOption func(*Synthesizer)

// WithSynthesizerClientVersion sets the client version reported to MCP servers.
func WithSynthesizerClientVersion(v string) SynthesizerOption {
	return func(c *Synthesizer) {
		c.clientVersion = v
	}
}

// WithSynthesizerHTTPClient injects the base *http.Client used to reach the MCP
// server during discovery. Like the invoker's WithHTTPClient, its Transport is
// wrapped by the MCP header injector and the rest of its configuration is
// preserved. Discovery connects live, so a proxy or mTLS setup that the
// invocation lane needs is needed here too.
func WithSynthesizerHTTPClient(c *http.Client) SynthesizerOption {
	return func(s *Synthesizer) {
		s.httpClient = c
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

// BindingSpecs returns the binding-spec identifiers supported by the MCP synthesizer.
func (c *Synthesizer) BindingSpecs() []openbindings.BindingSpecInfo {
	return mcpBindingSpecInfos()
}

// SynthesizeInterface converts an MCP server's capabilities to an
// OpenBindings interface. A source carrying content is a pinned listing
// (MCP-D-01): the pin is the artifact (§6 content primacy), synthesis is
// offline, and the server is never dialed — MCP-D-02 still requires the
// location (content does not waive it), and an invalid pin is refused
// loudly before any I/O. Without content, discovery connects live.
func (c *Synthesizer) SynthesizeInterface(ctx context.Context, in *openbindings.SynthesizeInput) (*openbindings.Interface, error) {
	observation, err := c.synthesizeObserved(ctx, in)
	if err != nil {
		return nil, err
	}
	return observation.iface, nil
}

func (c *Synthesizer) SynthesizeInterfaceWithCoverage(ctx context.Context, in *openbindings.SynthesizeInput) (*openbindings.SynthesizeResult, error) {
	observation, err := c.synthesizeObserved(ctx, in)
	if err != nil {
		return nil, err
	}
	return openbindings.NewSynthesisResult(
		observation.iface,
		synthesisCoverage(observation.disc, observation.iface),
		true,
	)
}

type synthesisObservation struct {
	iface *openbindings.Interface
	disc  *discovery
}

func (c *Synthesizer) synthesizeObserved(ctx context.Context, in *openbindings.SynthesizeInput) (*synthesisObservation, error) {
	if len(in.Sources) == 0 {
		skeleton, err := openbindings.SynthesisSkeleton(in)
		if err != nil {
			return nil, err
		}
		return &synthesisObservation{iface: &skeleton}, nil
	}
	if len(in.Sources) > 1 {
		return nil, openbindings.ErrMultipleSources
	}
	src := in.Sources[0]
	if src.BindingSpec != BindingSpec {
		return nil, fmt.Errorf("synthesizer supports exact binding specification %q, got %q", BindingSpec, src.BindingSpec)
	}
	if src.OutputLocation != "" {
		if err := validateEndpoint(src.OutputLocation); err != nil {
			return nil, fmt.Errorf("outputLocation: %w", err)
		}
	}
	var disc *discovery
	if src.Content != nil {
		if err := validateEndpoint(src.Location); err != nil {
			return nil, err
		}
		d, err := pinnedDiscovery(src.Content)
		if err != nil {
			return nil, err
		}
		disc = d
	} else {
		d, err := discover(ctx, c.clientVersion, c.httpClient, src.Location)
		if err != nil {
			return nil, fmt.Errorf("MCP discovery: %w", err)
		}
		disc = d
	}

	iface, err := convertToInterface(disc, src.Location, src.BindingSpec)
	if err != nil {
		return nil, fmt.Errorf("MCP convert: %w", err)
	}

	if src.Content != nil {
		entry := iface.Sources[DefaultSourceName]
		entry.Content = src.Content
		iface.Sources[DefaultSourceName] = entry
	} else if src.Embed {
		if disc.PinnedListing == nil {
			return nil, fmt.Errorf("MCP live discovery did not yield a complete pagination-exhausted listing to embed")
		}
		entry := iface.Sources[DefaultSourceName]
		entry.Content = disc.PinnedListing
		iface.Sources[DefaultSourceName] = entry
	}
	if err := openbindings.FinalizeSynthesis(iface, in, DefaultSourceName, src.BindingSpec); err != nil {
		return nil, err
	}

	return &synthesisObservation{iface: iface, disc: disc}, nil
}

// buildHTTPHeaders maps the bearer credential to the Authorization carrier
// incorporated by openbindings.mcp@1 §9.5, then carries explicitly named
// headers and cookies. Other generic credentials are challenged before here.
func buildHTTPHeaders(bindCtx map[string]any) map[string]string {
	headers := map[string]string{}

	if token := openbindings.ContextBearerToken(bindCtx); token != "" {
		headers["Authorization"] = "Bearer " + token
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
