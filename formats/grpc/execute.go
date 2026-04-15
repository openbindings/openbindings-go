package grpc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/golang/protobuf/jsonpb" //nolint:staticcheck // required by jhump/protoreflect/dynamic
	"github.com/golang/protobuf/proto"  //nolint:staticcheck // matches jhump/protoreflect return types
	"github.com/jhump/protoreflect/desc"                //nolint:staticcheck // no v2 equivalent yet
	"github.com/jhump/protoreflect/dynamic"             //nolint:staticcheck // no v2 equivalent yet
	"github.com/jhump/protoreflect/dynamic/grpcdynamic" //nolint:staticcheck // depends on protoreflect/dynamic
	"github.com/jhump/protoreflect/grpcreflect"         //nolint:staticcheck // depends on protoreflect/desc
	openbindings "github.com/openbindings/openbindings-go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func doGRPCCall(ctx context.Context, in *openbindings.BindingExecutionInput, conn *grpc.ClientConn, methodDesc *desc.MethodDescriptor) *openbindings.ExecuteOutput {
	start := time.Now()

	reqMsg, err := buildRequest(methodDesc, in.Input)
	if err != nil {
		return openbindings.FailedOutput(start, openbindings.ErrCodeInvalidInput, err.Error())
	}

	rpcCtx := applyGRPCContext(ctx, in.Context, in.Options)
	stub := grpcdynamic.NewStub(conn)

	resp, err := stub.InvokeRpc(rpcCtx, methodDesc, reqMsg)
	if err != nil {
		return openbindings.FailedOutput(start, grpcErrorCode(err), err.Error())
	}

	output, err := responseToJSON(resp)
	if err != nil {
		return openbindings.FailedOutput(start, openbindings.ErrCodeResponseError, err.Error())
	}

	return &openbindings.ExecuteOutput{Output: output, Status: 200, DurationMs: time.Since(start).Milliseconds()}
}

// applyGRPCContext attaches binding context credentials and execution option
// headers as gRPC outgoing metadata.
func applyGRPCContext(ctx context.Context, bindCtx map[string]any, opts *openbindings.ExecutionOptions) context.Context {
	md := metadata.MD{}

	if token := openbindings.ContextBearerToken(bindCtx); token != "" {
		md.Set("authorization", "Bearer "+token)
	} else if key := openbindings.ContextAPIKey(bindCtx); key != "" {
		md.Set("authorization", "ApiKey "+key)
	} else if u, p, ok := openbindings.ContextBasicAuth(bindCtx); ok {
		encoded := base64.StdEncoding.EncodeToString([]byte(u + ":" + p))
		md.Set("authorization", "Basic "+encoded)
	}

	if opts != nil {
		for k, v := range opts.Headers {
			md.Set(strings.ToLower(k), v)
		}
	}

	if len(md) == 0 {
		return ctx
	}
	return metadata.NewOutgoingContext(ctx, md)
}

func subscribe(ctx context.Context, in *openbindings.BindingExecutionInput, conn *grpc.ClientConn, refClient *grpcreflect.Client, methodDesc *desc.MethodDescriptor) (<-chan openbindings.StreamEvent, error) {
	start := time.Now()

	reqMsg, err := buildRequest(methodDesc, in.Input)
	if err != nil {
		if refClient != nil {
			refClient.Reset()
		}
		return openbindings.SingleEventChannel(openbindings.FailedOutput(start, openbindings.ErrCodeInvalidInput, err.Error())), nil
	}

	rpcCtx := applyGRPCContext(ctx, in.Context, in.Options)
	stub := grpcdynamic.NewStub(conn)
	stream, err := stub.InvokeRpcServerStream(rpcCtx, methodDesc, reqMsg)
	if err != nil {
		if refClient != nil {
			refClient.Reset()
		}
		return openbindings.SingleEventChannel(openbindings.FailedOutput(start, grpcErrorCode(err), err.Error())), nil
	}

	ch := make(chan openbindings.StreamEvent, 16)
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
				ch <- openbindings.StreamEvent{Error: &openbindings.ExecuteError{
					Code:    openbindings.ErrCodeStreamError,
					Message: err.Error(),
				}}
				return
			}

			output, err := responseToJSON(resp)
			if err != nil {
				ch <- openbindings.StreamEvent{Error: &openbindings.ExecuteError{
					Code:    openbindings.ErrCodeResponseError,
					Message: err.Error(),
				}}
				return
			}
			ch <- openbindings.StreamEvent{Data: output}
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

func buildRequest(method *desc.MethodDescriptor, input any) (*dynamic.Message, error) {
	msg := dynamic.NewMessage(method.GetInputType())
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
	if err := msg.UnmarshalJSONPB(&jsonpb.Unmarshaler{AllowUnknownFields: true}, jsonBytes); err != nil {
		return nil, fmt.Errorf("unmarshal input to protobuf: %w", err)
	}
	return msg, nil
}

func responseToJSON(resp proto.Message) (any, error) {
	dm, ok := resp.(*dynamic.Message)
	if !ok {
		return nil, fmt.Errorf("unexpected response type %T (expected *dynamic.Message)", resp)
	}
	jsonBytes, err := dm.MarshalJSONPB(&jsonpb.Marshaler{OrigName: true})
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
