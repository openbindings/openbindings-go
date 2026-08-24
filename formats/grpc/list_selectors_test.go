package grpc

import (
	"context"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func TestInspectSource_BasicSelectors(t *testing.T) {
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

	synthesizer := NewSynthesizer()
	result, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{
		Content: openbindings.TextContent(protoContent),
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Targets) != 2 {
		t.Fatalf("expected 2 selectors, got %d", len(result.Targets))
	}
	if !result.Exhaustive {
		t.Error("expected Exhaustive = true")
	}
}

func TestInspectSource_SelectorFormat(t *testing.T) {
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

	synthesizer := NewSynthesizer()
	result, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{
		Content: openbindings.TextContent(protoContent),
	})
	if err != nil {
		t.Fatal(err)
	}

	wantSelectors := map[string]bool{
		"testpkg.TestService/GetItem":   false,
		"testpkg.TestService/ListItems": false,
	}
	for _, selector := range result.Targets {
		if _, ok := wantSelectors[selector.Selector]; ok {
			wantSelectors[selector.Selector] = true
		}
	}
	for selector, found := range wantSelectors {
		if !found {
			t.Errorf("expected selector %q not found", selector)
		}
	}
}

func TestInspectSource_SelectorsMatchSynthesizeInterface(t *testing.T) {
	disc := buildTestDiscovery(t, simpleServiceFile("testpkg", "TestService",
		unaryMethod("GetItem"),
		unaryMethod("ListItems"),
	))

	iface, err := convertToInterface(disc, "localhost:50051", nil)
	if err != nil {
		t.Fatal(err)
	}

	createSelectors := map[string]bool{}
	for _, b := range iface.Bindings {
		createSelectors[b.Selector] = true
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

	synthesizer := NewSynthesizer()
	result, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{
		Content: openbindings.TextContent(protoContent),
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, selector := range result.Targets {
		if !createSelectors[selector.Selector] {
			t.Errorf("InspectSource selector %q not in SynthesizeInterface bindings", selector.Selector)
		}
	}
	if len(result.Targets) != len(createSelectors) {
		t.Errorf("selector count mismatch: InspectSource=%d, SynthesizeInterface=%d", len(result.Targets), len(createSelectors))
	}
}

func TestInspectSource_IncludesClientStreaming(t *testing.T) {
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

	synthesizer := NewSynthesizer()
	result, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{
		Content: openbindings.TextContent(protoContent),
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Targets) != 2 {
		t.Fatalf("expected both selectors, got %d", len(result.Targets))
	}
	if result.Targets[0].Selector != "testpkg.TestService/GetItem" {
		t.Errorf("selector = %q, want testpkg.TestService/GetItem", result.Targets[0].Selector)
	}
	if result.Targets[1].Selector != "testpkg.TestService/StreamUpload" {
		t.Errorf("selector = %q, want testpkg.TestService/StreamUpload", result.Targets[1].Selector)
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

	synthesizer := NewSynthesizer()
	result, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{
		Content: openbindings.TextContent(protoContent),
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Targets) != 1 {
		t.Fatalf("expected 1 selector, got %d", len(result.Targets))
	}
	if result.Targets[0].Selector != "testpkg.TestService/WatchItems" {
		t.Errorf("selector = %q, want testpkg.TestService/WatchItems", result.Targets[0].Selector)
	}
}

func TestInspectSource_EmptySource(t *testing.T) {
	synthesizer := NewSynthesizer()
	_, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{})
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

	synthesizer := NewSynthesizer()
	result, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{
		Content: openbindings.TextContent(protoContent),
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Targets) != 3 {
		t.Fatalf("expected 3 selectors, got %d", len(result.Targets))
	}
	if result.Targets[0].Selector != "testpkg.TestService/Alpha" {
		t.Errorf("first selector = %q, want testpkg.TestService/Alpha", result.Targets[0].Selector)
	}
	if result.Targets[1].Selector != "testpkg.TestService/Mike" {
		t.Errorf("second selector = %q, want testpkg.TestService/Mike", result.Targets[1].Selector)
	}
	if result.Targets[2].Selector != "testpkg.TestService/Zulu" {
		t.Errorf("third selector = %q, want testpkg.TestService/Zulu", result.Targets[2].Selector)
	}
}
