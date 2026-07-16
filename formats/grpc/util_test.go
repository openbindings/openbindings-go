package grpc

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
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

func TestGRPCError_StatusMapping(t *testing.T) {
	// Wire values pinned as literals: consumers switch on these strings.
	cases := []struct {
		grpcCode codes.Code
		want     string
	}{
		{codes.Unauthenticated, "ERR_AUTH_REQUIRED"},
		{codes.PermissionDenied, "ERR_PERMISSION_DENIED"},
		{codes.Unavailable, "ERR_CONNECT_FAILED"},
		{codes.DeadlineExceeded, "ERR_TIMEOUT"},
		{codes.Internal, "ERR_EXECUTION_FAILED"},
		{codes.NotFound, "ERR_EXECUTION_FAILED"},
	}
	for _, tc := range cases {
		t.Run(tc.grpcCode.String(), func(t *testing.T) {
			err := status.Error(tc.grpcCode, "boom")
			ie := grpcError(err, openbindings.ErrCodeExecutionFailed)
			if ie.Code != tc.want {
				t.Errorf("code = %q, want %q", ie.Code, tc.want)
			}
			details, ok := ie.Details.(map[string]any)
			if !ok || details["grpcCode"] != tc.grpcCode.String() {
				t.Errorf("details = %v, want grpcCode %q", ie.Details, tc.grpcCode.String())
			}
		})
	}
}

func TestGRPCError_FallbackCode(t *testing.T) {
	err := status.Error(codes.Internal, "mid-stream")
	if ie := grpcError(err, openbindings.ErrCodeStreamError); ie.Code != "ERR_STREAM_ERROR" {
		t.Errorf("code = %q, want ERR_STREAM_ERROR", ie.Code)
	}
}

func TestGRPCError_NonStatusError(t *testing.T) {
	ie := grpcError(errors.New("plain failure"), openbindings.ErrCodeExecutionFailed)
	if ie.Code != "ERR_EXECUTION_FAILED" {
		t.Errorf("code = %q, want ERR_EXECUTION_FAILED", ie.Code)
	}
	if ie.Message != "plain failure" {
		t.Errorf("message = %q", ie.Message)
	}
}

func TestGRPCError_StatusDetails(t *testing.T) {
	st, err := status.New(codes.PermissionDenied, "nope").WithDetails(wrapperspb.String("extra"))
	if err != nil {
		t.Fatal(err)
	}
	ie := grpcError(st.Err(), openbindings.ErrCodeExecutionFailed)
	details, ok := ie.Details.(map[string]any)
	if !ok {
		t.Fatalf("details = %T", ie.Details)
	}
	ds, ok := details["grpcDetails"].([]any)
	if !ok || len(ds) != 1 {
		t.Fatalf("grpcDetails = %v", details["grpcDetails"])
	}
}

func TestRefResolveError_TransportVsNotFound(t *testing.T) {
	if ie := refResolveError("pkg.Svc", status.Error(codes.Unavailable, "down")); ie.Code != "ERR_CONNECT_FAILED" {
		t.Errorf("unavailable: code = %q, want ERR_CONNECT_FAILED", ie.Code)
	}
	if ie := refResolveError("pkg.Svc", errors.New("symbol not found")); ie.Code != "ERR_REF_NOT_FOUND" {
		t.Errorf("not found: code = %q, want ERR_REF_NOT_FOUND", ie.Code)
	}
}

func TestToOBMetadata(t *testing.T) {
	if got := toOBMetadata(nil); got != nil {
		t.Errorf("nil md: got %v, want nil", got)
	}
	if got := toOBMetadata(metadata.MD{}); got != nil {
		t.Errorf("empty md: got %v, want nil", got)
	}
	src := metadata.Pairs("x-a", "1", "x-a", "2", "x-b", "3")
	got := toOBMetadata(src)
	if len(got["x-a"]) != 2 || got["x-a"][0] != "1" || got["x-a"][1] != "2" || got["x-b"][0] != "3" {
		t.Errorf("got %v", got)
	}
	// Values are copied, not aliased.
	got["x-a"][0] = "mutated"
	if src.Get("x-a")[0] != "1" {
		t.Error("toOBMetadata aliased the source slice")
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

func TestApplyGRPCContext_BearerToken(t *testing.T) {
	ctx := context.Background()
	bindCtx := map[string]any{"bearerToken": "tok_123"}
	result := mustApplyGRPCContext(t, ctx, bindCtx)

	md, ok := metadata.FromOutgoingContext(result)
	if !ok {
		t.Fatal("expected outgoing metadata")
	}
	auth := md.Get("authorization")
	if len(auth) != 1 || auth[0] != "Bearer tok_123" {
		t.Errorf("authorization = %v, want [Bearer tok_123]", auth)
	}
}

func TestApplyGRPCContext_APIKey(t *testing.T) {
	ctx := context.Background()
	bindCtx := map[string]any{"apiKey": "key_abc"}
	result := mustApplyGRPCContext(t, ctx, bindCtx)

	md, ok := metadata.FromOutgoingContext(result)
	if !ok {
		t.Fatal("expected outgoing metadata")
	}
	auth := md.Get("authorization")
	if len(auth) != 1 || auth[0] != "ApiKey key_abc" {
		t.Errorf("authorization = %v, want [ApiKey key_abc]", auth)
	}
}

func TestApplyGRPCContext_BasicAuth(t *testing.T) {
	ctx := context.Background()
	bindCtx := map[string]any{"basic": map[string]any{"username": "user", "password": "pass"}}
	result := mustApplyGRPCContext(t, ctx, bindCtx)

	md, ok := metadata.FromOutgoingContext(result)
	if !ok {
		t.Fatal("expected outgoing metadata")
	}
	auth := md.Get("authorization")
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("user:pass"))
	if len(auth) != 1 || auth[0] != want {
		t.Errorf("authorization = %v, want [%s]", auth, want)
	}
}

func TestApplyGRPCContext_BearerTakesPriority(t *testing.T) {
	ctx := context.Background()
	bindCtx := map[string]any{
		"bearerToken": "tok_123",
		"apiKey":      "key_abc",
	}
	result := mustApplyGRPCContext(t, ctx, bindCtx)

	md, _ := metadata.FromOutgoingContext(result)
	auth := md.Get("authorization")
	if len(auth) != 1 || auth[0] != "Bearer tok_123" {
		t.Errorf("authorization = %v, want [Bearer tok_123]", auth)
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
		"headers": map[string]any{"x-custom": "value"},
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
}

func TestIsInfraService(t *testing.T) {
	if !isInfraService("grpc.reflection.v1alpha.ServerReflection") {
		t.Error("expected grpc.reflection.* to be infra")
	}
	if !isInfraService("grpc.health.v1.Health") {
		t.Error("expected grpc.health.* to be infra")
	}
	if isInfraService("mypackage.MyService") {
		t.Error("expected mypackage.MyService to not be infra")
	}
}
