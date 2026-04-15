package grpc

import (
	"context"
	"encoding/base64"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
	"google.golang.org/grpc/metadata"
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

func TestNormalizeAddress_PlainHostPort(t *testing.T) {
	got := normalizeAddress("api.example.com:443")
	if got != "api.example.com:443" {
		t.Errorf("got %q, want %q", got, "api.example.com:443")
	}
}

func TestNormalizeAddress_WithScheme(t *testing.T) {
	got := normalizeAddress("https://api.example.com:443")
	if got != "api.example.com:443" {
		t.Errorf("got %q, want %q", got, "api.example.com:443")
	}
}

func TestNormalizeAddress_Empty(t *testing.T) {
	got := normalizeAddress("")
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestNormalizeAddress_Whitespace(t *testing.T) {
	got := normalizeAddress("  api.example.com:443  ")
	if got != "api.example.com:443" {
		t.Errorf("got %q, want %q", got, "api.example.com:443")
	}
}

func TestApplyGRPCContext_BearerToken(t *testing.T) {
	ctx := context.Background()
	bindCtx := map[string]any{"bearerToken": "tok_123"}
	result := applyGRPCContext(ctx, bindCtx, nil)

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
	result := applyGRPCContext(ctx, bindCtx, nil)

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
	result := applyGRPCContext(ctx, bindCtx, nil)

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
	result := applyGRPCContext(ctx, bindCtx, nil)

	md, _ := metadata.FromOutgoingContext(result)
	auth := md.Get("authorization")
	if len(auth) != 1 || auth[0] != "Bearer tok_123" {
		t.Errorf("authorization = %v, want [Bearer tok_123]", auth)
	}
}

func TestApplyGRPCContext_NoCredentials(t *testing.T) {
	ctx := context.Background()
	result := applyGRPCContext(ctx, nil, nil)

	_, ok := metadata.FromOutgoingContext(result)
	if ok {
		t.Error("expected no outgoing metadata when no credentials")
	}
}

func TestApplyGRPCContext_ExecutionOptionsHeaders(t *testing.T) {
	ctx := context.Background()
	opts := &openbindings.ExecutionOptions{
		Headers: map[string]string{"X-Custom": "value"},
	}
	result := applyGRPCContext(ctx, nil, opts)

	md, ok := metadata.FromOutgoingContext(result)
	if !ok {
		t.Fatal("expected outgoing metadata")
	}
	got := md.Get("x-custom")
	if len(got) != 1 || got[0] != "value" {
		t.Errorf("x-custom = %v, want [value]", got)
	}
}

func TestNeedsTLS_Port443(t *testing.T) {
	if !needsTLS("api.example.com:443") {
		t.Error("expected TLS for port 443")
	}
}

func TestNeedsTLS_HTTPS(t *testing.T) {
	if !needsTLS("https://api.example.com") {
		t.Error("expected TLS for https:// prefix")
	}
}

func TestNeedsTLS_PlainPort(t *testing.T) {
	if needsTLS("localhost:50051") {
		t.Error("expected no TLS for non-443 port")
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
