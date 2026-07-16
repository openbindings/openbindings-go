// Package grpc implements the openbindings.grpc@1 binding specification
// for OpenBindings.
//
// The package handles:
//   - Discovering gRPC services via server reflection (v1, v1alpha on
//     UNIMPLEMENTED), embedded .proto source text, or an embedded
//     FileDescriptorSet in canonical JSON
//   - Converting protobuf service descriptors to OpenBindings interfaces
//   - Invoking unary and server-streaming RPCs (client-streaming and
//     bidirectional methods are refused pre-dispatch as this
//     implementation's declared limitation, per GRPC-P-04)
package grpc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/jhump/protoreflect/v2/grpcdynamic"
	openbindings "github.com/openbindings/openbindings-go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const BindingSpec = "openbindings.grpc@1"
const DefaultSourceName = "grpcServer"

// Invoker handles binding invocation for gRPC sources.
type Invoker struct {
	conns   sync.Map // address -> *grpc.ClientConn
	dialCfg dialConfig
}

// InvokerOption configures an Invoker.
type InvokerOption func(*Invoker)

// WithTransportCredentials sets the invoker-level transport identity (mTLS
// client certificates, a custom CA pool, forced plaintext) for every
// connection this invoker dials. Per the transport configuration point
// (openbindings.grpc@1 §9.3, GRPC-P-02) it replaces the §4 address-form
// determination entirely — a caller who states the transport identity owns
// it — and is itself displaced by a per-invocation
// context.configuration.transport value.
func WithTransportCredentials(creds credentials.TransportCredentials) InvokerOption {
	return func(e *Invoker) { e.dialCfg.creds = creds }
}

// WithDialOptions appends grpc-go dial options (interceptors, keepalive,
// user-agent, ...) to every connection this invoker dials, after the
// defaults; grpc-go's last-wins semantics apply where an option overlaps.
// This is the grpc counterpart of the HTTP formats' NewInvokerWithClient:
// the invoker is openly grpc-go, so its transport speaks grpc-go's own
// vocabulary rather than an invented one.
func WithDialOptions(opts ...grpc.DialOption) InvokerOption {
	return func(e *Invoker) { e.dialCfg.extra = append(e.dialCfg.extra, opts...) }
}

var (
	_ openbindings.BindingInvoker       = (*Invoker)(nil)
	_ openbindings.InterfaceSynthesizer = (*Synthesizer)(nil)
	_ openbindings.SourceInspector      = (*Synthesizer)(nil)
)

// NewInvoker creates a new gRPC binding invoker.
func NewInvoker(opts ...InvokerOption) *Invoker {
	e := &Invoker{}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// getConn returns a cached channel for the (target, transport identity)
// pair, dialing one if absent. The transport tag is part of the key
// because the transport configuration point can vary per invocation
// (§9.3): distinct identities never share a channel.
func (e *Invoker) getConn(addr dialAddress, creds credentials.TransportCredentials, transportTag string) (*grpc.ClientConn, error) {
	key := connKey(addr.hostPort, transportTag)
	if v, ok := e.conns.Load(key); ok {
		return v.(*grpc.ClientConn), nil
	}
	conn, err := dial(addr, creds, e.dialCfg.extra)
	if err != nil {
		return nil, err
	}
	if actual, loaded := e.conns.LoadOrStore(key, conn); loaded {
		_ = conn.Close()
		return actual.(*grpc.ClientConn), nil
	}
	return conn, nil
}

// Close tears down all cached connections (io.Closer). After Close returns,
// the Invoker should not be used for new invocations.
func (e *Invoker) Close() error {
	var errs []error
	e.conns.Range(func(key, value any) bool {
		if err := value.(*grpc.ClientConn).Close(); err != nil {
			errs = append(errs, err)
		}
		e.conns.Delete(key)
		return true
	})
	return errors.Join(errs...)
}

// Formats returns the source formats supported by the gRPC invoker.
func (e *Invoker) BindingSpecs() []openbindings.BindingSpecInfo {
	return []openbindings.BindingSpecInfo{{BindingSpec: BindingSpec, Description: "gRPC via server reflection or .proto files"}}
}

// InvokeBinding invokes a gRPC binding, returning the invocation handle
// synchronously. Creation is inert: descriptor resolution and the RPC run on
// the binding goroutine. Unary methods read one input and emit one output;
// server-streaming methods read one input and emit per received message.
// gRPC metadata maps onto the handle's Header/Trailer surfaces.
func (e *Invoker) InvokeBinding(ctx context.Context, args *openbindings.BindingInvocationArgs) openbindings.Invocation[any, any] {
	inv := openbindings.NewInvocationImpl[any, any](ctx)
	go e.run(ctx, args, inv)
	return inv
}

// run resolves the ref to a method descriptor, reads the request from the
// handle, and dispatches the RPC. All pre-dispatch refusals (bad ref,
// missing or malformed target, configuration errors, descriptor load
// failures, schema-range and kind-coverage refusals) fire BEFORE any RPC
// is sent; with embedded content, resolution also precedes the dial (§7:
// a processor does not dial blind on the ref name).
func (e *Invoker) run(ctx context.Context, args *openbindings.BindingInvocationArgs, inv *openbindings.InvocationImpl[any, any]) {
	// bctx bounds all gRPC I/O to the invocation's lifetime: caller Cancel()
	// (or upstream ctx cancellation) tears down reflection and RPC streams.
	bctx, stop := openbindings.DoneContext(ctx, inv.Done())
	defer stop()

	svcName, methodName, err := parseRef(args.Ref)
	if err != nil {
		inv.FireError(&openbindings.InvocationError{
			Code:    openbindings.ErrCodeInvalidRef,
			Message: err.Error(),
		})
		return
	}

	cfg := openbindings.ContextConfiguration(args.Context)

	// Target configuration point (§9.3): the default is the source's
	// location; per-invocation/consumer configuration (context
	// configuration.target, both tiers merged by the operation invoker)
	// replaces it entirely, in the same §4 forms.
	target := strings.TrimSpace(args.Source.Location)
	if raw, ok := cfg["target"]; ok && raw != nil {
		s, isStr := raw.(string)
		if !isStr || strings.TrimSpace(s) == "" {
			inv.FireError(&openbindings.InvocationError{
				Code:    openbindings.ErrCodeSourceConfigError,
				Message: fmt.Sprintf("configuration.target must be a dial address string in the openbindings.grpc@1 §4 forms, got %T", raw),
			})
			return
		}
		target = strings.TrimSpace(s)
	}
	if target == "" {
		msg := "gRPC source requires a server dial address: set the source's location (host:port, grpc://host:port, or grpcs://host:port)"
		if args.Source.Content != nil {
			msg = "gRPC source carries its schema as embedded content but no server address (a content-only source is not conformant, GRPC-D-02): set the source's location to the service dial address, or supply the target configuration point (context configuration.target)"
		}
		inv.FireError(&openbindings.InvocationError{
			Code:    openbindings.ErrCodeSourceConfigError,
			Message: msg,
		})
		return
	}
	addr, err := parseDialAddress(target)
	if err != nil {
		inv.FireError(&openbindings.InvocationError{
			Code:    openbindings.ErrCodeSourceConfigError,
			Message: err.Error(),
		})
		return
	}

	// Transport configuration point (§9.3, GRPC-P-02): configuration →
	// invoker-level credentials → the §4 address-form determination.
	creds, transportTag, err := resolveTransport(cfg, e.dialCfg.creds, addr)
	if err != nil {
		inv.FireError(&openbindings.InvocationError{
			Code:    openbindings.ErrCodeSourceConfigError,
			Message: err.Error(),
		})
		return
	}

	// Credentials ride outgoing gRPC metadata (§9.5, GRPC-P-07); an
	// unplaceable key is surfaced here, pre-dispatch.
	rpcCtx, mdErr := applyGRPCContext(bctx, args.Context)
	if mdErr != nil {
		inv.FireError(&openbindings.InvocationError{
			Code:    openbindings.ErrCodeSourceConfigError,
			Message: mdErr.Error(),
		})
		return
	}

	// Resolve service and method descriptors. Embedded content is the
	// artifact the processor interprets (§6, content primacy): descriptors
	// come from it and reflection is never consulted — resolution and the
	// pre-dispatch gates run before any dial. A location-only source
	// resolves via Server Reflection after connecting (GRPC-P-01).
	var svcDesc protoreflect.ServiceDescriptor
	var methodDesc protoreflect.MethodDescriptor
	if args.Source.Content != nil {
		disc, parseErr := discoverFromContent(bctx, args.Source.Content)
		if parseErr != nil {
			inv.FireError(&openbindings.InvocationError{
				Code:    openbindings.ErrCodeSourceLoadFailed,
				Message: parseErr.Error(),
			})
			return
		}
		m, ie := resolveMethod(disc, svcName, methodName)
		if ie != nil {
			inv.FireError(ie)
			return
		}
		methodDesc = m
		if ie := preflightMethod(methodDesc); ie != nil {
			inv.FireError(ie)
			return
		}
	}

	conn, err := e.getConn(addr, creds, transportTag)
	if err != nil {
		inv.FireError(&openbindings.InvocationError{
			Code:    openbindings.ErrCodeConnectFailed,
			Message: err.Error(),
		})
		return
	}

	if methodDesc == nil {
		refClient := newReflClient(rpcCtx, conn)
		defer refClient.Reset()
		svcDesc, err = resolveService(refClient, protoreflect.FullName(svcName))
		if err != nil {
			inv.FireError(refResolveError(svcName, err))
			return
		}
		methodDesc = svcDesc.Methods().ByName(protoreflect.Name(methodName))
		if methodDesc == nil {
			inv.FireError(&openbindings.InvocationError{
				Code:    openbindings.ErrCodeRefNotFound,
				Message: fmt.Sprintf("method %q not found in service %q", methodName, svcName),
			})
			return
		}
		if ie := preflightMethod(methodDesc); ie != nil {
			inv.FireError(ie)
			return
		}
	}

	noInput := args.Binding != nil && args.InputSchema == nil
	reqMsg, ok := readRequest(bctx, inv, methodDesc, noInput)
	if !ok {
		return
	}

	stub := grpcdynamic.NewStub(conn)
	if methodDesc.IsStreamingServer() {
		runServerStream(rpcCtx, inv, stub, methodDesc, reqMsg)
	} else {
		runUnary(rpcCtx, inv, stub, methodDesc, reqMsg)
	}
}

// preflightMethod applies the pre-dispatch gates that follow method
// resolution: §3's accepted schema range over the bound method's
// transitive closure (GRPC-P-03), then interaction-kind coverage
// (GRPC-P-04). Client-streaming and bidirectional methods are refused
// before dispatch as this implementation's declared limitation — a
// coverage declaration, never a reinterpretation of the shape.
func preflightMethod(methodDesc protoreflect.MethodDescriptor) *openbindings.InvocationError {
	if err := validateBoundClosure(methodDesc); err != nil {
		return &openbindings.InvocationError{
			Code:    openbindings.ErrCodeSourceLoadFailed,
			Message: err.Error(),
		}
	}
	if methodDesc.IsStreamingClient() {
		kind := "client-streaming"
		if methodDesc.IsStreamingServer() {
			kind = "bidirectional-streaming"
		}
		return &openbindings.InvocationError{
			Code:    openbindings.ErrCodeExecutionFailed,
			Message: fmt.Sprintf("gRPC method %q is %s; this invoker covers unary and server-streaming methods (implementation-declared limitation, openbindings.grpc@1 §8 / GRPC-P-04)", methodDesc.FullName(), kind),
		}
	}
	return nil
}

// Synthesizer handles interface synthesis from gRPC servers.
type Synthesizer struct {
	dialCfg dialConfig
}

// SynthesizerOption configures a Synthesizer.
type SynthesizerOption func(*Synthesizer)

// WithSynthesizerTransportCredentials sets the transport identity used when
// discovering via server reflection. Discovery dials the live service, so a
// setup the invocation lane needs (mTLS, custom CA) is needed here too.
func WithSynthesizerTransportCredentials(creds credentials.TransportCredentials) SynthesizerOption {
	return func(c *Synthesizer) { c.dialCfg.creds = creds }
}

// WithSynthesizerDialOptions appends grpc-go dial options to reflection
// discovery connections, after the defaults.
func WithSynthesizerDialOptions(opts ...grpc.DialOption) SynthesizerOption {
	return func(c *Synthesizer) { c.dialCfg.extra = append(c.dialCfg.extra, opts...) }
}

// NewSynthesizer creates a new gRPC interface synthesizer.
func NewSynthesizer(opts ...SynthesizerOption) *Synthesizer {
	c := &Synthesizer{}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Formats returns the source formats supported by the gRPC synthesizer.
func (c *Synthesizer) BindingSpecs() []openbindings.BindingSpecInfo {
	return []openbindings.BindingSpecInfo{{BindingSpec: BindingSpec, Description: "gRPC via server reflection or .proto files"}}
}

// SynthesizeInterface discovers gRPC services and converts to an OpenBindings interface.
// Supports two discovery modes:
//   - Live server reflection (default): connects to the address and introspects via gRPC reflection
//   - Proto file: parses a .proto file when the location ends in .proto or inline content is provided
func (c *Synthesizer) SynthesizeInterface(ctx context.Context, in *openbindings.SynthesizeInput) (*openbindings.Interface, error) {
	if len(in.Sources) == 0 {
		return nil, openbindings.ErrNoSources
	}
	if len(in.Sources) > 1 {
		return nil, openbindings.ErrMultipleSources
	}
	src := in.Sources[0]

	var disc *discovery
	var err error
	var sourceLocation string

	if src.Content != nil || isProtoFile(src.Location) {
		// Parse from .proto file or inline content.
		disc, err = discoverFromProto(ctx, src.Location, src.Content)
		if err != nil {
			return nil, fmt.Errorf("gRPC proto parse: %w", err)
		}
		sourceLocation = src.Location
	} else {
		// Discover via live server reflection.
		addr := src.Location
		if addr == "" {
			return nil, fmt.Errorf("gRPC source requires a location (host:port address or .proto file path)")
		}
		disc, err = discover(ctx, addr, c.dialCfg)
		if err != nil {
			return nil, fmt.Errorf("gRPC discovery: %w", err)
		}
		sourceLocation = addr
	}

	iface, err := convertToInterface(disc, sourceLocation, in.OnWarning)
	if err != nil {
		return nil, fmt.Errorf("gRPC convert: %w", err)
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
