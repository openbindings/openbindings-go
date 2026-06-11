package grpc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/jhump/protoreflect/v2/grpcdynamic"
	openbindings "github.com/openbindings/openbindings-go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// readRequest obtains the single request message for a unary or
// server-streaming call from the invocation handle.
//
// No-input bindings close input on entry (binding contract: the caller never
// has to Close()) and send an empty request without waiting for a write. A
// binding is no-input when its request message has no fields, OR when the
// operation layer drives an operation that declares no input (noInput: Binding
// set with InputSchema nil) — the latter guards a no-input operation over a
// fielded message from parking on a write that never comes. For all other
// methods the first written input becomes the request; a close-without-write
// is a terminal ERR_MISSING_INPUT.
//
// The bool result reports whether dispatch should proceed; on false the
// invocation is already terminal.
func readRequest(ctx context.Context, inv openbindings.BindingHandle[any, any], methodDesc protoreflect.MethodDescriptor, noInput bool) (proto.Message, bool) {
	if noInput || methodDesc.Input().Fields().Len() == 0 {
		_ = inv.CloseInput()
		return dynamicpb.NewMessage(methodDesc.Input()), true
	}

	raw, err := inv.ReadInput(ctx)
	if err == io.EOF {
		inv.FireError(&openbindings.InvocationError{
			Code:    openbindings.ErrCodeMissingInput,
			Message: fmt.Sprintf("gRPC method %q requires an input message", methodDesc.FullName()),
		})
		return nil, false
	}
	if err != nil {
		return nil, false // invocation already terminal (cancelled or errored)
	}
	_ = inv.CloseInput() // unary request: one message, close after the first read

	reqMsg, buildErr := buildRequest(methodDesc, raw)
	if buildErr != nil {
		inv.FireError(&openbindings.InvocationError{
			Code:    openbindings.ErrCodeValidationFailed,
			Message: buildErr.Error(),
		})
		return nil, false
	}
	return reqMsg, true
}

// runUnary dispatches a unary RPC and emits its single response.
func runUnary(rpcCtx context.Context, inv openbindings.BindingHandle[any, any], stub *grpcdynamic.Stub, methodDesc protoreflect.MethodDescriptor, reqMsg proto.Message) {
	var header, trailer metadata.MD
	resp, err := stub.InvokeRpc(rpcCtx, methodDesc, reqMsg, grpc.Header(&header), grpc.Trailer(&trailer))

	// gRPC metadata maps natively onto the handle's metadata surfaces.
	_ = inv.SetHeader(toOBMetadata(header))
	if md := toOBMetadata(trailer); md != nil {
		inv.SetTrailer(md)
	}

	if err != nil {
		inv.FireError(grpcError(err, openbindings.ErrCodeExecutionFailed))
		return
	}

	output, jerr := responseToJSON(resp)
	if jerr != nil {
		inv.FireError(&openbindings.InvocationError{
			Code:    openbindings.ErrCodeResponseError,
			Message: jerr.Error(),
		})
		return
	}
	if err := inv.EmitOutput(output); err != nil {
		return // invocation terminated while the emit was parked
	}
	inv.CloseOutput()
}

// runServerStream dispatches a server-streaming RPC and emits each received
// message. EmitOutput's parking is the backpressure: a slow caller flow-
// controls the stream via gRPC windowing, and a terminal while parked stops
// the loop. Stream teardown on caller cancel rides rpcCtx (DoneContext).
func runServerStream(rpcCtx context.Context, inv openbindings.BindingHandle[any, any], stub *grpcdynamic.Stub, methodDesc protoreflect.MethodDescriptor, reqMsg proto.Message) {
	stream, err := stub.InvokeRpcServerStream(rpcCtx, methodDesc, reqMsg)
	if err != nil {
		inv.FireError(grpcError(err, openbindings.ErrCodeExecutionFailed))
		return
	}

	// Leading metadata: Header blocks until the server's headers arrive (or
	// the RPC fails, in which case the recv loop below surfaces the error).
	if md, herr := stream.Header(); herr == nil {
		_ = inv.SetHeader(toOBMetadata(md))
	}

	for {
		resp, rerr := stream.RecvMsg()
		if rerr != nil {
			// Trailing metadata is valid once RecvMsg returns non-nil.
			if md := toOBMetadata(stream.Trailer()); md != nil {
				inv.SetTrailer(md)
			}
			if rerr == io.EOF {
				inv.CloseOutput()
				return
			}
			inv.FireError(grpcError(rerr, openbindings.ErrCodeStreamError))
			return
		}

		output, jerr := responseToJSON(resp)
		if jerr != nil {
			inv.FireError(&openbindings.InvocationError{
				Code:    openbindings.ErrCodeResponseError,
				Message: jerr.Error(),
			})
			return
		}
		if err := inv.EmitOutput(output); err != nil {
			return // invocation terminated while the emit was parked
		}
	}
}

// applyGRPCContext attaches binding context credentials and transport-hint
// headers as gRPC outgoing metadata.
func applyGRPCContext(ctx context.Context, bindCtx map[string]any) context.Context {
	md := metadata.MD{}

	if token := openbindings.ContextBearerToken(bindCtx); token != "" {
		md.Set("authorization", "Bearer "+token)
	} else if key := openbindings.ContextAPIKey(bindCtx); key != "" {
		md.Set("authorization", "ApiKey "+key)
	} else if u, p, ok := openbindings.ContextBasicAuth(bindCtx); ok {
		encoded := base64.StdEncoding.EncodeToString([]byte(u + ":" + p))
		md.Set("authorization", "Basic "+encoded)
	}

	for k, v := range openbindings.ContextHeaders(bindCtx) {
		md.Set(strings.ToLower(k), v)
	}

	if len(md) == 0 {
		return ctx
	}
	return metadata.NewOutgoingContext(ctx, md)
}

// toOBMetadata converts gRPC metadata to the handle's Metadata shape (both
// are map[string][]string). Returns nil for empty metadata.
func toOBMetadata(md metadata.MD) openbindings.Metadata {
	if len(md) == 0 {
		return nil
	}
	out := make(openbindings.Metadata, len(md))
	for k, vs := range md {
		out[k] = append([]string(nil), vs...)
	}
	return out
}

func parseRef(ref string) (string, string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", "", fmt.Errorf("empty gRPC ref")
	}
	idx := strings.LastIndex(ref, "/")
	if idx < 0 || idx == 0 || idx == len(ref)-1 {
		return "", "", fmt.Errorf("gRPC ref %q must be in the form package.Service/Method", ref)
	}
	return ref[:idx], ref[idx+1:], nil
}

func buildRequest(method protoreflect.MethodDescriptor, input any) (proto.Message, error) {
	msg := dynamicpb.NewMessage(method.Input())
	if input == nil {
		return msg, nil
	}
	inputMap, ok := input.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("gRPC input must be a JSON object, got %T", input)
	}
	jsonBytes, err := json.Marshal(inputMap)
	if err != nil {
		return nil, fmt.Errorf("marshal input: %w", err)
	}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(jsonBytes, msg); err != nil {
		return nil, fmt.Errorf("unmarshal input to protobuf: %w", err)
	}
	return msg, nil
}

func responseToJSON(resp proto.Message) (any, error) {
	// Emit proto3 JSON canonical names (camelCase) so response field names
	// match what the creator writes into OBI schemas via field.JSONName().
	// UseProtoNames: true would emit snake_case and desync from the OBI contract.
	jsonBytes, err := protojson.Marshal(resp)
	if err != nil {
		return nil, fmt.Errorf("marshal response: %w", err)
	}
	var result any
	if err := json.Unmarshal(jsonBytes, &result); err != nil {
		return nil, fmt.Errorf("parse response JSON: %w", err)
	}
	return result, nil
}

// grpcError maps a gRPC error to a terminal *InvocationError. The gRPC
// status code (and any status details) ride along in Details; unmapped
// status codes fall back to fallbackCode.
func grpcError(err error, fallbackCode string) *openbindings.InvocationError {
	s, ok := status.FromError(err)
	if !ok {
		return &openbindings.InvocationError{Code: fallbackCode, Message: err.Error()}
	}

	code := fallbackCode
	switch s.Code() {
	case codes.Unauthenticated:
		code = openbindings.ErrCodeAuthRequired
	case codes.PermissionDenied:
		code = openbindings.ErrCodePermissionDenied
	case codes.Unavailable:
		code = openbindings.ErrCodeConnectFailed
	case codes.DeadlineExceeded:
		code = openbindings.ErrCodeTimeout
	}

	details := map[string]any{"grpcCode": s.Code().String()}
	if msgs := s.Proto().GetDetails(); len(msgs) > 0 {
		var ds []any
		for _, m := range msgs {
			b, merr := protojson.Marshal(m)
			if merr != nil {
				continue
			}
			var v any
			if json.Unmarshal(b, &v) == nil {
				ds = append(ds, v)
			}
		}
		if len(ds) > 0 {
			details["grpcDetails"] = ds
		}
	}

	return &openbindings.InvocationError{Code: code, Message: err.Error(), Details: details}
}

// refResolveError maps a reflection-resolution failure. Transport-level
// statuses surface as their mapped codes (a dead server is ERR_CONNECT_FAILED,
// not a missing ref); anything else means the symbol didn't resolve.
func refResolveError(svcName string, err error) *openbindings.InvocationError {
	if s, ok := status.FromError(err); ok {
		switch s.Code() {
		case codes.Unavailable, codes.DeadlineExceeded, codes.Unauthenticated, codes.PermissionDenied:
			return grpcError(err, openbindings.ErrCodeConnectFailed)
		}
	}
	return &openbindings.InvocationError{
		Code:    openbindings.ErrCodeRefNotFound,
		Message: fmt.Sprintf("resolve service %q: %v", svcName, err),
	}
}
