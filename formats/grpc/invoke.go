package grpc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jhump/protoreflect/v2/grpcdynamic"
	"github.com/jhump/protoreflect/v2/grpcreflect"
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

func doGRPCCall(ctx context.Context, in *openbindings.BindingInvocationInput, conn *grpc.ClientConn, methodDesc protoreflect.MethodDescriptor) *openbindings.InvocationOutput {
	start := time.Now()

	reqMsg, err := buildRequest(methodDesc, in.Input)
	if err != nil {
		return openbindings.FailedOutput(start, openbindings.ErrCodeInvalidInput, err.Error())
	}

	rpcCtx := applyGRPCContext(ctx, in.Context)
	stub := grpcdynamic.NewStub(conn)

	resp, err := stub.InvokeRpc(rpcCtx, methodDesc, reqMsg)
	if err != nil {
		return openbindings.FailedOutput(start, grpcErrorCode(err), err.Error())
	}

	output, err := responseToJSON(resp)
	if err != nil {
		return openbindings.FailedOutput(start, openbindings.ErrCodeResponseError, err.Error())
	}

	return &openbindings.InvocationOutput{Output: output, Status: 200, DurationMs: time.Since(start).Milliseconds()}
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

func subscribe(ctx context.Context, in *openbindings.BindingInvocationInput, conn *grpc.ClientConn, refClient *grpcreflect.Client, methodDesc protoreflect.MethodDescriptor) (<-chan openbindings.InvocationOutput, error) {
	start := time.Now()

	reqMsg, err := buildRequest(methodDesc, in.Input)
	if err != nil {
		if refClient != nil {
			refClient.Reset()
		}
		return openbindings.SingleEventChannel(openbindings.FailedOutput(start, openbindings.ErrCodeInvalidInput, err.Error())), nil
	}

	rpcCtx := applyGRPCContext(ctx, in.Context)
	stub := grpcdynamic.NewStub(conn)
	stream, err := stub.InvokeRpcServerStream(rpcCtx, methodDesc, reqMsg)
	if err != nil {
		if refClient != nil {
			refClient.Reset()
		}
		return openbindings.SingleEventChannel(openbindings.FailedOutput(start, grpcErrorCode(err), err.Error())), nil
	}

	ch := make(chan openbindings.InvocationOutput, 16)
	go func() {
		defer close(ch)
		defer func() {
			if refClient != nil {
				refClient.Reset()
			}
		}()

		for {
			resp, err := stream.RecvMsg()
			if err != nil {
				if err == io.EOF || ctx.Err() != nil {
					return
				}
				ch <- openbindings.InvocationOutput{Error: &openbindings.InvocationError{
					Code:    openbindings.ErrCodeStreamError,
					Message: err.Error(),
				}}
				return
			}

			output, err := responseToJSON(resp)
			if err != nil {
				ch <- openbindings.InvocationOutput{Error: &openbindings.InvocationError{
					Code:    openbindings.ErrCodeResponseError,
					Message: err.Error(),
				}}
				return
			}
			ch <- openbindings.InvocationOutput{Output: output}
		}
	}()

	return ch, nil
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

// grpcErrorCode maps a gRPC error to a standard error code constant.
func grpcErrorCode(err error) string {
	if s, ok := status.FromError(err); ok {
		switch s.Code() {
		case codes.Unauthenticated:
			return openbindings.ErrCodeAuthRequired
		case codes.PermissionDenied:
			return openbindings.ErrCodePermissionDenied
		}
	}
	return openbindings.ErrCodeExecutionFailed
}
