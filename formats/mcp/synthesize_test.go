package mcp

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"
	openbindings "github.com/openbindings/openbindings-go"
)

func TestListingCapturePreservesEntitiesWithoutPaginationCarriage(t *testing.T) {
	capture := newListingCapture()
	capture.observe("tools/list", []byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"first","inputSchema":{"type":"object"},"outputSchema":{"type":"object"},"title":"First"}],"nextCursor":"p2"}}`))
	capture.observe("tools/list", []byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"second","inputSchema":{"type":"object"},"outputSchema":{"type":"object"}}]}}`))
	content := capture.content()
	var pin struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(content, &pin); err != nil {
		t.Fatal(err)
	}
	if len(pin.Tools) != 2 || pin.Tools[0]["title"] != "First" || strings.Contains(string(content), "nextCursor") {
		t.Fatalf("captured listing = %s", content)
	}
}

func TestConvertToInterfaceProjectsOnlyEligibleToolApplicationContracts(t *testing.T) {
	outputSchema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"result": map[string]any{"type": "number"}},
		"required":   []any{"result"},
	}
	discovery := &discovery{
		ServerInfo: &gomcp.Implementation{Name: "test-server", Version: "1.0.0", Title: "Test Server"},
		Tools: []*gomcp.Tool{
			{Name: "calc", Description: "Calculate", InputSchema: map[string]any{"type": "object"}, OutputSchema: outputSchema},
			{Name: "legacy", InputSchema: map[string]any{"type": "object"}},
			{Name: "task", InputSchema: map[string]any{"type": "object"}, OutputSchema: map[string]any{"type": "object"}},
		},
		RequiredTaskTools: map[string]bool{"task": true},
		Resources:         []*gomcp.Resource{{Name: "config", URI: "app://config"}},
		Prompts:           []*gomcp.Prompt{{Name: "review"}},
	}
	iface, err := convertToInterface(discovery, "https://mcp.example.test", BindingSpec)
	if err != nil {
		t.Fatal(err)
	}
	if iface.Name != "test-server" || iface.Version != "1.0.0" || iface.Description != "Test Server" {
		t.Fatalf("metadata = %#v", iface)
	}
	if len(iface.Operations) != 1 || !reflect.DeepEqual(iface.Operations["calc"].Output, outputSchema) {
		t.Fatalf("operations = %#v", iface.Operations)
	}
	if binding := iface.Bindings["calc."+DefaultSourceName]; binding.Ref != "tools/calc" {
		t.Fatalf("binding = %#v", binding)
	}
	if source := iface.Sources[DefaultSourceName]; source.BindingSpec != BindingSpec || source.Location != "https://mcp.example.test" {
		t.Fatalf("source = %#v", source)
	}
}

func TestConvertToInterfaceUsesDeterministicEligibleToolKeys(t *testing.T) {
	schema := map[string]any{"type": "object"}
	iface, err := convertToInterface(&discovery{Tools: []*gomcp.Tool{
		{Name: "a b", OutputSchema: schema},
		{Name: "a_b", OutputSchema: schema},
		{Name: "2fa", OutputSchema: schema},
	}}, "")
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(iface.Operations))
	for key := range iface.Operations {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if !reflect.DeepEqual(keys, []string{"_2fa", "a_b", "tool_a_b"}) {
		t.Fatalf("keys = %#v", keys)
	}
}

func TestSynthesisCoverageMakesEveryExclusionVisible(t *testing.T) {
	discovery := &discovery{
		Tools: []*gomcp.Tool{
			{Name: "ok", OutputSchema: map[string]any{"type": "object"}},
			{Name: "missing"},
			{Name: "task", OutputSchema: map[string]any{"type": "object"}},
		},
		RequiredTaskTools: map[string]bool{"task": true},
		Resources:         []*gomcp.Resource{{Name: "resource", URI: "app://x"}},
		Prompts:           []*gomcp.Prompt{{Name: "prompt"}},
	}
	iface, err := convertToInterface(discovery, "")
	if err != nil {
		t.Fatal(err)
	}
	entries := synthesisCoverage(discovery, iface)
	if len(entries) != 5 {
		t.Fatalf("coverage = %#v", entries)
	}
	statuses := map[string]openbindings.SynthesisCoverageStatus{}
	for _, entry := range entries {
		statuses[entry.SourceRef] = entry.Status
	}
	if statuses["tools/ok"] != openbindings.SynthesisRepresented ||
		statuses["tools/missing"] != openbindings.SynthesisExcluded ||
		statuses["tools/task"] != openbindings.SynthesisExcluded ||
		statuses["resources/app://x"] != openbindings.SynthesisExcluded ||
		statuses["prompts/prompt"] != openbindings.SynthesisExcluded {
		t.Fatalf("coverage statuses = %#v", statuses)
	}
}
