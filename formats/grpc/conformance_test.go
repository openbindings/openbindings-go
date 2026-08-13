package grpc

// Conformance tests for openbindings.grpc@1, keyed to the specification's
// rule identifiers (GRPC-D-01..03, GRPC-P-01..07). Each test names the rule
// it pins; together they are this implementation's behavioral contract with
// the binding specification.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	openbindings "github.com/openbindings/openbindings-go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/reflection"
	v1reflectiongrpc "google.golang.org/grpc/reflection/grpc_reflection_v1"
	v1alphareflectiongrpc "google.golang.org/grpc/reflection/grpc_reflection_v1alpha"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/durationpb"
)

// ---------------------------------------------------------------------------
// GRPC-D-02 — location grammar (§4)
// ---------------------------------------------------------------------------

func TestConformance_D02_DialAddressGrammar(t *testing.T) {
	valid := []struct {
		address      string
		wantTLS      bool
		wantExplicit bool
	}{
		{"api.example.com:50051", false, false},
		{"api.example.com:443", false, false}, // ports never imply transport
		{"api.example.com:8443", false, false},
		{"grpc://api.example.com:443", false, true},
		{"grpcs://api.example.com:8443", true, true},
		{"10.0.0.7:50051", false, false},
		{"[::1]:50051", false, false},
		{"grpcs://[2001:db8::1]:50051", true, true},
	}
	for _, tc := range valid {
		addr, err := parseDialAddress(tc.address)
		if err != nil {
			t.Errorf("parseDialAddress(%q) must accept: %v", tc.address, err)
			continue
		}
		if addr.tls != tc.wantTLS {
			t.Errorf("parseDialAddress(%q).tls = %v, want %v", tc.address, addr.tls, tc.wantTLS)
		}
		if addr.explicit != tc.wantExplicit {
			t.Errorf("parseDialAddress(%q).explicit = %v, want %v", tc.address, addr.explicit, tc.wantExplicit)
		}
		if strings.Contains(addr.hostPort, "://") {
			t.Errorf("parseDialAddress(%q) must strip the scheme before dialing, got %q", tc.address, addr.hostPort)
		}
	}

	invalid := []struct {
		address string
		mention string
	}{
		{"https://api.example.com:443", "scheme"}, // one spelling per meaning: https is CUT
		{"http://api.example.com:80", "scheme"},   // ... and so is http
		{"unix:///tmp/sock", "scheme"},            // no other scheme spelling is defined
		{"api.example.com", "port"},               // this specification defaults no ports
		{"grpc://api.example.com", "port"},        // scheme'd form still needs a port
		{"api.example.com:", "port"},              // empty port
		{"api.example.com:https", "port"},         // non-numeric port
		{"api.example.com:443/v1", "path"},        // path component
		{"api.example.com:443?x=1", "query"},      // query component
		{"api.example.com:443#frag", "fragment"},  // fragment component
		{"user@api.example.com:443", "userinfo"},  // userinfo component
		{"::1:50051", "port"},                     // IPv6 must be bracketed
	}
	for _, tc := range invalid {
		_, err := parseDialAddress(tc.address)
		if err == nil {
			t.Errorf("parseDialAddress(%q) must refuse (GRPC-D-02)", tc.address)
			continue
		}
		if !strings.Contains(err.Error(), tc.mention) {
			t.Errorf("parseDialAddress(%q) refusal must mention %q, got: %v", tc.address, tc.mention, err)
		}
		if !strings.Contains(err.Error(), "GRPC-D-02") {
			t.Errorf("parseDialAddress(%q) refusal must cite GRPC-D-02, got: %v", tc.address, err)
		}
	}
}

// A non-conformant location refuses loudly at the invocation boundary,
// before any dial.
func TestConformance_D02_NonConformantLocationRefusedPreDial(t *testing.T) {
	invoker := NewInvoker()
	defer invoker.Close()

	for _, location := range []string{"https://api.example.com:443", "api.example.com:443/v1"} {
		inv := invoker.InvokeBinding(testCtx(t), &openbindings.BindingInvocationArgs{
			Source: openbindings.InvocationSource{BindingSpec: BindingSpec, Location: location},
			Ref:    "testpkg.ItemService/GetItem",
		})
		_, terr := drainInvocation(t, inv)
		if terr == nil || terr.Code != openbindings.ErrCodeSourceConfigError {
			t.Fatalf("location %q: expected ERR_SOURCE_CONFIG_ERROR, got %v", location, terr)
		}
	}
}

// ---------------------------------------------------------------------------
// GRPC-D-01 — content carriages (§3, §5)
// ---------------------------------------------------------------------------

// String content: single-file .proto source text whose imports are limited
// to google/protobuf/*, resolved from this processor's bundled copies.
func TestConformance_D01_ProtoText_GoogleProtobufImportsResolve(t *testing.T) {
	proto := `syntax = "proto3";
package wkt;
import "google/protobuf/duration.proto";
service Clock { rpc Wait(google.protobuf.Duration) returns (WaitReply); }
message WaitReply { string status = 1; }
`
	disc, err := discoverFromContent(context.Background(), openbindings.TextContent(proto))
	if err != nil {
		t.Fatalf("google/protobuf/* imports must resolve from bundled copies (§3): %v", err)
	}
	if len(disc.services) != 1 || string(disc.services[0].FullName()) != "wkt.Clock" {
		t.Fatalf("services = %v", disc.services)
	}
}

func TestConformance_D01_ProtoText_NonGoogleImportRefused(t *testing.T) {
	proto := `syntax = "proto3";
package bad;
import "corp/shared.proto";
service S { rpc Do(Req) returns (Req); }
message Req { string id = 1; }
`
	_, err := discoverFromContent(context.Background(), openbindings.TextContent(proto))
	if err == nil {
		t.Fatal("a non-google/protobuf import must refuse loudly at load (§3)")
	}
	for _, want := range []string{"google/protobuf/*", "corp/shared.proto"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal must mention %q, got: %v", want, err)
		}
	}
}

// descriptorSetJSON serializes FileDescriptorProtos as a FileDescriptorSet
// in canonical protobuf JSON, decoded to the map shape OBI content carries.
func descriptorSetJSON(t *testing.T, fdps ...*descriptorpb.FileDescriptorProto) map[string]any {
	t.Helper()
	raw, err := protojson.Marshal(&descriptorpb.FileDescriptorSet{File: fdps})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// durationFDP returns google/protobuf/duration.proto as a
// FileDescriptorProto, completing the test schema's closure.
func durationFDP() *descriptorpb.FileDescriptorProto {
	return protodesc.ToFileDescriptorProto(durationpb.File_google_protobuf_duration_proto)
}

// Object content: a FileDescriptorSet in canonical JSON is a valid schema
// carrier, end to end through an invocation.
func TestConformance_D01_DescriptorSetJSON_InvokesEndToEnd(t *testing.T) {
	dialer, _ := setupTestServer(t)
	invoker := newTestInvoker(t, dialer)
	defer invoker.Close()

	ctx := testCtx(t)
	inv := invoker.InvokeBinding(ctx, &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{
			BindingSpec: BindingSpec,
			Location:    bufconnLocation,
			Content:     mustContent(descriptorSetJSON(t, durationFDP(), testItemsFDP())),
		},
		Ref:     "testpkg.ItemService/GetItem",
		Context: map[string]any{"configuration": map[string]any{"transport": "plaintext"}},
	})
	if err := inv.Write(ctx, map[string]any{"id": "fds"}); err != nil {
		t.Fatal(err)
	}
	v, err := openbindings.Single(ctx, inv.Outputs())
	if err != nil {
		t.Fatalf("FileDescriptorSet-in-JSON content must be a valid schema carrier (GRPC-D-01): %v", err)
	}
	if v.(map[string]any)["name"] != "item-fds" {
		t.Errorf("response = %v", v)
	}
}

func TestConformance_D01_DescriptorSetJSON_UnknownMemberRefused(t *testing.T) {
	content := descriptorSetJSON(t, durationFDP(), testItemsFDP())
	content["sizzle"] = true
	_, err := discoverFromContent(context.Background(), mustContent(content))
	if err == nil {
		t.Fatal("unknown members in descriptor-set content must refuse loudly (§3)")
	}
	if !strings.Contains(err.Error(), "sizzle") {
		t.Errorf("refusal must name the unknown member, got: %v", err)
	}
}

func TestConformance_D01_DescriptorSetJSON_BracketKeyedExtensionRefused(t *testing.T) {
	content := descriptorSetJSON(t, durationFDP(), testItemsFDP())
	// Splice a compiled custom option (the runtime bracket-key convention)
	// into the first file's options.
	file0 := content["file"].([]any)[0].(map[string]any)
	file0["options"] = map[string]any{"[corp.build_tag]": "v1"}
	_, err := discoverFromContent(context.Background(), mustContent(content))
	if err == nil {
		t.Fatal("bracket-keyed extension members must refuse loudly (§3): a conformant pin carries option-stripped descriptors")
	}
	for _, want := range []string{"[corp.build_tag]", "option-stripped"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal must mention %q, got: %v", want, err)
		}
	}
}

func TestConformance_D01_DescriptorSetJSON_MissingDependencyRefused(t *testing.T) {
	// The test schema imports google/protobuf/duration.proto; a set carried
	// without it is not the compiled, self-contained closure §3 pins.
	_, err := discoverFromContent(context.Background(), mustContent(descriptorSetJSON(t, testItemsFDP())))
	if err == nil {
		t.Fatal("a descriptor set missing a dependency must refuse loudly (§3: self-contained closure)")
	}
	if !strings.Contains(err.Error(), "self-contained") {
		t.Errorf("refusal must mention the self-contained pin, got: %v", err)
	}
}

// No other JSON type or shape is an accepted content value (§5).
func TestConformance_D01_OtherContentTypesRefused(t *testing.T) {
	for _, content := range []any{42.0, true, []any{"x"}} {
		_, err := discoverFromContent(context.Background(), mustContent(content))
		if err == nil {
			t.Errorf("content %T must refuse (GRPC-D-01: string, object, or absent)", content)
		}
	}
}

// ---------------------------------------------------------------------------
// GRPC-D-03 — ref form and byte-exact matching (§7)
// ---------------------------------------------------------------------------

func TestConformance_D03_PackagelessServiceRef(t *testing.T) {
	proto := `syntax = "proto3";
service CoffeeShop { rpc GetMenu(MenuRequest) returns (Menu); }
message MenuRequest {}
message Menu { string items = 1; }
`
	svcName, methodName, err := parseRef("CoffeeShop/GetMenu")
	if err != nil {
		t.Fatalf("a packageless ref is legal (GRPC-D-03): %v", err)
	}
	disc, err := discoverFromContent(context.Background(), openbindings.TextContent(proto))
	if err != nil {
		t.Fatal(err)
	}
	var found protoreflect.ServiceDescriptor
	for _, svc := range disc.services {
		if string(svc.FullName()) == svcName {
			found = svc
		}
	}
	if found == nil {
		t.Fatalf("packageless service %q must resolve against its bare name", svcName)
	}
	if found.Methods().ByName(protoreflect.Name(methodName)) == nil {
		t.Fatalf("method %q must resolve", methodName)
	}
}

// Matching is byte-exact, no case folding: a case-mismatched ref is
// unresolvable, checked offline against embedded content (before any dial —
// resolution precedes dispatch).
func TestConformance_D03_ByteExactMatching(t *testing.T) {
	proto := `syntax = "proto3";
package tiny;
service Tiny { rpc Ping(PingMsg) returns (PingMsg); }
message PingMsg { string msg = 1; }
`
	invoker := NewInvoker()
	defer invoker.Close()

	for _, ref := range []string{"tiny.Tiny/ping", "tiny.tiny/Ping", "TINY.Tiny/Ping"} {
		inv := invoker.InvokeBinding(testCtx(t), &openbindings.BindingInvocationArgs{
			// The location is a valid form but unreachable: the refusal must
			// fire from offline resolution, never a dial.
			Source: openbindings.InvocationSource{BindingSpec: BindingSpec, Location: "grpc://203.0.113.9:50051", Content: openbindings.TextContent(proto)},
			Ref:    ref,
		})
		_, terr := drainInvocation(t, inv)
		if terr == nil || terr.Code != openbindings.ErrCodeRefNotFound {
			t.Fatalf("ref %q: byte-exact matching must refuse with ERR_REF_NOT_FOUND, got %v", ref, terr)
		}
	}
}

// ---------------------------------------------------------------------------
// GRPC-P-01 — reflection version selection (§3)
// ---------------------------------------------------------------------------

// setupReflVariant builds a bufconn server whose v1 reflection availability
// is controlled: "absent" registers only v1alpha; "deny" registers both but
// fails v1 streams with PERMISSION_DENIED.
func setupReflVariant(t *testing.T, v1Mode string) func(context.Context, string) (net.Conn, error) {
	t.Helper()
	files, _ := testItemsRegistry(t)

	var opts []grpc.ServerOption
	if v1Mode == "deny" {
		opts = append(opts, grpc.ChainStreamInterceptor(func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
			if info.FullMethod == "/grpc.reflection.v1.ServerReflection/ServerReflectionInfo" {
				return status.Error(codes.PermissionDenied, "v1 reflection denied")
			}
			return handler(srv, ss)
		}))
	}
	svr := grpc.NewServer(opts...)
	svr.RegisterService(&grpc.ServiceDesc{
		ServiceName: "testpkg.ItemService",
		HandlerType: (*any)(nil),
		Metadata:    "testpkg/items.proto",
	}, &struct{}{})

	reflOpts := reflection.ServerOptions{Services: svr, DescriptorResolver: files}
	if v1Mode != "absent" {
		v1reflectiongrpc.RegisterServerReflectionServer(svr, reflection.NewServerV1(reflOpts))
	}
	v1alphareflectiongrpc.RegisterServerReflectionServer(svr, reflection.NewServer(reflOpts))

	lis := bufconn.Listen(1024 * 1024)
	go func() { _ = svr.Serve(lis) }()
	t.Cleanup(func() { svr.Stop() })
	return func(ctx context.Context, _ string) (net.Conn, error) {
		return lis.DialContext(ctx)
	}
}

// v1 first; v1alpha iff the v1 stream fails UNIMPLEMENTED.
func TestConformance_P01_V1AlphaFallback_OnUnimplemented(t *testing.T) {
	dialer := setupReflVariant(t, "absent")
	disc, err := discover(testCtx(t), "127.0.0.1:50051",
		dialConfig{extra: []grpc.DialOption{grpc.WithContextDialer(dialer)}})
	if err != nil {
		t.Fatalf("v1 UNIMPLEMENTED must fall back to v1alpha (GRPC-P-01): %v", err)
	}
	if len(disc.services) != 1 || string(disc.services[0].FullName()) != "testpkg.ItemService" {
		t.Fatalf("services = %v", disc.services)
	}
}

// Any other v1 status is that source's failure, not a version signal: the
// processor must NOT fall back (v1alpha would have answered here).
func TestConformance_P01_NonUnimplementedStatusIsSourceFailure(t *testing.T) {
	dialer := setupReflVariant(t, "deny")
	_, err := discover(testCtx(t), "127.0.0.1:50051",
		dialConfig{extra: []grpc.DialOption{grpc.WithContextDialer(dialer)}})
	if err == nil {
		t.Fatal("a PERMISSION_DENIED v1 stream must be the source's failure, never a silent v1alpha fallback (GRPC-P-01)")
	}
	if status.Code(errors.Unwrap(err)) != codes.PermissionDenied && !strings.Contains(err.Error(), "denied") {
		t.Errorf("the surfaced failure must carry the v1 status, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// GRPC-P-02 — transport configuration point (§4, §9.3)
// ---------------------------------------------------------------------------

// The transport override replaces the address-form determination ENTIRELY:
// it wins even against an explicit grpcs:// scheme.
func TestConformance_P02_TransportOverrideBeatsExplicitScheme(t *testing.T) {
	dialer, _ := setupTestServer(t)
	invoker := NewInvoker(WithDialOptions(grpc.WithContextDialer(dialer)))
	defer invoker.Close()

	ctx := testCtx(t)
	inv := invoker.InvokeBinding(ctx, &openbindings.BindingInvocationArgs{
		Source:  openbindings.InvocationSource{BindingSpec: BindingSpec, Location: "grpcs://127.0.0.1:443"},
		Ref:     "testpkg.ItemService/GetItem",
		Context: map[string]any{"configuration": map[string]any{"transport": "plaintext"}},
	})
	if err := inv.Write(ctx, map[string]any{"id": "p"}); err != nil {
		t.Fatal(err)
	}
	if _, err := openbindings.Single(ctx, inv.Outputs()); err != nil {
		t.Fatalf("configuration.transport=plaintext must beat the explicit grpcs:// scheme (GRPC-P-02): %v", err)
	}

	// Negative control: without the override, the scheme's TLS
	// determination stands and fails against the plaintext server.
	ctl, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	inv2 := invoker.InvokeBinding(ctl, &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{BindingSpec: BindingSpec, Location: "grpcs://127.0.0.1:443"},
		Ref:    "testpkg.ItemService/GetItem",
	})
	_ = inv2.Write(ctl, map[string]any{"id": "p"})
	if _, err := openbindings.Single(ctl, inv2.Outputs()); err == nil {
		t.Fatal("grpcs:// against the plaintext server should fail; the negative control is vacuous")
	}
}

func TestConformance_P02_MalformedTransportConfigRefused(t *testing.T) {
	invoker := NewInvoker()
	defer invoker.Close()

	cases := []any{
		"mtls", // not a defined election
		map[string]any{"certificateAuthority": "..."}, // unknown member
		42.0, // not a defined shape
	}
	for _, transport := range cases {
		inv := invoker.InvokeBinding(testCtx(t), &openbindings.BindingInvocationArgs{
			Source:  openbindings.InvocationSource{BindingSpec: BindingSpec, Location: "127.0.0.1:50051"},
			Ref:     "testpkg.ItemService/GetItem",
			Context: map[string]any{"configuration": map[string]any{"transport": transport}},
		})
		_, terr := drainInvocation(t, inv)
		if terr == nil || terr.Code != openbindings.ErrCodeSourceConfigError {
			t.Fatalf("transport %v: expected ERR_SOURCE_CONFIG_ERROR, got %v", transport, terr)
		}
	}
}

// The target configuration point replaces the source's location entirely —
// the effective dial address AND its transport determination come from the
// configured target, not the displaced location.
func TestConformance_TargetPointReplacesLocation(t *testing.T) {
	dialer, _ := setupTestServer(t)
	invoker := NewInvoker(WithDialOptions(grpc.WithContextDialer(dialer)))
	defer invoker.Close()

	ctx := testCtx(t)
	inv := invoker.InvokeBinding(ctx, &openbindings.BindingInvocationArgs{
		// The location explicitly elects TLS and would fail against the
		// plaintext server; the configured target explicitly elects plaintext. Success
		// proves the replacement.
		Source:  openbindings.InvocationSource{BindingSpec: BindingSpec, Location: "grpcs://203.0.113.9:443"},
		Ref:     "testpkg.ItemService/GetItem",
		Context: map[string]any{"configuration": map[string]any{"target": "grpc://127.0.0.1:50051"}},
	})
	if err := inv.Write(ctx, map[string]any{"id": "t"}); err != nil {
		t.Fatal(err)
	}
	if _, err := openbindings.Single(ctx, inv.Outputs()); err != nil {
		t.Fatalf("configuration.target must replace the location (§9.3): %v", err)
	}
}

// ---------------------------------------------------------------------------
// GRPC-P-03 — input mapping (§9.1)
// ---------------------------------------------------------------------------

// Unknown input fields are refused loudly before dispatch — the canonical
// mapping's own default posture — never silently discarded.
func TestConformance_P03_UnknownInputFieldRefusedPreDispatch(t *testing.T) {
	dialer, ts := setupTestServer(t)
	invoker := newTestInvoker(t, dialer)
	defer invoker.Close()

	ctx := testCtx(t)
	inv := invoker.InvokeBinding(ctx, bufconnArgs("testpkg.ItemService/GetItem", nil))
	if err := inv.Write(ctx, map[string]any{"id": "1", "bogus": true}); err != nil {
		t.Fatal(err)
	}
	vals, terr := drainInvocation(t, inv)
	if len(vals) != 0 || terr == nil {
		t.Fatalf("unknown input field must refuse pre-dispatch, got %d outputs, terr=%v", len(vals), terr)
	}
	if terr.Code != openbindings.ErrCodeValidationFailed {
		t.Errorf("code = %q, want ERR_VALIDATION_FAILED", terr.Code)
	}
	if terr.HasData() {
		t.Errorf("validator diagnostics crossed as abstract data: %#v", terr.Data)
	}
	if got := ts.auth(); got != "" {
		t.Error("the refusal must fire BEFORE dispatch; the server saw the call")
	}
}

// The accepted input shape is the request type's CANONICAL JSON form: a
// google.protobuf.Duration-typed request is a JSON string, not an object.
func TestConformance_P03_WellKnownTypeCanonicalFormRequest(t *testing.T) {
	dialer, _ := setupTestServer(t)
	invoker := newTestInvoker(t, dialer)
	defer invoker.Close()

	ctx := testCtx(t)
	inv := invoker.InvokeBinding(ctx, bufconnArgs("testpkg.ItemService/EchoDuration", nil))
	if err := inv.Write(ctx, "3.5s"); err != nil {
		t.Fatal(err)
	}
	v, err := openbindings.Single(ctx, inv.Outputs())
	if err != nil {
		t.Fatalf("a Duration-typed request must accept its canonical JSON string form (GRPC-P-03): %v", err)
	}
	if v.(map[string]any)["name"] != "dur-3.500000000" {
		t.Errorf("response = %v, want dur-3.500000000", v)
	}
}

// ---------------------------------------------------------------------------
// §3 accepted schema range (bound-method closure; via GRPC-P-03)
// ---------------------------------------------------------------------------

// methodFromContent compiles embedded content and resolves one method.
func methodFromContent(t *testing.T, content any, svcName, methodName string) protoreflect.MethodDescriptor {
	t.Helper()
	disc, err := discoverFromContent(context.Background(), mustContent(content))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	for _, svc := range disc.services {
		if string(svc.FullName()) == svcName {
			m := svc.Methods().ByName(protoreflect.Name(methodName))
			if m == nil {
				t.Fatalf("method %q not found in %q", methodName, svcName)
			}
			return m
		}
	}
	t.Fatalf("service %q not found", svcName)
	return nil
}

func TestConformance_SchemaRange_BoundClosure(t *testing.T) {
	cases := []struct {
		name    string
		proto   string
		mention string // empty = accepted
	}{
		{
			name: "proto3 always satisfies the range",
			proto: `syntax = "proto3";
package ok3;
message Req { string id = 1; map<string, Inner> tags = 2; }
message Inner { repeated int64 ns = 1; }
message Resp { string name = 1; }
service S { rpc Do(Req) returns (Resp); }
`,
		},
		{
			name: "plain editions file satisfies the range",
			proto: `edition = "2023";
package oked;
message Req { string id = 1; }
message Resp { string name = 1; }
service S { rpc Do(Req) returns (Resp); }
`,
		},
		{
			name: "proto2 without refused constructs satisfies the range",
			proto: `syntax = "proto2";
package ok2;
message Req { optional string id = 1; }
message Resp { optional string name = 1; }
service S { rpc Do(Req) returns (Resp); }
`,
		},
		{
			name: "proto2 required presence refused",
			proto: `syntax = "proto2";
package p2;
message Req { required string id = 1; }
message Resp { optional string name = 1; }
service S { rpc Do(Req) returns (Resp); }
`,
			mention: "required presence",
		},
		{
			name: "editions LEGACY_REQUIRED presence refused",
			proto: `edition = "2023";
package edreq;
message Req { string id = 1 [features.field_presence = LEGACY_REQUIRED]; }
message Resp { string name = 1; }
service S { rpc Do(Req) returns (Resp); }
`,
			mention: "required presence",
		},
		{
			name: "proto2 group refused",
			proto: `syntax = "proto2";
package p2g;
message Req {
  optional group Extras = 1 {
    optional string s = 1;
  }
}
message Resp { optional string name = 1; }
service S { rpc Do(Req) returns (Resp); }
`,
			mention: "group",
		},
		{
			name: "editions DELIMITED encoding refused",
			proto: `edition = "2023";
package eddel;
message Inner { string x = 1; }
message Req { Inner inner = 1 [features.message_encoding = DELIMITED]; }
message Resp { string name = 1; }
service S { rpc Do(Req) returns (Resp); }
`,
			mention: "DELIMITED",
		},
		{
			name: "editions json_format LEGACY_BEST_EFFORT refused",
			proto: `edition = "2023";
option features.json_format = LEGACY_BEST_EFFORT;
package edjf;
message Req { string id = 1; }
message Resp { string name = 1; }
service S { rpc Do(Req) returns (Resp); }
`,
			mention: "json_format",
		},
		{
			name: "refused construct in a NESTED closure message refused",
			proto: `syntax = "proto2";
package p2n;
message Req { optional Deep deep = 1; }
message Deep { required string id = 1; }
message Resp { optional string name = 1; }
service S { rpc Do(Req) returns (Resp); }
`,
			mention: "required presence",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var pkg string
			for _, line := range strings.Split(tc.proto, "\n") {
				if strings.HasPrefix(line, "package ") {
					pkg = strings.TrimSuffix(strings.TrimPrefix(line, "package "), ";")
				}
			}
			method := methodFromContent(t, tc.proto, pkg+".S", "Do")
			err := validateBoundClosure(method)
			if tc.mention == "" {
				if err != nil {
					t.Fatalf("must satisfy the accepted range (§3): %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("must refuse (§3: %s)", tc.mention)
			}
			if !strings.Contains(err.Error(), tc.mention) {
				t.Errorf("refusal must mention %q, got: %v", tc.mention, err)
			}
		})
	}
}

// The range refusal fires through the invocation path, pre-dispatch and —
// with embedded content — pre-dial.
func TestConformance_SchemaRange_RefusedPreDispatch(t *testing.T) {
	proto := `syntax = "proto2";
package p2;
message Req { required string id = 1; }
message Resp { optional string name = 1; }
service S { rpc Do(Req) returns (Resp); }
`
	invoker := NewInvoker()
	defer invoker.Close()

	inv := invoker.InvokeBinding(testCtx(t), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{BindingSpec: BindingSpec, Location: "grpc://203.0.113.9:50051", Content: openbindings.TextContent(proto)},
		Ref:    "p2.S/Do",
	})
	_, terr := drainInvocation(t, inv)
	if terr == nil || terr.Code != openbindings.ErrCodeSourceLoadFailed {
		t.Fatalf("expected ERR_SOURCE_LOAD_FAILED, got %v", terr)
	}
	if terr.HasData() {
		t.Errorf("schema-loader diagnostics crossed as abstract data: %#v", terr.Data)
	}
}

// Files in a carried set outside every bound closure are inert carriage —
// never grounds for refusal (a proto2 file with required fields rides
// along; the bound method's clean closure still dispatches).
func TestConformance_SchemaRange_InertCarriageNotRefused(t *testing.T) {
	dialer, _ := setupTestServer(t)
	invoker := newTestInvoker(t, dialer)
	defer invoker.Close()

	dirty := &descriptorpb.FileDescriptorProto{
		Name:    ptr("corp/legacy.proto"),
		Package: ptr("corp"),
		Syntax:  ptr("proto2"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: ptr("Legacy"), Field: []*descriptorpb.FieldDescriptorProto{
				{Name: ptr("id"), Number: ptr(int32(1)), JsonName: ptr("id"),
					Type:  ptr(descriptorpb.FieldDescriptorProto_TYPE_STRING),
					Label: ptr(descriptorpb.FieldDescriptorProto_LABEL_REQUIRED)},
			}},
		},
	}

	ctx := testCtx(t)
	inv := invoker.InvokeBinding(ctx, &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{
			BindingSpec: BindingSpec,
			Location:    bufconnLocation,
			Content:     mustContent(descriptorSetJSON(t, durationFDP(), testItemsFDP(), dirty)),
		},
		Ref:     "testpkg.ItemService/GetItem",
		Context: map[string]any{"configuration": map[string]any{"transport": "plaintext"}},
	})
	if err := inv.Write(ctx, map[string]any{"id": "inert"}); err != nil {
		t.Fatal(err)
	}
	if _, err := openbindings.Single(ctx, inv.Outputs()); err != nil {
		t.Fatalf("a proto2 file outside the bound closure is inert carriage, never grounds for refusal (§3): %v", err)
	}
}

// ---------------------------------------------------------------------------
// GRPC-P-04 — interaction-kind coverage (§8)
// ---------------------------------------------------------------------------

// Bidirectional methods preserve the artifact's two independent stream
// directions: a response can arrive before caller input closes.
func TestConformance_P04_BidiFlowsWhileInputOpen(t *testing.T) {
	dialer, _ := setupTestServer(t)
	invoker := newTestInvoker(t, dialer)
	defer invoker.Close()

	ctx := testCtx(t)
	inv := invoker.InvokeBinding(ctx, bufconnArgs("testpkg.ItemService/Chat", nil))
	if err := inv.Write(ctx, map[string]any{"id": "one"}); err != nil {
		t.Fatal(err)
	}
	stream := inv.Outputs()
	value, err := stream.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if value.(map[string]any)["name"] != "chat-one" {
		t.Fatalf("response = %v", value)
	}
	if err := inv.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Read(ctx); err != io.EOF {
		t.Fatalf("terminal = %v, want clean EOF", err)
	}
}

// ---------------------------------------------------------------------------
// GRPC-P-05 / GRPC-P-06 — decode and classification (§9.2, §9.4)
// ---------------------------------------------------------------------------

// Output values are emitted as response messages arrive (never buffered to
// pre-confirm the final status), and values already emitted STAND when a
// non-OK final status later classifies the invocation as a failure.
func TestConformance_P05_P06_EmittedValuesStandOnLateFailure(t *testing.T) {
	dialer, _ := setupTestServer(t)
	invoker := newTestInvoker(t, dialer)
	defer invoker.Close()

	inv := invoker.InvokeBinding(testCtx(t), bufconnArgs("testpkg.ItemService/FailItems", nil))
	vals, terr := drainInvocation(t, inv)
	if len(vals) != 2 {
		t.Fatalf("the two messages emitted before the failure must stand, got %d", len(vals))
	}
	if terr == nil {
		t.Fatal("a non-OK final status must classify the invocation as a failure (GRPC-P-06)")
	}
	if terr.HasData() {
		t.Errorf("native gRPC status leaked as abstract data: %v", terr.Data)
	}
	if vals[1].(map[string]any)["name"] != "second" {
		t.Errorf("second emitted value = %v", vals[1])
	}
}

// ---------------------------------------------------------------------------
// GRPC-P-07 — credentials and metadata placement (§9.5)
// ---------------------------------------------------------------------------

// A key that violates the metadata name grammar or uses the reserved grpc-
// prefix is UNPLACEABLE: surfaced pre-dispatch, never normalized or
// case-folded into place, never silently skipped.
func TestConformance_P07_UnplaceableMetadataKeysSurfaced(t *testing.T) {
	dialer, _ := setupTestServer(t)
	invoker := newTestInvoker(t, dialer)
	defer invoker.Close()

	cases := []struct {
		key string
	}{
		{"X-Custom"},     // uppercase: would need folding into place
		{"grpc-timeout"}, // protocol-reserved prefix
		{"bad key"},      // space outside the grammar
	}
	for _, tc := range cases {
		inv := invoker.InvokeBinding(testCtx(t), bufconnArgs("testpkg.ItemService/GetItem",
			map[string]any{"headers": map[string]any{tc.key: "v"}}))
		_, terr := drainInvocation(t, inv)
		if terr == nil || terr.Code != openbindings.ErrCodeSourceConfigError {
			t.Fatalf("key %q: expected ERR_SOURCE_CONFIG_ERROR, got %v", tc.key, terr)
		}
		if terr.HasData() {
			t.Errorf("key %q: native metadata evidence crossed as abstract data: %#v", tc.key, terr.Data)
		}
	}
}

// Valid lowercase keys (the -bin convention included) place cleanly.
func TestConformance_P07_ValidMetadataKeysPlace(t *testing.T) {
	for _, key := range []string{"x-api-key", "x-trace-bin", "x_1.k-2"} {
		if err := checkMetadataKey(key); err != nil {
			t.Errorf("key %q must be placeable: %v", key, err)
		}
	}
}

// mustContent marshals a Go value into the raw-JSON content carriage
// (Source.Content presence semantics: raw JSON, nil = absent).
func mustContent(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
