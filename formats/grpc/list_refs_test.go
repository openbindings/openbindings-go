package grpc

import (
	"context"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func TestListBindableRefs_BasicRefs(t *testing.T) {
	protoContent := `
syntax = "proto3";
package testpkg;

message Request { string id = 1; }
message Response { string value = 1; }

service TestService {
  rpc GetItem(Request) returns (Response);
  rpc ListItems(Request) returns (Response);
}
`

	creator := NewCreator()
	result, err := creator.ListBindableRefs(context.Background(), &openbindings.Source{
		Content: protoContent,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Refs) != 2 {
		t.Fatalf("expected 2 refs, got %d", len(result.Refs))
	}
	if !result.Exhaustive {
		t.Error("expected Exhaustive = true")
	}
}

func TestListBindableRefs_RefFormat(t *testing.T) {
	protoContent := `
syntax = "proto3";
package testpkg;

message Request { string id = 1; }
message Response { string value = 1; }

service TestService {
  rpc GetItem(Request) returns (Response);
  rpc ListItems(Request) returns (Response);
}
`

	creator := NewCreator()
	result, err := creator.ListBindableRefs(context.Background(), &openbindings.Source{
		Content: protoContent,
	})
	if err != nil {
		t.Fatal(err)
	}

	wantRefs := map[string]bool{
		"testpkg.TestService/GetItem":   false,
		"testpkg.TestService/ListItems": false,
	}
	for _, ref := range result.Refs {
		if _, ok := wantRefs[ref.Ref]; ok {
			wantRefs[ref.Ref] = true
		}
	}
	for ref, found := range wantRefs {
		if !found {
			t.Errorf("expected ref %q not found", ref)
		}
	}
}

func TestListBindableRefs_RefsMatchCreateInterface(t *testing.T) {
	disc := buildTestDiscovery(t, simpleServiceFile("testpkg", "TestService",
		unaryMethod("GetItem"),
		unaryMethod("ListItems"),
	))

	iface, err := convertToInterface(disc, "localhost:50051")
	if err != nil {
		t.Fatal(err)
	}

	createRefs := map[string]bool{}
	for _, b := range iface.Bindings {
		createRefs[b.Ref] = true
	}

	protoContent := `
syntax = "proto3";
package testpkg;

message Request { string id = 1; }
message Response { string value = 1; }

service TestService {
  rpc GetItem(Request) returns (Response);
  rpc ListItems(Request) returns (Response);
}
`

	creator := NewCreator()
	result, err := creator.ListBindableRefs(context.Background(), &openbindings.Source{
		Content: protoContent,
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, ref := range result.Refs {
		if !createRefs[ref.Ref] {
			t.Errorf("ListBindableRefs ref %q not in CreateInterface bindings", ref.Ref)
		}
	}
	if len(result.Refs) != len(createRefs) {
		t.Errorf("ref count mismatch: ListBindableRefs=%d, CreateInterface=%d", len(result.Refs), len(createRefs))
	}
}

func TestListBindableRefs_FiltersClientStreaming(t *testing.T) {
	protoContent := `
syntax = "proto3";
package testpkg;

message Request { string id = 1; }
message Response { string value = 1; }

service TestService {
  rpc GetItem(Request) returns (Response);
  rpc StreamUpload(stream Request) returns (Response);
}
`

	creator := NewCreator()
	result, err := creator.ListBindableRefs(context.Background(), &openbindings.Source{
		Content: protoContent,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Refs) != 1 {
		t.Fatalf("expected 1 ref (client streaming filtered), got %d", len(result.Refs))
	}
	if result.Refs[0].Ref != "testpkg.TestService/GetItem" {
		t.Errorf("ref = %q, want testpkg.TestService/GetItem", result.Refs[0].Ref)
	}
}

func TestListBindableRefs_IncludesServerStreaming(t *testing.T) {
	protoContent := `
syntax = "proto3";
package testpkg;

message Request { string id = 1; }
message Response { string value = 1; }

service TestService {
  rpc WatchItems(Request) returns (stream Response);
}
`

	creator := NewCreator()
	result, err := creator.ListBindableRefs(context.Background(), &openbindings.Source{
		Content: protoContent,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Refs) != 1 {
		t.Fatalf("expected 1 ref, got %d", len(result.Refs))
	}
	if result.Refs[0].Ref != "testpkg.TestService/WatchItems" {
		t.Errorf("ref = %q, want testpkg.TestService/WatchItems", result.Refs[0].Ref)
	}
}

func TestListBindableRefs_EmptySource(t *testing.T) {
	creator := NewCreator()
	_, err := creator.ListBindableRefs(context.Background(), &openbindings.Source{})
	if err == nil {
		t.Error("expected error for empty source")
	}
}

func TestListBindableRefs_Sorted(t *testing.T) {
	protoContent := `
syntax = "proto3";
package testpkg;

message Request { string id = 1; }
message Response { string value = 1; }

service TestService {
  rpc Zulu(Request) returns (Response);
  rpc Alpha(Request) returns (Response);
  rpc Mike(Request) returns (Response);
}
`

	creator := NewCreator()
	result, err := creator.ListBindableRefs(context.Background(), &openbindings.Source{
		Content: protoContent,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Refs) != 3 {
		t.Fatalf("expected 3 refs, got %d", len(result.Refs))
	}
	if result.Refs[0].Ref != "testpkg.TestService/Alpha" {
		t.Errorf("first ref = %q, want testpkg.TestService/Alpha", result.Refs[0].Ref)
	}
	if result.Refs[1].Ref != "testpkg.TestService/Mike" {
		t.Errorf("second ref = %q, want testpkg.TestService/Mike", result.Refs[1].Ref)
	}
	if result.Refs[2].Ref != "testpkg.TestService/Zulu" {
		t.Errorf("third ref = %q, want testpkg.TestService/Zulu", result.Refs[2].Ref)
	}
}
