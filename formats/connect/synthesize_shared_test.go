package connect

import (
	"testing"

	"google.golang.org/protobuf/types/descriptorpb"
)

// sharedTypeServiceFile declares a message (Pair) that reuses one user-defined
// message type (Point) in two sibling fields (a, b). Both fields must point to
// the same complete definition.
func sharedTypeServiceFile() *descriptorpb.FileDescriptorProto {
	return &descriptorpb.FileDescriptorProto{
		Name:    ptr("shared.proto"),
		Package: ptr("testpkg"),
		Syntax:  ptr("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: ptr("Point"), Field: []*descriptorpb.FieldDescriptorProto{
				{Name: ptr("label"), Number: ptr(int32(1)), Type: ptr(descriptorpb.FieldDescriptorProto_TYPE_STRING),
					JsonName: ptr("label"), Label: ptr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL)},
			}},
			{Name: ptr("Pair"), Field: []*descriptorpb.FieldDescriptorProto{
				{Name: ptr("a"), Number: ptr(int32(1)),
					Type:     ptr(descriptorpb.FieldDescriptorProto_TYPE_MESSAGE),
					TypeName: ptr(".testpkg.Point"), JsonName: ptr("a"),
					Label: ptr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL)},
				{Name: ptr("b"), Number: ptr(int32(2)),
					Type:     ptr(descriptorpb.FieldDescriptorProto_TYPE_MESSAGE),
					TypeName: ptr(".testpkg.Point"), JsonName: ptr("b"),
					Label: ptr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL)},
			}},
			{Name: ptr("Empty")},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{
			{Name: ptr("SharedService"), Method: []*descriptorpb.MethodDescriptorProto{
				{Name: ptr("GetPair"), InputType: ptr(".testpkg.Empty"), OutputType: ptr(".testpkg.Pair")},
			}},
		},
	}
}

// cyclicServiceFile declares a self-referential message (Node.next: Node). Its
// schema must terminate structurally while preserving the recursive contract.
func cyclicServiceFile() *descriptorpb.FileDescriptorProto {
	return &descriptorpb.FileDescriptorProto{
		Name:    ptr("cyclic.proto"),
		Package: ptr("testpkg"),
		Syntax:  ptr("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: ptr("Node"), Field: []*descriptorpb.FieldDescriptorProto{
				{Name: ptr("val"), Number: ptr(int32(1)), Type: ptr(descriptorpb.FieldDescriptorProto_TYPE_STRING),
					JsonName: ptr("val"), Label: ptr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL)},
				{Name: ptr("next"), Number: ptr(int32(2)),
					Type:     ptr(descriptorpb.FieldDescriptorProto_TYPE_MESSAGE),
					TypeName: ptr(".testpkg.Node"), JsonName: ptr("next"),
					Label: ptr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL)},
			}},
			{Name: ptr("Empty")},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{
			{Name: ptr("NodeService"), Method: []*descriptorpb.MethodDescriptorProto{
				{Name: ptr("GetNode"), InputType: ptr(".testpkg.Empty"), OutputType: ptr(".testpkg.Node")},
			}},
		},
	}
}

// TestConvertToInterface_SharedTypeNotTruncated pins the F1/C8f fix: a message
// type reused in sibling (non-cyclic) positions must point to one complete
// shared definition, not collapse to a bare {"type":"object"}.
func TestConvertToInterface_SharedTypeNotTruncated(t *testing.T) {
	disc := buildTestDiscovery(t, sharedTypeServiceFile())
	iface, err := convertToInterface(disc, "http://localhost:8080", nil)
	if err != nil {
		t.Fatal(err)
	}

	out, ok := iface.Operations["GetPair"].Output.(map[string]any)
	if !ok {
		t.Fatal("expected Pair output schema")
	}
	props, ok := out["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected Pair properties, got %v", out)
	}

	defs, _ := out["$defs"].(map[string]any)
	point, _ := defs["testpkg.Point"].(map[string]any)
	pointProps, _ := point["properties"].(map[string]any)
	if _, ok := pointProps["label"]; !ok {
		t.Fatalf("shared Point definition is missing label: %v", point)
	}
	wantRef := out["$id"].(string) + "#/$defs/testpkg.Point"
	for _, fieldName := range []string{"a", "b"} {
		field, _ := props[fieldName].(map[string]any)
		if field["$ref"] != wantRef {
			t.Errorf("field %q ref = %v, want %q", fieldName, field["$ref"], wantRef)
		}
	}
}

// TestConvertToInterface_CyclePreservedByReference asserts a true cycle
// terminates while preserving the recursive contract through a local schema
// resource rather than broadening it to an unconstrained object.
func TestConvertToInterface_CyclePreservedByReference(t *testing.T) {
	disc := buildTestDiscovery(t, cyclicServiceFile())
	iface, err := convertToInterface(disc, "http://localhost:8080", nil)
	if err != nil {
		t.Fatal(err)
	}

	out, ok := iface.Operations["GetNode"].Output.(map[string]any)
	if !ok {
		t.Fatal("expected Node output schema")
	}
	props, ok := out["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected Node properties, got %v", out)
	}
	next, _ := props["next"].(map[string]any)
	wantRef := out["$id"].(string) + "#/$defs/testpkg.Node"
	if next["$ref"] != wantRef {
		t.Errorf("recursive next ref = %v, want %q", next["$ref"], wantRef)
	}
}
