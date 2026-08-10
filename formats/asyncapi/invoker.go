// Package asyncapi is the AsyncAPI 3.x binding invoker and interface
// synthesizer for the OpenBindings Go SDK: unary HTTP publishes and
// WebSocket publish/subscription cells behind the SDK's
// cardinality-agnostic Invocation handle. The public surface is the format
// contract (Invoker, Synthesizer, their constructors, and the binding-spec
// identifier) plus the endpoint-resolution seam (ParseDocument,
// Document.ResolveEndpoint — §9.2's server and address configuration points
// for consumers that dial with their own transport); the rest of the
// document model is internal.
package asyncapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	openbindings "github.com/openbindings/openbindings-go"
)

// maxRedirects bounds redirect chains for artifact fetches and HTTP publishes.
// Prevents redirect loops without imposing any total request timeout
// (which is the caller's responsibility via context).
const maxRedirects = 10

func newDefaultHTTPClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Invoker handles binding invocation for AsyncAPI 3.x sources.
//
// The document cache and WebSocket pool are scoped to the Invoker instance
// (keyed by source location / server-and-credentials respectively) and live
// as long as it does — scope instances per tenant to bound growth and avoid
// cross-tenant contamination in multi-tenant servers.
type Invoker struct {
	httpClient *http.Client
	mu         sync.RWMutex
	docCache   map[string]*document
	wsPool     *wsPool
}

var (
	_ openbindings.BindingInvoker  = (*Invoker)(nil)
	_ openbindings.BindingPreparer = (*Invoker)(nil)
)

// NewInvoker creates a new AsyncAPI binding invoker with a default HTTP
// client. Use NewInvokerWithClient to inject a custom client (e.g., for
// tests, or to add a transport layer for tracing or auth).
func NewInvoker() *Invoker {
	return NewInvokerWithClient(nil)
}

// NewInvokerWithClient creates an Invoker that uses the supplied
// *http.Client for all outbound requests, including the WebSocket upgrade
// handshake. A nil client falls back to the default. The caller is
// responsible for configuring redirect policy, transport, and any other
// client-level behavior. No overall request timeout should be set on the
// client because the caller controls cancellation via context.
func NewInvokerWithClient(client *http.Client) *Invoker {
	if client == nil {
		client = newDefaultHTTPClient()
	}
	return &Invoker{
		httpClient: client,
		docCache:   make(map[string]*document),
		wsPool:     newWSPool(client),
	}
}

// Close shuts down all pooled WebSocket connections (io.Closer). After Close
// returns, the Invoker should not be used for new invocations.
func (e *Invoker) Close() error {
	e.wsPool.closeAll()
	return nil
}

// cachedLoadDocument loads an AsyncAPI doc, caching by location within a process.
// When content is provided, the cache is bypassed and updated with the fresh parse.
func (e *Invoker) cachedLoadDocument(ctx context.Context, location string, content json.RawMessage) (*document, error) {
	if location != "" && content == nil {
		e.mu.RLock()
		if doc, ok := e.docCache[location]; ok {
			e.mu.RUnlock()
			return doc, nil
		}
		e.mu.RUnlock()
	}

	doc, err := loadDocument(ctx, e.httpClient, location, content)
	if err != nil {
		return nil, err
	}

	if location != "" {
		e.mu.Lock()
		e.docCache[location] = doc
		e.mu.Unlock()
	}
	return doc, nil
}

// BindingSpecs returns the binding-spec identifiers supported by the AsyncAPI invoker.
func (e *Invoker) BindingSpecs() []openbindings.BindingSpecInfo {
	return []openbindings.BindingSpecInfo{
		{BindingSpec: BindingSpec, Description: "AsyncAPI 3.0 event-driven APIs (reply-preserving revision)"},
		{BindingSpec: LegacyBindingSpec, Description: "AsyncAPI 3.0 event-driven APIs (revision-1 compatibility)"},
	}
}

// InvokeBinding invokes an AsyncAPI binding, returning the invocation handle
// synchronously. Creation is inert: the binding's work runs on its own
// goroutine and every pre-dispatch failure (bad ref, missing server,
// CONTEXT_REQUIRED) is raised before any observable side effect.
//
// Cell-to-handle mapping (complementary perspective, ASYNC-P-02: the
// artifact describes the application; invoking `send` subscribes, invoking
// `receive` publishes):
//   - receive + http/https: unary publish (one input -> body, response -> output)
//   - receive + ws/wss: client-streaming publish (each input -> one frame)
//   - send + http/https: excluded before dispatch
//   - send + ws/wss: server-streaming subscription (frames -> outputs; input
//     is closed at establishment)
func (e *Invoker) InvokeBinding(ctx context.Context, args *openbindings.BindingInvocationArgs) openbindings.Invocation[any, any] {
	inv := openbindings.NewInvocationImpl[any, any](ctx)
	go func() {
		// Bound all underlying I/O to the invocation's lifetime.
		bctx, stop := openbindings.DoneContext(ctx, inv.Done())
		defer stop()

		doc, err := e.cachedLoadDocument(bctx, args.Source.Location, args.Source.Content)
		if err != nil {
			if bctx.Err() != nil {
				return // cancellation is already terminal
			}
			inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeSourceLoadFailed, Message: err.Error()})
			return
		}
		runBinding(bctx, e.httpClient, e.wsPool, args, inv, doc)
	}()
	return inv
}

// PrepareBinding is the side-effect-free preflight: it reports the context
// this binding would require, or nil when the binding can proceed (or the
// answer is not knowable without network I/O). Only inline source content
// and the warm doc cache are consulted; nothing is fetched.
func (e *Invoker) PrepareBinding(ctx context.Context, args *openbindings.BindingInvocationArgs) (*openbindings.ContextRequiredDetails, error) {
	var doc *document
	if args.Source.Content != nil {
		// loadDocument performs no I/O when content is inline.
		d, err := loadDocument(ctx, e.httpClient, args.Source.Location, args.Source.Content)
		if err != nil {
			return nil, nil
		}
		doc = d
	} else if args.Source.Location != "" {
		e.mu.RLock()
		doc = e.docCache[args.Source.Location]
		e.mu.RUnlock()
	}
	if doc == nil {
		return nil, nil
	}

	opID, err := parseRef(args.Ref)
	if err != nil {
		return nil, nil
	}
	asyncOp, ok := doc.Operations[opID]
	if !ok {
		return nil, nil
	}
	var ch *channel
	if c, ok := doc.Channels[extractRefName(asyncOp.Channel.Ref)]; ok {
		ch = &c
	}
	target, err := resolveTarget(doc, ch, args.Context)
	if err != nil {
		return nil, nil
	}
	return requiredContext(doc, &asyncOp, target.SecurityServer, target.ServerURL, args.Context), nil
}

// Synthesizer handles interface synthesis from AsyncAPI documents.
type Synthesizer struct {
	httpClient *http.Client
}

var (
	_ openbindings.InterfaceSynthesizer = (*Synthesizer)(nil)
	_ openbindings.CoverageSynthesizer  = (*Synthesizer)(nil)
	_ openbindings.SourceInspector      = (*Synthesizer)(nil)
)

// NewSynthesizer creates a new AsyncAPI interface synthesizer.
func NewSynthesizer() *Synthesizer {
	return &Synthesizer{
		httpClient: newDefaultHTTPClient(),
	}
}

// BindingSpecs returns the binding-spec identifiers supported by the AsyncAPI synthesizer.
func (c *Synthesizer) BindingSpecs() []openbindings.BindingSpecInfo {
	return []openbindings.BindingSpecInfo{
		{BindingSpec: BindingSpec, Description: "AsyncAPI 3.0 event-driven APIs (reply-preserving revision)"},
		{BindingSpec: LegacyBindingSpec, Description: "AsyncAPI 3.0 event-driven APIs (revision-1 compatibility)"},
	}
}

// SynthesizeInterface converts an AsyncAPI document to an OpenBindings interface.
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
		synthesisCoverage(observation.doc, observation.iface),
		true,
	)
}

type synthesisObservation struct {
	iface *openbindings.Interface
	doc   *document
}

func (c *Synthesizer) synthesizeObserved(ctx context.Context, in *openbindings.SynthesizeInput) (*synthesisObservation, error) {
	if len(in.Sources) == 0 {
		skeleton, err := openbindings.SynthesisSkeleton(in)
		return &synthesisObservation{iface: &skeleton}, err
	}
	if len(in.Sources) > 1 {
		return nil, openbindings.ErrMultipleSources
	}
	src := in.Sources[0]
	if src.BindingSpec != BindingSpec && src.BindingSpec != LegacyBindingSpec {
		return nil, fmt.Errorf("synthesizer supports exact binding specifications %q and %q, got %q", BindingSpec, LegacyBindingSpec, src.BindingSpec)
	}
	if src.OutputLocation != "" {
		if err := validateDocumentAddress(src.OutputLocation); err != nil {
			return nil, fmt.Errorf("outputLocation: %w", err)
		}
	}
	// Authoring convenience: a bare filesystem path loads and is emitted as
	// its absolute file:// spelling. Emitting the original relative spelling
	// would create a source this binding implementation is guaranteed to
	// refuse under ASYNC-D-02.
	loadLocation, err := absolutizeArtifactLocation(src.Location)
	if err != nil {
		return nil, err
	}
	artifactContent := src.Content
	if src.Embed && artifactContent == nil {
		data, embedErr := sourceToBytes(ctx, c.httpClient, loadLocation, nil)
		if embedErr != nil {
			return nil, fmt.Errorf("embed AsyncAPI source: %w", embedErr)
		}
		artifactContent = openbindings.TextContent(string(data))
	}
	doc, err := loadDocument(ctx, c.httpClient, loadLocation, artifactContent)
	if err != nil {
		return nil, err
	}
	normalizedInput := *in
	normalizedInput.Sources = append([]openbindings.SynthesizeSource(nil), in.Sources...)
	normalizedInput.Sources[0].Location = loadLocation
	iface, err := synthesizeInterfaceWithDoc(ctx, &normalizedInput, doc)
	if err != nil {
		return nil, err
	}
	// Content-fed synthesis: the emitted source must stay invocable. A
	// source needs location or content; with no location, dropping the
	// provided content would emit neither.
	if artifactContent != nil {
		if entry, ok := iface.Sources[DefaultSourceName]; ok {
			entry.Content = append(json.RawMessage(nil), artifactContent...)
			iface.Sources[DefaultSourceName] = entry
		}
	}
	if err := openbindings.FinalizeSynthesis(iface, in, DefaultSourceName, src.BindingSpec); err != nil {
		return nil, err
	}
	return &synthesisObservation{iface: iface, doc: doc}, nil
}

// BuiltinHooks exposes the asyncapi builtin decoder to the seam's
// cross-format dispatch. Per the consultation matrix, asyncapi consults
// the DECODE axis only (per delivery unit; a WS frame has no scalar
// completion status, so the classifier is never consulted and none is
// exposed — openbindings.BuiltinClassify on this format is loud).
// The dispatch decoder resolves the declared message contentType from the
// per-unit Meta's content-type where present, else text — hook bodies
// wanting the document-declared type should decline to the builtin the
// in-flow lane supplies.
func (e *Invoker) BuiltinHooks() (openbindings.OutputDecoder, openbindings.ResultClassifier) {
	decode := func(site openbindings.InvokeSite, raw openbindings.RawResult) (any, error) {
		ct := ""
		if vs := raw.Meta["content-type"]; len(vs) > 0 {
			ct = vs[0]
		}
		return builtinDecodeFor(ct)(site, raw)
	}
	return decode, nil
}
