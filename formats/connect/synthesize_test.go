package connect

import (
	"context"
	"testing"

	"github.com/openbindings/openbindings-go/synthesize"

	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	openbindings "github.com/openbindings/openbindings-go"
)

func protoSchemaVariant(t *testing.T, value any, schemaType string) map[string]any {
	t.Helper()
	schema, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("schema is %T, want object: %v", value, value)
	}
	if schema["type"] == schemaType {
		return schema
	}
	if variants, ok := schema["anyOf"].([]any); ok {
		for _, variant := range variants {
			if candidate, ok := variant.(map[string]any); ok {
				if candidate["type"] == schemaType {
					return candidate
				}
				if nested, ok := candidate["anyOf"].([]any); ok {
					for _, member := range nested {
						if result, ok := member.(map[string]any); ok && result["type"] == schemaType {
							return result
						}
					}
				}
			}
		}
	}
	t.Fatalf("schema has no %q variant: %v", schemaType, schema)
	return nil
}

func TestConvertToInterface_CreatesOperations(t *testing.T) {
	disc, err := discoverFromProto(context.Background(), "", openbindings.TextContent(`
syntax = "proto3";
package testpkg;

message Request { string id = 1; }
message Response { string value = 1; }

service TestService {
  rpc GetItem(Request) returns (Response);
  rpc ListItems(Request) returns (Response);
}
`))
	if err != nil {
		t.Fatal(err)
	}

	iface, err := convertToInterface(disc, "http://localhost:8080", nil)
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
	disc, err := discoverFromProto(context.Background(), "", openbindings.TextContent(`
syntax = "proto3";
package testpkg;

message Request { string id = 1; }
message Response { string value = 1; }

service TestService {
  rpc GetItem(Request) returns (Response);
}
`))
	if err != nil {
		t.Fatal(err)
	}

	iface, err := convertToInterface(disc, "http://localhost:8080", nil)
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

func TestConvertToInterface_IncludesClientStreaming(t *testing.T) {
	disc, err := discoverFromProto(context.Background(), "", openbindings.TextContent(`
syntax = "proto3";
package testpkg;

message Request { string id = 1; }
message Response { string value = 1; }

service TestService {
  rpc GetItem(Request) returns (Response);
  rpc StreamUpload(stream Request) returns (Response);
}
`))
	if err != nil {
		t.Fatal(err)
	}

	iface, err := convertToInterface(disc, "http://localhost:8080", nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(iface.Operations) != 2 {
		t.Fatalf("expected both binding-spec-supported operations, got %d", len(iface.Operations))
	}
	if _, ok := iface.Operations["GetItem"]; !ok {
		t.Error("expected operation 'GetItem'")
	}
	if _, ok := iface.Operations["StreamUpload"]; !ok {
		t.Error("expected operation 'StreamUpload'")
	}
}

func TestConvertToInterface_SourceEntry(t *testing.T) {
	disc, err := discoverFromProto(context.Background(), "", openbindings.TextContent(`
syntax = "proto3";
package testpkg;

message Request {}
message Response {}

service TestService {
  rpc DoSomething(Request) returns (Response);
}
`))
	if err != nil {
		t.Fatal(err)
	}

	iface, err := convertToInterface(disc, "http://api.example.com", nil)
	if err != nil {
		t.Fatal(err)
	}

	src, ok := iface.Sources[DefaultSourceName]
	if !ok {
		t.Fatal("expected source entry")
	}
	if src.BindingSpec != BindingSpec {
		t.Errorf("format = %q, want %q", src.BindingSpec, BindingSpec)
	}
	if src.Location != "http://api.example.com" {
		t.Errorf("location = %q, want %q", src.Location, "http://api.example.com")
	}
}

func TestConvertToInterface_NilDiscovery(t *testing.T) {
	_, err := convertToInterface(nil, "http://localhost:8080", nil)
	if err == nil {
		t.Error("expected error for nil discovery")
	}
}

func TestConvertToInterface_InputOutputSchemas(t *testing.T) {
	disc, err := discoverFromProto(context.Background(), "", openbindings.TextContent(`
syntax = "proto3";
package testpkg;

message GetItemRequest { string id = 1; }
message GetItemResponse { string name = 1; }

service TestService {
  rpc GetItem(GetItemRequest) returns (GetItemResponse);
}
`))
	if err != nil {
		t.Fatal(err)
	}

	iface, err := convertToInterface(disc, "http://localhost:8080", nil)
	if err != nil {
		t.Fatal(err)
	}

	op := iface.Operations["GetItem"]
	if op.Input == nil {
		t.Fatal("expected input schema")
	}
	if op.Input.(map[string]any)["type"] != "object" {
		t.Errorf("input type = %v, want object", op.Input.(map[string]any)["type"])
	}
	if op.Output == nil {
		t.Fatal("expected output schema")
	}
	if op.Output.(map[string]any)["type"] != "object" {
		t.Errorf("output type = %v, want object", op.Output.(map[string]any)["type"])
	}
}

// ---------- synthesis fidelity parity with formats/grpc ----------
//
// The tests below assert the exact shapes formats/grpc's schemaWalker
// produces for the same constructs (well-known types, oneof, int64), proving
// this package's translation matches grpc's rather than diverging (previously
// connect emitted int64 as a bare JSON string and had no WKT/oneof handling
// at all). Both packages implement the identical proto3-JSON mapping, so the
// expected values below are transcribed from formats/grpc/synthesize_test.go.

func ptr[T any](v T) *T { return &v }

// buildTestDiscovery creates a discovery with the given services for testing.
// Files are registered into a shared protoregistry so later files can
// resolve dependencies declared by earlier ones (used for the well-known-type
// fixture below, which protocompile's content-mode resolver — unlike a real
// buf/protoc import path — cannot resolve from `import` statements alone).
func buildTestDiscovery(t *testing.T, files ...*descriptorpb.FileDescriptorProto) *discovery {
	t.Helper()
	disc := &discovery{}
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

// timestampFile returns a synthetic FileDescriptorProto for
// google.protobuf.Timestamp so tests can reference it as a field type
// without depending on protocompile resolving the real import.
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

func TestWellKnownSchema_Timestamp(t *testing.T) {
	got := wellKnownSchema("google.protobuf.Timestamp")
	if got["type"] != "string" || got["format"] != "date-time" {
		t.Errorf("got %v, want {type: string, format: date-time}", got)
	}
}

func TestWellKnownSchema_Struct(t *testing.T) {
	got := wellKnownSchema("google.protobuf.Struct")
	if got["type"] != "object" {
		t.Errorf("got %v, want {type: object}", got)
	}
}

func TestWellKnownSchema_Int64Wrappers(t *testing.T) {
	for _, fqn := range []string{"google.protobuf.Int64Value", "google.protobuf.UInt64Value"} {
		got := protoSchemaVariant(t, wellKnownSchema(fqn), "integer")
		if got["type"] != "integer" {
			t.Errorf("%s: type = %v, want integer", fqn, got["type"])
		}
		if got["format"] != "int64" {
			t.Errorf("%s: format = %v, want int64", fqn, got["format"])
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
	if atType, ok := props["@type"].(map[string]any); !ok || atType["type"] != "string" {
		t.Errorf("@type property = %v, want {type: string}", props["@type"])
	}
	req, ok := got["required"].([]any)
	if !ok || len(req) != 1 || req[0] != "@type" {
		t.Errorf("required = %v, want [@type]", got["required"])
	}
}

func TestWellKnownSchema_NotWellKnown(t *testing.T) {
	for _, fqn := range []string{"testpkg.Request", "google.protobuf.ThisDoesNotExist", ""} {
		if got := wellKnownSchema(fqn); got != nil {
			t.Errorf("%q: got %v, want nil", fqn, got)
		}
	}
}

// A Timestamp-typed field must emit the canonical string/date-time shape,
// not a traversal into Timestamp's own seconds/nanos fields.
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

	disc := buildTestDiscovery(t, wkFDP, useFDP)
	iface, err := convertToInterface(disc, "http://localhost:8080", nil)
	if err != nil {
		t.Fatal(err)
	}

	op := iface.Operations["GetItem"]
	props, ok := op.Input.(map[string]any)["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected input properties")
	}
	createdAt := protoSchemaVariant(t, props["createdAt"], "string")
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

// int64-family fields must emit {"type":"integer","format":"int64"}, matching
// grpc — not the bare {"type":"string"} connect previously emitted.
func TestConvertToInterface_Int64Field(t *testing.T) {
	disc, err := discoverFromProto(context.Background(), "", openbindings.TextContent(`
syntax = "proto3";
package testpkg;

message Request { int64 count = 1; }
message Response { string value = 1; }

service TestService {
  rpc GetItem(Request) returns (Response);
}
`))
	if err != nil {
		t.Fatal(err)
	}

	iface, err := convertToInterface(disc, "http://localhost:8080", nil)
	if err != nil {
		t.Fatal(err)
	}

	props, ok := iface.Operations["GetItem"].Input.(map[string]any)["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected input properties")
	}
	count := protoSchemaVariant(t, props["count"], "integer")
	if count["type"] != "integer" {
		t.Errorf("count.type = %v, want integer", count["type"])
	}
	if count["format"] != "int64" {
		t.Errorf("count.format = %v, want int64", count["format"])
	}
}

func TestConvertToInterface_OneofSingleGroupProjectsOptionalMembers(t *testing.T) {
	disc, err := discoverFromProto(context.Background(), "", openbindings.TextContent(`
syntax = "proto3";
package testpkg;

message Request {
  oneof identifier {
    string item_id = 1;
    int32 item_index = 2;
  }
}
message Response { string value = 1; }

service TestService {
  rpc GetItem(Request) returns (Response);
}
`))
	if err != nil {
		t.Fatal(err)
	}

	iface, err := convertToInterface(disc, "http://localhost:8080", nil)
	if err != nil {
		t.Fatal(err)
	}

	input, _ := iface.Operations["GetItem"].Input.(map[string]any)
	if _, ok := input["oneOf"]; ok {
		t.Fatal("ProtoJSON oneof is at-most-one and may be unset; an exactly-one schema is invalid")
	}
	props, ok := input["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected oneof members as optional properties")
	}
	for _, name := range []string{"itemId", "item_id", "itemIndex", "item_index"} {
		if _, present := props[name]; !present {
			t.Errorf("missing accepted ProtoJSON spelling %q", name)
		}
	}
}

// Multiple oneof groups cannot be expressed as top-level oneOf under the
// v0.1 schema profile (no oneOf-inside-allOf); members fall back to
// independent optional properties and a warning surfaces the loss, exactly
// as formats/grpc does.
func TestConvertToInterface_OneofMultipleGroupsFallsBackToProperties(t *testing.T) {
	disc, err := discoverFromProto(context.Background(), "", openbindings.TextContent(`
syntax = "proto3";
package testpkg;

message Request {
  oneof identifier {
    string item_id = 1;
    int32 item_index = 2;
  }
  oneof payload {
    string text_payload = 3;
    bytes binary_payload = 4;
  }
}
message Response { string value = 1; }

service TestService {
  rpc GetItem(Request) returns (Response);
}
`))
	if err != nil {
		t.Fatal(err)
	}

	var warnings []synthesize.SynthesizerWarning
	iface, err := convertToInterface(disc, "http://localhost:8080", func(w synthesize.SynthesizerWarning) {
		warnings = append(warnings, w)
	})
	if err != nil {
		t.Fatal(err)
	}

	input, _ := iface.Operations["GetItem"].Input.(map[string]any)
	if _, ok := input["oneOf"]; ok {
		t.Error("multi-group oneof must not emit top-level oneOf")
	}
	props, ok := input["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties")
	}
	for _, want := range []string{"itemId", "itemIndex", "textPayload", "binaryPayload"} {
		if _, ok := props[want]; !ok {
			t.Errorf("expected fallback property %q", want)
		}
	}

	if len(warnings) != 0 {
		t.Fatalf("exact null-aware oneof constraints should need no warning, got %+v", warnings)
	}
	if constraints, _ := input["allOf"].([]any); len(constraints) == 0 {
		t.Error("multi-group oneof schema does not enforce at-most-one constraints")
	}
}

// Exact oneof projection should not turn a sound synthesized operation into a
// lossy warning on the public synthesis surface.
func TestSynthesizeInterface_ExactOneofNeedsNoWarning(t *testing.T) {
	proto := `
syntax = "proto3";
package testpkg;

message Request {
  oneof identifier {
    string item_id = 1;
    int32 item_index = 2;
  }
  oneof payload {
    string text_payload = 3;
    bytes binary_payload = 4;
  }
}

message Response { string value = 1; }

service TestService {
  rpc GetItem(Request) returns (Response);
}
`
	var warnings []synthesize.SynthesizerWarning
	c := NewSynthesizer()
	_, err := c.SynthesizeInterface(context.Background(), &synthesize.SynthesizeInput{
		Sources: []synthesize.SynthesizeSource{{BindingSpec: BindingSpec, Location: "https://connect.example.test", Content: openbindings.TextContent(proto)}},
		OnWarning: func(w synthesize.SynthesizerWarning) {
			warnings = append(warnings, w)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("exact oneof projection emitted warnings: %+v", warnings)
	}
}

func TestSynthesizeInterfaceWithCoverage_AccountsForSchemaRangeExclusion(t *testing.T) {
	proto := `
syntax = "proto2";
package testpkg;
message Good { optional string value = 1; }
message Bad { required string value = 1; }
service TestService {
  rpc Accepted(Good) returns (Good);
  rpc Excluded(Bad) returns (Good);
}`
	result, err := NewSynthesizer().SynthesizeInterfaceWithCoverage(context.Background(), &synthesize.SynthesizeInput{
		Sources: []synthesize.SynthesizeSource{{
			BindingSpec: BindingSpec,
			Location:    "https://connect.example.test",
			Content:     openbindings.TextContent(proto),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(result.Interface.Bindings); got != 1 {
		t.Fatalf("bindings = %d, want 1", got)
	}
	if result.Coverage.FullyRepresented {
		t.Fatal("coverage with a binding-spec exclusion must not be fully represented")
	}
	if got := len(result.Coverage.Entries); got != 2 {
		t.Fatalf("coverage entries = %d, want 2: %+v", got, result.Coverage.Entries)
	}
	if entry := result.Coverage.Entries[0]; entry.SourceRef != "testpkg.TestService/Accepted" || entry.Status != synthesize.SynthesisRepresented {
		t.Fatalf("accepted entry = %+v", entry)
	}
	if entry := result.Coverage.Entries[1]; entry.SourceRef != "testpkg.TestService/Excluded" || entry.Status != synthesize.SynthesisExcluded || entry.ReasonCode != "connect.schema_range" || entry.Rule != "CONN-P-02" {
		t.Fatalf("excluded entry = %+v", entry)
	}
}
