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
			// When the invocation context is done (caller cancel or lifetime
			// deadline), the handle owns the terminal classification via its
			// AfterFunc — a deadline as ERR_TIMEOUT, a cancel as ERR_CANCELLED.
			// Defer to it rather than racing a stream-error terminal off the
			// same RecvMsg wakeup, mirroring the OpenAPI SSE path. Both racers
			// already agree on ERR_TIMEOUT for a deadline once the handle
			// classifies it; this guard makes the outcome deterministic.
			if rpcCtx.Err() != nil {
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

// runClientStream opens the artifact-declared client stream before reading
// its values, sends each canonical ProtoJSON request message, then closes the
// request side and emits the method's one response. A later invalid value is
// terminal and the invocation-owned context cancels the already-open RPC.
func runClientStream(rpcCtx context.Context, inv openbindings.BindingHandle[any, any], stub *grpcdynamic.Stub, methodDesc protoreflect.MethodDescriptor, noInput bool) {
	stream, err := stub.InvokeRpcClientStream(rpcCtx, methodDesc)
	if err != nil {
		inv.FireError(grpcError(err, openbindings.ErrCodeExecutionFailed))
		return
	}
	if noInput {
		_ = inv.CloseInput()
	} else {
		for {
			raw, rerr := inv.ReadInput(rpcCtx)
			if rerr == io.EOF {
				break
			}
			if rerr != nil {
				return
			}
			msg, buildErr := buildRequest(methodDesc, raw)
			if buildErr != nil {
				inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeValidationFailed, Message: buildErr.Error()})
				return
			}
			if sendErr := stream.SendMsg(msg); sendErr != nil {
				inv.FireError(grpcError(sendErr, openbindings.ErrCodeStreamError))
				return
			}
		}
	}
	resp, recvErr := stream.CloseAndReceive()
	if md, herr := stream.Header(); herr == nil {
		_ = inv.SetHeader(toOBMetadata(md))
	}
	if md := toOBMetadata(stream.Trailer()); md != nil {
		inv.SetTrailer(md)
	}
	if recvErr != nil {
		if rpcCtx.Err() == nil {
			inv.FireError(grpcError(recvErr, openbindings.ErrCodeStreamError))
		}
		return
	}
	emitUnaryResponse(inv, resp)
}

// runBidiStream sends and receives concurrently so a server response can be
// emitted while the caller's input side remains open. Either direction's
// failure terminates the invocation and cancels the other through rpcCtx;
// values emitted before a later status or local validation failure stand.
func runBidiStream(rpcCtx context.Context, inv openbindings.BindingHandle[any, any], stub *grpcdynamic.Stub, methodDesc protoreflect.MethodDescriptor, noInput bool) {
	stream, err := stub.InvokeRpcBidiStream(rpcCtx, methodDesc)
	if err != nil {
		inv.FireError(grpcError(err, openbindings.ErrCodeExecutionFailed))
		return
	}

	go func() {
		if noInput {
			_ = inv.CloseInput()
			if closeErr := stream.CloseSend(); closeErr != nil && rpcCtx.Err() == nil {
				inv.FireError(grpcError(closeErr, openbindings.ErrCodeStreamError))
			}
			return
		}
		for {
			raw, readErr := inv.ReadInput(rpcCtx)
			if readErr == io.EOF {
				if closeErr := stream.CloseSend(); closeErr != nil && rpcCtx.Err() == nil {
					inv.FireError(grpcError(closeErr, openbindings.ErrCodeStreamError))
				}
				return
			}
			if readErr != nil {
				return
			}
			msg, buildErr := buildRequest(methodDesc, raw)
			if buildErr != nil {
				inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeValidationFailed, Message: buildErr.Error()})
				return
			}
			if sendErr := stream.SendMsg(msg); sendErr != nil {
				if rpcCtx.Err() == nil {
					inv.FireError(grpcError(sendErr, openbindings.ErrCodeStreamError))
				}
				return
			}
		}
	}()

	if md, herr := stream.Header(); herr == nil {
		_ = inv.SetHeader(toOBMetadata(md))
	}
	for {
		resp, recvErr := stream.RecvMsg()
		if recvErr != nil {
			if md := toOBMetadata(stream.Trailer()); md != nil {
				inv.SetTrailer(md)
			}
			if recvErr == io.EOF {
				inv.CloseOutput()
				return
			}
			if rpcCtx.Err() == nil {
				inv.FireError(grpcError(recvErr, openbindings.ErrCodeStreamError))
			}
			return
		}
		output, jsonErr := responseToJSON(resp)
		if jsonErr != nil {
			inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeResponseError, Message: jsonErr.Error()})
			return
		}
		if emitErr := inv.EmitOutput(output); emitErr != nil {
			return
		}
	}
}

func emitUnaryResponse(inv openbindings.BindingHandle[any, any], resp proto.Message) {
	output, err := responseToJSON(resp)
	if err != nil {
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeResponseError, Message: err.Error()})
		return
	}
	if err := inv.EmitOutput(output); err != nil {
		return
	}
	inv.CloseOutput()
}

// applyGRPCContext attaches caller-supplied, explicitly named header entries
// as outgoing gRPC metadata per §9.5 (GRPC-P-07). Generic bearer, basic,
// and API-key credentials have no artifact-declared carriage and are
// challenged by the invocation path rather than mapped here. A key that violates the gRPC metadata
// name grammar (lowercase letters, digits, `-`, `_`, `.`) or uses the
// protocol-reserved `grpc-` prefix is UNPLACEABLE — surfaced to the
// consumer as a loud pre-dispatch refusal, never normalized or case-folded
// into place, never silently skipped.
func applyGRPCContext(ctx context.Context, bindCtx map[string]any) (context.Context, error) {
	md := metadata.MD{}

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
		if strings.HasSuffix(k, "-bin") {
			encoded := make([]string, len(vs))
			for i, value := range vs {
				encoded[i] = base64.StdEncoding.EncodeToString([]byte(value))
			}
			out[k] = encoded
			continue
		}
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
	if ref == "" {
		return "", "", fmt.Errorf("empty gRPC ref")
	}
	idx := strings.Index(ref, "/")
	if idx < 0 || idx != strings.LastIndex(ref, "/") || idx == 0 || idx == len(ref)-1 {
		return "", "", fmt.Errorf("gRPC ref %q must be <fully-qualified-service>/<method> (openbindings.grpc@1 GRPC-D-03)", ref)
	}
	return ref[:idx], ref[idx+1:], nil
}

// resolveMethod resolves <fully-qualified-service>/<method> against a
// discovered embedded schema. Matching against the schema's declared
// services is byte-exact, no case folding (GRPC-D-03); a ref matching no
// service or method makes the binding unresolvable.
func resolveMethod(disc *discovery, svcName, methodName string) (protoreflect.MethodDescriptor, *openbindings.InvocationError) {
	var svcDesc protoreflect.ServiceDescriptor
	for _, svc := range disc.services {
		if string(svc.FullName()) == svcName {
			svcDesc = svc
			break
		}
	}
	if svcDesc == nil {
		return nil, &openbindings.InvocationError{
			Code:    openbindings.ErrCodeRefNotFound,
			Message: fmt.Sprintf("service %q not found in embedded schema", svcName),
		}
	}
	m := svcDesc.Methods().ByName(protoreflect.Name(methodName))
	if m == nil {
		return nil, &openbindings.InvocationError{
			Code:    openbindings.ErrCodeRefNotFound,
			Message: fmt.Sprintf("method %q not found in service %q", methodName, svcName),
		}
	}
	return m, nil
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

// grpcError reports unsuccessful completion without projecting gRPC's status
// space into a portable error taxonomy. The complete native status is retained
// only on the explicit diagnostic lane. A non-status local error uses the
// caller-supplied SDK code.
func grpcError(err error, fallbackCode string) *openbindings.InvocationError {
	s, ok := status.FromError(err)
	if !ok {
		return &openbindings.InvocationError{Code: fallbackCode, Message: err.Error()}
	}

	statusDetails := make([]any, 0, len(s.Proto().GetDetails()))
	for _, detail := range s.Proto().GetDetails() {
		statusDetails = append(statusDetails, map[string]any{
			"typeUrl":     detail.GetTypeUrl(),
			"valueBase64": base64.StdEncoding.EncodeToString(detail.GetValue()),
		})
	}
	grpcStatus := map[string]any{
		"code":    int32(s.Code()),
		"message": s.Message(),
	}
	if len(statusDetails) > 0 {
		grpcStatus["details"] = statusDetails
	}
	diagnostics := map[string]any{
		"grpcCode":   s.Code().String(),
		"grpcStatus": grpcStatus,
	}
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
			diagnostics["grpcDetails"] = ds
		}
	}

	message := s.Message()
	if message == "" {
		message = "Invocation completed unsuccessfully"
	}
	return &openbindings.InvocationError{Code: openbindings.ErrCodeExecutionFailed, Message: message, Diagnostics: diagnostics}
}

// refResolveError maps a reflection-resolution failure. Transport-level
// statuses surface as their mapped codes (an unavailable/unreachable server is
// ERR_UNAVAILABLE, a deadline is ERR_TIMEOUT, an auth rejection its auth code —
// all transient/auth rather than a missing ref); anything else means the symbol
// didn't resolve.
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
