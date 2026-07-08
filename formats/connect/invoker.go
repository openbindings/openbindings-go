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

const FormatToken = "connect"
const DefaultSourceName = "connectServer"

// maxRedirects bounds the redirect chain a single request may follow.
// Prevents redirect loops without imposing any total request timeout
// (which is the caller's responsibility via context).
const maxRedirects = 10

// Invoker handles binding invocation for Connect sources.
type Invoker struct {
	client *http.Client
}

// NewInvoker creates a new Connect binding invoker.
func NewInvoker() *Invoker {
	return &Invoker{
		client: &http.Client{
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= maxRedirects {
					return fmt.Errorf("stopped after %d redirects", maxRedirects)
				}
				return nil
			},
		},
	}
}

// Formats returns the source formats supported by the Connect invoker.
func (e *Invoker) Formats() []openbindings.FormatInfo {
	return []openbindings.FormatInfo{{Token: FormatToken, Description: "Connect (Buf) via HTTP"}}
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
// All pre-dispatch failures (bad ref, missing base URL, descriptor failures)
// terminate the handle before any network side effect.
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

	baseURL := strings.TrimSpace(args.Source.Location)
	if baseURL == "" {
		inv.FireError(&openbindings.InvocationError{
			Code:    openbindings.ErrCodeSourceConfigError,
			Message: "Connect source requires a base URL location",
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
func (c *Synthesizer) Formats() []openbindings.FormatInfo {
	return []openbindings.FormatInfo{{Token: FormatToken, Description: "Connect (Buf) via HTTP"}}
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

	iface, err := convertToInterface(disc, src.Location)
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
