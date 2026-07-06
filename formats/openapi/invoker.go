// Package openapi implements the OpenAPI binding format for OpenBindings.
//
// The package handles:
//   - Converting OpenAPI 3.x documents to OpenBindings interfaces
//   - Invoking operations via HTTP requests through the Invocation handle
//   - Deriving context requirements (CONTEXT_REQUIRED negotiation) from the
//     document's securitySchemes
package openapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/getkin/kin-openapi/openapi3"
	openbindings "github.com/openbindings/openbindings-go"
)

// FormatToken identifies this package as an OpenAPI 3.x handler.
const FormatToken = "openapi@^3.0.0"

// DefaultSourceName is the default source key used when registering an OpenAPI source in an OBI.
const DefaultSourceName = "openapi"

// maxRedirects bounds the redirect chain a single HTTP request may follow.
// Prevents redirect loops without imposing any total request timeout
// (which is the caller's responsibility via context).
const maxRedirects = 10

// newDefaultHTTPClient constructs an HTTP client with the invoker's default
// redirect policy and no overall timeout (the caller controls cancellation
// via context). Each Invoker gets its own client so multiple Invokers can
// be configured independently and tests can substitute clients without
// reaching into package-level globals.
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

// Invoker handles binding invocation for OpenAPI 3.x sources.
//
// Each Invoker owns an HTTP client (*http.Client is safe for concurrent use
// by multiple goroutines, so all calls on a single Invoker share one client)
// and a per-instance document cache keyed by source location. The cache is
// scoped to the Invoker instance to avoid cross-tenant contamination in
// multi-tenant servers.
type Invoker struct {
	client   *http.Client
	mu       sync.RWMutex
	docCache map[string]*openapi3.T
}

var (
	_ openbindings.BindingInvoker  = (*Invoker)(nil)
	_ openbindings.BindingPreparer = (*Invoker)(nil)
)

// NewInvoker creates a new OpenAPI binding invoker with a default HTTP
// client. Use NewInvokerWithClient to inject a custom client (e.g., for
// tests, or to add a transport layer for tracing or auth).
func NewInvoker() *Invoker {
	return &Invoker{
		client:   newDefaultHTTPClient(),
		docCache: make(map[string]*openapi3.T),
	}
}

// NewInvokerWithClient creates an Invoker that uses the supplied
// *http.Client for all outbound requests. The caller is responsible for
// configuring redirect policy, transport, and any other client-level
// behavior. No overall request timeout should be set on the client because
// the caller controls cancellation via context.
func NewInvokerWithClient(client *http.Client) *Invoker {
	if client == nil {
		client = newDefaultHTTPClient()
	}
	return &Invoker{
		client:   client,
		docCache: make(map[string]*openapi3.T),
	}
}

// cachedLoadDocument loads an OpenAPI doc, caching by location within a process.
// When content is provided, the cache is bypassed and updated with the fresh parse.
func (e *Invoker) cachedLoadDocument(location string, content any) (*openapi3.T, error) {
	if location != "" && content == nil {
		e.mu.RLock()
		if doc, ok := e.docCache[location]; ok {
			e.mu.RUnlock()
			return doc, nil
		}
		e.mu.RUnlock()
	}

	doc, err := loadDocument(location, content)
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

// Formats returns the binding format tokens this invoker supports.
func (e *Invoker) Formats() []openbindings.FormatInfo {
	return []openbindings.FormatInfo{{Token: FormatToken, Description: "OpenAPI 3.x HTTP APIs"}}
}

// InvokeBinding invokes an HTTP request based on an OpenAPI binding. The
// Invocation handle is returned synchronously; creation is inert and the
// HTTP work is scheduled on its own goroutine. Input messages flow through
// the handle's Write channel. All pre-dispatch failures (bad ref, missing
// server URL, unresolvable operation, missing context) terminate the handle
// BEFORE any network side effect.
func (e *Invoker) InvokeBinding(ctx context.Context, args *openbindings.BindingInvocationArgs) openbindings.Invocation[any, any] {
	inv := openbindings.NewInvocationImpl[any, any](ctx)
	go func() {
		if err := e.run(ctx, args, inv); err != nil {
			inv.FireError(openbindings.AsInvocationError(err))
		}
	}()
	return inv
}

func (e *Invoker) run(ctx context.Context, args *openbindings.BindingInvocationArgs, inv *openbindings.InvocationImpl[any, any]) error {
	doc, err := e.cachedLoadDocument(args.Source.Location, args.Source.Content)
	if err != nil {
		inv.FireError(&openbindings.InvocationError{
			Code:    openbindings.ErrCodeSourceLoadFailed,
			Message: err.Error(),
		})
		return nil
	}
	runBinding(ctx, e.client, args, inv, doc)
	return nil
}

// PrepareBinding is the side-effect-free preflight (the prepareBinding
// operation of the openbindings.binding-invoker role): it derives the
// operation's auth requirements from the document's securitySchemes and
// reports the context the invocation would require, or nil when it can
// proceed.
//
// It uses the source content or a previously cached document; it never
// fetches. When the document would have to be fetched to learn its security
// schemes, it reports no requirement and lets the invocation raise the
// challenge instead.
func (e *Invoker) PrepareBinding(_ context.Context, args *openbindings.BindingInvocationArgs) (*openbindings.ContextRequiredDetails, error) {
	doc := e.prepareDoc(args.Source.Location, args.Source.Content)
	if doc == nil || doc.Paths == nil {
		return nil, nil
	}

	pathTemplate, method, err := parseRef(args.Ref)
	if err != nil {
		return nil, nil
	}
	pathItem := doc.Paths.Find(pathTemplate)
	if pathItem == nil {
		return nil, nil
	}
	op := pathItem.GetOperation(strings.ToUpper(method))
	if op == nil {
		return nil, nil
	}

	baseURL, err := resolveBaseURLWithLocation(doc, args.Context, args.Source.Location)
	if err != nil {
		// No server URL: the invocation fails with ErrCodeSourceConfigError
		// before auth matters, so there is no context to report.
		return nil, nil
	}
	return requiredContext(doc, op, args.Context, baseURL), nil
}

// prepareDoc returns the OpenAPI document without performing network I/O:
// inline source content is parsed locally (external refs disabled so the
// parse cannot fetch), and a location-only source is served from the warm
// document cache. Returns nil when the document is not knowable without I/O.
func (e *Invoker) prepareDoc(location string, content any) *openapi3.T {
	if content != nil {
		data, err := openbindings.ContentToBytes(content)
		if err != nil {
			return nil
		}
		loader := openapi3.NewLoader() // external refs NOT allowed: no I/O
		doc, err := loader.LoadFromData(data)
		if err != nil {
			return nil
		}
		return doc
	}
	if location == "" {
		return nil
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.docCache[location]
}

// Synthesizer handles interface creation from OpenAPI documents.
type Synthesizer struct{}

var (
	_ openbindings.InterfaceSynthesizer = (*Synthesizer)(nil)
	_ openbindings.SourceInspector      = (*Synthesizer)(nil)
)

// NewSynthesizer creates a new OpenAPI interface synthesizer.
func NewSynthesizer() *Synthesizer {
	return &Synthesizer{}
}

// Formats returns the format tokens this synthesizer supports.
func (c *Synthesizer) Formats() []openbindings.FormatInfo {
	return []openbindings.FormatInfo{{Token: FormatToken, Description: "OpenAPI 3.x HTTP APIs"}}
}

// SynthesizeInterface converts an OpenAPI document to an OpenBindings interface.
func (c *Synthesizer) SynthesizeInterface(ctx context.Context, in *openbindings.SynthesizeInput) (*openbindings.Interface, error) {
	if len(in.Sources) == 0 {
		return nil, openbindings.ErrNoSources
	}
	src := in.Sources[0]
	doc, err := loadDocument(src.Location, src.Content)
	if err != nil {
		return nil, fmt.Errorf("load OpenAPI document: %w", err)
	}
	iface := convertDocToInterface(doc, src.Location)
	if in.Name != "" {
		iface.Name = in.Name
	}
	if in.Version != "" {
		iface.Version = in.Version
	}
	if in.Description != "" {
		iface.Description = in.Description
	}
	return &iface, nil
}

// BuiltinHooks exposes the openapi builtins to the consultation seam's
// cross-format dispatch (openbindings.BuiltinDecode/BuiltinClassify): the
// decoder follows the delivery unit's declared Content-Type header (read
// from raw.Meta — wire framing, never payload sniffing); the classifier
// is the 2xx convention floor.
func (e *Invoker) BuiltinHooks() (openbindings.OutputDecoder, openbindings.ResultClassifier) {
	decode := func(site openbindings.InvokeSite, raw openbindings.RawResult) (any, error) {
		ct := ""
		if vs := raw.Meta["Content-Type"]; len(vs) > 0 {
			ct = vs[0]
		}
		return decodeByContentType(ct)(site, raw)
	}
	return decode, BuiltinClassify
}

// PlanContributions reports the openapi axis chain leaves: both §6 rules
// are spec/wire answers, and routing is spec-covered (in:/requestBody) —
// the FieldRouter is consulted but decline-only in v1.
func (e *Invoker) PlanContributions(_ *openbindings.BindingInvocationArgs) (*openbindings.BindingPlan, error) {
	return &openbindings.BindingPlan{
		Decode:   openbindings.PlanAxis{Chain: []string{"header/content-type"}},
		Classify: openbindings.PlanAxis{Chain: []string{"assumption/2xx"}},
	}, nil
}
