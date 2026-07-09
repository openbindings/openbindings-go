package operationgraph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"

	openbindings "github.com/openbindings/openbindings-go"
)

// FormatToken identifies this package as an operation graph handler.
const FormatToken = "openbindings.operation-graph@0.2.0"

// Invoker handles binding invocation for operation graph sources.
//
// The document and compiled-schema caches are scoped to the Invoker
// instance (keyed by source location) and live as long as it does — scope
// instances per tenant to bound growth and avoid cross-tenant
// contamination in multi-tenant servers.
type Invoker struct {
	invoker  *openbindings.OperationInvoker
	mu       sync.RWMutex
	docCache map[string]any
	schemas  *schemaCache
	client   *http.Client
}

// NewInvoker creates a new operation graph binding invoker with a default
// HTTP client (used only to fetch graph documents; use NewInvokerWithClient
// to inject a custom one). The OperationInvoker is used to invoke
// sub-operations referenced by operation and each nodes in the graph — the
// mutual reference is inherent to the format (graphs recurse through the
// operation invoker), so wiring is two-phase:
//
//	opInv := openbindings.NewOperationInvoker(openapi.NewInvoker())
//	opInv.AddBindingInvoker(operationgraph.NewInvoker(opInv))
func NewInvoker(invoker *openbindings.OperationInvoker) *Invoker {
	return NewInvokerWithClient(invoker, nil)
}

// NewInvokerWithClient creates an Invoker that uses the supplied
// *http.Client to fetch graph documents. A nil client falls back to the
// default. The caller is responsible for configuring redirect policy,
// transport, and any other client-level behavior. No overall request
// timeout should be set on the client because the caller controls
// cancellation via context.
func NewInvokerWithClient(invoker *openbindings.OperationInvoker, client *http.Client) *Invoker {
	if client == nil {
		client = &http.Client{
			CheckRedirect: func(_ *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return fmt.Errorf("stopped after 10 redirects")
				}
				return nil
			},
		}
	}
	return &Invoker{
		invoker:  invoker,
		docCache: make(map[string]any),
		schemas:  newSchemaCache(),
		client:   client,
	}
}

// maxGraphDocBytes bounds a graph document fetched from a remote location.
const maxGraphDocBytes = 8 << 20 // 8 MiB

// Formats returns the binding format tokens this invoker supports.
func (e *Invoker) Formats() []openbindings.FormatInfo {
	return []openbindings.FormatInfo{{Token: FormatToken, Description: "OpenBindings operation graphs"}}
}

// InvokeBinding invokes an operation graph binding. The handle is returned
// synchronously; the graph engine runs on its own goroutine. Caller writes
// stream in through the handle's input side (each write becomes one event at
// the graph's input node, rooting a lineage) and graph events stream out
// through the output side.
//
// Pre-flight failures (source load, ref resolution, version refusal per
// OG-T-02, validation per OG-T-01) surface as a terminal error through the
// handle without consuming any caller input. Creation is inert: the handle
// returns synchronously and the document load (a potential network fetch)
// happens on the driving goroutine, per the core invocation contract.
func (e *Invoker) InvokeBinding(ctx context.Context, args *openbindings.BindingInvocationArgs) openbindings.Invocation[any, any] {
	inv := openbindings.NewInvocationImpl[any, any](ctx)
	go e.drive(ctx, args, inv)
	return inv
}

// drive performs the preflight (load, resolve, version-check, validate) and
// runs the engine, firing preflight failures as terminal errors on the
// handle.
func (e *Invoker) drive(ctx context.Context, args *openbindings.BindingInvocationArgs, inv *openbindings.InvocationImpl[any, any]) {
	doc, err := e.loadDocument(ctx, args.Source.Location, args.Source.Content)
	if err != nil {
		inv.FireError(&openbindings.InvocationError{
			Code:    openbindings.ErrCodeSourceLoadFailed,
			Message: err.Error(),
		})
		return
	}

	// The ref is a REQUIRED JSON Pointer fragment addressing the graph
	// definition within the (otherwise unconstrained) host document.
	target, err := resolveRef(doc, args.Ref)
	if err != nil {
		code := openbindings.ErrCodeRefNotFound
		var re *refError
		if errors.As(err, &re) && re.invalid {
			code = openbindings.ErrCodeInvalidRef
		}
		inv.FireError(&openbindings.InvocationError{
			Code:    code,
			Message: err.Error(),
		})
		return
	}
	graph, err := graphFromValue(target)
	if err != nil {
		inv.FireError(&openbindings.InvocationError{
			Code:    openbindings.ErrCodeValidationFailed,
			Message: fmt.Sprintf("ref %q: %v", args.Ref, err),
		})
		return
	}

	// OG-T-02: refuse graphs declaring a format version this implementation
	// does not support. The graph's own field governs (the source format
	// token is advisory).
	if graph.Version != "" && semverRe.MatchString(graph.Version) {
		if err := checkVersion(graph.Version); err != nil {
			inv.FireError(&openbindings.InvocationError{
				Code:    openbindings.ErrCodeUnsupportedFormatVersion,
				Message: err.Error(),
			})
			return
		}
	}

	// OG-T-01: validate before acting; fail the binding rather than execute
	// an invalid graph. When Interface is absent (direct binding invocation)
	// the OG-V-11 reference check is skipped; bad references fail at runtime.
	var opKeys map[string]bool
	if args.Interface != nil {
		opKeys = make(map[string]bool, len(args.Interface.Operations))
		for k := range args.Interface.Operations {
			opKeys[k] = true
		}
	}
	if err := Validate(graph, opKeys); err != nil {
		inv.FireError(&openbindings.InvocationError{
			Code:    openbindings.ErrCodeValidationFailed,
			Message: err.Error(),
		})
		return
	}

	eng := newEngine(graph, e.invoker, args, e.invoker.TransformEvaluator, e.schemas)
	eng.execute(ctx, inv)
}

// loadDocument loads and parses an operation graph source document into a
// generic JSON value (the host document's shape is unconstrained by the
// format). Parsed documents are cached by location when content is absent.
func (e *Invoker) loadDocument(ctx context.Context, location string, content any) (any, error) {
	if location != "" && content == nil {
		e.mu.RLock()
		if doc, ok := e.docCache[location]; ok {
			e.mu.RUnlock()
			return doc, nil
		}
		e.mu.RUnlock()
	}

	var data []byte
	switch v := content.(type) {
	case []byte:
		data = v
	case string:
		data = []byte(v)
	case json.RawMessage:
		data = []byte(v)
	default:
		if content != nil {
			var err error
			data, err = json.Marshal(content)
			if err != nil {
				return nil, fmt.Errorf("marshal content: %w", err)
			}
		}
	}

	if data == nil {
		if location == "" {
			return nil, fmt.Errorf("source must have location or content")
		}
		var err error
		data, err = e.loadLocation(ctx, location)
		if err != nil {
			return nil, err
		}
	}

	var doc any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse operation graph document: %w", err)
	}

	if location != "" {
		e.mu.Lock()
		e.docCache[location] = doc
		e.mu.Unlock()
	}
	return doc, nil
}

// loadLocation reads a graph document from an http(s) URL or a local file
// path (bare path or file:// URI). Remote responses are size-bounded and
// the fetch runs under ctx (the invocation's lifetime), per the core
// contract that the caller controls cancellation via context.
func (e *Invoker) loadLocation(ctx context.Context, location string) ([]byte, error) {
	if openbindings.IsHTTPURL(location) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
		if err != nil {
			return nil, fmt.Errorf("invalid location %q: %w", location, err)
		}
		resp, err := e.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetch operation graph %q: %w", location, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("fetch operation graph %q: HTTP %d", location, resp.StatusCode)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxGraphDocBytes+1))
		if err != nil {
			return nil, fmt.Errorf("read operation graph %q: %w", location, err)
		}
		if len(data) > maxGraphDocBytes {
			return nil, fmt.Errorf("operation graph %q exceeds %d bytes", location, maxGraphDocBytes)
		}
		return data, nil
	}

	path := strings.TrimPrefix(location, "file://")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read operation graph %q: %w", location, err)
	}
	return data, nil
}
