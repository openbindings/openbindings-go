package grpc

import (
	"context"
	"errors"
	"testing"

	"github.com/openbindings/openbindings-go/invoke"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestParseRef_Valid(t *testing.T) {
	svc, method, err := parseRef("mypackage.MyService/GetItem")
	if err != nil {
		t.Fatal(err)
	}
	if svc != "mypackage.MyService" {
		t.Errorf("svc = %q, want %q", svc, "mypackage.MyService")
	}
	if method != "GetItem" {
		t.Errorf("method = %q, want %q", method, "GetItem")
	}
}

func TestParseRef_NestedPackage(t *testing.T) {
	svc, method, err := parseRef("com.example.api.v1.UserService/CreateUser")
	if err != nil {
		t.Fatal(err)
	}
	if svc != "com.example.api.v1.UserService" {
		t.Errorf("svc = %q, want %q", svc, "com.example.api.v1.UserService")
	}
	if method != "CreateUser" {
		t.Errorf("method = %q, want %q", method, "CreateUser")
	}
}

func TestParseRef_Empty(t *testing.T) {
	_, _, err := parseRef("")
	if err == nil {
		t.Error("expected error for empty ref")
	}
}

func TestParseRef_NoSlash(t *testing.T) {
	_, _, err := parseRef("mypackage.MyService")
	if err == nil {
		t.Error("expected error for ref without slash")
	}
}

func TestParseRef_TrailingSlash(t *testing.T) {
	_, _, err := parseRef("mypackage.MyService/")
	if err == nil {
		t.Error("expected error for trailing slash")
	}
}

func TestParseRef_LeadingSlash(t *testing.T) {
	_, _, err := parseRef("/GetItem")
	if err == nil {
		t.Error("expected error for leading slash only")
	}
}

func TestGRPCError_StatusIsDiagnosticNotPortableClassification(t *testing.T) {
	for _, grpcCode := range []codes.Code{
		codes.Unauthenticated, codes.PermissionDenied, codes.Unavailable,
		codes.ResourceExhausted, codes.DeadlineExceeded, codes.Canceled,
		codes.Internal, codes.NotFound,
	} {
		t.Run(grpcCode.String(), func(t *testing.T) {
			ie := grpcError(status.Error(grpcCode, "boom"), invoke.ErrCodeExecutionFailed)
			if ie.Code != invoke.ErrCodeExecutionFailed {
				t.Errorf("code = %q, want protocol-independent unsuccessful completion", ie.Code)
			}
			if ie.HasData() {
				t.Errorf("native status leaked as abstract data: %v", ie.Data)
			}
		})
	}
}

func TestGRPCError_StatusOverridesStreamFallback(t *testing.T) {
	err := status.Error(codes.Internal, "mid-stream")
	if ie := grpcError(err, invoke.ErrCodeStreamError); ie.Code != "ERR_EXECUTION_FAILED" {
		t.Errorf("code = %q, want ERR_EXECUTION_FAILED", ie.Code)
	}
}

func TestGRPCError_NonStatusError(t *testing.T) {
	ie := grpcError(errors.New("plain failure"), invoke.ErrCodeExecutionFailed)
	if ie.Code != "ERR_EXECUTION_FAILED" {
		t.Errorf("code = %q, want ERR_EXECUTION_FAILED", ie.Code)
	}
	if ie.Error() != invoke.ErrCodeExecutionFailed {
		t.Errorf("error text = %q", ie.Error())
	}
}

func TestGRPCError_StatusDetails(t *testing.T) {
	st, err := status.New(codes.PermissionDenied, "nope").WithDetails(wrapperspb.String("extra"))
	if err != nil {
		t.Fatal(err)
	}
	ie := grpcError(st.Err(), invoke.ErrCodeExecutionFailed)
	if ie.HasData() {
		t.Fatalf("native status details leaked as abstract data: %v", ie.Data)
	}
}

func TestRefResolveError_TransportVsNotFound(t *testing.T) {
	// A reflection-time transport status is unsuccessful completion, not a
	// missing ref; its native gRPC status remains diagnostic only.
	if ie := refResolveError("pkg.Svc", status.Error(codes.Unavailable, "down")); ie.Code != invoke.ErrCodeExecutionFailed {
		t.Errorf("unavailable: code = %q, want %s", ie.Code, invoke.ErrCodeExecutionFailed)
	}
	if ie := refResolveError("pkg.Svc", errors.New("symbol not found")); ie.Code != "ERR_REF_NOT_FOUND" {
		t.Errorf("not found: code = %q, want ERR_REF_NOT_FOUND", ie.Code)
	}
}

// mustApplyGRPCContext asserts the context applies cleanly (no unplaceable
// keys) and returns the resulting context.
func mustApplyGRPCContext(t *testing.T, ctx context.Context, bindCtx map[string]any) context.Context {
	t.Helper()
	result, err := applyGRPCContext(ctx, bindCtx)
	if err != nil {
		t.Fatalf("applyGRPCContext: %v", err)
	}
	return result
}

func TestApplyGRPCContext_GenericBearerDoesNotInventMetadata(t *testing.T) {
	result := mustApplyGRPCContext(t, context.Background(), map[string]any{"bearerToken": "tok_123"})
	if _, ok := metadata.FromOutgoingContext(result); ok {
		t.Fatal("generic bearer credential must not be assigned an invented gRPC metadata field")
	}
}

func TestApplyGRPCContext_APIKey(t *testing.T) {
	result := mustApplyGRPCContext(t, context.Background(), map[string]any{"apiKey": "key_abc"})
	if _, ok := metadata.FromOutgoingContext(result); ok {
		t.Fatal("generic API credential must not be assigned an invented gRPC metadata field")
	}
}

func TestApplyGRPCContext_BasicAuth(t *testing.T) {
	result := mustApplyGRPCContext(t, context.Background(), map[string]any{
		"basic": map[string]any{"username": "user", "password": "pass"},
	})
	if _, ok := metadata.FromOutgoingContext(result); ok {
		t.Fatal("generic basic credential must not be assigned an invented gRPC metadata field")
	}
}

func TestApplyGRPCContext_NoCredentials(t *testing.T) {
	ctx := context.Background()
	result := mustApplyGRPCContext(t, ctx, nil)

	_, ok := metadata.FromOutgoingContext(result)
	if ok {
		t.Error("expected no outgoing metadata when no credentials")
	}
}

func TestApplyGRPCContext_ContextHeaders(t *testing.T) {
	ctx := context.Background()
	bindCtx := map[string]any{
		"headers": map[string]any{"x-custom": "value", "authorization": "Bearer explicit"},
	}
	result := mustApplyGRPCContext(t, ctx, bindCtx)

	md, ok := metadata.FromOutgoingContext(result)
	if !ok {
		t.Fatal("expected outgoing metadata")
	}
	got := md.Get("x-custom")
	if len(got) != 1 || got[0] != "value" {
		t.Errorf("x-custom = %v, want [value]", got)
	}
	if got := md.Get("authorization"); len(got) != 1 || got[0] != "Bearer explicit" {
		t.Errorf("authorization = %v, want explicitly supplied metadata", got)
	}
}

func TestIsInfraService(t *testing.T) {
	if !isInfraService("grpc.reflection.v1alpha.ServerReflection") {
		t.Error("expected grpc.reflection.* to be infra")
	}
	if isInfraService("grpc.health.v1.Health") {
		t.Error("expected declared grpc.health.* services to remain invocable")
	}
	if isInfraService("mypackage.MyService") {
		t.Error("expected mypackage.MyService to not be infra")
	}
}
