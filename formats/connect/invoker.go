// Package connect implements the Connect (Buf) binding format for OpenBindings.
//
// The package handles:
//   - Discovering services from .proto files or inline protobuf definitions
//   - Converting protobuf service descriptors to OpenBindings interfaces
//   - Invoking unary RPCs via the Connect protocol (HTTP POST with JSON)
//
// Connect uses the same protobuf service definitions and ref convention as gRPC
// (package.Service/Method) but communicates over HTTP/1.1 or HTTP/2 with a
// simpler wire format. See https://connectrpc.com for protocol details.
package connect

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	openbindings "github.com/openbindings/openbindings-go"
)

const BindingSpec = "openbindings.connect@1"
const DefaultSourceName = "connectServer"

// maxRedirects bounds the redirect chain a single request may follow.
// Prevents redirect loops without imposing any total request timeout
// (which is the caller's responsibility via context).
const maxRedirects = 10

// Invoker handles binding invocation for Connect sources.
type Invoker struct {
	client *http.Client
}

// NewInvoker creates a new Connect binding invoker with a default HTTP
// client. Use NewInvokerWithClient to inject a custom client (e.g., for
// tests, or to add a transport layer for tracing or auth).
func NewInvoker() *Invoker {
	return NewInvokerWithClient(nil)
}

// NewInvokerWithClient creates an Invoker that uses the supplied
// *http.Client for all outbound requests. A nil client falls back to the
// default. The caller is responsible for configuring redirect policy,
// transport, and any other client-level behavior. No overall request
// timeout should be set on the client because the caller controls
// cancellation via context.
func NewInvokerWithClient(client *http.Client) *Invoker {
	if client == nil {
		client = &http.Client{
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= maxRedirects {
					return fmt.Errorf("stopped after %d redirects", maxRedirects)
				}
				return nil
			},
		}
	}
	return &Invoker{client: client}
}

// Formats returns the source formats supported by the Connect invoker.
func (e *Invoker) BindingSpecs() []openbindings.BindingSpecInfo {
	return []openbindings.BindingSpecInfo{{BindingSpec: BindingSpec, Description: "Connect (Buf) via HTTP"}}
}

var _ openbindings.BindingInvoker = (*Invoker)(nil)

// InvokeBinding invokes a Connect binding and returns the invocation handle
// synchronously; the HTTP work runs on its own goroutine. The single request
// message flows through the handle's Write channel (unary and
// server-streaming methods both take exactly one input).
//
// For unary methods the invocation yields one output. For server-streaming
// methods (detected via the inline proto descriptor) it yields one output per
// server-streamed message and closes when the server's end-stream envelope is
// received or the caller cancels.
//
// Server-streaming requires the source to provide inline proto `content` so
// the invoker can determine that the method is streaming. If no proto content
// is available, the invoker falls back to unary invocation and the binding
// will fail at runtime if the method is actually streaming.
//
// Client-streaming and bidirectional methods are out of scope. When a
// descriptor is available (inline proto `content`), a client-streaming or
// bidi-streaming method is refused pre-dispatch with a clear invocation
// error rather than misread as unary; without a descriptor this cardinality
// is unknowable in advance and the unary-fallback behavior above applies.
//
// All pre-dispatch failures (bad ref, missing base URL, descriptor failures,
// unsupported cardinality) terminate the handle before any network side
// effect.
func (e *Invoker) InvokeBinding(ctx context.Context, args *openbindings.BindingInvocationArgs) openbindings.Invocation[any, any] {
	inv := openbindings.NewInvocationImpl[any, any](ctx)
	go e.run(ctx, args, inv)
	return inv
}

func (e *Invoker) run(ctx context.Context, args *openbindings.BindingInvocationArgs, inv *openbindings.InvocationImpl[any, any]) {
	// Bound all I/O to the invocation's lifetime: caller Cancel(), an
	// abandoned output stream, or upstream ctx cancellation tears down any
	// in-flight HTTP request.
	bctx, stop := openbindings.DoneContext(ctx, inv.Done())
	defer stop()

	// ----- Pre-side-effect failures: fire before any input is consumed and
	// before any network I/O, so a no-input-consumed retry is safe. -----

	svcName, methodName, err := parseRef(args.Ref)
	if err != nil {
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeInvalidRef, Message: err.Error()})
		return
	}

	// The base URL: the source's location, else the context metadata
	// override (`metadata.baseURL`, the same key the grpc and openapi
	// invokers honor for targeting the same OBI at a different host). An
	// embed-mode source carries the SCHEMA as content, so the base URL is
	// the one thing the document cannot supply from the artifact itself.
	baseURL := strings.TrimSpace(args.Source.Location)
	if baseURL == "" {
		if meta := openbindings.ContextMetadata(args.Context); meta != nil {
			if v, ok := meta["baseURL"].(string); ok {
				baseURL = strings.TrimSpace(v)
			}
		}
	}
	if baseURL == "" {
		msg := "Connect source requires a base URL: set the source's location"
		if args.Source.Content != nil {
			msg = "Connect source carries its schema as embedded content but no base URL: set the source's location to the service base URL, or provide baseURL in context metadata"
		}
		inv.FireError(&openbindings.InvocationError{
			Code:    openbindings.ErrCodeSourceConfigError,
			Message: msg,
		})
		return
	}

	// Resolve the method descriptor from inline content. The descriptor lets
	// the invoker distinguish unary from server-streaming methods and lets it
	// use proto-aware marshaling for accurate field names. If no content is
	// provided, we fall through as unary with generic JSON marshaling.
	var methodDesc *methodInfo
	if args.Source.Content != nil {
		desc, parseErr := resolveMethod(bctx, args.Source.Content, svcName, methodName)
		if parseErr != nil {
			inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeSourceLoadFailed, Message: parseErr.Error()})
			return
		}
		methodDesc = desc
	}

	// A resolved descriptor exposes cardinality: refuse client-streaming and
	// bidi-streaming methods loudly here, before any input is consumed or any
	// network I/O, rather than silently mis-dispatching them as unary (which
	// would read one input, drop the rest, and send a single unframed request
	// the server never expects). Mirrors grpc's pre-dispatch refusal. Without
	// a descriptor (no embedded proto content), cardinality is unknowable in
	// advance; that case stays unary and fails at runtime if the method is
	// actually streaming — the format's documented convention.
	if methodDesc != nil && methodDesc.method != nil && methodDesc.method.IsStreamingClient() {
		kind := "client-streaming"
		if methodDesc.method.IsStreamingServer() {
			kind = "bidi-streaming"
		}
		inv.FireError(&openbindings.InvocationError{
			Code:    openbindings.ErrCodeExecutionFailed,
			Message: fmt.Sprintf("Connect method %s/%s is %s; the connect invoker supports unary and server-streaming methods", svcName, methodName, kind),
		})
		return
	}

	// ----- Input flows through the handle, not the args. Unary and
	// server-streaming both take exactly one request message. -----
	//
	// No-input convention: when the operation layer drives an operation that
	// declares no input (Binding set, InputSchema nil), close input on entry
	// and dispatch an empty request — a caller of a no-input operation never
	// writes nor closes, so reading would park forever.
	var input any
	if args.Binding != nil && args.InputSchema == nil {
		_ = inv.CloseInput()
	} else {
		in, gotInput, rerr := readFirstInput(bctx, inv)
		if rerr != nil {
			// Terminal (cancelled) or ctx error; FireError is an idempotent
			// no-op when the invocation already terminated.
			inv.FireError(openbindings.AsInvocationError(rerr))
			return
		}
		if !gotInput && !emptyRequestMessage(methodDesc) {
			inv.FireError(&openbindings.InvocationError{
				Code:    openbindings.ErrCodeMissingInput,
				Message: fmt.Sprintf("Connect method %s/%s requires an input message", svcName, methodName),
			})
			return
		}
		_ = inv.CloseInput()
		input = in
	}

	headers := buildHTTPHeaders(args.Context)

	if methodDesc != nil && methodDesc.method != nil && methodDesc.method.IsStreamingServer() {
		e.runStreaming(bctx, inv, baseURL, svcName, methodName, input, headers, methodDesc)
		return
	}
	e.runUnary(bctx, inv, baseURL, svcName, methodName, input, headers, methodDesc)
}

// readFirstInput reads the single request message from the handle.
// A bare close (io.EOF) reports gotInput=false with a nil error.
func readFirstInput(ctx context.Context, inv openbindings.BindingHandle[any, any]) (input any, gotInput bool, err error) {
	v, err := inv.ReadInput(ctx)
	if err == io.EOF {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return v, true, nil
}

// emptyRequestMessage reports whether the method's request message is
// provably empty (zero fields), in which case a bare input close is
// acceptable and an empty message is dispatched. Without a descriptor the
// invoker cannot prove emptiness and requires an input.
func emptyRequestMessage(mi *methodInfo) bool {
	return mi != nil && mi.method != nil && mi.method.Input().Fields().Len() == 0
}

// Synthesizer handles interface synthesis from protobuf definitions for the Connect format.
type Synthesizer struct{}

// NewSynthesizer creates a new Connect interface synthesizer.
func NewSynthesizer() *Synthesizer { return &Synthesizer{} }

// Formats returns the source formats supported by the Connect synthesizer.
func (c *Synthesizer) BindingSpecs() []openbindings.BindingSpecInfo {
	return []openbindings.BindingSpecInfo{{BindingSpec: BindingSpec, Description: "Connect (Buf) via HTTP"}}
}

// SynthesizeInterface parses a .proto file or inline content and converts to an
// OpenBindings interface with Connect bindings.
func (c *Synthesizer) SynthesizeInterface(ctx context.Context, in *openbindings.SynthesizeInput) (*openbindings.Interface, error) {
	if len(in.Sources) == 0 {
		return nil, openbindings.ErrNoSources
	}
	if len(in.Sources) > 1 {
		return nil, openbindings.ErrMultipleSources
	}
	src := in.Sources[0]
	if src.Location == "" && src.Content == nil {
		return nil, fmt.Errorf("Connect source requires a location or content")
	}

	disc, err := discoverFromProto(ctx, src.Location, src.Content)
	if err != nil {
		return nil, fmt.Errorf("Connect proto parse: %w", err)
	}

	iface, err := convertToInterface(disc, src.Location, in.OnWarning)
	if err != nil {
		return nil, fmt.Errorf("Connect convert: %w", err)
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
	return &iface, nil
}
