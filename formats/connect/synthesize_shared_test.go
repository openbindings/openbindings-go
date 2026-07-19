package connect

import (
	"testing"

	"google.golang.org/protobuf/types/descriptorpb"
)

// sharedTypeServiceFile declares a message (Pair) that reuses one user-defined
// message type (Point) in two sibling fields (a, b). This is ordinary DAG
// reuse, not a cycle: the schema walker must emit the full Point schema in
// BOTH positions. A permanent `visited` set truncates the second occurrence to
// a bare {"type":"object"}.
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

// cyclicServiceFile declares a self-referential message (Node.next: Node). A
// true cycle must terminate as a bare placeholder object; this behavior is
// correct and must be preserved by the delete-on-unwind fix.
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
// type reused in sibling (non-cyclic) positions must carry the full schema in
// every position, not collapse to a bare {"type":"object"} after the first.
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

	for _, fieldName := range []string{"a", "b"} {
		field, ok := props[fieldName].(map[string]any)
		if !ok {
			t.Fatalf("expected %s property, got %v", fieldName, props)
		}
		fieldProps, ok := field["properties"].(map[string]any)
		if !ok {
			t.Errorf("field %q truncated to a property-less object (shared-type reuse mistaken for a cycle): %v", fieldName, field)
			continue
		}
		if _, ok := fieldProps["label"]; !ok {
			t.Errorf("field %q is missing Point.label; got %v", fieldName, fieldProps)
		}
	}
}

// TestConvertToInterface_CyclePlaceholderPreserved asserts a true cycle still
// terminates as a bare placeholder object (never recurses forever, never
// carries nested properties). This must hold both before and after the fix.
func TestConvertToInterface_CyclePlaceholderPreserved(t *testing.T) {
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
	next, ok := props["next"].(map[string]any)
	if !ok {
		t.Fatalf("expected next property, got %v", props)
	}
	if _, hasProps := next["properties"]; hasProps {
		t.Errorf("cyclic self-reference should be a bare placeholder, got %v", next)
	}
	if next["type"] != "object" {
		t.Errorf("cycle placeholder type = %v, want object", next["type"])
	}
}
