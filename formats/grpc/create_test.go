package grpc

import (
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/schemaprofile"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

// buildTestDiscovery creates a Discovery with the given services for testing.
// Files are registered into a shared protoregistry so later files can resolve
// dependencies declared by earlier ones.
func buildTestDiscovery(t *testing.T, files ...*descriptorpb.FileDescriptorProto) *discovery {
	t.Helper()
	disc := &discovery{address: "localhost:50051"}
	var registry protoregistry.Files
	for _, fdp := range files {
		fd, err := protodesc.NewFile(fdp, &registry)
		if err != nil {
			t.Fatal(err)
		}
		if err := registry.RegisterFile(fd); err != nil {
			t.Fatal(err)
		}
		services := fd.Services()
		for i := 0; i < services.Len(); i++ {
			disc.services = append(disc.services, services.Get(i))
		}
	}
	return disc
}

// servicesFromFiles parses files into FileDescriptors and returns just their
// service descriptors, for tests that want to construct a discovery directly.
func servicesFromFiles(t *testing.T, files ...*descriptorpb.FileDescriptorProto) []protoreflect.ServiceDescriptor {
	t.Helper()
	var registry protoregistry.Files
	var out []protoreflect.ServiceDescriptor
	for _, fdp := range files {
		fd, err := protodesc.NewFile(fdp, &registry)
		if err != nil {
			t.Fatal(err)
		}
		if err := registry.RegisterFile(fd); err != nil {
			t.Fatal(err)
		}
		services := fd.Services()
		for i := 0; i < services.Len(); i++ {
			out = append(out, services.Get(i))
		}
	}
	return out
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
		Name:            ptr(name),
		InputType:       ptr(".testpkg.Request"),
		OutputType:      ptr(".testpkg.Response"),
		ServerStreaming: ptr(true),
	}
}

func clientStreamMethod(name string) *descriptorpb.MethodDescriptorProto {
	return &descriptorpb.MethodDescriptorProto{
		Name:            ptr(name),
		InputType:       ptr(".testpkg.Request"),
		OutputType:      ptr(".testpkg.Response"),
		ClientStreaming: ptr(true),
	}
}

func TestConvertToInterface_CreatesOperations(t *testing.T) {
	disc := buildTestDiscovery(t, simpleServiceFile("testpkg", "TestService",
		unaryMethod("GetItem"),
		unaryMethod("ListItems"),
	))

	iface, err := convertToInterface(disc, "localhost:50051", nil)
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

	iface, err := convertToInterface(disc, "localhost:50051", nil)
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

	iface, err := convertToInterface(disc, "api.example.com:443", nil)
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

	iface, err := convertToInterface(disc, "localhost:50051", nil)
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

	iface, err := convertToInterface(disc, "localhost:50051", nil)
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

	iface, err := convertToInterface(disc, "localhost:50051", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(iface.Operations) != 3 {
		t.Fatalf("expected 3 operations, got %d", len(iface.Operations))
	}
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

	iface, err := convertToInterface(disc, "localhost:50051", nil)
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
	_, err := convertToInterface(nil, "localhost:50051", nil)
	if err == nil {
		t.Error("expected error for nil discovery")
	}
}

func TestConvertToInterface_SingleServiceName(t *testing.T) {
	disc := buildTestDiscovery(t, simpleServiceFile("testpkg", "ItemService",
		unaryMethod("GetItem"),
	))

	iface, err := convertToInterface(disc, "localhost:50051", nil)
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
	iface, err := convertToInterface(disc, "localhost:50051", nil)
	if err != nil {
		t.Fatal(err)
	}
	if iface.Name != "testpkg" {
		t.Errorf("name = %q, want %q", iface.Name, "testpkg")
	}
}

// ---------- well-known types ----------

func TestWellKnownSchema_Timestamp(t *testing.T) {
	got := wellKnownSchema("google.protobuf.Timestamp")
	if got["type"] != "string" {
		t.Errorf("type = %v, want string", got["type"])
	}
	if got["format"] != "date-time" {
		t.Errorf("format = %v, want date-time", got["format"])
	}
}

func TestWellKnownSchema_Duration(t *testing.T) {
	got := wellKnownSchema("google.protobuf.Duration")
	if got["type"] != "string" {
		t.Errorf("type = %v, want string", got["type"])
	}
}

func TestWellKnownSchema_FieldMask(t *testing.T) {
	got := wellKnownSchema("google.protobuf.FieldMask")
	if got["type"] != "string" {
		t.Errorf("type = %v, want string", got["type"])
	}
}

func TestWellKnownSchema_Struct(t *testing.T) {
	got := wellKnownSchema("google.protobuf.Struct")
	if got["type"] != "object" {
		t.Errorf("type = %v, want object", got["type"])
	}
	if _, hasProps := got["properties"]; hasProps {
		t.Error("Struct should not declare properties (arbitrary JSON)")
	}
}

func TestWellKnownSchema_Value(t *testing.T) {
	got := wellKnownSchema("google.protobuf.Value")
	if len(got) != 0 {
		t.Errorf("Value should be empty schema (any JSON), got %v", got)
	}
}

func TestWellKnownSchema_ListValue(t *testing.T) {
	got := wellKnownSchema("google.protobuf.ListValue")
	if got["type"] != "array" {
		t.Errorf("type = %v, want array", got["type"])
	}
}

func TestWellKnownSchema_Empty(t *testing.T) {
	got := wellKnownSchema("google.protobuf.Empty")
	if got["type"] != "object" {
		t.Errorf("type = %v, want object", got["type"])
	}
}

func TestWellKnownSchema_BoolValue(t *testing.T) {
	got := wellKnownSchema("google.protobuf.BoolValue")
	if got["type"] != "boolean" {
		t.Errorf("type = %v, want boolean", got["type"])
	}
}

func TestWellKnownSchema_StringValue(t *testing.T) {
	got := wellKnownSchema("google.protobuf.StringValue")
	if got["type"] != "string" {
		t.Errorf("type = %v, want string", got["type"])
	}
}

func TestWellKnownSchema_BytesValue(t *testing.T) {
	got := wellKnownSchema("google.protobuf.BytesValue")
	if got["type"] != "string" {
		t.Errorf("type = %v, want string", got["type"])
	}
	if _, ok := got["contentEncoding"]; ok {
		t.Errorf("contentEncoding should not be present, got %v", got["contentEncoding"])
	}
}

func TestWellKnownSchema_Int32Wrappers(t *testing.T) {
	for _, fqn := range []string{"google.protobuf.Int32Value", "google.protobuf.UInt32Value"} {
		got := wellKnownSchema(fqn)
		if got["type"] != "integer" {
			t.Errorf("%s: type = %v, want integer", fqn, got["type"])
		}
	}
}

func TestWellKnownSchema_Int64Wrappers(t *testing.T) {
	for _, fqn := range []string{"google.protobuf.Int64Value", "google.protobuf.UInt64Value"} {
		got := wellKnownSchema(fqn)
		if got["type"] != "integer" {
			t.Errorf("%s: type = %v, want integer", fqn, got["type"])
		}
		if got["format"] != "int64" {
			t.Errorf("%s: format = %v, want int64", fqn, got["format"])
		}
	}
}

func TestWellKnownSchema_FloatWrappers(t *testing.T) {
	for _, fqn := range []string{"google.protobuf.FloatValue", "google.protobuf.DoubleValue"} {
		got := wellKnownSchema(fqn)
		if got["type"] != "number" {
			t.Errorf("%s: type = %v, want number", fqn, got["type"])
		}
	}
}

func TestWellKnownSchema_Any(t *testing.T) {
	got := wellKnownSchema("google.protobuf.Any")
	if got["type"] != "object" {
		t.Fatalf("type = %v, want object", got["type"])
	}
	props, ok := got["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties map")
	}
	atType, ok := props["@type"].(map[string]any)
	if !ok {
		t.Fatal("expected @type property")
	}
	if atType["type"] != "string" {
		t.Errorf("@type.type = %v, want string", atType["type"])
	}
	req, ok := got["required"].([]any)
	if !ok || len(req) != 1 || req[0] != "@type" {
		t.Errorf("required = %v, want [@type]", got["required"])
	}
}

func TestWellKnownSchema_NotWellKnown(t *testing.T) {
	for _, fqn := range []string{
		"testpkg.Request",
		"com.example.Thing",
		"google.protobuf.ThisDoesNotExist",
		"",
	} {
		if got := wellKnownSchema(fqn); got != nil {
			t.Errorf("%q: got %v, want nil", fqn, got)
		}
	}
}

// timestampFile returns a synthetic FileDescriptorProto for
// google.protobuf.Timestamp so integration tests can reference it as a
// field type without requiring the real well-known-type descriptors to be
// registered in the running process.
func timestampFile() *descriptorpb.FileDescriptorProto {
	return &descriptorpb.FileDescriptorProto{
		Name:    ptr("google/protobuf/timestamp.proto"),
		Package: ptr("google.protobuf"),
		Syntax:  ptr("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: ptr("Timestamp"), Field: []*descriptorpb.FieldDescriptorProto{
				{Name: ptr("seconds"), Number: ptr(int32(1)), Type: ptr(descriptorpb.FieldDescriptorProto_TYPE_INT64),
					JsonName: ptr("seconds"), Label: ptr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL)},
				{Name: ptr("nanos"), Number: ptr(int32(2)), Type: ptr(descriptorpb.FieldDescriptorProto_TYPE_INT32),
					JsonName: ptr("nanos"), Label: ptr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL)},
			}},
		},
	}
}

func TestConvertToInterface_WellKnownTimestampField(t *testing.T) {
	wkFDP := timestampFile()
	useFDP := &descriptorpb.FileDescriptorProto{
		Name:       ptr("testpkg.proto"),
		Package:    ptr("testpkg"),
		Syntax:     ptr("proto3"),
		Dependency: []string{"google/protobuf/timestamp.proto"},
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: ptr("Request"), Field: []*descriptorpb.FieldDescriptorProto{
				{Name: ptr("id"), Number: ptr(int32(1)), Type: ptr(descriptorpb.FieldDescriptorProto_TYPE_STRING),
					JsonName: ptr("id"), Label: ptr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL)},
				{Name: ptr("created_at"), Number: ptr(int32(2)),
					Type:     ptr(descriptorpb.FieldDescriptorProto_TYPE_MESSAGE),
					TypeName: ptr(".google.protobuf.Timestamp"),
					JsonName: ptr("createdAt"),
					Label:    ptr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL)},
			}},
			{Name: ptr("Response"), Field: []*descriptorpb.FieldDescriptorProto{
				{Name: ptr("value"), Number: ptr(int32(1)), Type: ptr(descriptorpb.FieldDescriptorProto_TYPE_STRING),
					JsonName: ptr("value"), Label: ptr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL)},
			}},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{
			{Name: ptr("TestService"), Method: []*descriptorpb.MethodDescriptorProto{
				{Name: ptr("GetItem"), InputType: ptr(".testpkg.Request"), OutputType: ptr(".testpkg.Response")},
			}},
		},
	}

	services := servicesFromFiles(t, wkFDP, useFDP)
	disc := &discovery{address: "localhost:50051", services: services}
	iface, err := convertToInterface(disc, "localhost:50051", nil)
	if err != nil {
		t.Fatal(err)
	}

	op := iface.Operations["GetItem"]
	props, ok := op.Input["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected input properties")
	}
	createdAt, ok := props["createdAt"].(map[string]any)
	if !ok {
		t.Fatalf("expected createdAt property, got %v", props)
	}
	if createdAt["type"] != "string" {
		t.Errorf("createdAt.type = %v, want string (canonical Timestamp form, not seconds/nanos object)", createdAt["type"])
	}
	if createdAt["format"] != "date-time" {
		t.Errorf("createdAt.format = %v, want date-time", createdAt["format"])
	}
	if _, hasProps := createdAt["properties"]; hasProps {
		t.Error("createdAt should not have nested properties (should not traverse Timestamp's fields)")
	}
}

// ---------- oneof ----------

func TestConvertToInterface_OneofSingleGroup(t *testing.T) {
	file := &descriptorpb.FileDescriptorProto{
		Name:    ptr("testpkg.proto"),
		Package: ptr("testpkg"),
		Syntax:  ptr("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: ptr("Request"),
				OneofDecl: []*descriptorpb.OneofDescriptorProto{
					{Name: ptr("identifier")},
				},
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: ptr("item_id"), Number: ptr(int32(1)), Type: ptr(descriptorpb.FieldDescriptorProto_TYPE_STRING),
						JsonName: ptr("itemId"), Label: ptr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL),
						OneofIndex: ptr(int32(0))},
					{Name: ptr("item_index"), Number: ptr(int32(2)), Type: ptr(descriptorpb.FieldDescriptorProto_TYPE_INT32),
						JsonName: ptr("itemIndex"), Label: ptr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL),
						OneofIndex: ptr(int32(0))},
				},
			},
			{Name: ptr("Response"), Field: []*descriptorpb.FieldDescriptorProto{
				{Name: ptr("value"), Number: ptr(int32(1)), Type: ptr(descriptorpb.FieldDescriptorProto_TYPE_STRING),
					JsonName: ptr("value"), Label: ptr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL)},
			}},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{
			{Name: ptr("TestService"), Method: []*descriptorpb.MethodDescriptorProto{
				{Name: ptr("GetItem"), InputType: ptr(".testpkg.Request"), OutputType: ptr(".testpkg.Response")},
			}},
		},
	}

	disc := buildTestDiscovery(t, file)
	iface, err := convertToInterface(disc, "localhost:50051", nil)
	if err != nil {
		t.Fatal(err)
	}

	input := iface.Operations["GetItem"].Input
	variants, ok := input["oneOf"].([]any)
	if !ok {
		t.Fatalf("expected oneOf on input schema, got %v", input)
	}
	if len(variants) != 2 {
		t.Fatalf("expected 2 oneOf variants, got %d", len(variants))
	}

	if props, ok := input["properties"].(map[string]any); ok {
		if _, present := props["itemId"]; present {
			t.Error("oneof member itemId should not appear in top-level properties")
		}
		if _, present := props["itemIndex"]; present {
			t.Error("oneof member itemIndex should not appear in top-level properties")
		}
	}

	seen := map[string]bool{}
	for _, v := range variants {
		vm, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("variant not a map: %v", v)
		}
		if vm["type"] != "object" {
			t.Errorf("variant type = %v, want object", vm["type"])
		}
		req, ok := vm["required"].([]any)
		if !ok || len(req) != 1 {
			t.Fatalf("variant required = %v, want single-element array", vm["required"])
		}
		name, _ := req[0].(string)
		seen[name] = true
	}
	if !seen["itemId"] || !seen["itemIndex"] {
		t.Errorf("expected variants for itemId and itemIndex, got %v", seen)
	}
}

func TestConvertToInterface_OneofWithRegularFields(t *testing.T) {
	file := &descriptorpb.FileDescriptorProto{
		Name:    ptr("testpkg.proto"),
		Package: ptr("testpkg"),
		Syntax:  ptr("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: ptr("Request"),
				OneofDecl: []*descriptorpb.OneofDescriptorProto{
					{Name: ptr("identifier")},
				},
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: ptr("tenant"), Number: ptr(int32(1)), Type: ptr(descriptorpb.FieldDescriptorProto_TYPE_STRING),
						JsonName: ptr("tenant"), Label: ptr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL)},
					{Name: ptr("item_id"), Number: ptr(int32(2)), Type: ptr(descriptorpb.FieldDescriptorProto_TYPE_STRING),
						JsonName: ptr("itemId"), Label: ptr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL),
						OneofIndex: ptr(int32(0))},
					{Name: ptr("item_index"), Number: ptr(int32(3)), Type: ptr(descriptorpb.FieldDescriptorProto_TYPE_INT32),
						JsonName: ptr("itemIndex"), Label: ptr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL),
						OneofIndex: ptr(int32(0))},
				},
			},
			{Name: ptr("Response"), Field: []*descriptorpb.FieldDescriptorProto{
				{Name: ptr("value"), Number: ptr(int32(1)), Type: ptr(descriptorpb.FieldDescriptorProto_TYPE_STRING),
					JsonName: ptr("value"), Label: ptr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL)},
			}},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{
			{Name: ptr("TestService"), Method: []*descriptorpb.MethodDescriptorProto{
				{Name: ptr("GetItem"), InputType: ptr(".testpkg.Request"), OutputType: ptr(".testpkg.Response")},
			}},
		},
	}

	disc := buildTestDiscovery(t, file)
	iface, err := convertToInterface(disc, "localhost:50051", nil)
	if err != nil {
		t.Fatal(err)
	}

	input := iface.Operations["GetItem"].Input

	props, ok := input["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties")
	}
	if _, ok := props["tenant"]; !ok {
		t.Error("expected non-oneof field `tenant` in properties")
	}
	if _, ok := props["itemId"]; ok {
		t.Error("oneof member itemId should not be in top-level properties")
	}

	if _, ok := input["oneOf"].([]any); !ok {
		t.Error("expected oneOf on input")
	}
}

func TestConvertToInterface_OneofMultipleGroupsFallsBackToProperties(t *testing.T) {
	file := &descriptorpb.FileDescriptorProto{
		Name:    ptr("testpkg.proto"),
		Package: ptr("testpkg"),
		Syntax:  ptr("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: ptr("Request"),
				OneofDecl: []*descriptorpb.OneofDescriptorProto{
					{Name: ptr("identifier")},
					{Name: ptr("payload")},
				},
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: ptr("item_id"), Number: ptr(int32(1)), Type: ptr(descriptorpb.FieldDescriptorProto_TYPE_STRING),
						JsonName: ptr("itemId"), Label: ptr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL),
						OneofIndex: ptr(int32(0))},
					{Name: ptr("item_index"), Number: ptr(int32(2)), Type: ptr(descriptorpb.FieldDescriptorProto_TYPE_INT32),
						JsonName: ptr("itemIndex"), Label: ptr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL),
						OneofIndex: ptr(int32(0))},
					{Name: ptr("text_payload"), Number: ptr(int32(3)), Type: ptr(descriptorpb.FieldDescriptorProto_TYPE_STRING),
						JsonName: ptr("textPayload"), Label: ptr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL),
						OneofIndex: ptr(int32(1))},
					{Name: ptr("binary_payload"), Number: ptr(int32(4)), Type: ptr(descriptorpb.FieldDescriptorProto_TYPE_BYTES),
						JsonName: ptr("binaryPayload"), Label: ptr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL),
						OneofIndex: ptr(int32(1))},
				},
			},
			{Name: ptr("Response"), Field: []*descriptorpb.FieldDescriptorProto{
				{Name: ptr("value"), Number: ptr(int32(1)), Type: ptr(descriptorpb.FieldDescriptorProto_TYPE_STRING),
					JsonName: ptr("value"), Label: ptr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL)},
			}},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{
			{Name: ptr("TestService"), Method: []*descriptorpb.MethodDescriptorProto{
				{Name: ptr("GetItem"), InputType: ptr(".testpkg.Request"), OutputType: ptr(".testpkg.Response")},
			}},
		},
	}

	var warnings []openbindings.CreatorWarning
	disc := buildTestDiscovery(t, file)
	iface, err := convertToInterface(disc, "localhost:50051", func(w openbindings.CreatorWarning) {
		warnings = append(warnings, w)
	})
	if err != nil {
		t.Fatal(err)
	}

	input := iface.Operations["GetItem"].Input
	if _, ok := input["oneOf"]; ok {
		t.Error("multi-group oneof should not emit oneOf (v0.1 profile limitation)")
	}
	props, ok := input["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties")
	}
	for _, name := range []string{"itemId", "itemIndex", "textPayload", "binaryPayload"} {
		if _, ok := props[name]; !ok {
			t.Errorf("expected %q in properties (multi-group fallback)", name)
		}
	}

	if len(warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d: %+v", len(warnings), warnings)
	}
	got := warnings[0]
	if got.Code != "grpc.multi_group_oneof" {
		t.Errorf("warning code = %q, want grpc.multi_group_oneof", got.Code)
	}
	if got.Path != "operations.GetItem.input" {
		t.Errorf("warning path = %q, want operations.GetItem.input", got.Path)
	}
	if got.Details["message"] != "testpkg.Request" {
		t.Errorf("warning details.message = %v, want testpkg.Request", got.Details["message"])
	}
	groups, ok := got.Details["groups"].([]string)
	if !ok {
		t.Fatalf("warning details.groups not []string: %T", got.Details["groups"])
	}
	if len(groups) != 2 {
		t.Errorf("warning groups = %v, want 2 entries", groups)
	}
}

func TestConvertToInterface_Proto3OptionalNotTreatedAsOneof(t *testing.T) {
	file := &descriptorpb.FileDescriptorProto{
		Name:    ptr("testpkg.proto"),
		Package: ptr("testpkg"),
		Syntax:  ptr("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: ptr("Request"),
				OneofDecl: []*descriptorpb.OneofDescriptorProto{
					{Name: ptr("_page_size")},
				},
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: ptr("query"), Number: ptr(int32(1)), Type: ptr(descriptorpb.FieldDescriptorProto_TYPE_STRING),
						JsonName: ptr("query"), Label: ptr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL)},
					{Name: ptr("page_size"), Number: ptr(int32(2)), Type: ptr(descriptorpb.FieldDescriptorProto_TYPE_INT32),
						JsonName:       ptr("pageSize"),
						Label:          ptr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL),
						OneofIndex:     ptr(int32(0)),
						Proto3Optional: ptr(true)},
				},
			},
			{Name: ptr("Response"), Field: []*descriptorpb.FieldDescriptorProto{
				{Name: ptr("value"), Number: ptr(int32(1)), Type: ptr(descriptorpb.FieldDescriptorProto_TYPE_STRING),
					JsonName: ptr("value"), Label: ptr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL)},
			}},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{
			{Name: ptr("TestService"), Method: []*descriptorpb.MethodDescriptorProto{
				{Name: ptr("GetItem"), InputType: ptr(".testpkg.Request"), OutputType: ptr(".testpkg.Response")},
			}},
		},
	}

	disc := buildTestDiscovery(t, file)
	iface, err := convertToInterface(disc, "localhost:50051", nil)
	if err != nil {
		t.Fatal(err)
	}

	input := iface.Operations["GetItem"].Input
	if _, ok := input["oneOf"]; ok {
		t.Error("proto3 optional field should not produce oneOf (synthetic oneof)")
	}
	props, ok := input["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties")
	}
	if _, ok := props["query"]; !ok {
		t.Error("expected `query` in properties")
	}
	if _, ok := props["pageSize"]; !ok {
		t.Error("expected proto3-optional `pageSize` in properties (not inside oneOf)")
	}
}

func TestConvertToInterface_OneofShapeAcceptedByProfile(t *testing.T) {
	file := &descriptorpb.FileDescriptorProto{
		Name:    ptr("testpkg.proto"),
		Package: ptr("testpkg"),
		Syntax:  ptr("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: ptr("Request"),
				OneofDecl: []*descriptorpb.OneofDescriptorProto{
					{Name: ptr("identifier")},
				},
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: ptr("tenant"), Number: ptr(int32(1)), Type: ptr(descriptorpb.FieldDescriptorProto_TYPE_STRING),
						JsonName: ptr("tenant"), Label: ptr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL)},
					{Name: ptr("item_id"), Number: ptr(int32(2)), Type: ptr(descriptorpb.FieldDescriptorProto_TYPE_STRING),
						JsonName: ptr("itemId"), Label: ptr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL),
						OneofIndex: ptr(int32(0))},
					{Name: ptr("item_index"), Number: ptr(int32(3)), Type: ptr(descriptorpb.FieldDescriptorProto_TYPE_INT32),
						JsonName: ptr("itemIndex"), Label: ptr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL),
						OneofIndex: ptr(int32(0))},
				},
			},
			{Name: ptr("Response"), Field: []*descriptorpb.FieldDescriptorProto{
				{Name: ptr("value"), Number: ptr(int32(1)), Type: ptr(descriptorpb.FieldDescriptorProto_TYPE_STRING),
					JsonName: ptr("value"), Label: ptr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL)},
			}},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{
			{Name: ptr("TestService"), Method: []*descriptorpb.MethodDescriptorProto{
				{Name: ptr("GetItem"), InputType: ptr(".testpkg.Request"), OutputType: ptr(".testpkg.Response")},
			}},
		},
	}

	disc := buildTestDiscovery(t, file)
	iface, err := convertToInterface(disc, "localhost:50051", nil)
	if err != nil {
		t.Fatal(err)
	}

	n := &schemaprofile.Normalizer{}
	input := iface.Operations["GetItem"].Input
	if _, err := n.Normalize(input); err != nil {
		t.Fatalf("oneof schema rejected by v0.1 profile: %v", err)
	}
}

func TestConvertToInterface_ByteAndInt64ShapesAcceptedByProfile(t *testing.T) {
	wkFDP := timestampFile()
	wrapperFDP := &descriptorpb.FileDescriptorProto{
		Name:    ptr("google/protobuf/wrappers.proto"),
		Package: ptr("google.protobuf"),
		Syntax:  ptr("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: ptr("Int64Value"), Field: []*descriptorpb.FieldDescriptorProto{
				{Name: ptr("value"), Number: ptr(int32(1)), Type: ptr(descriptorpb.FieldDescriptorProto_TYPE_INT64),
					JsonName: ptr("value"), Label: ptr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL)},
			}},
			{Name: ptr("BytesValue"), Field: []*descriptorpb.FieldDescriptorProto{
				{Name: ptr("value"), Number: ptr(int32(1)), Type: ptr(descriptorpb.FieldDescriptorProto_TYPE_BYTES),
					JsonName: ptr("value"), Label: ptr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL)},
			}},
		},
	}
	useFDP := &descriptorpb.FileDescriptorProto{
		Name:    ptr("testpkg.proto"),
		Package: ptr("testpkg"),
		Syntax:  ptr("proto3"),
		Dependency: []string{
			"google/protobuf/timestamp.proto",
			"google/protobuf/wrappers.proto",
		},
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: ptr("Request"), Field: []*descriptorpb.FieldDescriptorProto{
				{Name: ptr("count"), Number: ptr(int32(1)), Type: ptr(descriptorpb.FieldDescriptorProto_TYPE_INT64),
					JsonName: ptr("count"), Label: ptr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL)},
				{Name: ptr("payload"), Number: ptr(int32(2)), Type: ptr(descriptorpb.FieldDescriptorProto_TYPE_BYTES),
					JsonName: ptr("payload"), Label: ptr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL)},
				{Name: ptr("wrapped_count"), Number: ptr(int32(3)),
					Type:     ptr(descriptorpb.FieldDescriptorProto_TYPE_MESSAGE),
					TypeName: ptr(".google.protobuf.Int64Value"),
					JsonName: ptr("wrappedCount"),
					Label:    ptr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL)},
				{Name: ptr("wrapped_payload"), Number: ptr(int32(4)),
					Type:     ptr(descriptorpb.FieldDescriptorProto_TYPE_MESSAGE),
					TypeName: ptr(".google.protobuf.BytesValue"),
					JsonName: ptr("wrappedPayload"),
					Label:    ptr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL)},
				{Name: ptr("created_at"), Number: ptr(int32(5)),
					Type:     ptr(descriptorpb.FieldDescriptorProto_TYPE_MESSAGE),
					TypeName: ptr(".google.protobuf.Timestamp"),
					JsonName: ptr("createdAt"),
					Label:    ptr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL)},
			}},
			{Name: ptr("Response"), Field: []*descriptorpb.FieldDescriptorProto{
				{Name: ptr("value"), Number: ptr(int32(1)), Type: ptr(descriptorpb.FieldDescriptorProto_TYPE_STRING),
					JsonName: ptr("value"), Label: ptr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL)},
			}},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{
			{Name: ptr("TestService"), Method: []*descriptorpb.MethodDescriptorProto{
				{Name: ptr("GetItem"), InputType: ptr(".testpkg.Request"), OutputType: ptr(".testpkg.Response")},
			}},
		},
	}

	services := servicesFromFiles(t, wkFDP, wrapperFDP, useFDP)
	disc := &discovery{address: "localhost:50051", services: services}
	iface, err := convertToInterface(disc, "localhost:50051", nil)
	if err != nil {
		t.Fatal(err)
	}

	n := &schemaprofile.Normalizer{}
	input := iface.Operations["GetItem"].Input
	if _, err := n.Normalize(input); err != nil {
		t.Fatalf("bytes/int64 schema rejected by v0.1 profile: %v", err)
	}

	props, _ := input["properties"].(map[string]any)
	count, _ := props["count"].(map[string]any)
	if count["type"] != "integer" || count["format"] != "int64" {
		t.Errorf("count = %v, want integer+int64", count)
	}
	payload, _ := props["payload"].(map[string]any)
	if payload["type"] != "string" {
		t.Errorf("payload.type = %v, want string", payload["type"])
	}
	if _, hasEnc := payload["contentEncoding"]; hasEnc {
		t.Error("payload should not carry contentEncoding (v0.1 profile rejects it)")
	}
	wrappedCount, _ := props["wrappedCount"].(map[string]any)
	if wrappedCount["type"] != "integer" || wrappedCount["format"] != "int64" {
		t.Errorf("wrappedCount = %v, want integer+int64", wrappedCount)
	}
	wrappedPayload, _ := props["wrappedPayload"].(map[string]any)
	if wrappedPayload["type"] != "string" {
		t.Errorf("wrappedPayload.type = %v, want string", wrappedPayload["type"])
	}
	if _, hasEnc := wrappedPayload["contentEncoding"]; hasEnc {
		t.Error("wrappedPayload should not carry contentEncoding")
	}
}
