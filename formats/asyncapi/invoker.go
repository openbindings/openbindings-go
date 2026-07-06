package asyncapi

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	openbindings "github.com/openbindings/openbindings-go"
)

// maxRedirects bounds redirect chains for HTTP fetches and SSE/POST invocations.
// Prevents redirect loops without imposing any total request timeout
// (which is the caller's responsibility via context).
const maxRedirects = 10

func newDefaultHTTPClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("stopped after %d redirects", maxRedirects)
			}
			return nil
		},
	}
}

// Invoker handles binding invocation for AsyncAPI 3.x sources.
type Invoker struct {
	httpClient *http.Client
	mu         sync.RWMutex
	docCache   map[string]*Document
	wsPool     *wsPool
}

var (
	_ openbindings.BindingInvoker  = (*Invoker)(nil)
	_ openbindings.BindingPreparer = (*Invoker)(nil)
)

// NewInvoker creates a new AsyncAPI binding invoker.
func NewInvoker() *Invoker {
	return &Invoker{
		httpClient: newDefaultHTTPClient(),
		docCache:   make(map[string]*Document),
		wsPool:     newWSPool(),
	}
}

// Close shuts down all pooled WebSocket connections. After Close returns, the
// Invoker should not be used for new invocations.
func (e *Invoker) Close() {
	e.wsPool.closeAll()
}

// cachedLoadDocument loads an AsyncAPI doc, caching by location within a process.
// When content is provided, the cache is bypassed and updated with the fresh parse.
func (e *Invoker) cachedLoadDocument(ctx context.Context, location string, content any) (*Document, error) {
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

// Formats returns the source formats supported by the AsyncAPI invoker.
func (e *Invoker) Formats() []openbindings.FormatInfo {
	return []openbindings.FormatInfo{{Token: FormatToken, Description: "AsyncAPI 3.x event-driven APIs"}}
}

// InvokeBinding invokes an AsyncAPI binding, returning the invocation handle
// synchronously. Creation is inert: the binding's work runs on its own
// goroutine and every pre-dispatch failure (bad ref, missing server,
// CONTEXT_REQUIRED) is raised before any observable side effect.
//
// Channel-to-handle mapping:
//   - send + http/https: unary POST (first input -> body, response -> output)
//   - send + ws/wss: client-streaming publish (each input -> one frame)
//   - receive + http/https: SSE subscribe (server events -> outputs)
//   - receive + ws/wss: WebSocket subscribe, bidi-capable (frames -> outputs,
//     caller inputs -> control frames)
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
	var doc *Document
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
	serverURL, _, err := resolveServer(doc, args.Context)
	if err != nil {
		return nil, nil
	}
	return requiredContext(doc, &asyncOp, serverURL, args.Context), nil
}

// Synthesizer handles interface creation from AsyncAPI documents.
type Synthesizer struct {
	httpClient *http.Client
}

var (
	_ openbindings.InterfaceSynthesizer = (*Synthesizer)(nil)
	_ openbindings.SourceInspector      = (*Synthesizer)(nil)
)

// NewSynthesizer creates a new AsyncAPI interface synthesizer.
func NewSynthesizer() *Synthesizer {
	return &Synthesizer{
		httpClient: newDefaultHTTPClient(),
	}
}

// Formats returns the source formats supported by the AsyncAPI synthesizer.
func (c *Synthesizer) Formats() []openbindings.FormatInfo {
	return []openbindings.FormatInfo{{Token: FormatToken, Description: "AsyncAPI 3.x event-driven APIs"}}
}

// SynthesizeInterface converts an AsyncAPI document to an OpenBindings interface.
func (c *Synthesizer) SynthesizeInterface(ctx context.Context, in *openbindings.SynthesizeInput) (*openbindings.Interface, error) {
	if len(in.Sources) == 0 {
		return nil, openbindings.ErrNoSources
	}
	src := in.Sources[0]
	doc, err := loadDocument(ctx, c.httpClient, src.Location, src.Content)
	if err != nil {
		return nil, err
	}
	return synthesizeInterfaceWithDoc(ctx, in, doc)
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

// PlanContributions: decode is a spec answer (the declared message
// contentType, else the text assumption); classify and route are not
// consulted on this format.
func (e *Invoker) PlanContributions(_ *openbindings.BindingInvocationArgs) (*openbindings.BindingPlan, error) {
	return &openbindings.BindingPlan{
		Decode:   openbindings.PlanAxis{Chain: []string{"spec/content-type"}},
		Classify: openbindings.PlanAxis{Chain: []string{"not-consulted"}},
	}, nil
}
