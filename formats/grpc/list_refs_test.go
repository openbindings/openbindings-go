package grpc

import (
	"context"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func TestInspectSource_BasicRefs(t *testing.T) {
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
	result, err := creator.InspectSource(context.Background(), &openbindings.Source{
		Content: protoContent,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Targets) != 2 {
		t.Fatalf("expected 2 refs, got %d", len(result.Targets))
	}
	if !result.Exhaustive {
		t.Error("expected Exhaustive = true")
	}
}

func TestInspectSource_RefFormat(t *testing.T) {
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
	result, err := creator.InspectSource(context.Background(), &openbindings.Source{
		Content: protoContent,
	})
	if err != nil {
		t.Fatal(err)
	}

	wantRefs := map[string]bool{
		"testpkg.TestService/GetItem":   false,
		"testpkg.TestService/ListItems": false,
	}
	for _, ref := range result.Targets {
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

func TestInspectSource_RefsMatchCreateInterface(t *testing.T) {
	disc := buildTestDiscovery(t, simpleServiceFile("testpkg", "TestService",
		unaryMethod("GetItem"),
		unaryMethod("ListItems"),
	))

	iface, err := convertToInterface(disc, "localhost:50051", nil)
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
	result, err := creator.InspectSource(context.Background(), &openbindings.Source{
		Content: protoContent,
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, ref := range result.Targets {
		if !createRefs[ref.Ref] {
			t.Errorf("InspectSource ref %q not in CreateInterface bindings", ref.Ref)
		}
	}
	if len(result.Targets) != len(createRefs) {
		t.Errorf("ref count mismatch: InspectSource=%d, CreateInterface=%d", len(result.Targets), len(createRefs))
	}
}

func TestInspectSource_FiltersClientStreaming(t *testing.T) {
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
	result, err := creator.InspectSource(context.Background(), &openbindings.Source{
		Content: protoContent,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Targets) != 1 {
		t.Fatalf("expected 1 ref (client streaming filtered), got %d", len(result.Targets))
	}
	if result.Targets[0].Ref != "testpkg.TestService/GetItem" {
		t.Errorf("ref = %q, want testpkg.TestService/GetItem", result.Targets[0].Ref)
	}
}

func TestInspectSource_IncludesServerStreaming(t *testing.T) {
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
	result, err := creator.InspectSource(context.Background(), &openbindings.Source{
		Content: protoContent,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Targets) != 1 {
		t.Fatalf("expected 1 ref, got %d", len(result.Targets))
	}
	if result.Targets[0].Ref != "testpkg.TestService/WatchItems" {
		t.Errorf("ref = %q, want testpkg.TestService/WatchItems", result.Targets[0].Ref)
	}
}

func TestInspectSource_EmptySource(t *testing.T) {
	creator := NewCreator()
	_, err := creator.InspectSource(context.Background(), &openbindings.Source{})
	if err == nil {
		t.Error("expected error for empty source")
	}
}

func TestInspectSource_Sorted(t *testing.T) {
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
	result, err := creator.InspectSource(context.Background(), &openbindings.Source{
		Content: protoContent,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Targets) != 3 {
		t.Fatalf("expected 3 refs, got %d", len(result.Targets))
	}
	if result.Targets[0].Ref != "testpkg.TestService/Alpha" {
		t.Errorf("first ref = %q, want testpkg.TestService/Alpha", result.Targets[0].Ref)
	}
	if result.Targets[1].Ref != "testpkg.TestService/Mike" {
		t.Errorf("second ref = %q, want testpkg.TestService/Mike", result.Targets[1].Ref)
	}
	if result.Targets[2].Ref != "testpkg.TestService/Zulu" {
		t.Errorf("third ref = %q, want testpkg.TestService/Zulu", result.Targets[2].Ref)
	}
}
