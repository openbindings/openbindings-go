// Package grpc implements the openbindings.grpc@1 binding specification
// for OpenBindings.
//
// The package handles:
//   - Discovering gRPC services via server reflection (v1, v1alpha on
//     UNIMPLEMENTED), embedded .proto source text, or an embedded
//     FileDescriptorSet in canonical JSON
//   - Converting protobuf service descriptors to OpenBindings interfaces
//   - Invoking unary, client-streaming, server-streaming, and bidirectional
//     RPCs with the interaction shape declared by protobuf
package grpc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/openbindings/openbindings-go/invoke"
	"github.com/openbindings/openbindings-go/synthesize"

	"github.com/jhump/protoreflect/v2/grpcdynamic"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/reflect/protoreflect"

	openbindings "github.com/openbindings/openbindings-go"
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
	_ invoke.BindingInvoker           = (*Invoker)(nil)
	_ synthesize.InterfaceSynthesizer = (*Synthesizer)(nil)
	_ synthesize.CoverageSynthesizer  = (*Synthesizer)(nil)
	_ synthesize.SourceInspector      = (*Synthesizer)(nil)
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

// BindingSpecs returns the binding-spec identifiers supported by the gRPC invoker.
func (e *Invoker) BindingSpecs() []openbindings.BindingSpecInfo {
	return []openbindings.BindingSpecInfo{{BindingSpec: BindingSpec, Description: "gRPC via server reflection or .proto files"}}
}

// InvokeBinding invokes a gRPC binding, returning the invocation handle
// synchronously. Creation is inert: descriptor resolution and the RPC run on
// the binding goroutine. Unary methods read one input and emit one output;
// server-streaming methods read one input and emit per received message.
// Native gRPC metadata stays below the abstract invocation boundary.
func (e *Invoker) InvokeBinding(ctx context.Context, args *invoke.BindingInvocationArgs) invoke.Invocation[any, any] {
	inv := invoke.NewInvocationImpl[any, any](ctx)
	go e.run(ctx, args, inv)
	return inv
}

// run resolves the selector to a method descriptor, reads the request from the
// handle, and dispatches the RPC. All pre-dispatch refusals (bad selector,
// missing or malformed target, configuration errors, descriptor load
// failures, schema-range and kind-coverage refusals) fire BEFORE any RPC
// is sent; with embedded content, resolution also precedes the dial (§7:
// a processor does not dial blind on the selector name).
func (e *Invoker) run(ctx context.Context, args *invoke.BindingInvocationArgs, inv *invoke.InvocationImpl[any, any]) {
	// bctx bounds all gRPC I/O to the invocation's lifetime: caller Cancel()
	// (or upstream ctx cancellation) tears down reflection and RPC streams.
	bctx, stop := invoke.DoneContext(ctx, inv.Done())
	defer stop()

	svcName, methodName, err := parseSelector(args.Selector)
	if err != nil {
		inv.FireError(&invoke.InvocationError{
			Code: invoke.ErrCodeInvalidSelector,
		})
		return
	}

	cfg := invoke.ContextConfiguration(args.Context)

	// Target configuration point (§9.3): the default is the source's
	// location; per-invocation/consumer configuration (context
	// configuration.target, both tiers merged by the operation invoker)
	// replaces it entirely, in the same §4 forms.
	target := strings.TrimSpace(args.Source.Location)
	if raw, ok := cfg["target"]; ok && raw != nil {
		s, isStr := raw.(string)
		if !isStr || strings.TrimSpace(s) == "" {
			inv.FireError(&invoke.InvocationError{
				Code: invoke.ErrCodeSourceConfigError,
			})
			return
		}
		target = strings.TrimSpace(s)
	}
	if target == "" {
		inv.FireError(&invoke.InvocationError{
			Code: invoke.ErrCodeSourceConfigError,
		})
		return
	}
	addr, err := parseDialAddress(target)
	if err != nil {
		inv.FireError(&invoke.InvocationError{
			Code: invoke.ErrCodeSourceConfigError,
		})
		return
	}

	// Transport configuration point (§9.3, GRPC-P-02): configuration →
	// invoker-level credentials → the §4 address-form determination.
	creds, transportTag, err := resolveTransport(cfg, e.dialCfg.creds, addr)
	if err != nil {
		inv.FireError(&invoke.InvocationError{
			Code: invoke.ErrCodeSourceConfigError,
		})
		return
	}

	// Credentials ride outgoing gRPC metadata (§9.5, GRPC-P-07); an
	// unplaceable key is surfaced here, pre-dispatch.
	_, _, hasBasic := invoke.ContextBasicAuth(args.Context)
	if invoke.ContextAPIKey(args.Context) != "" || invoke.ContextBearerToken(args.Context) != "" || hasBasic {
		inv.FireError(&invoke.InvocationError{
			Code: invoke.ErrCodeContextRequired,
		})
		return
	}
	rpcCtx, mdErr := applyGRPCContext(bctx, args.Context)
	if mdErr != nil {
		inv.FireError(&invoke.InvocationError{
			Code: invoke.ErrCodeSourceConfigError,
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
			inv.FireError(&invoke.InvocationError{
				Code: invoke.ErrCodeSourceLoadFailed,
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
		inv.FireError(&invoke.InvocationError{
			Code: invoke.ErrCodeConnectFailed,
		})
		return
	}

	if methodDesc == nil {
		refClient := newReflClient(rpcCtx, conn)
		defer refClient.Reset()
		svcDesc, err = resolveService(refClient, protoreflect.FullName(svcName))
		if err != nil {
			inv.FireError(selectorResolveError(svcName, err))
			return
		}
		methodDesc = svcDesc.Methods().ByName(protoreflect.Name(methodName))
		if methodDesc == nil {
			inv.FireError(&invoke.InvocationError{
				Code: invoke.ErrCodeSelectorNotFound,
			})
			return
		}
		if ie := preflightMethod(methodDesc); ie != nil {
			inv.FireError(ie)
			return
		}
	}

	stub := grpcdynamic.NewStub(conn)
	noInput := args.Binding != nil && args.InputSchema == nil
	switch {
	case methodDesc.IsStreamingClient() && methodDesc.IsStreamingServer():
		runBidiStream(rpcCtx, inv, stub, methodDesc, noInput)
	case methodDesc.IsStreamingClient():
		runClientStream(rpcCtx, inv, stub, methodDesc, noInput)
	case methodDesc.IsStreamingServer():
		reqMsg, ok := readRequest(bctx, inv, methodDesc, noInput)
		if !ok {
			return
		}
		runServerStream(rpcCtx, inv, stub, methodDesc, reqMsg)
	default:
		reqMsg, ok := readRequest(bctx, inv, methodDesc, noInput)
		if !ok {
			return
		}
		runUnary(rpcCtx, inv, stub, methodDesc, reqMsg)
	}
}

// preflightMethod applies the pre-dispatch gates that follow method
// resolution: §3's accepted schema range over the bound method's
// transitive closure (GRPC-P-03). All four protobuf interaction kinds are
// covered; the artifact's method declaration remains authoritative.
func preflightMethod(methodDesc protoreflect.MethodDescriptor) *invoke.InvocationError {
	if err := validateBoundClosure(methodDesc); err != nil {
		return &invoke.InvocationError{
			Code: invoke.ErrCodeSourceLoadFailed,
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

// BindingSpecs returns the binding-spec identifiers supported by the gRPC synthesizer.
func (c *Synthesizer) BindingSpecs() []openbindings.BindingSpecInfo {
	return []openbindings.BindingSpecInfo{{BindingSpec: BindingSpec, Description: "gRPC via server reflection or .proto files"}}
}

// SynthesizeInterface discovers gRPC services and converts to an OpenBindings interface.
// Supports the binding specification's two discovery modes: embedded
// protobuf content, or live server reflection when content is absent.
func (c *Synthesizer) SynthesizeInterface(ctx context.Context, in *synthesize.SynthesizeInput) (*openbindings.Interface, error) {
	observation, err := c.synthesizeObserved(ctx, in)
	if err != nil {
		return nil, err
	}
	return observation.iface, nil
}

// SynthesizeInterfaceWithCoverage returns the creation-time-sound OBI and a
// durable disposition for every protobuf method and lossy projection observed
// by the same descriptor load.
func (c *Synthesizer) SynthesizeInterfaceWithCoverage(ctx context.Context, in *synthesize.SynthesizeInput) (*synthesize.SynthesizeResult, error) {
	observation, err := c.synthesizeObserved(ctx, in)
	if err != nil {
		return nil, err
	}
	return synthesize.NewSynthesisResult(
		observation.iface,
		synthesisCoverage(observation.disc, observation.iface, observation.warnings),
		true,
	)
}

type synthesisObservation struct {
	iface    *openbindings.Interface
	disc     *discovery
	warnings []synthesize.SynthesizerWarning
}

func (c *Synthesizer) synthesizeObserved(ctx context.Context, in *synthesize.SynthesizeInput) (*synthesisObservation, error) {
	if len(in.Sources) == 0 {
		skeleton, err := synthesize.SynthesisSkeleton(in)
		if err != nil {
			return nil, err
		}
		return &synthesisObservation{iface: &skeleton}, nil
	}
	if len(in.Sources) > 1 {
		return nil, synthesize.ErrMultipleSources
	}
	src := in.Sources[0]
	if src.BindingSpec != BindingSpec {
		return nil, fmt.Errorf("synthesizer supports exact binding specification %q, got %q", BindingSpec, src.BindingSpec)
	}
	// Embedded proto/descriptor content is a complete discovery artifact and
	// may be synthesized offline without an invocation target. Validate the
	// live lane only when the caller actually supplied one.
	if src.Location != "" {
		if _, err := parseDialAddress(src.Location); err != nil {
			return nil, fmt.Errorf("location: %w", err)
		}
	}
	if src.OutputLocation != "" {
		if _, err := parseDialAddress(src.OutputLocation); err != nil {
			return nil, fmt.Errorf("outputLocation: %w", err)
		}
	}
	if src.Embed && src.Content == nil {
		return nil, fmt.Errorf("gRPC reflection embedding is not supported: preserving the complete reflected descriptor closure is required; provide embedded protobuf content explicitly")
	}

	var disc *discovery
	var err error
	var sourceLocation string

	if src.Content != nil {
		// Embedded content is authoritative and displaces reflection.
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

	var warnings []synthesize.SynthesizerWarning
	observeWarning := func(warning synthesize.SynthesizerWarning) {
		warnings = append(warnings, warning)
		if in.OnWarning != nil {
			in.OnWarning(warning)
		}
	}
	iface, err := convertToInterface(disc, sourceLocation, observeWarning)
	if err != nil {
		return nil, fmt.Errorf("gRPC convert: %w", err)
	}
	if src.Content != nil {
		entry := iface.Sources[DefaultSourceName]
		entry.Content = src.Content
		iface.Sources[DefaultSourceName] = entry
	}
	if err := synthesize.FinalizeSynthesis(&iface, in, DefaultSourceName, BindingSpec); err != nil {
		return nil, err
	}
	return &synthesisObservation{iface: &iface, disc: disc, warnings: warnings}, nil
}
