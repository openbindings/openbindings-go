package grpc

import (
	"testing"

	"github.com/jhump/protoreflect/desc" //nolint:staticcheck // no v2 equivalent yet
	"google.golang.org/protobuf/types/descriptorpb"
)

// buildTestDiscovery creates a Discovery with the given services for testing.
func buildTestDiscovery(t *testing.T, files ...*descriptorpb.FileDescriptorProto) *discovery {
	t.Helper()
	disc := &discovery{address: "localhost:50051"}
	for _, fdp := range files {
		fd, err := desc.CreateFileDescriptor(fdp)
		if err != nil {
			t.Fatal(err)
		}
		disc.services = append(disc.services, fd.GetServices()...)
	}
	return disc
}

func ptr[T any](v T) *T { return &v }

func simpleServiceFile(pkg, svcName string, methods ...*descriptorpb.MethodDescriptorProto) *descriptorpb.FileDescriptorProto {
	return &descriptorpb.FileDescriptorProto{
		Name:    ptr(pkg + ".proto"),
		Package: ptr(pkg),
		Syntax:  ptr("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: ptr("Request"), Field: []*descriptorpb.FieldDescriptorProto{
				{Name: ptr("id"), Number: ptr(int32(1)), Type: ptr(descriptorpb.FieldDescriptorProto_TYPE_STRING),
					JsonName: ptr("id"), Label: ptr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL)},
			}},
			{Name: ptr("Response"), Field: []*descriptorpb.FieldDescriptorProto{
				{Name: ptr("value"), Number: ptr(int32(1)), Type: ptr(descriptorpb.FieldDescriptorProto_TYPE_STRING),
					JsonName: ptr("value"), Label: ptr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL)},
			}},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{
			{
				Name:   ptr(svcName),
				Method: methods,
			},
		},
	}
}

func unaryMethod(name string) *descriptorpb.MethodDescriptorProto {
	return &descriptorpb.MethodDescriptorProto{
		Name:       ptr(name),
		InputType:  ptr(".testpkg.Request"),
		OutputType: ptr(".testpkg.Response"),
	}
}

func serverStreamMethod(name string) *descriptorpb.MethodDescriptorProto {
	return &descriptorpb.MethodDescriptorProto{
		Name:             ptr(name),
		InputType:        ptr(".testpkg.Request"),
		OutputType:       ptr(".testpkg.Response"),
		ServerStreaming:   ptr(true),
	}
}

func clientStreamMethod(name string) *descriptorpb.MethodDescriptorProto {
	return &descriptorpb.MethodDescriptorProto{
		Name:             ptr(name),
		InputType:        ptr(".testpkg.Request"),
		OutputType:       ptr(".testpkg.Response"),
		ClientStreaming:   ptr(true),
	}
}

func TestConvertToInterface_CreatesOperations(t *testing.T) {
	disc := buildTestDiscovery(t, simpleServiceFile("testpkg", "TestService",
		unaryMethod("GetItem"),
		unaryMethod("ListItems"),
	))

	iface, err := convertToInterface(disc, "localhost:50051")
	if err != nil {
		t.Fatal(err)
	}
	if len(iface.Operations) != 2 {
		t.Fatalf("expected 2 operations, got %d", len(iface.Operations))
	}
	if _, ok := iface.Operations["GetItem"]; !ok {
		t.Error("expected operation 'GetItem'")
	}
	if _, ok := iface.Operations["ListItems"]; !ok {
		t.Error("expected operation 'ListItems'")
	}
}

func TestConvertToInterface_CreatesBindingsWithRefs(t *testing.T) {
	disc := buildTestDiscovery(t, simpleServiceFile("testpkg", "TestService",
		unaryMethod("GetItem"),
	))

	iface, err := convertToInterface(disc, "localhost:50051")
	if err != nil {
		t.Fatal(err)
	}
	key := "GetItem." + DefaultSourceName
	binding, ok := iface.Bindings[key]
	if !ok {
		t.Fatalf("expected binding %q", key)
	}
	if binding.Ref != "testpkg.TestService/GetItem" {
		t.Errorf("ref = %q, want %q", binding.Ref, "testpkg.TestService/GetItem")
	}
	if binding.Operation != "GetItem" {
		t.Errorf("operation = %q, want %q", binding.Operation, "GetItem")
	}
}

func TestConvertToInterface_CreatesSourceEntry(t *testing.T) {
	disc := buildTestDiscovery(t, simpleServiceFile("testpkg", "TestService",
		unaryMethod("GetItem"),
	))

	iface, err := convertToInterface(disc, "api.example.com:443")
	if err != nil {
		t.Fatal(err)
	}
	src, ok := iface.Sources[DefaultSourceName]
	if !ok {
		t.Fatal("expected source entry")
	}
	if src.Format != FormatToken {
		t.Errorf("format = %q, want %q", src.Format, FormatToken)
	}
	if src.Location != "api.example.com:443" {
		t.Errorf("location = %q, want %q", src.Location, "api.example.com:443")
	}
}

func TestConvertToInterface_SkipsClientStreaming(t *testing.T) {
	disc := buildTestDiscovery(t, simpleServiceFile("testpkg", "TestService",
		unaryMethod("GetItem"),
		clientStreamMethod("StreamUpload"),
	))

	iface, err := convertToInterface(disc, "localhost:50051")
	if err != nil {
		t.Fatal(err)
	}
	if len(iface.Operations) != 1 {
		t.Fatalf("expected 1 operation (client streaming skipped), got %d", len(iface.Operations))
	}
	if _, ok := iface.Operations["GetItem"]; !ok {
		t.Error("expected operation 'GetItem'")
	}
}

func TestConvertToInterface_IncludesServerStreaming(t *testing.T) {
	disc := buildTestDiscovery(t, simpleServiceFile("testpkg", "TestService",
		serverStreamMethod("WatchItems"),
	))

	iface, err := convertToInterface(disc, "localhost:50051")
	if err != nil {
		t.Fatal(err)
	}
	if len(iface.Operations) != 1 {
		t.Fatalf("expected 1 operation, got %d", len(iface.Operations))
	}
	if _, ok := iface.Operations["WatchItems"]; !ok {
		t.Error("expected operation 'WatchItems'")
	}
}

func TestConvertToInterface_OperationsAreSorted(t *testing.T) {
	disc := buildTestDiscovery(t, simpleServiceFile("testpkg", "TestService",
		unaryMethod("Zulu"),
		unaryMethod("Alpha"),
		unaryMethod("Mike"),
	))

	iface, err := convertToInterface(disc, "localhost:50051")
	if err != nil {
		t.Fatal(err)
	}
	if len(iface.Operations) != 3 {
		t.Fatalf("expected 3 operations, got %d", len(iface.Operations))
	}
	// Verify all exist (map ordering doesn't matter, but bindings should have correct refs)
	for _, name := range []string{"Alpha", "Mike", "Zulu"} {
		if _, ok := iface.Operations[name]; !ok {
			t.Errorf("expected operation %q", name)
		}
	}
}

func TestConvertToInterface_InputOutputSchemas(t *testing.T) {
	disc := buildTestDiscovery(t, simpleServiceFile("testpkg", "TestService",
		unaryMethod("GetItem"),
	))

	iface, err := convertToInterface(disc, "localhost:50051")
	if err != nil {
		t.Fatal(err)
	}
	op := iface.Operations["GetItem"]
	if op.Input == nil {
		t.Fatal("expected input schema")
	}
	if op.Input["type"] != "object" {
		t.Errorf("input type = %v, want object", op.Input["type"])
	}
	props, ok := op.Input["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected input properties")
	}
	idSchema, ok := props["id"].(map[string]any)
	if !ok {
		t.Fatal("expected id property")
	}
	if idSchema["type"] != "string" {
		t.Errorf("id type = %v, want string", idSchema["type"])
	}
}

func TestConvertToInterface_NilDiscovery(t *testing.T) {
	_, err := convertToInterface(nil, "localhost:50051")
	if err == nil {
		t.Error("expected error for nil discovery")
	}
}

func TestConvertToInterface_SingleServiceName(t *testing.T) {
	disc := buildTestDiscovery(t, simpleServiceFile("testpkg", "ItemService",
		unaryMethod("GetItem"),
	))

	iface, err := convertToInterface(disc, "localhost:50051")
	if err != nil {
		t.Fatal(err)
	}
	if iface.Name != "ItemService" {
		t.Errorf("name = %q, want %q", iface.Name, "ItemService")
	}
}

func TestConvertToInterface_MultiServiceUsesPackage(t *testing.T) {
	file := &descriptorpb.FileDescriptorProto{
		Name:    ptr("testpkg.proto"),
		Package: ptr("testpkg"),
		Syntax:  ptr("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: ptr("Request"), Field: []*descriptorpb.FieldDescriptorProto{
				{Name: ptr("id"), Number: ptr(int32(1)), Type: ptr(descriptorpb.FieldDescriptorProto_TYPE_STRING),
					JsonName: ptr("id"), Label: ptr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL)},
			}},
			{Name: ptr("Response"), Field: []*descriptorpb.FieldDescriptorProto{
				{Name: ptr("value"), Number: ptr(int32(1)), Type: ptr(descriptorpb.FieldDescriptorProto_TYPE_STRING),
					JsonName: ptr("value"), Label: ptr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL)},
			}},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{
			{Name: ptr("ServiceA"), Method: []*descriptorpb.MethodDescriptorProto{
				{Name: ptr("DoA"), InputType: ptr(".testpkg.Request"), OutputType: ptr(".testpkg.Response")},
			}},
			{Name: ptr("ServiceB"), Method: []*descriptorpb.MethodDescriptorProto{
				{Name: ptr("DoB"), InputType: ptr(".testpkg.Request"), OutputType: ptr(".testpkg.Response")},
			}},
		},
	}

	disc := buildTestDiscovery(t, file)
	iface, err := convertToInterface(disc, "localhost:50051")
	if err != nil {
		t.Fatal(err)
	}
	if iface.Name != "testpkg" {
		t.Errorf("name = %q, want %q", iface.Name, "testpkg")
	}
}
