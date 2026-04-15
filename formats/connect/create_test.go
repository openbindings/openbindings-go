package connect

import (
	"testing"
)

func TestConvertToInterface_CreatesOperations(t *testing.T) {
	disc, err := discoverFromProto("", `
syntax = "proto3";
package testpkg;

message Request { string id = 1; }
message Response { string value = 1; }

service TestService {
  rpc GetItem(Request) returns (Response);
  rpc ListItems(Request) returns (Response);
}
`)
	if err != nil {
		t.Fatal(err)
	}

	iface, err := convertToInterface(disc, "http://localhost:8080")
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
	disc, err := discoverFromProto("", `
syntax = "proto3";
package testpkg;

message Request { string id = 1; }
message Response { string value = 1; }

service TestService {
  rpc GetItem(Request) returns (Response);
}
`)
	if err != nil {
		t.Fatal(err)
	}

	iface, err := convertToInterface(disc, "http://localhost:8080")
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

func TestConvertToInterface_SkipsClientStreaming(t *testing.T) {
	disc, err := discoverFromProto("", `
syntax = "proto3";
package testpkg;

message Request { string id = 1; }
message Response { string value = 1; }

service TestService {
  rpc GetItem(Request) returns (Response);
  rpc StreamUpload(stream Request) returns (Response);
}
`)
	if err != nil {
		t.Fatal(err)
	}

	iface, err := convertToInterface(disc, "http://localhost:8080")
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

func TestConvertToInterface_SourceEntry(t *testing.T) {
	disc, err := discoverFromProto("", `
syntax = "proto3";
package testpkg;

message Request {}
message Response {}

service TestService {
  rpc DoSomething(Request) returns (Response);
}
`)
	if err != nil {
		t.Fatal(err)
	}

	iface, err := convertToInterface(disc, "http://api.example.com")
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
	if src.Location != "http://api.example.com" {
		t.Errorf("location = %q, want %q", src.Location, "http://api.example.com")
	}
}

func TestConvertToInterface_NilDiscovery(t *testing.T) {
	_, err := convertToInterface(nil, "http://localhost:8080")
	if err == nil {
		t.Error("expected error for nil discovery")
	}
}

func TestConvertToInterface_InputOutputSchemas(t *testing.T) {
	disc, err := discoverFromProto("", `
syntax = "proto3";
package testpkg;

message GetItemRequest { string id = 1; }
message GetItemResponse { string name = 1; }

service TestService {
  rpc GetItem(GetItemRequest) returns (GetItemResponse);
}
`)
	if err != nil {
		t.Fatal(err)
	}

	iface, err := convertToInterface(disc, "http://localhost:8080")
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
	if op.Output == nil {
		t.Fatal("expected output schema")
	}
	if op.Output["type"] != "object" {
		t.Errorf("output type = %v, want object", op.Output["type"])
	}
}
