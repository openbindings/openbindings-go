package mcp

import (
	"context"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func TestInspectSourceExposesOnlyEligibleTools(t *testing.T) {
	server, _ := setupMCPServer(t)
	synthesizer := NewSynthesizer()
	result, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{
		BindingSpec: BindingSpec,
		Location:    server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Exhaustive || len(result.Targets) != 3 {
		t.Fatalf("inspection = %#v", result)
	}
	for _, target := range result.Targets {
		if !strings.HasPrefix(target.Ref, "tools/") || target.OperationKey == "" || target.Operation == nil {
			t.Fatalf("target = %#v", target)
		}
	}
}

func TestInspectSourceRefsMatchSynthesis(t *testing.T) {
	server, _ := setupMCPServer(t)
	synthesizer := NewSynthesizer()
	iface, err := synthesizer.SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{Sources: []openbindings.SynthesizeSource{{
		BindingSpec: BindingSpec,
		Location:    server.URL,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{BindingSpec: BindingSpec, Location: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	bindings := map[string]string{}
	for _, binding := range iface.Bindings {
		bindings[binding.Ref] = binding.Operation
	}
	for _, target := range inspection.Targets {
		if bindings[target.Ref] != target.OperationKey {
			t.Fatalf("target %#v does not match synthesized bindings %#v", target, bindings)
		}
	}
}

func TestInspectPinnedListingIsOfflineAndReportsExclusions(t *testing.T) {
	server, requests := deadEndServer(t)
	content := map[string]any{
		"tools":     []any{applicationTool("echo"), map[string]any{"name": "legacy"}},
		"resources": []any{map[string]any{"uri": "app://status", "name": "status"}},
		"prompts":   []any{map[string]any{"name": "greet"}},
	}
	synthesizer := NewSynthesizer()
	result, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{
		BindingSpec: BindingSpec,
		Location:    server.URL,
		Content:     mustContent(content),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Targets) != 1 || result.Targets[0].Ref != "tools/echo" || result.Targets[0].OperationKey != "echo" {
		t.Fatalf("targets = %#v", result.Targets)
	}
	if requests.Load() != 0 {
		t.Fatalf("pinned inspection made %d requests", requests.Load())
	}
}

func TestInspectSourceRefusesInvalidPinAndMissingLocation(t *testing.T) {
	server, requests := deadEndServer(t)
	synthesizer := NewSynthesizer()
	_, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{
		BindingSpec: BindingSpec,
		Location:    server.URL,
		Content:     mustContent(map[string]any{"nextCursor": "later"}),
	})
	if err == nil || !strings.Contains(err.Error(), "MCP-D-01") {
		t.Fatalf("invalid pin error = %v", err)
	}
	_, err = synthesizer.InspectSource(context.Background(), &openbindings.Source{
		BindingSpec: BindingSpec,
		Content:     mustContent(map[string]any{"tools": []any{applicationTool("echo")}}),
	})
	if err == nil || !strings.Contains(err.Error(), "MCP-D-02") {
		t.Fatalf("missing location error = %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("pre-I/O refusals made %d requests", requests.Load())
	}
}
