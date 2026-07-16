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
// is the §9.1 absent input value, which marshals as the empty request
// message for unary and server-streaming methods (GRPC-P-03).
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
		// §9.1: an absent input value marshals as the empty request message.
		return dynamicpb.NewMessage(methodDesc.Input()), true
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

// applyGRPCContext attaches binding-context credentials and caller-supplied
// header entries as outgoing gRPC metadata per §9.5 (GRPC-P-07): a bearer
// token rides `authorization: Bearer <token>`; any other entry naming a
// valid metadata key rides that key. A key that violates the gRPC metadata
// name grammar (lowercase letters, digits, `-`, `_`, `.`) or uses the
// protocol-reserved `grpc-` prefix is UNPLACEABLE — surfaced to the
// consumer as a loud pre-dispatch refusal, never normalized or case-folded
// into place, never silently skipped.
func applyGRPCContext(ctx context.Context, bindCtx map[string]any) (context.Context, error) {
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
		if err := checkMetadataKey(k); err != nil {
			return nil, err
		}
		md.Set(k, v)
	}

	if len(md) == 0 {
		return ctx, nil
	}
	return metadata.NewOutgoingContext(ctx, md), nil
}

// checkMetadataKey validates a caller-supplied key against the gRPC
// metadata name grammar (§9.5, GRPC-P-07).
func checkMetadataKey(k string) error {
	if strings.HasPrefix(k, "grpc-") {
		return fmt.Errorf(
			"context header %q uses the protocol-reserved grpc- prefix and cannot be placed as gRPC metadata; unplaceable credentials are surfaced, never silently skipped (openbindings.grpc@1 §9.5)", k)
	}
	if k == "" {
		return fmt.Errorf("context header with empty name cannot be placed as gRPC metadata (openbindings.grpc@1 §9.5)")
	}
	for i := 0; i < len(k); i++ {
		c := k[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' {
			continue
		}
		return fmt.Errorf(
			"context header %q violates the gRPC metadata name grammar (lowercase letters, digits, '-', '_', '.') and is unplaceable; keys are never normalized or case-folded into place (openbindings.grpc@1 §9.5)", k)
	}
	return nil
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

// parseRef splits a binding ref per GRPC-D-03 (§7):
// <fully-qualified-service>/<method> — the service's package-qualified
// name, or its bare name when its file declares no package
// (`CoffeeShop/GetMenu` is legal), one '/', and the unqualified RPC name.
// Matching downstream is byte-exact; no case folding.
func parseRef(ref string) (string, string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", "", fmt.Errorf("empty gRPC ref")
	}
	idx := strings.LastIndex(ref, "/")
	if idx < 0 || idx == 0 || idx == len(ref)-1 {
		return "", "", fmt.Errorf("gRPC ref %q must be <fully-qualified-service>/<method> (openbindings.grpc@1 GRPC-D-03)", ref)
	}
	return ref[:idx], ref[idx+1:], nil
}

// buildRequest unmarshals one caller-facing input value into the request
// message per §9.1 (GRPC-P-03): the accepted shape is the request type's
// CANONICAL JSON form — an object for ordinary messages, the mapping's
// defined form where it differs (a string for a google.protobuf.Duration-
// typed request, the wrapped value for wrapper types, and so on).
// Unmarshalling follows the mapping's own rules, including its default
// posture on unknown fields: they are refused loudly, never silently
// discarded. A value that fails to unmarshal is refused before dispatch.
func buildRequest(method protoreflect.MethodDescriptor, input any) (proto.Message, error) {
	msg := dynamicpb.NewMessage(method.Input())
	if input == nil {
		return msg, nil
	}
	jsonBytes, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("marshal input: %w", err)
	}
	if err := protojson.Unmarshal(jsonBytes, msg); err != nil {
		return nil, fmt.Errorf("input is not the canonical JSON form of %s: %v", method.Input().FullName(), err)
	}
	return msg, nil
}

func responseToJSON(resp proto.Message) (any, error) {
	// Emit proto3 JSON canonical names (camelCase) so response field names
	// match what the synthesizer writes into OBI schemas via field.JSONName().
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
