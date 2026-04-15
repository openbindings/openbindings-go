// Package grpc implements the gRPC binding format for OpenBindings.
//
// The package handles:
//   - Discovering gRPC services via server reflection or .proto files
//   - Converting protobuf service descriptors to OpenBindings interfaces
//   - Executing unary and server-streaming RPCs
package grpc

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jhump/protoreflect/desc"        //nolint:staticcheck // no v2 equivalent yet
	"github.com/jhump/protoreflect/grpcreflect" //nolint:staticcheck // depends on protoreflect/desc
	openbindings "github.com/openbindings/openbindings-go"
	"google.golang.org/grpc"
)

const FormatToken = "grpc"
const DefaultSourceName = "grpcServer"

// Executor handles binding execution for gRPC sources.
type Executor struct {
	conns sync.Map // address -> *grpc.ClientConn
}

// NewExecutor creates a new gRPC binding executor.
func NewExecutor() *Executor { return &Executor{} }

func (e *Executor) getConn(ctx context.Context, address string) (*grpc.ClientConn, error) {
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
func (e *Executor) Close() {
	e.conns.Range(func(key, value any) bool {
		_ = value.(*grpc.ClientConn).Close()
		e.conns.Delete(key)
		return true
	})
}

// Formats returns the source formats supported by the gRPC executor.
func (e *Executor) Formats() []openbindings.FormatInfo {
	return []openbindings.FormatInfo{{Token: FormatToken, Description: "gRPC via server reflection or .proto files"}}
}

// ExecuteBinding executes a gRPC binding, returning a channel of stream events.
// For server-streaming RPCs it yields events as they arrive; for unary RPCs it
// returns a single event.
func (e *Executor) ExecuteBinding(ctx context.Context, in *openbindings.BindingExecutionInput) (<-chan openbindings.StreamEvent, error) {
	enriched := in
	if in.Store != nil {
		key := normalizeAddress(in.Source.Location)
		if key != "" {
			if stored, err := in.Store.Get(ctx, key); err == nil && len(stored) > 0 {
				cp := *in
				if len(in.Context) == 0 {
					cp.Context = stored
				} else {
					merged := make(map[string]any, len(stored)+len(in.Context))
					for k, v := range stored {
						merged[k] = v
					}
					for k, v := range in.Context {
						merged[k] = v
					}
					cp.Context = merged
				}
				enriched = &cp
			}
		}
	}

	start := time.Now()

	svcName, methodName, err := parseRef(enriched.Ref)
	if err != nil {
		return openbindings.SingleEventChannel(openbindings.FailedOutput(start, openbindings.ErrCodeInvalidRef, err.Error())), nil
	}

	conn, err := e.getConn(ctx, in.Source.Location)
	if err != nil {
		return openbindings.SingleEventChannel(openbindings.FailedOutput(start, openbindings.ErrCodeConnectFailed, err.Error())), nil
	}

	rpcCtx := applyGRPCContext(ctx, enriched.Context, enriched.Options)

	// Resolve service and method descriptors. If inline content is provided
	// (e.g., a .proto definition), parse it directly. Otherwise use server reflection.
	// Note: isProtoFile is NOT checked here because Source.Location is the server
	// address for execution. Proto file locations are only used by the Creator.
	var refClient *grpcreflect.Client
	var svcDesc *desc.ServiceDescriptor
	var methodDesc *desc.MethodDescriptor

	if enriched.Source.Content != nil {
		disc, parseErr := discoverFromProto(enriched.Source.Location, enriched.Source.Content)
		if parseErr != nil {
			return openbindings.SingleEventChannel(openbindings.FailedOutput(start, openbindings.ErrCodeSourceLoadFailed, parseErr.Error())), nil
		}
		for _, svc := range disc.services {
			if svc.GetFullyQualifiedName() == svcName {
				svcDesc = svc
				break
			}
		}
		if svcDesc == nil {
			return openbindings.SingleEventChannel(openbindings.FailedOutput(start, openbindings.ErrCodeRefNotFound,
				fmt.Sprintf("service %q not found in proto definition", svcName))), nil
		}
		methodDesc = svcDesc.FindMethodByName(methodName)
	} else {
		refClient = grpcreflect.NewClientAuto(rpcCtx, conn)
		var err error
		svcDesc, err = refClient.ResolveService(svcName)
		if err != nil {
			refClient.Reset()
			return openbindings.SingleEventChannel(openbindings.FailedOutput(start, openbindings.ErrCodeRefNotFound,
				fmt.Sprintf("resolve service %q: %v", svcName, err))), nil
		}
		methodDesc = svcDesc.FindMethodByName(methodName)
	}

	if methodDesc == nil {
		if refClient != nil {
			refClient.Reset()
		}
		return openbindings.SingleEventChannel(openbindings.FailedOutput(start, openbindings.ErrCodeRefNotFound,
			fmt.Sprintf("method %q not found in service %q", methodName, svcName))), nil
	}

	if methodDesc.IsServerStreaming() {
		return subscribe(ctx, enriched, conn, refClient, methodDesc)
	}

	if refClient != nil {
		refClient.Reset()
	}
	result := doGRPCCall(ctx, enriched, conn, methodDesc)

	// Auth retry: if the RPC returned auth_required and we have security methods
	// and callbacks, resolve credentials and retry once.
	if result.Error != nil && result.Error.Code == openbindings.ErrCodeAuthRequired &&
		len(enriched.Security) > 0 && enriched.Callbacks != nil {
		creds, resolveErr := openbindings.ResolveSecurity(ctx, enriched.Security, enriched.Callbacks, nil)
		if resolveErr == nil && creds != nil {
			if enriched == in {
				cp := *in
				enriched = &cp
			}
			merged := make(map[string]any)
			for k, v := range enriched.Context {
				merged[k] = v
			}
			for k, v := range creds {
				merged[k] = v
			}
			enriched.Context = merged

			if enriched.Store != nil {
				storeKey := normalizeAddress(enriched.Source.Location)
				if storeKey != "" {
					_ = enriched.Store.Set(ctx, storeKey, enriched.Context)
				}
			}

			result = doGRPCCall(ctx, enriched, conn, methodDesc)
		}
	}

	return openbindings.SingleEventChannel(result), nil
}

// Creator handles interface creation from gRPC servers.
type Creator struct{}

// NewCreator creates a new gRPC interface creator.
func NewCreator() *Creator { return &Creator{} }

// Formats returns the source formats supported by the gRPC creator.
func (c *Creator) Formats() []openbindings.FormatInfo {
	return []openbindings.FormatInfo{{Token: FormatToken, Description: "gRPC via server reflection or .proto files"}}
}

// CreateInterface discovers gRPC services and converts to an OpenBindings interface.
// Supports two discovery modes:
//   - Live server reflection (default): connects to the address and introspects via gRPC reflection
//   - Proto file: parses a .proto file when the location ends in .proto or inline content is provided
func (c *Creator) CreateInterface(ctx context.Context, in *openbindings.CreateInput) (*openbindings.Interface, error) {
	if len(in.Sources) == 0 {
		return nil, openbindings.ErrNoSources
	}
	src := in.Sources[0]

	var disc *discovery
	var err error
	var sourceLocation string

	if src.Content != nil || isProtoFile(src.Location) {
		// Parse from .proto file or inline content.
		disc, err = discoverFromProto(src.Location, src.Content)
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

	iface, err := convertToInterface(disc, sourceLocation)
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

// normalizeAddress extracts a stable key from a gRPC address.
func normalizeAddress(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	if strings.Contains(addr, "://") {
		if u, err := url.Parse(addr); err == nil {
			return openbindings.NormalizeContextKey(u.Host)
		}
	}
	return openbindings.NormalizeContextKey(addr)
}
