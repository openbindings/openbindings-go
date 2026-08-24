package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/openbindings/openbindings-go/invoke"

	"github.com/jhump/protoreflect/v2/grpcdynamic"
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
func readRequest(ctx context.Context, inv invoke.BindingHandle[any, any], methodDesc protoreflect.MethodDescriptor, noInput bool) (proto.Message, bool) {
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
		inv.FireError(&invoke.InvocationError{
			Code: invoke.ErrCodeValidationFailed,
		})
		return nil, false
	}
	return reqMsg, true
}

// runUnary dispatches a unary RPC and emits its single response.
func runUnary(rpcCtx context.Context, inv invoke.BindingHandle[any, any], stub *grpcdynamic.Stub, methodDesc protoreflect.MethodDescriptor, reqMsg proto.Message) {
	resp, err := stub.InvokeRpc(rpcCtx, methodDesc, reqMsg)

	if err != nil {
		inv.FireError(grpcError(err, invoke.ErrCodeExecutionFailed))
		return
	}

	output, jerr := responseToJSON(resp)
	if jerr != nil {
		inv.FireError(&invoke.InvocationError{
			Code: invoke.ErrCodeResponseError,
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
func runServerStream(rpcCtx context.Context, inv invoke.BindingHandle[any, any], stub *grpcdynamic.Stub, methodDesc protoreflect.MethodDescriptor, reqMsg proto.Message) {
	stream, err := stub.InvokeRpcServerStream(rpcCtx, methodDesc, reqMsg)
	if err != nil {
		inv.FireError(grpcError(err, invoke.ErrCodeExecutionFailed))
		return
	}

	for {
		resp, rerr := stream.RecvMsg()
		if rerr != nil {
			if rerr == io.EOF {
				inv.CloseOutput()
				return
			}
			// When the invocation context is done (caller cancel or lifetime
			// deadline), the handle owns the terminal classification via its
			// AfterFunc — both an explicit cancel and a caller-supplied lifetime
			// deadline are ERR_CANCELLED at the abstract boundary.
			// Defer to it rather than racing a stream-error terminal off the
			// same RecvMsg wakeup, mirroring the OpenAPI SSE path. Both racers
			// already agree on ERR_CANCELLED once the handle
			// classifies it; this guard makes the outcome deterministic.
			if rpcCtx.Err() != nil {
				return
			}
			inv.FireError(grpcError(rerr, invoke.ErrCodeStreamError))
			return
		}

		output, jerr := responseToJSON(resp)
		if jerr != nil {
			inv.FireError(&invoke.InvocationError{
				Code: invoke.ErrCodeResponseError,
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
func runClientStream(rpcCtx context.Context, inv invoke.BindingHandle[any, any], stub *grpcdynamic.Stub, methodDesc protoreflect.MethodDescriptor, noInput bool) {
	stream, err := stub.InvokeRpcClientStream(rpcCtx, methodDesc)
	if err != nil {
		inv.FireError(grpcError(err, invoke.ErrCodeExecutionFailed))
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
				inv.FireError(&invoke.InvocationError{Code: invoke.ErrCodeValidationFailed})
				return
			}
			if sendErr := stream.SendMsg(msg); sendErr != nil {
				inv.FireError(grpcError(sendErr, invoke.ErrCodeStreamError))
				return
			}
		}
	}
	resp, recvErr := stream.CloseAndReceive()
	if recvErr != nil {
		if rpcCtx.Err() == nil {
			inv.FireError(grpcError(recvErr, invoke.ErrCodeStreamError))
		}
		return
	}
	emitUnaryResponse(inv, resp)
}

// runBidiStream sends and receives concurrently so a server response can be
// emitted while the caller's input side remains open. Either direction's
// failure terminates the invocation and cancels the other through rpcCtx;
// values emitted before a later status or local validation failure stand.
func runBidiStream(rpcCtx context.Context, inv invoke.BindingHandle[any, any], stub *grpcdynamic.Stub, methodDesc protoreflect.MethodDescriptor, noInput bool) {
	stream, err := stub.InvokeRpcBidiStream(rpcCtx, methodDesc)
	if err != nil {
		inv.FireError(grpcError(err, invoke.ErrCodeExecutionFailed))
		return
	}

	go func() {
		if noInput {
			_ = inv.CloseInput()
			if closeErr := stream.CloseSend(); closeErr != nil && rpcCtx.Err() == nil {
				inv.FireError(grpcError(closeErr, invoke.ErrCodeStreamError))
			}
			return
		}
		for {
			raw, readErr := inv.ReadInput(rpcCtx)
			if readErr == io.EOF {
				if closeErr := stream.CloseSend(); closeErr != nil && rpcCtx.Err() == nil {
					inv.FireError(grpcError(closeErr, invoke.ErrCodeStreamError))
				}
				return
			}
			if readErr != nil {
				return
			}
			msg, buildErr := buildRequest(methodDesc, raw)
			if buildErr != nil {
				inv.FireError(&invoke.InvocationError{Code: invoke.ErrCodeValidationFailed})
				return
			}
			if sendErr := stream.SendMsg(msg); sendErr != nil {
				if rpcCtx.Err() == nil {
					inv.FireError(grpcError(sendErr, invoke.ErrCodeStreamError))
				}
				return
			}
		}
	}()

	for {
		resp, recvErr := stream.RecvMsg()
		if recvErr != nil {
			if recvErr == io.EOF {
				inv.CloseOutput()
				return
			}
			if rpcCtx.Err() == nil {
				inv.FireError(grpcError(recvErr, invoke.ErrCodeStreamError))
			}
			return
		}
		output, jsonErr := responseToJSON(resp)
		if jsonErr != nil {
			inv.FireError(&invoke.InvocationError{Code: invoke.ErrCodeResponseError})
			return
		}
		if emitErr := inv.EmitOutput(output); emitErr != nil {
			return
		}
	}
}

func emitUnaryResponse(inv invoke.BindingHandle[any, any], resp proto.Message) {
	output, err := responseToJSON(resp)
	if err != nil {
		inv.FireError(&invoke.InvocationError{Code: invoke.ErrCodeResponseError})
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

	for k, v := range invoke.ContextHeaders(bindCtx) {
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

// parseSelector splits a binding selector per GRPC-D-03 (§7):
// <fully-qualified-service>/<method> — the service's package-qualified
// name, or its bare name when its file declares no package
// (`CoffeeShop/GetMenu` is legal), one '/', and the unqualified RPC name.
// Matching downstream is byte-exact; no case folding.
func parseSelector(selector string) (string, string, error) {
	if selector == "" {
		return "", "", fmt.Errorf("empty gRPC selector")
	}
	idx := strings.Index(selector, "/")
	if idx < 0 || idx != strings.LastIndex(selector, "/") || idx == 0 || idx == len(selector)-1 {
		return "", "", fmt.Errorf("gRPC selector %q must be <fully-qualified-service>/<method> (openbindings.grpc@1 GRPC-D-03)", selector)
	}
	return selector[:idx], selector[idx+1:], nil
}

// resolveMethod resolves <fully-qualified-service>/<method> against a
// discovered embedded schema. Matching against the schema's declared
// services is byte-exact, no case folding (GRPC-D-03); a selector matching no
// service or method makes the binding unresolvable.
func resolveMethod(disc *discovery, svcName, methodName string) (protoreflect.MethodDescriptor, *invoke.InvocationError) {
	var svcDesc protoreflect.ServiceDescriptor
	for _, svc := range disc.services {
		if string(svc.FullName()) == svcName {
			svcDesc = svc
			break
		}
	}
	if svcDesc == nil {
		return nil, &invoke.InvocationError{
			Code: invoke.ErrCodeSelectorNotFound,
		}
	}
	m := svcDesc.Methods().ByName(protoreflect.Name(methodName))
	if m == nil {
		return nil, &invoke.InvocationError{
			Code: invoke.ErrCodeSelectorNotFound,
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
func grpcError(err error, fallbackCode string) *invoke.InvocationError {
	_, ok := status.FromError(err)
	if !ok {
		return &invoke.InvocationError{Code: fallbackCode}
	}

	return &invoke.InvocationError{Code: invoke.ErrCodeExecutionFailed}
}

// selectorResolveError maps a reflection-resolution failure. Transport-level
// statuses surface as structural unsuccessful completion rather than a
// missing selector; anything else means the symbol didn't resolve. Native status
// distinctions remain below the OpenBindings bridge.
func selectorResolveError(svcName string, err error) *invoke.InvocationError {
	if s, ok := status.FromError(err); ok {
		switch s.Code() {
		case codes.Unavailable, codes.DeadlineExceeded, codes.Unauthenticated, codes.PermissionDenied:
			return grpcError(err, invoke.ErrCodeConnectFailed)
		}
	}
	return &invoke.InvocationError{
		Code: invoke.ErrCodeSelectorNotFound,
	}
}
