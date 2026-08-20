package connect

// Conformance tests for openbindings.connect@1, keyed to the
// specification's rule identifiers (CONN-D-01..03, CONN-P-01..07). Where a
// rule incorporates openbindings.grpc@1 by exact-identifier citation
// (CONN-D-01's schema carriages and accepted range, CONN-P-02's
// input/decode correspondence), the pinned behavior matches
// formats/grpc's. Each test names the rule it pins; together they are
// this implementation's behavioral contract with the binding
// specification.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openbindings/openbindings-go/invoke"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/durationpb"

	openbindings "github.com/openbindings/openbindings-go"
)

// ---------------------------------------------------------------------------
// CONN-D-02 — base URL grammar (§4)
// ---------------------------------------------------------------------------

func TestConformance_D02_BaseURLGrammar(t *testing.T) {
	valid := []string{
		"https://api.example.com",
		"http://api.example.com",
		"https://api.example.com:8443",
		"https://example.com/api",         // path prefix, no trailing slash
		"http://127.0.0.1:20290/proxy/v2", // multi-segment prefix
		"https://[::1]:8443",              // bracketed IPv6 literal
	}
	for _, location := range valid {
		if err := validateBaseURL(location); err != nil {
			t.Errorf("validateBaseURL(%q) must accept: %v", location, err)
		}
	}

	invalid := []struct {
		location string
		mention  string
	}{
		{"https://example.com/", "trailing"},     // trailing slash: the path prefix has none
		{"https://example.com/api/", "trailing"}, // ... including on a prefix
		{"grpc://example.com:443", "http"},       // http/https only
		{"example.com", "http"},                  // not absolute
		{"https://example.com?x=1", "query"},     // query component
		{"https://example.com#frag", "fragment"}, // fragment component
		{"https://user@example.com", "userinfo"}, // userinfo component
		{"https://", "host"},                     // no host
	}
	for _, tc := range invalid {
		err := validateBaseURL(tc.location)
		if err == nil {
			t.Errorf("validateBaseURL(%q) must refuse (CONN-D-02)", tc.location)
			continue
		}
		if !strings.Contains(err.Error(), tc.mention) {
			t.Errorf("validateBaseURL(%q) refusal must mention %q, got: %v", tc.location, tc.mention, err)
		}
		if !strings.Contains(err.Error(), "CONN-D-02") {
			t.Errorf("validateBaseURL(%q) refusal must cite CONN-D-02, got: %v", tc.location, err)
		}
	}
}

// A non-conformant location refuses loudly at the invocation boundary,
// before any network I/O.
func TestConformance_D02_NonConformantLocationRefusedPreDispatch(t *testing.T) {
	ctx := testContext(t)
	for _, location := range []string{"https://api.example.com/", "https://api.example.com?x=1", "grpcs://api.example.com"} {
		inv := NewInvoker().InvokeBinding(ctx, unaryArgs(location, testProto, "testpkg.TestService/GetItem"))
		ierr := mustTerminalError(t, ctx, inv, invoke.ErrCodeSourceConfigError)
		if ierr.HasData() {
			t.Errorf("location %q: implementation evidence crossed as abstract data: %#v", location, ierr.Data)
		}
	}
}

// The request URL is the base URL STRING-CONCATENATED with
// /<fully-qualified-service>/<method> — concatenation, not RFC 3986
// resolution, so a path prefix is preserved (§4).
func TestConformance_D02_PathPrefixPreservedByConcatenation(t *testing.T) {
	ctx := testContext(t)
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"p","name":"x"}`)
	}))
	t.Cleanup(srv.Close)

	inv := invokeWith(t, ctx, NewInvoker(), unaryArgs(srv.URL+"/api/v2", testProto, "testpkg.TestService/GetItem"),
		map[string]any{"id": "p"})
	if _, err := invoke.Single[any](ctx, inv.Outputs()); err != nil {
		t.Fatalf("Single: %v", err)
	}
	if gotPath != "/api/v2/testpkg.TestService/GetItem" {
		t.Errorf("request path = %q, want /api/v2/testpkg.TestService/GetItem (CONN-D-02 §4 concatenation)", gotPath)
	}
}

// ---------------------------------------------------------------------------
// CONN-D-01 — content carriages (§3, §5; openbindings.grpc@1 GRPC-D-01
// incorporated)
// ---------------------------------------------------------------------------

// wktProto imports a google/protobuf/* file, exercising the string
// carriage's import pin and (compiled) a multi-file closure.
const wktProto = `syntax = "proto3";
package wkt;
import "google/protobuf/duration.proto";
message WaitRequest { google.protobuf.Duration timeout = 1; }
message WaitReply { string status = 1; }
service Clock { rpc Wait(WaitRequest) returns (WaitReply); }
`

// String content: single-file .proto source text whose imports are limited
// to google/protobuf/*, resolved from this processor's bundled copies.
func TestConformance_D01_ProtoText_GoogleProtobufImportsResolve(t *testing.T) {
	disc, err := discoverFromContent(context.Background(), openbindings.TextContent(wktProto))
	if err != nil {
		t.Fatalf("google/protobuf/* imports must resolve from bundled copies (openbindings.grpc@1 §3, via CONN-D-01): %v", err)
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
		t.Fatal("a non-google/protobuf import must refuse loudly at load (openbindings.grpc@1 §3, via CONN-D-01)")
	}
	for _, want := range []string{"google/protobuf/*", "corp/shared.proto"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal must mention %q, got: %v", want, err)
		}
	}
}

// protoFDP compiles proto source text and returns its FileDescriptorProto.
func protoFDP(t *testing.T, protoText string) *descriptorpb.FileDescriptorProto {
	t.Helper()
	disc, err := discoverFromContent(context.Background(), openbindings.TextContent(protoText))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if len(disc.services) == 0 {
		t.Fatal("no services in fixture proto")
	}
	return protodesc.ToFileDescriptorProto(disc.services[0].ParentFile())
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
// FileDescriptorProto, completing an importing schema's closure.
func durationFDP() *descriptorpb.FileDescriptorProto {
	return protodesc.ToFileDescriptorProto(durationpb.File_google_protobuf_duration_proto)
}

// Object content: a FileDescriptorSet in canonical JSON is a valid schema
// carrier, end to end through an invocation.
func TestConformance_D01_DescriptorSetJSON_InvokesEndToEnd(t *testing.T) {
	ctx := testContext(t)
	srv := fakeConnectServer(t, http.StatusOK, `{"id":"fds","name":"carried"}`)

	inv := invokeWith(t, ctx, NewInvoker(),
		unaryArgs(srv.URL, descriptorSetJSON(t, protoFDP(t, testProto)), "testpkg.TestService/GetItem"),
		map[string]any{"id": "fds"})
	v, err := invoke.Single[any](ctx, inv.Outputs())
	if err != nil {
		t.Fatalf("FileDescriptorSet-in-JSON content must be a valid schema carrier (CONN-D-01): %v", err)
	}
	if v.(map[string]any)["name"] != "carried" {
		t.Errorf("response = %v", v)
	}
}

func TestConformance_D01_DescriptorSetJSON_UnknownMemberRefused(t *testing.T) {
	content := descriptorSetJSON(t, protoFDP(t, testProto))
	content["sizzle"] = true
	_, err := discoverFromContent(context.Background(), mustContent(content))
	if err == nil {
		t.Fatal("unknown members in descriptor-set content must refuse loudly (openbindings.grpc@1 §3, via CONN-D-01)")
	}
	if !strings.Contains(err.Error(), "sizzle") {
		t.Errorf("refusal must name the unknown member, got: %v", err)
	}
}

func TestConformance_D01_DescriptorSetJSON_BracketKeyedExtensionRefused(t *testing.T) {
	content := descriptorSetJSON(t, protoFDP(t, testProto))
	// Splice a compiled custom option (the runtime bracket-key convention)
	// into the first file's options.
	file0 := content["file"].([]any)[0].(map[string]any)
	file0["options"] = map[string]any{"[corp.build_tag]": "v1"}
	_, err := discoverFromContent(context.Background(), mustContent(content))
	if err == nil {
		t.Fatal("bracket-keyed extension members must refuse loudly: a conformant pin carries option-stripped descriptors (openbindings.grpc@1 §3, via CONN-D-01)")
	}
	for _, want := range []string{"[corp.build_tag]", "option-stripped"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal must mention %q, got: %v", want, err)
		}
	}
}

func TestConformance_D01_DescriptorSetJSON_MissingDependencyRefused(t *testing.T) {
	// wktProto imports google/protobuf/duration.proto; a set carried
	// without it is not the compiled, self-contained closure the pin
	// requires.
	_, err := discoverFromContent(context.Background(), mustContent(descriptorSetJSON(t, protoFDP(t, wktProto))))
	if err == nil {
		t.Fatal("a descriptor set missing a dependency must refuse loudly (self-contained closure, openbindings.grpc@1 §3, via CONN-D-01)")
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
			t.Errorf("content %T must refuse (CONN-D-01: string or object)", content)
		}
	}
}

// ---------------------------------------------------------------------------
// CONN-D-03 — ref form and byte-exact matching (§7)
// ---------------------------------------------------------------------------

// Matching is byte-exact in schema mode, no case folding: a case-mismatched
// ref is unresolvable, checked offline against the embedded schema before
// any network I/O (unreachableServer fails the test if reached).
func TestConformance_D03_ByteExactMatching(t *testing.T) {
	ctx := testContext(t)
	srv := unreachableServer(t)

	for _, ref := range []string{"testpkg.TestService/getItem", "testpkg.testservice/GetItem", "TESTPKG.TestService/GetItem"} {
		inv := NewInvoker().InvokeBinding(ctx, unaryArgs(srv.URL, testProto, ref))
		outputs, terr := collectOutputs(t, ctx, inv)
		if len(outputs) != 0 || terr == nil || terr.Code != invoke.ErrCodeRefNotFound {
			t.Fatalf("ref %q: byte-exact matching must refuse with ERR_REF_NOT_FOUND (CONN-D-03), got %v", ref, terr)
		}
	}
}

// A packageless schema binds through the service's bare name (CONN-D-03:
// package-qualified or bare for packageless schemas).
func TestConformance_D03_PackagelessServiceRef(t *testing.T) {
	const packagelessProto = `syntax = "proto3";
service CoffeeShop { rpc GetMenu(MenuRequest) returns (Menu); }
message MenuRequest {}
message Menu { string items = 1; }
`
	ctx := testContext(t)
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"items":"espresso"}`)
	}))
	t.Cleanup(srv.Close)

	// MenuRequest has no fields: a bare handle (no write) dispatches.
	inv := invokeWith(t, ctx, NewInvoker(), unaryArgs(srv.URL, packagelessProto, "CoffeeShop/GetMenu"))
	v, err := invoke.Single[any](ctx, inv.Outputs())
	if err != nil {
		t.Fatalf("a packageless ref must resolve against the bare service name (CONN-D-03): %v", err)
	}
	if v.(map[string]any)["items"] != "espresso" {
		t.Errorf("response = %v", v)
	}
	if gotPath != "/CoffeeShop/GetMenu" {
		t.Errorf("request path = %q, want /CoffeeShop/GetMenu", gotPath)
	}
}

// ---------------------------------------------------------------------------
// CONN-P-01 — mode discrimination (§3)
// ---------------------------------------------------------------------------

// The mode is discriminated structurally by content presence: with content
// the schema's field semantics govern (an unknown input field is refused,
// CONN-P-02); without content there are no field semantics at all and the
// same value rides verbatim (CONN-P-03).
func TestConformance_P01_ModeDiscriminatedByContentPresence(t *testing.T) {
	ctx := testContext(t)
	input := map[string]any{"id": "1", "bogus": true}

	// Schema mode: refused pre-dispatch.
	schemaSrv := unreachableServer(t)
	inv := invokeWith(t, ctx, NewInvoker(), unaryArgs(schemaSrv.URL, testProto, "testpkg.TestService/GetItem"), input)
	mustTerminalError(t, ctx, inv, invoke.ErrCodeValidationFailed)

	// Descriptorless mode: the identical value rides verbatim.
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(srv.Close)

	inv2 := invokeWith(t, ctx, NewInvoker(), unaryArgs(srv.URL, nil, "testpkg.TestService/GetItem"), input)
	if _, err := invoke.Single[any](ctx, inv2.Outputs()); err != nil {
		t.Fatalf("descriptorless mode must dispatch the value verbatim (CONN-P-01/CONN-P-03): %v", err)
	}
	if !strings.Contains(gotBody, "bogus") {
		t.Errorf("descriptorless body must carry the value verbatim, got %q", gotBody)
	}
}

// ---------------------------------------------------------------------------
// CONN-P-02 — schema-mode input and decode (§9.2; GRPC-P-03/GRPC-P-05
// incorporated)
// ---------------------------------------------------------------------------

// Unknown input fields are refused loudly before dispatch — the canonical
// mapping's own default posture — never silently discarded.
func TestConformance_P02_UnknownInputFieldRefusedPreDispatch(t *testing.T) {
	ctx := testContext(t)
	srv := unreachableServer(t) // fails the test if any dispatch happens

	inv := invokeWith(t, ctx, NewInvoker(), unaryArgs(srv.URL, testProto, "testpkg.TestService/GetItem"),
		map[string]any{"id": "1", "bogus": true})
	outputs, terr := collectOutputs(t, ctx, inv)
	if len(outputs) != 0 || terr == nil {
		t.Fatalf("unknown input field must refuse pre-dispatch (CONN-P-02), got %d outputs, terr=%v", len(outputs), terr)
	}
	if terr.Code != invoke.ErrCodeValidationFailed {
		t.Errorf("code = %q, want ERR_VALIDATION_FAILED", terr.Code)
	}
	if terr.HasData() {
		t.Errorf("validator diagnostics crossed as abstract data: %#v", terr.Data)
	}
}

// The accepted input shape is the request type's CANONICAL JSON form: a
// google.protobuf.Duration-typed request is a JSON string, not an object.
func TestConformance_P02_WellKnownTypeCanonicalFormRequest(t *testing.T) {
	const durationReqProto = `syntax = "proto3";
package wkt;
import "google/protobuf/duration.proto";
message WaitReply { string status = 1; }
service Clock { rpc Wait(google.protobuf.Duration) returns (WaitReply); }
`
	ctx := testContext(t)
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"status":"waited"}`)
	}))
	t.Cleanup(srv.Close)

	inv := invokeWith(t, ctx, NewInvoker(), unaryArgs(srv.URL, durationReqProto, "wkt.Clock/Wait"), "3.5s")
	if _, err := invoke.Single[any](ctx, inv.Outputs()); err != nil {
		t.Fatalf("a Duration-typed request must accept its canonical JSON string form (CONN-P-02 via GRPC-P-03): %v", err)
	}
	var sent any
	if err := json.Unmarshal([]byte(gotBody), &sent); err != nil {
		t.Fatalf("body %q is not JSON: %v", gotBody, err)
	}
	if s, ok := sent.(string); !ok || !strings.HasSuffix(s, "s") {
		t.Errorf("request body = %q, want the Duration's canonical JSON string form", gotBody)
	}
}

// A 200 response whose body fails to unmarshal against the response
// descriptor is a failure outcome — loud, never a silently passed-through
// value.
func TestConformance_P02_UndecodableResponseIsLoudFailure(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"field type mismatch", `{"id": 42}`}, // number for a string field
		{"non-JSON body", `not json at all`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := testContext(t)
			srv := fakeConnectServer(t, http.StatusOK, tc.body)

			inv := invokeWith(t, ctx, NewInvoker(), unaryArgs(srv.URL, testProto, "testpkg.TestService/GetItem"),
				map[string]any{"id": "x"})
			ierr := mustTerminalError(t, ctx, inv, invoke.ErrCodeResponseError)
			if ierr.HasData() {
				t.Errorf("native decode evidence crossed as abstract data: %#v", ierr.Data)
			}
		})
	}
}

// Connect's JSON codec uses canonical ProtoJSON in both directions. Its
// default parser refuses unknown response members; binary protobuf's unknown
// field skipping does not authorize a different JSON posture.
func TestConformance_P02_UnknownResponseMemberRefused(t *testing.T) {
	ctx := testContext(t)
	srv := fakeConnectServer(t, http.StatusOK, `{"id":"a","name":"b","driftedField":{"x":1}}`)

	inv := invokeWith(t, ctx, NewInvoker(), unaryArgs(srv.URL, testProto, "testpkg.TestService/GetItem"),
		map[string]any{"id": "a"})
	ierr := mustTerminalError(t, ctx, inv, invoke.ErrCodeResponseError)
	if ierr.HasData() {
		t.Errorf("native response mismatch crossed as abstract data: %#v", ierr.Data)
	}
}

// ---------------------------------------------------------------------------
// Accepted schema range (openbindings.grpc@1 §3, via CONN-D-01)
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
		pkg     string
		proto   string
		mention string // empty = accepted
	}{
		{
			name: "proto3 always satisfies the range",
			pkg:  "ok3",
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
			pkg:  "oked",
			proto: `edition = "2023";
package oked;
message Req { string id = 1; }
message Resp { string name = 1; }
service S { rpc Do(Req) returns (Resp); }
`,
		},
		{
			name: "proto2 required presence refused",
			pkg:  "p2",
			proto: `syntax = "proto2";
package p2;
message Req { required string id = 1; }
message Resp { optional string name = 1; }
service S { rpc Do(Req) returns (Resp); }
`,
			mention: "required presence",
		},
		{
			name: "proto2 group refused",
			pkg:  "p2g",
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
			pkg:  "eddel",
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
			pkg:  "edjf",
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
			pkg:  "p2n",
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
			method := methodFromContent(t, tc.proto, tc.pkg+".S", "Do")
			err := validateBoundClosure(method)
			if tc.mention == "" {
				if err != nil {
					t.Fatalf("must satisfy the accepted range (openbindings.grpc@1 §3, via CONN-D-01): %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("must refuse (%s)", tc.mention)
			}
			if !strings.Contains(err.Error(), tc.mention) {
				t.Errorf("refusal must mention %q, got: %v", tc.mention, err)
			}
		})
	}
}

// The range refusal fires through the invocation path, pre-dispatch and
// pre-network.
func TestConformance_SchemaRange_RefusedPreDispatch(t *testing.T) {
	proto := `syntax = "proto2";
package p2;
message Req { required string id = 1; }
message Resp { optional string name = 1; }
service S { rpc Do(Req) returns (Resp); }
`
	ctx := testContext(t)
	srv := unreachableServer(t)

	inv := NewInvoker().InvokeBinding(ctx, unaryArgs(srv.URL, proto, "p2.S/Do"))
	ierr := mustTerminalError(t, ctx, inv, invoke.ErrCodeSourceLoadFailed)
	if ierr.HasData() {
		t.Errorf("schema-loader diagnostics crossed as abstract data: %#v", ierr.Data)
	}
}

// Files in a carried set outside every bound closure are inert carriage —
// never grounds for refusal (a proto2 file with required fields rides
// along; the bound method's clean closure still dispatches).
func TestConformance_SchemaRange_InertCarriageNotRefused(t *testing.T) {
	ctx := testContext(t)
	srv := fakeConnectServer(t, http.StatusOK, `{"id":"inert","name":"ok"}`)

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

	inv := invokeWith(t, ctx, NewInvoker(),
		unaryArgs(srv.URL, descriptorSetJSON(t, protoFDP(t, testProto), dirty), "testpkg.TestService/GetItem"),
		map[string]any{"id": "inert"})
	if _, err := invoke.Single[any](ctx, inv.Outputs()); err != nil {
		t.Fatalf("a proto2 file outside the bound closure is inert carriage, never grounds for refusal (openbindings.grpc@1 §3, via CONN-D-01): %v", err)
	}
}

// ---------------------------------------------------------------------------
// CONN-P-03 — descriptorless mode input and decode (§9.3)
// ---------------------------------------------------------------------------

func TestConformance_P03_DescriptorlessVerbatimValues(t *testing.T) {
	ctx := testContext(t)
	var gotBody string
	respBody := `{"ok":true}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, respBody)
	}))
	t.Cleanup(srv.Close)
	args := func() *invoke.BindingInvocationArgs {
		return unaryArgs(srv.URL, nil, "testpkg.TestService/GetItem")
	}

	// ANY JSON value rides verbatim: an array input, an array output.
	respBody = `[{"x":1},2]`
	inv := invokeWith(t, ctx, NewInvoker(), args(), []any{1.0, 2.0, 3.0})
	v, err := invoke.Single[any](ctx, inv.Outputs())
	if err != nil {
		t.Fatalf("descriptorless values are verbatim JSON (CONN-P-03): %v", err)
	}
	if gotBody != `[1,2,3]` {
		t.Errorf("request body = %q, want [1,2,3]", gotBody)
	}
	if arr, ok := v.([]any); !ok || len(arr) != 2 {
		t.Errorf("output = %#v, want the verbatim parsed array", v)
	}

	// An ABSENT input cannot acquire a request shape in descriptorless mode.
	respBody = `{"ok":true}`
	inv = invokeWith(t, ctx, NewInvoker(), args())
	ierr := mustTerminalError(t, ctx, inv, invoke.ErrCodeValidationFailed)
	if ierr.HasData() {
		t.Errorf("validation diagnostics crossed as abstract data: %#v", ierr.Data)
	}

	// An explicit JSON null is a VALUE, not absence, and rides as `null`.
	inv = invokeWith(t, ctx, NewInvoker(), args(), nil)
	if _, err := invoke.Single[any](ctx, inv.Outputs()); err != nil {
		t.Fatalf("null input: %v", err)
	}
	if gotBody != `null` {
		t.Errorf("null input body = %q, want null (CONN-P-03: any JSON value, verbatim)", gotBody)
	}

	// An empty response body carries no JSON value and is a protocol error.
	respBody = ``
	inv = invokeWith(t, ctx, NewInvoker(), args(), map[string]any{"q": 1})
	outputs, terr := collectOutputs(t, ctx, inv)
	if len(outputs) != 0 || terr == nil || terr.Code != invoke.ErrCodeResponseError {
		t.Fatalf("empty body must fail without invented output (CONN-P-03): outputs=%v error=%v", outputs, terr)
	}
}

// A 200 response whose body fails to parse as JSON is a loud
// protocol-error failure outcome, never a string.
func TestConformance_P03_ParseFailureIsLoudNeverAString(t *testing.T) {
	ctx := testContext(t)
	srv := fakeConnectServer(t, http.StatusOK, `plain text, not JSON`)

	inv := invokeWith(t, ctx, NewInvoker(), unaryArgs(srv.URL, nil, "testpkg.TestService/GetItem"),
		map[string]any{"id": "x"})
	ierr := mustTerminalError(t, ctx, inv, invoke.ErrCodeResponseError)
	if ierr.HasData() {
		t.Errorf("native parse evidence crossed as abstract data: %#v", ierr.Data)
	}
}

// A 200 response whose content type is not application/json is a loud
// protocol-error failure outcome.
func TestConformance_P03_NonJSONContentTypeIsLoud(t *testing.T) {
	ctx := testContext(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"looks":"like json"}`)
	}))
	t.Cleanup(srv.Close)

	inv := invokeWith(t, ctx, NewInvoker(), unaryArgs(srv.URL, nil, "testpkg.TestService/GetItem"),
		map[string]any{"id": "x"})
	ierr := mustTerminalError(t, ctx, inv, invoke.ErrCodeResponseError)
	if ierr.HasData() {
		t.Errorf("native media evidence crossed as abstract data: %#v", ierr.Data)
	}
}

// ---------------------------------------------------------------------------
// CONN-P-04 — interaction-kind coverage (§8)
// ---------------------------------------------------------------------------

// Bidirectional methods are refused BEFORE dispatch as this
// implementation's declared limitation (client-streaming is pinned by
// TestInvokeBinding_ClientStreamingRefused).
func TestConformance_P04_BidiRefusedPreDispatchAsDeclaredLimitation(t *testing.T) {
	const bidiProto = `syntax = "proto3";
package testpkg;
message ChatMsg { string text = 1; }
service ChatService { rpc Chat(stream ChatMsg) returns (stream ChatMsg); }
`
	ctx := testContext(t)
	srv := unreachableServer(t)

	inv := NewInvoker().WithFullDuplexTransport(false).InvokeBinding(ctx, unaryArgs(srv.URL, bidiProto, "testpkg.ChatService/Chat"))
	outputs, terr := collectOutputs(t, ctx, inv)
	if len(outputs) != 0 || terr == nil {
		t.Fatal("bidi must refuse pre-dispatch (CONN-P-04)")
	}
	if terr.Code != invoke.ErrCodeExecutionFailed || terr.HasData() {
		t.Errorf("bidi refusal = %#v, want code-only ERR_EXECUTION_FAILED", terr)
	}
}

// ---------------------------------------------------------------------------
// CONN-P-05 — framing: POST-only dispatch, protocol version header,
// compression advertised only as implemented (§2, §8)
// ---------------------------------------------------------------------------

// Every dispatch under this identifier is a POST carrying
// Connect-Protocol-Version: 1 — the GET dispatch lane is EXCLUDED from
// revision 1 by definition (§2), across all three dispatch lanes: schema
// unary, descriptorless unary, and streaming.
func TestConformance_P05_EveryDispatchIsPOSTWithProtocolVersion(t *testing.T) {
	ctx := testContext(t)
	type seen struct{ method, version string }
	var requests []seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, seen{r.Method, r.Header.Get("Connect-Protocol-Version")})
		_, _ = io.ReadAll(r.Body)
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/connect+json") {
			w.Header().Set("Content-Type", "application/connect+json")
			w.WriteHeader(http.StatusOK)
			_ = writeConnectEnvelope(w, 0, []byte(`{"message":"m","timestamp":"1"}`))
			_ = writeConnectEnvelope(w, connectFlagEndStream, []byte(`{}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"a","name":"b"}`)
	}))
	t.Cleanup(srv.Close)

	// Schema-mode unary.
	inv := invokeWith(t, ctx, NewInvoker(), unaryArgs(srv.URL, testProto, "testpkg.TestService/GetItem"),
		map[string]any{"id": "a"})
	if _, err := invoke.Single[any](ctx, inv.Outputs()); err != nil {
		t.Fatalf("schema unary: %v", err)
	}
	// Descriptorless unary.
	inv = invokeWith(t, ctx, NewInvoker(), unaryArgs(srv.URL, nil, "testpkg.TestService/GetItem"),
		map[string]any{"id": "a"})
	if _, err := invoke.Single[any](ctx, inv.Outputs()); err != nil {
		t.Fatalf("descriptorless unary: %v", err)
	}
	// Server-streaming.
	inv = invokeWith(t, ctx, NewInvoker(), streamingArgs(srv.URL), map[string]any{"source": "s"})
	if outputs, terr := collectOutputs(t, ctx, inv); terr != nil || len(outputs) != 1 {
		t.Fatalf("streaming: outputs=%d terr=%v", len(outputs), terr)
	}

	if len(requests) != 3 {
		t.Fatalf("expected 3 dispatches, saw %d", len(requests))
	}
	for i, req := range requests {
		if req.method != http.MethodPost {
			t.Errorf("dispatch %d used %s; every dispatch is a POST — the GET lane is excluded (§2, CONN-P-05)", i, req.method)
		}
		if req.version != "1" {
			t.Errorf("dispatch %d Connect-Protocol-Version = %q, want 1 on EVERY request (CONN-P-05)", i, req.version)
		}
	}
}

// A processor advertises only the encodings it implements — identity
// alone — and sends identity-encoded frames.
func TestConformance_P05_StreamingAdvertisesOnlyImplementedEncodings(t *testing.T) {
	ctx := testContext(t)
	var gotAcceptEncoding, gotContentEncoding string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAcceptEncoding = r.Header.Get("Connect-Accept-Encoding")
		gotContentEncoding = r.Header.Get("Connect-Content-Encoding")
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/connect+json")
		w.WriteHeader(http.StatusOK)
		_ = writeConnectEnvelope(w, connectFlagEndStream, []byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	inv := invokeWith(t, ctx, NewInvoker(), streamingArgs(srv.URL), map[string]any{"source": "s"})
	if _, terr := collectOutputs(t, ctx, inv); terr != nil {
		t.Fatalf("unexpected terminal error: %v", terr)
	}
	if gotAcceptEncoding != "identity" {
		t.Errorf("Connect-Accept-Encoding = %q, want identity: a processor advertises only the encodings it implements (CONN-P-05)", gotAcceptEncoding)
	}
	if gotContentEncoding != "" {
		t.Errorf("Connect-Content-Encoding = %q, want absent (identity frames)", gotContentEncoding)
	}
}

// The protocol closes every stream with an END_STREAM envelope; a stream
// that ends without one cannot be classified a success. Emitted values
// stand.
func TestConformance_P05_MissingEndStreamIsLoudFailure(t *testing.T) {
	ctx := testContext(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/connect+json")
		w.WriteHeader(http.StatusOK)
		_ = writeConnectEnvelope(w, 0, []byte(`{"message":"one","timestamp":"1"}`))
		_ = writeConnectEnvelope(w, 0, []byte(`{"message":"two","timestamp":"2"}`))
		// No END_STREAM: the handler returns and the body closes.
	}))
	t.Cleanup(srv.Close)

	inv := invokeWith(t, ctx, NewInvoker(), streamingArgs(srv.URL), map[string]any{"source": "s"})
	outputs, terr := collectOutputs(t, ctx, inv)
	if len(outputs) != 2 {
		t.Fatalf("the two values emitted before the truncation must stand, got %d", len(outputs))
	}
	if terr == nil || terr.Code != invoke.ErrCodeStreamError {
		t.Fatalf("a stream ending without END_STREAM must fail loudly (CONN-P-05/CONN-P-06), got %v", terr)
	}
	if terr.HasData() {
		t.Errorf("native framing evidence crossed as abstract data: %#v", terr.Data)
	}
}

// ---------------------------------------------------------------------------
// CONN-P-06 — classification (§9.5)
// ---------------------------------------------------------------------------

// A unary invocation succeeds IFF the final status is 200 — Connect's own
// rule, not a 2xx heuristic: a 202 or 204 is a failure outcome.
func TestConformance_P06_UnarySuccessIff200(t *testing.T) {
	for _, status := range []int{202, 204, 226} {
		ctx := testContext(t)
		body := `{"id":"a","name":"b"}`
		if status == 204 {
			body = "" // 204 carries no body
		}
		srv := fakeConnectServer(t, status, body)

		inv := invokeWith(t, ctx, NewInvoker(), unaryArgs(srv.URL, testProto, "testpkg.TestService/GetItem"),
			map[string]any{"id": "a"})
		outputs, terr := collectOutputs(t, ctx, inv)
		if len(outputs) != 0 || terr == nil {
			t.Fatalf("status %d: unary success is 200 and only 200 (CONN-P-06), got %d outputs, terr=%v", status, len(outputs), terr)
		}
	}
}

// A streaming invocation succeeds only if the stream rode HTTP 200, even
// when the response is otherwise well-framed.
func TestConformance_P06_StreamingSuccessRequires200(t *testing.T) {
	ctx := testContext(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/connect+json")
		w.WriteHeader(299) // 2xx but not 200
		_ = writeConnectEnvelope(w, 0, []byte(`{"message":"m","timestamp":"1"}`))
		_ = writeConnectEnvelope(w, connectFlagEndStream, []byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	inv := invokeWith(t, ctx, NewInvoker(), streamingArgs(srv.URL), map[string]any{"source": "s"})
	outputs, terr := collectOutputs(t, ctx, inv)
	if len(outputs) != 0 || terr == nil {
		t.Fatalf("streaming success requires riding 200 (CONN-P-06), got %d outputs, terr=%v", len(outputs), terr)
	}
}

// Output values already emitted STAND; an END_STREAM error makes the
// invocation a failure without retracting them.
func TestConformance_P06_EmittedValuesStandOnEndStreamError(t *testing.T) {
	ctx := testContext(t)
	srv := fakeConnectStreamingServer(t,
		[]string{`{"message":"kept","timestamp":"1"}`},
		`{"error":{"code":"resource_exhausted","message":"late failure"}}`)

	inv := invokeWith(t, ctx, NewInvoker(), streamingArgs(srv.URL), map[string]any{"source": "s"})
	outputs, terr := collectOutputs(t, ctx, inv)
	if len(outputs) != 1 {
		t.Fatalf("the value emitted before the END_STREAM error must stand (CONN-P-06), got %d", len(outputs))
	}
	if outputs[0].(map[string]any)["message"] != "kept" {
		t.Errorf("emitted value = %v", outputs[0])
	}
	if terr == nil || terr.Code != invoke.ErrCodeExecutionFailed {
		t.Fatalf("the END_STREAM error must classify the invocation as a failure, got %v", terr)
	}
	if terr.HasData() {
		t.Fatalf("native END_STREAM error evidence crossed as abstract data: %#v", terr.Data)
	}
}

// ---------------------------------------------------------------------------
// CONN-P-07 — credentials ride HTTP headers (§9.6)
// ---------------------------------------------------------------------------

func TestConformance_P07_CredentialsRideRequestHeaders(t *testing.T) {
	ctx := testContext(t)
	var gotAuth, gotTenant, gotCookie string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotTenant = r.Header.Get("X-Tenant")
		gotCookie = r.Header.Get("Cookie")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"a","name":"b"}`)
	}))
	t.Cleanup(srv.Close)

	args := unaryArgs(srv.URL, testProto, "testpkg.TestService/GetItem")
	args.Context = map[string]any{
		"bearerToken": "tok-1",
		"headers":     map[string]any{"X-Tenant": "acme"},
		"cookies":     map[string]any{"session": "s1"},
	}
	inv := invokeWith(t, ctx, NewInvoker(), args, map[string]any{"id": "a"})
	if _, err := invoke.Single[any](ctx, inv.Outputs()); err != nil {
		t.Fatalf("Single: %v", err)
	}
	if gotAuth != "Bearer tok-1" {
		t.Errorf("Authorization = %q, want Bearer tok-1 (CONN-P-07: a bearer token rides Authorization: Bearer)", gotAuth)
	}
	if gotTenant != "acme" {
		t.Errorf("X-Tenant = %q, want acme (CONN-P-07: any credential naming a header rides that header)", gotTenant)
	}
	if gotCookie != "session=s1" {
		t.Errorf("Cookie = %q, want session=s1 (CONN-P-07: Cookie included)", gotCookie)
	}
}

// An API key rides the header the credential NAMES (§9.6): connect defines
// no standard header for one, unlike grpc@1 §9.5's fixed authorization
// metadata key, so the consumer names it via context.headers. It therefore
// co-exists with a bearer token (each on its own header) rather than being
// excluded by it — the deliberate connect divergence from the grpc family.
func TestConformance_P07_APIKeyRidesConsumerNamedHeaderCoexistsWithBearer(t *testing.T) {
	ctx := testContext(t)
	var gotAuth, gotAPIKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAPIKey = r.Header.Get("X-API-Key")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"a","name":"b"}`)
	}))
	t.Cleanup(srv.Close)

	args := unaryArgs(srv.URL, testProto, "testpkg.TestService/GetItem")
	args.Context = map[string]any{
		"bearerToken": "tok-1",
		"headers":     map[string]any{"X-API-Key": "k-9"},
	}
	inv := invokeWith(t, ctx, NewInvoker(), args, map[string]any{"id": "a"})
	if _, err := invoke.Single[any](ctx, inv.Outputs()); err != nil {
		t.Fatalf("Single: %v", err)
	}
	if gotAuth != "Bearer tok-1" {
		t.Errorf("Authorization = %q, want Bearer tok-1 (bearer and a consumer-named apiKey co-exist, §9.6)", gotAuth)
	}
	if gotAPIKey != "k-9" {
		t.Errorf("X-API-Key = %q, want k-9 (an API key rides the header the consumer names, §9.6)", gotAPIKey)
	}
}

// A well-known apiKey field with no consumer-named header cannot be expressed
// as a request header under connect (§9.6): it is SURFACED to the consumer
// pre-dispatch, never placed on an invented Authorization: ApiKey (grpc@1's
// rule, which connect@1 §9.6 declines) and never silently skipped.
func TestConformance_P07_BareAPIKeyFieldSurfacedLoudly(t *testing.T) {
	ctx := testContext(t)
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"a","name":"b"}`)
	}))
	t.Cleanup(srv.Close)

	args := unaryArgs(srv.URL, testProto, "testpkg.TestService/GetItem")
	args.Context = map[string]any{"apiKey": "k-secret"}
	inv := NewInvoker().InvokeBinding(ctx, args)
	ierr := mustTerminalError(t, ctx, inv, invoke.ErrCodeContextRequired)
	if hit {
		t.Error("server was contacted; an inexpressible apiKey must be surfaced BEFORE dispatch (§9.6)")
	}
	details := invoke.ContextRequiredFrom(ierr)
	if details == nil || len(details.Alternatives) == 0 {
		t.Fatalf("inexpressible credential must produce a structural context challenge: %#v", ierr.Data)
	}
	encoded, _ := json.Marshal(ierr)
	if strings.Contains(string(encoded), "k-secret") {
		t.Errorf("abstract error leaked the apiKey value: %s", encoded)
	}
}

// A bearer token present alongside a bare apiKey field must not silently drop
// the apiKey (the grpc-transcribed else-if did): the inexpressible apiKey is
// surfaced pre-dispatch, §9.6.
func TestConformance_P07_BearerDoesNotSilentlyDropAPIKeyField(t *testing.T) {
	ctx := testContext(t)
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"a","name":"b"}`)
	}))
	t.Cleanup(srv.Close)

	args := unaryArgs(srv.URL, testProto, "testpkg.TestService/GetItem")
	args.Context = map[string]any{"bearerToken": "tok-1", "apiKey": "k-secret"}
	inv := NewInvoker().InvokeBinding(ctx, args)
	_ = mustTerminalError(t, ctx, inv, invoke.ErrCodeContextRequired)
	if hit {
		t.Error("server was contacted; a bearer must not let a bare apiKey be silently dropped — the apiKey is surfaced pre-dispatch (§9.6)")
	}
}

// A non-200 streaming response's Connect error body is read whole so
// applyConnectError retains the native Connect error diagnostically even when
// the body is exactly the response cap: streaming.go reads
// maxResponseBytes+1 (matching the unary path), so a cap-sized error payload
// is not truncated by one byte into invalid JSON.
func TestConformance_P06_StreamingErrorBodyReadWholeAtCapBoundary(t *testing.T) {
	ctx := testContext(t)
	// A Connect error body of exactly maxResponseBytes+1 (one byte over the
	// old cap): valid JSON only when the final '}' is read. The pre-fix read,
	// LimitReader(maxResponseBytes), drops that byte and the JSON is invalid;
	// the +1 read reads it whole.
	const prefix = `{"code":"unauthenticated","message":"`
	const suffix = `"}`
	padLen := int(maxResponseBytes) + 1 - len(prefix) - len(suffix) // total == maxResponseBytes+1
	body := prefix + strings.Repeat("x", padLen) + suffix

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The Connect body code stays native diagnostic evidence; it never
		// selects a portable OpenBindings error code.
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	args := streamingArgs(srv.URL)
	inv := invokeWith(t, ctx, NewInvoker(), args, map[string]any{"source": "s"})
	ierr := mustTerminalError(t, ctx, inv, invoke.ErrCodeExecutionFailed)
	if ierr.HasData() {
		t.Fatalf("Connect-native error evidence leaked as abstract data: %+v", ierr.Data)
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
