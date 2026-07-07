// Package grpc implements the gRPC binding format for OpenBindings.
//
// The package handles:
//   - Discovering gRPC services via server reflection or .proto files
//   - Converting protobuf service descriptors to OpenBindings interfaces
//   - Invoking unary and server-streaming RPCs
package grpc

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/jhump/protoreflect/v2/grpcdynamic"
	"github.com/jhump/protoreflect/v2/grpcreflect"
	openbindings "github.com/openbindings/openbindings-go"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const FormatToken = "grpc"
const DefaultSourceName = "grpcServer"

// Invoker handles binding invocation for gRPC sources.
type Invoker struct {
	conns sync.Map // address -> *grpc.ClientConn
}

var (
	_ openbindings.BindingInvoker       = (*Invoker)(nil)
	_ openbindings.InterfaceSynthesizer = (*Synthesizer)(nil)
	_ openbindings.SourceInspector      = (*Synthesizer)(nil)
)

// NewInvoker creates a new gRPC binding invoker.
func NewInvoker() *Invoker { return &Invoker{} }

func (e *Invoker) getConn(ctx context.Context, address string) (*grpc.ClientConn, error) {
	key := address
	if v, ok := e.conns.Load(key); ok {
		return v.(*grpc.ClientConn), nil
	}
	conn, err := dial(ctx, address)
	if err != nil {
		return nil, err
	}
	if actual, loaded := e.conns.LoadOrStore(key, conn); loaded {
		_ = conn.Close()
		return actual.(*grpc.ClientConn), nil
	}
	return conn, nil
}

// Close tears down all cached connections.
func (e *Invoker) Close() {
	e.conns.Range(func(key, value any) bool {
		_ = value.(*grpc.ClientConn).Close()
		e.conns.Delete(key)
		return true
	})
}

// Formats returns the source formats supported by the gRPC invoker.
func (e *Invoker) Formats() []openbindings.FormatInfo {
	return []openbindings.FormatInfo{{Token: FormatToken, Description: "gRPC via server reflection or .proto files"}}
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
// handle, and dispatches the RPC. All pre-dispatch failures (bad ref,
// missing target, descriptor load failures) fire BEFORE any RPC is sent.
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

	if strings.TrimSpace(args.Source.Location) == "" {
		inv.FireError(&openbindings.InvocationError{
			Code:    openbindings.ErrCodeSourceConfigError,
			Message: "gRPC source requires a location (server address)",
		})
		return
	}

	rpcCtx := applyGRPCContext(bctx, args.Context)

	// Resolve service and method descriptors. If inline content is provided
	// (e.g., a .proto definition), parse it directly — before dialing.
	// Otherwise use server reflection. Note: isProtoFile is NOT checked here
	// because Source.Location is the server address for invocation; proto
	// file locations are only used by the Synthesizer.
	var svcDesc protoreflect.ServiceDescriptor
	if args.Source.Content != nil {
		disc, parseErr := discoverFromProto(bctx, args.Source.Location, args.Source.Content)
		if parseErr != nil {
			inv.FireError(&openbindings.InvocationError{
				Code:    openbindings.ErrCodeSourceLoadFailed,
				Message: parseErr.Error(),
			})
			return
		}
		for _, svc := range disc.services {
			if string(svc.FullName()) == svcName {
				svcDesc = svc
				break
			}
		}
		if svcDesc == nil {
			inv.FireError(&openbindings.InvocationError{
				Code:    openbindings.ErrCodeRefNotFound,
				Message: fmt.Sprintf("service %q not found in proto definition", svcName),
			})
			return
		}
	}

	conn, err := e.getConn(bctx, args.Source.Location)
	if err != nil {
		inv.FireError(&openbindings.InvocationError{
			Code:    openbindings.ErrCodeConnectFailed,
			Message: err.Error(),
		})
		return
	}

	if svcDesc == nil {
		refClient := grpcreflect.NewClientAuto(rpcCtx, conn)
		defer refClient.Reset()
		svcDesc, err = resolveService(refClient, protoreflect.FullName(svcName))
		if err != nil {
			inv.FireError(refResolveError(svcName, err))
			return
		}
	}

	methodDesc := svcDesc.Methods().ByName(protoreflect.Name(methodName))
	if methodDesc == nil {
		inv.FireError(&openbindings.InvocationError{
			Code:    openbindings.ErrCodeRefNotFound,
			Message: fmt.Sprintf("method %q not found in service %q", methodName, svcName),
		})
		return
	}

	if methodDesc.IsStreamingClient() {
		kind := "client-streaming"
		if methodDesc.IsStreamingServer() {
			kind = "bidi-streaming"
		}
		inv.FireError(&openbindings.InvocationError{
			Code:    openbindings.ErrCodeExecutionFailed,
			Message: fmt.Sprintf("gRPC method %q is %s; the grpc invoker supports unary and server-streaming methods", methodDesc.FullName(), kind),
		})
		return
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

// Synthesizer handles interface synthesis from gRPC servers.
type Synthesizer struct{}

// NewSynthesizer creates a new gRPC interface synthesizer.
func NewSynthesizer() *Synthesizer { return &Synthesizer{} }

// Formats returns the source formats supported by the gRPC synthesizer.
func (c *Synthesizer) Formats() []openbindings.FormatInfo {
	return []openbindings.FormatInfo{{Token: FormatToken, Description: "gRPC via server reflection or .proto files"}}
}

// SynthesizeInterface discovers gRPC services and converts to an OpenBindings interface.
// Supports two discovery modes:
//   - Live server reflection (default): connects to the address and introspects via gRPC reflection
//   - Proto file: parses a .proto file when the location ends in .proto or inline content is provided
func (c *Synthesizer) SynthesizeInterface(ctx context.Context, in *openbindings.SynthesizeInput) (*openbindings.Interface, error) {
	if len(in.Sources) == 0 {
		return nil, openbindings.ErrNoSources
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
		disc, err = discover(ctx, addr)
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
