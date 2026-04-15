package grpc

import (
	"context"
	"net"
	"testing"

	"github.com/jhump/protoreflect/grpcreflect" //nolint:staticcheck // depends on protoreflect/desc
	openbindings "github.com/openbindings/openbindings-go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/reflection"
	v1reflectiongrpc "google.golang.org/grpc/reflection/grpc_reflection_v1"
	v1alphareflectiongrpc "google.golang.org/grpc/reflection/grpc_reflection_v1alpha"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// testServer records the last authorization header it received.
type testServer struct {
	lastAuth string
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
				captureAuth(ctx, ts)
				req := dynamicpb.NewMessage(reqDesc)
				if err := dec(req); err != nil {
					return nil, err
				}
				id := req.Get(reqDesc.Fields().ByName("id")).String()
				resp := dynamicpb.NewMessage(respDesc)
				resp.Set(respDesc.Fields().ByName("id"), protoreflect.ValueOfString(id))
				resp.Set(respDesc.Fields().ByName("name"), protoreflect.ValueOfString("item-"+id))
				return resp, nil
			},
		}},
		Streams: []grpc.StreamDesc{{
			StreamName:   "ListItems",
			ServerStreams: true,
			Handler: func(srv any, stream grpc.ServerStream) error {
				captureAuth(stream.Context(), ts)
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
		}},
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

func captureAuth(ctx context.Context, ts *testServer) {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if vals := md.Get("authorization"); len(vals) > 0 {
			ts.lastAuth = vals[0]
		}
	}
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

func drainStream(t *testing.T, ch <-chan openbindings.StreamEvent) []openbindings.StreamEvent {
	t.Helper()
	var events []openbindings.StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}
	return events
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
		if isInfraService(name) {
			continue
		}
		svcDesc, err := refClient.ResolveService(name)
		if err != nil {
			t.Fatal(err)
		}
		disc.services = append(disc.services, svcDesc)
	}

	iface, err := convertToInterface(disc, "bufconn")
	if err != nil {
		t.Fatal(err)
	}

	if iface.Name != "ItemService" {
		t.Errorf("name = %q, want %q", iface.Name, "ItemService")
	}
	if len(iface.Operations) != 2 {
		t.Fatalf("expected 2 operations, got %d", len(iface.Operations))
	}
	if _, ok := iface.Operations["GetItem"]; !ok {
		t.Error("expected operation GetItem")
	}
	if _, ok := iface.Operations["ListItems"]; !ok {
		t.Error("expected operation ListItems")
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

func TestIntegration_ExecuteUnary(t *testing.T) {
	dialer, ts := setupTestServer(t)
	conn := dialTestServer(t, dialer)

	executor := NewExecutor()
	defer executor.Close()
	executor.conns.Store("bufconn", conn)

	ch, err := executor.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
		Source:  openbindings.BindingExecutionSource{Format: FormatToken, Location: "bufconn"},
		Ref:     "testpkg.ItemService/GetItem",
		Input:   map[string]any{"id": "42"},
		Context: map[string]any{"bearerToken": "tok_secret"},
	})
	if err != nil {
		t.Fatal(err)
	}

	events := drainStream(t, ch)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Error != nil {
		t.Fatalf("unexpected error: %s: %s", events[0].Error.Code, events[0].Error.Message)
	}

	if ts.lastAuth != "Bearer tok_secret" {
		t.Errorf("auth = %q, want %q", ts.lastAuth, "Bearer tok_secret")
	}

	resp, ok := events[0].Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map response, got %T", events[0].Data)
	}
	if resp["id"] != "42" {
		t.Errorf("id = %v, want 42", resp["id"])
	}
	if resp["name"] != "item-42" {
		t.Errorf("name = %v, want item-42", resp["name"])
	}
}

func TestIntegration_ExecuteStreaming(t *testing.T) {
	dialer, ts := setupTestServer(t)
	conn := dialTestServer(t, dialer)

	executor := NewExecutor()
	defer executor.Close()
	executor.conns.Store("bufconn", conn)

	ch, err := executor.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
		Source:  openbindings.BindingExecutionSource{Format: FormatToken, Location: "bufconn"},
		Ref:     "testpkg.ItemService/ListItems",
		Context: map[string]any{"bearerToken": "stream_tok"},
	})
	if err != nil {
		t.Fatal(err)
	}

	events := drainStream(t, ch)
	// Expect exactly 2 data events with clean stream completion (no trailing error).
	// io.EOF from RecvMsg is the normal end-of-stream signal and should not be
	// emitted as a stream_error event.
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	for i, ev := range events {
		if ev.Error != nil {
			t.Fatalf("event %d: unexpected error: %s: %s", i, ev.Error.Code, ev.Error.Message)
		}
	}

	if ts.lastAuth != "Bearer stream_tok" {
		t.Errorf("auth = %q, want %q", ts.lastAuth, "Bearer stream_tok")
	}

	first, ok := events[0].Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map data, got %T", events[0].Data)
	}
	if first["id"] != "1" || first["name"] != "first" {
		t.Errorf("first item = %v, want {id:1, name:first}", first)
	}
}

func TestIntegration_StoredCredentials(t *testing.T) {
	dialer, ts := setupTestServer(t)
	conn := dialTestServer(t, dialer)

	executor := NewExecutor()
	defer executor.Close()
	executor.conns.Store("bufconn", conn)

	store := openbindings.NewMemoryStore()
	ctx := context.Background()
	_ = store.Set(ctx, "bufconn", map[string]any{"bearerToken": "stored_token"})

	ch, err := executor.ExecuteBinding(ctx, &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{Format: FormatToken, Location: "bufconn"},
		Ref:    "testpkg.ItemService/GetItem",
		Input:  map[string]any{"id": "1"},
		Store:  store,
	})
	if err != nil {
		t.Fatal(err)
	}

	events := drainStream(t, ch)
	if len(events) != 1 || events[0].Error != nil {
		t.Fatalf("expected 1 successful event, got %d", len(events))
	}

	if ts.lastAuth != "Bearer stored_token" {
		t.Errorf("auth = %q, want %q", ts.lastAuth, "Bearer stored_token")
	}
}

func TestIntegration_InvalidRef(t *testing.T) {
	dialer, _ := setupTestServer(t)
	conn := dialTestServer(t, dialer)

	executor := NewExecutor()
	defer executor.Close()
	executor.conns.Store("bufconn", conn)

	ch, err := executor.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{Format: FormatToken, Location: "bufconn"},
		Ref:    "not-a-valid-ref",
	})
	if err != nil {
		t.Fatal(err)
	}

	events := drainStream(t, ch)
	if len(events) != 1 || events[0].Error == nil {
		t.Fatal("expected 1 error event")
	}
	if events[0].Error.Code != openbindings.ErrCodeInvalidRef {
		t.Errorf("code = %q, want invalid_ref", events[0].Error.Code)
	}
}
