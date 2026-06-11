package grpc

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/jhump/protoreflect/v2/grpcreflect"
	openbindings "github.com/openbindings/openbindings-go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/reflection"
	v1reflectiongrpc "google.golang.org/grpc/reflection/grpc_reflection_v1"
	v1alphareflectiongrpc "google.golang.org/grpc/reflection/grpc_reflection_v1alpha"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// testServer records the metadata it received and can demand authentication.
type testServer struct {
	mu          sync.Mutex
	lastAuth    string
	lastMD      metadata.MD
	requireAuth bool
}

func (ts *testServer) capture(ctx context.Context) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return
	}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.lastMD = md.Copy()
	if vals := md.Get("authorization"); len(vals) > 0 {
		ts.lastAuth = vals[0]
	}
}

func (ts *testServer) auth() string {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.lastAuth
}

func (ts *testServer) header(key string) []string {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.lastMD.Get(key)
}

func (ts *testServer) setRequireAuth(v bool) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.requireAuth = v
}

func (ts *testServer) authRejected(ctx context.Context) bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if !ts.requireAuth {
		return false
	}
	md, ok := metadata.FromIncomingContext(ctx)
	return !ok || len(md.Get("authorization")) == 0
}

func setupTestServer(t *testing.T) (func(context.Context, string) (net.Conn, error), *testServer) {
	t.Helper()

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    ptr("testpkg/items.proto"),
		Package: ptr("testpkg"),
		Syntax:  ptr("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: ptr("GetItemRequest"), Field: []*descriptorpb.FieldDescriptorProto{
				{Name: ptr("id"), Number: ptr(int32(1)), JsonName: ptr("id"),
					Type: ptr(descriptorpb.FieldDescriptorProto_TYPE_STRING), Label: ptr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL)},
			}},
			{Name: ptr("GetItemResponse"), Field: []*descriptorpb.FieldDescriptorProto{
				{Name: ptr("id"), Number: ptr(int32(1)), JsonName: ptr("id"),
					Type: ptr(descriptorpb.FieldDescriptorProto_TYPE_STRING), Label: ptr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL)},
				{Name: ptr("name"), Number: ptr(int32(2)), JsonName: ptr("name"),
					Type: ptr(descriptorpb.FieldDescriptorProto_TYPE_STRING), Label: ptr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL)},
			}},
			{Name: ptr("ListItemsRequest")},
			{Name: ptr("Item"), Field: []*descriptorpb.FieldDescriptorProto{
				{Name: ptr("id"), Number: ptr(int32(1)), JsonName: ptr("id"),
					Type: ptr(descriptorpb.FieldDescriptorProto_TYPE_STRING), Label: ptr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL)},
				{Name: ptr("name"), Number: ptr(int32(2)), JsonName: ptr("name"),
					Type: ptr(descriptorpb.FieldDescriptorProto_TYPE_STRING), Label: ptr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL)},
			}},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: ptr("ItemService"),
			Method: []*descriptorpb.MethodDescriptorProto{
				{Name: ptr("GetItem"), InputType: ptr(".testpkg.GetItemRequest"), OutputType: ptr(".testpkg.GetItemResponse")},
				{Name: ptr("ListItems"), InputType: ptr(".testpkg.ListItemsRequest"), OutputType: ptr(".testpkg.Item"), ServerStreaming: ptr(true)},
				{Name: ptr("WatchItems"), InputType: ptr(".testpkg.ListItemsRequest"), OutputType: ptr(".testpkg.Item"), ServerStreaming: ptr(true)},
				{Name: ptr("UploadItems"), InputType: ptr(".testpkg.GetItemRequest"), OutputType: ptr(".testpkg.GetItemResponse"), ClientStreaming: ptr(true)},
			},
		}},
	}

	fd, err := protodesc.NewFile(fdp, nil)
	if err != nil {
		t.Fatal(err)
	}
	files := new(protoregistry.Files)
	if err := files.RegisterFile(fd); err != nil {
		t.Fatal(err)
	}

	// Look up message descriptors for building dynamic responses.
	reqDesc := fd.Messages().ByName("GetItemRequest")
	respDesc := fd.Messages().ByName("GetItemResponse")
	itemDesc := fd.Messages().ByName("Item")

	ts := &testServer{}
	svr := grpc.NewServer()

	svr.RegisterService(&grpc.ServiceDesc{
		ServiceName: "testpkg.ItemService",
		HandlerType: (*any)(nil),
		Methods: []grpc.MethodDesc{{
			MethodName: "GetItem",
			Handler: func(srv any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
				ts.capture(ctx)
				if ts.authRejected(ctx) {
					return nil, status.Error(codes.Unauthenticated, "authentication required")
				}
				req := dynamicpb.NewMessage(reqDesc)
				if err := dec(req); err != nil {
					return nil, err
				}
				_ = grpc.SetHeader(ctx, metadata.Pairs("x-test-header", "from-server"))
				grpc.SetTrailer(ctx, metadata.Pairs("x-test-trailer", "bye"))
				id := req.Get(reqDesc.Fields().ByName("id")).String()
				resp := dynamicpb.NewMessage(respDesc)
				resp.Set(respDesc.Fields().ByName("id"), protoreflect.ValueOfString(id))
				resp.Set(respDesc.Fields().ByName("name"), protoreflect.ValueOfString("item-"+id))
				return resp, nil
			},
		}},
		Streams: []grpc.StreamDesc{
			{
				StreamName:    "ListItems",
				ServerStreams: true,
				Handler: func(srv any, stream grpc.ServerStream) error {
					ts.capture(stream.Context())
					_ = stream.SetHeader(metadata.Pairs("x-stream-header", "sh"))
					stream.SetTrailer(metadata.Pairs("x-stream-trailer", "st"))
					for _, pair := range [][2]string{{"1", "first"}, {"2", "second"}} {
						msg := dynamicpb.NewMessage(itemDesc)
						msg.Set(itemDesc.Fields().ByName("id"), protoreflect.ValueOfString(pair[0]))
						msg.Set(itemDesc.Fields().ByName("name"), protoreflect.ValueOfString(pair[1]))
						if err := stream.SendMsg(msg); err != nil {
							return err
						}
					}
					return nil
				},
			},
			{
				StreamName:    "WatchItems",
				ServerStreams: true,
				Handler: func(srv any, stream grpc.ServerStream) error {
					ts.capture(stream.Context())
					msg := dynamicpb.NewMessage(itemDesc)
					msg.Set(itemDesc.Fields().ByName("id"), protoreflect.ValueOfString("w1"))
					msg.Set(itemDesc.Fields().ByName("name"), protoreflect.ValueOfString("watching"))
					if err := stream.SendMsg(msg); err != nil {
						return err
					}
					// Block until the client tears the stream down (cancel).
					<-stream.Context().Done()
					return stream.Context().Err()
				},
			},
		},
		Metadata: "testpkg/items.proto",
	}, ts)

	reflOpts := reflection.ServerOptions{Services: svr, DescriptorResolver: files}
	v1reflectiongrpc.RegisterServerReflectionServer(svr, reflection.NewServerV1(reflOpts))
	v1alphareflectiongrpc.RegisterServerReflectionServer(svr, reflection.NewServer(reflOpts))

	lis := bufconn.Listen(1024 * 1024)
	go func() { _ = svr.Serve(lis) }()
	t.Cleanup(func() { svr.Stop() })

	return func(ctx context.Context, _ string) (net.Conn, error) {
		return lis.DialContext(ctx)
	}, ts
}

func dialTestServer(t *testing.T, dialer func(context.Context, string) (net.Conn, error)) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// newTestInvoker returns an invoker whose "bufconn" address is wired to the
// test server's in-memory listener.
func newTestInvoker(t *testing.T, dialer func(context.Context, string) (net.Conn, error)) *Invoker {
	t.Helper()
	conn := dialTestServer(t, dialer)
	invoker := NewInvoker()
	invoker.conns.Store("bufconn", conn)
	return invoker
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// drainInvocation reads the output stream to completion, returning the
// outputs and the terminal error (nil on clean io.EOF close).
func drainInvocation(t *testing.T, inv openbindings.Invocation[any, any]) ([]any, *openbindings.InvocationError) {
	t.Helper()
	ctx := testCtx(t)
	out := inv.Outputs()
	var vals []any
	for {
		v, err := out.Read(ctx)
		if err == io.EOF {
			return vals, nil
		}
		if err != nil {
			var ie *openbindings.InvocationError
			if errors.As(err, &ie) {
				return vals, ie
			}
			t.Fatalf("unexpected error type %T: %v", err, err)
		}
		vals = append(vals, v)
	}
}

func bufconnArgs(ref string, bindCtx map[string]any) *openbindings.BindingInvocationArgs {
	return &openbindings.BindingInvocationArgs{
		Source:  openbindings.InvocationSource{Format: FormatToken, Location: "bufconn"},
		Ref:     ref,
		Context: bindCtx,
	}
}

// --- Integration Tests ---

// TestIntegration_CreateInterface_FromReflection exercises the live-server
// reflection path by manually building a discovery from a reflection client.
// It covers the internal convertToInterface code path against a real reflection
// stream. The public Creator.CreateInterface API is exercised separately by
// TestIntegration_CreateInterface_PublicAPI below.
func TestIntegration_CreateInterface_FromReflection(t *testing.T) {
	dialer, _ := setupTestServer(t)
	conn := dialTestServer(t, dialer)
	ctx := context.Background()

	refClient := grpcreflect.NewClientAuto(ctx, conn)
	defer refClient.Reset()

	serviceNames, err := refClient.ListServices()
	if err != nil {
		t.Fatal(err)
	}

	disc := &discovery{address: "bufconn"}
	for _, name := range serviceNames {
		if isInfraService(string(name)) {
			continue
		}
		svcDesc, err := resolveService(refClient, name)
		if err != nil {
			t.Fatal(err)
		}
		disc.services = append(disc.services, svcDesc)
	}

	iface, err := convertToInterface(disc, "bufconn", nil)
	if err != nil {
		t.Fatal(err)
	}

	if iface.Name != "ItemService" {
		t.Errorf("name = %q, want %q", iface.Name, "ItemService")
	}
	// UploadItems is client-streaming and must be skipped by the creator.
	if len(iface.Operations) != 3 {
		t.Fatalf("expected 3 operations, got %d", len(iface.Operations))
	}
	if _, ok := iface.Operations["GetItem"]; !ok {
		t.Error("expected operation GetItem")
	}
	if _, ok := iface.Operations["ListItems"]; !ok {
		t.Error("expected operation ListItems")
	}
	if _, ok := iface.Operations["WatchItems"]; !ok {
		t.Error("expected operation WatchItems")
	}
	if iface.Sources[DefaultSourceName].Format != FormatToken {
		t.Errorf("format = %q, want %q", iface.Sources[DefaultSourceName].Format, FormatToken)
	}
}

// TestIntegration_CreateInterface_PublicAPI exercises the Creator.CreateInterface
// public method end-to-end via the inline proto content path. This is the API
// that consumers actually call.
func TestIntegration_CreateInterface_PublicAPI(t *testing.T) {
	const proto = `
syntax = "proto3";
package testpkg;

message GetItemRequest { string id = 1; }
message GetItemResponse { string id = 1; string name = 2; }
message ListItemsRequest {}
message Item { string id = 1; string name = 2; }
message ListItemsResponse { repeated Item items = 1; }

service ItemService {
  rpc GetItem(GetItemRequest) returns (GetItemResponse);
  rpc ListItems(ListItemsRequest) returns (ListItemsResponse);
}
`

	creator := NewCreator()
	iface, err := creator.CreateInterface(context.Background(), &openbindings.CreateInput{
		Sources: []openbindings.CreateSource{
			{Format: FormatToken, Location: "localhost:50051", Content: proto},
		},
	})
	if err != nil {
		t.Fatalf("CreateInterface error: %v", err)
	}

	// Operations exist with stable names.
	if len(iface.Operations) != 2 {
		t.Fatalf("expected 2 operations, got %d", len(iface.Operations))
	}
	if _, ok := iface.Operations["GetItem"]; !ok {
		t.Error("expected operation GetItem")
	}
	if _, ok := iface.Operations["ListItems"]; !ok {
		t.Error("expected operation ListItems")
	}

	// Source entry references the gRPC format token and the supplied location.
	src, ok := iface.Sources[DefaultSourceName]
	if !ok {
		t.Fatalf("expected source %q", DefaultSourceName)
	}
	if src.Format != FormatToken {
		t.Errorf("source format = %q, want %q", src.Format, FormatToken)
	}
	if src.Location != "localhost:50051" {
		t.Errorf("source location = %q, want %q", src.Location, "localhost:50051")
	}

	// Bindings should resolve to package.Service/Method refs.
	bindingKey := "GetItem." + DefaultSourceName
	binding, ok := iface.Bindings[bindingKey]
	if !ok {
		t.Fatalf("expected binding %q", bindingKey)
	}
	if binding.Ref != "testpkg.ItemService/GetItem" {
		t.Errorf("binding ref = %q, want %q", binding.Ref, "testpkg.ItemService/GetItem")
	}
	if binding.Operation != "GetItem" {
		t.Errorf("binding operation = %q, want %q", binding.Operation, "GetItem")
	}
}

// TestIntegration_CreateInterface_PublicAPI_NoSources verifies the public API
// rejects empty source lists.
func TestIntegration_CreateInterface_PublicAPI_NoSources(t *testing.T) {
	creator := NewCreator()
	_, err := creator.CreateInterface(context.Background(), &openbindings.CreateInput{})
	if err == nil {
		t.Fatal("expected error for empty sources")
	}
}

func TestIntegration_InvokeUnary(t *testing.T) {
	dialer, ts := setupTestServer(t)
	invoker := newTestInvoker(t, dialer)
	defer invoker.Close()

	ctx := testCtx(t)
	inv := invoker.InvokeBinding(ctx, bufconnArgs("testpkg.ItemService/GetItem",
		map[string]any{"bearerToken": "tok_secret"}))
	if err := inv.Write(ctx, map[string]any{"id": "42"}); err != nil {
		t.Fatal(err)
	}

	v, err := openbindings.Single(ctx, inv.Outputs())
	if err != nil {
		t.Fatal(err)
	}

	if got := ts.auth(); got != "Bearer tok_secret" {
		t.Errorf("auth = %q, want %q", got, "Bearer tok_secret")
	}

	resp, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("expected map response, got %T", v)
	}
	if resp["id"] != "42" {
		t.Errorf("id = %v, want 42", resp["id"])
	}
	if resp["name"] != "item-42" {
		t.Errorf("name = %v, want item-42", resp["name"])
	}
}

func TestIntegration_UnaryHeaderTrailer(t *testing.T) {
	dialer, _ := setupTestServer(t)
	invoker := newTestInvoker(t, dialer)
	defer invoker.Close()

	ctx := testCtx(t)
	inv := invoker.InvokeBinding(ctx, bufconnArgs("testpkg.ItemService/GetItem", nil))
	if err := inv.Write(ctx, map[string]any{"id": "7"}); err != nil {
		t.Fatal(err)
	}
	if _, err := openbindings.Single(ctx, inv.Outputs()); err != nil {
		t.Fatal(err)
	}

	md, err := inv.Header(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := md["x-test-header"]; len(got) != 1 || got[0] != "from-server" {
		t.Errorf("header x-test-header = %v, want [from-server]", got)
	}
	if got := inv.Trailer()["x-test-trailer"]; len(got) != 1 || got[0] != "bye" {
		t.Errorf("trailer x-test-trailer = %v, want [bye]", got)
	}
}

func TestIntegration_InvokeServerStreaming(t *testing.T) {
	dialer, ts := setupTestServer(t)
	invoker := newTestInvoker(t, dialer)
	defer invoker.Close()

	ctx := testCtx(t)
	// ListItemsRequest has no fields: the binding closes input on entry and
	// dispatches the empty request without a Write.
	inv := invoker.InvokeBinding(ctx, bufconnArgs("testpkg.ItemService/ListItems",
		map[string]any{"bearerToken": "stream_tok"}))

	vals, terr := drainInvocation(t, inv)
	if terr != nil {
		t.Fatalf("unexpected terminal error: %s: %s", terr.Code, terr.Message)
	}
	if len(vals) != 2 {
		t.Fatalf("expected 2 outputs, got %d", len(vals))
	}

	if got := ts.auth(); got != "Bearer stream_tok" {
		t.Errorf("auth = %q, want %q", got, "Bearer stream_tok")
	}

	first, ok := vals[0].(map[string]any)
	if !ok {
		t.Fatalf("expected map output, got %T", vals[0])
	}
	if first["id"] != "1" || first["name"] != "first" {
		t.Errorf("first item = %v, want {id:1, name:first}", first)
	}

	// Stream metadata maps onto the handle.
	md, err := inv.Header(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := md["x-stream-header"]; len(got) != 1 || got[0] != "sh" {
		t.Errorf("header x-stream-header = %v, want [sh]", got)
	}
	if got := inv.Trailer()["x-stream-trailer"]; len(got) != 1 || got[0] != "st" {
		t.Errorf("trailer x-stream-trailer = %v, want [st]", got)
	}
}

func TestIntegration_ContextHeadersForwarded(t *testing.T) {
	dialer, ts := setupTestServer(t)
	invoker := newTestInvoker(t, dialer)
	defer invoker.Close()

	ctx := testCtx(t)
	inv := invoker.InvokeBinding(ctx, bufconnArgs("testpkg.ItemService/GetItem",
		map[string]any{"headers": map[string]any{"X-Custom": "custom-value"}}))
	if err := inv.Write(ctx, map[string]any{"id": "1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := openbindings.Single(ctx, inv.Outputs()); err != nil {
		t.Fatal(err)
	}

	if got := ts.header("x-custom"); len(got) != 1 || got[0] != "custom-value" {
		t.Errorf("x-custom = %v, want [custom-value]", got)
	}
}

func TestIntegration_Unauthenticated(t *testing.T) {
	dialer, ts := setupTestServer(t)
	ts.setRequireAuth(true)
	invoker := newTestInvoker(t, dialer)
	defer invoker.Close()

	ctx := testCtx(t)
	inv := invoker.InvokeBinding(ctx, bufconnArgs("testpkg.ItemService/GetItem", nil))
	if err := inv.Write(ctx, map[string]any{"id": "1"}); err != nil {
		t.Fatal(err)
	}

	vals, terr := drainInvocation(t, inv)
	if len(vals) != 0 || terr == nil {
		t.Fatalf("expected terminal error, got %d outputs terr=%v", len(vals), terr)
	}
	if terr.Code != openbindings.ErrCodeAuthRequired {
		t.Errorf("code = %q, want ERR_AUTH_REQUIRED", terr.Code)
	}
	details, ok := terr.Details.(map[string]any)
	if !ok || details["grpcCode"] != "Unauthenticated" {
		t.Errorf("details = %v, want grpcCode Unauthenticated", terr.Details)
	}
}

func TestIntegration_MissingInput(t *testing.T) {
	dialer, _ := setupTestServer(t)
	invoker := newTestInvoker(t, dialer)
	defer invoker.Close()

	ctx := testCtx(t)
	// GetItemRequest has fields, so a close-without-write is ERR_MISSING_INPUT.
	inv := invoker.InvokeBinding(ctx, bufconnArgs("testpkg.ItemService/GetItem", nil))
	if err := inv.Close(); err != nil {
		t.Fatal(err)
	}

	_, terr := drainInvocation(t, inv)
	if terr == nil || terr.Code != openbindings.ErrCodeMissingInput {
		t.Fatalf("expected ERR_MISSING_INPUT, got %v", terr)
	}
}

func TestIntegration_CancelMidStream(t *testing.T) {
	dialer, _ := setupTestServer(t)
	invoker := newTestInvoker(t, dialer)
	defer invoker.Close()

	ctx := testCtx(t)
	// WatchItems emits one item then blocks until the stream is torn down.
	inv := invoker.InvokeBinding(ctx, bufconnArgs("testpkg.ItemService/WatchItems", nil))

	out := inv.Outputs()
	first, err := out.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first.(map[string]any)["id"] != "w1" {
		t.Fatalf("first = %v", first)
	}

	inv.Cancel()
	_, err = out.Read(ctx)
	var ie *openbindings.InvocationError
	if !errors.As(err, &ie) || ie.Code != openbindings.ErrCodeCancelled {
		t.Fatalf("expected ERR_CANCELLED, got %v", err)
	}
}

func TestIntegration_InvalidRef(t *testing.T) {
	dialer, _ := setupTestServer(t)
	invoker := newTestInvoker(t, dialer)
	defer invoker.Close()

	inv := invoker.InvokeBinding(testCtx(t), bufconnArgs("not-a-valid-ref", nil))

	vals, terr := drainInvocation(t, inv)
	if len(vals) != 0 || terr == nil {
		t.Fatal("expected a terminal error and no outputs")
	}
	if terr.Code != openbindings.ErrCodeInvalidRef {
		t.Errorf("code = %q, want ERR_INVALID_REF", terr.Code)
	}
}

func TestIntegration_RefNotFound_Method(t *testing.T) {
	dialer, _ := setupTestServer(t)
	invoker := newTestInvoker(t, dialer)
	defer invoker.Close()

	inv := invoker.InvokeBinding(testCtx(t), bufconnArgs("testpkg.ItemService/NoSuchMethod", nil))

	_, terr := drainInvocation(t, inv)
	if terr == nil || terr.Code != openbindings.ErrCodeRefNotFound {
		t.Fatalf("expected ERR_REF_NOT_FOUND, got %v", terr)
	}
}

func TestIntegration_ClientStreamingUnsupported(t *testing.T) {
	dialer, _ := setupTestServer(t)
	invoker := newTestInvoker(t, dialer)
	defer invoker.Close()

	// The error fires pre-dispatch, before any input is required.
	inv := invoker.InvokeBinding(testCtx(t), bufconnArgs("testpkg.ItemService/UploadItems", nil))

	_, terr := drainInvocation(t, inv)
	if terr == nil || terr.Code != openbindings.ErrCodeExecutionFailed {
		t.Fatalf("expected ERR_EXECUTION_FAILED, got %v", terr)
	}
}

func TestIntegration_MissingLocation(t *testing.T) {
	invoker := NewInvoker()
	defer invoker.Close()

	inv := invoker.InvokeBinding(testCtx(t), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{Format: FormatToken},
		Ref:    "testpkg.ItemService/GetItem",
	})

	_, terr := drainInvocation(t, inv)
	if terr == nil || terr.Code != openbindings.ErrCodeSourceConfigError {
		t.Fatalf("expected ERR_SOURCE_CONFIG_ERROR, got %v", terr)
	}
}

func TestIntegration_CtxCancelBeforeWrite(t *testing.T) {
	dialer, _ := setupTestServer(t)
	invoker := newTestInvoker(t, dialer)
	defer invoker.Close()

	ctx, cancel := context.WithCancel(context.Background())
	inv := invoker.InvokeBinding(ctx, bufconnArgs("testpkg.ItemService/GetItem", nil))
	cancel()

	_, terr := drainInvocation(t, inv)
	if terr == nil || terr.Code != openbindings.ErrCodeCancelled {
		t.Fatalf("expected ERR_CANCELLED, got %v", terr)
	}
}
