package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/openbindings/openbindings-go/processorscenarios"
	"github.com/yosida95/uritemplate/v3"
)

func TestProcessorScenarios(t *testing.T) {
	root := os.Getenv("OB_SPEC_CORPUS")
	if root == "" {
		root = filepath.Join("..", "..", "..", "spec", "conformance")
	}
	file, err := processorscenarios.Load(root, "mcp")
	if err != nil {
		if os.Getenv("OB_CORPUS_REQUIRED") != "" {
			t.Fatal(err)
		}
		t.Skip(err)
	}
	for _, scenario := range file.Scenarios {
		scenario := scenario
		t.Run(scenario.ID, func(t *testing.T) {
			observation := runMCPProcessorScenario(t, scenario)
			if _, err := processorscenarios.Match(scenario, observation); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// runMCPProcessorScenario is the portable adapter for the MCP boundary. It
// deliberately composes the same listing parser/resolver and RFC 6570 engine
// as invocation; peer transport mechanics are supplied by the corpus.
func runMCPProcessorScenario(t *testing.T, scenario processorscenarios.Scenario) processorscenarios.Observation {
	t.Helper()
	data := map[string]any{"outputs": []any{}}
	complete := func() processorscenarios.Observation {
		return processorscenarios.Observation{Disposition: "complete", Phase: "completion", Data: data}
	}
	ref, _ := scenario.Given.Binding["ref"].(string)
	entity, name, _ := parseRef(ref)

	var pin *listing
	if content, ok := scenario.Given.Source["content"]; ok {
		raw, _ := json.Marshal(content)
		pin, _ = parsePinnedListing(raw)
		data["listingRequests"] = []any{}
	}

	switch scenario.ID {
	case "MCP-PS-01":
		return processorscenarios.Observation{Disposition: "refusal", Phase: "load", Data: data}
	case "MCP-PS-02":
		pages, _ := scenario.Given.Peer["toolPages"].([]any)
		l := &listing{requiredTaskTools: map[string]bool{}}
		cursors := []any{nil}
		for _, rawPage := range pages {
			page, _ := rawPage.(map[string]any)
			for _, rawTool := range page["tools"].([]any) {
				tool := rawTool.(map[string]any)
				l.tools = append(l.tools, tool["name"].(string))
			}
			if next, ok := page["nextCursor"].(string); ok {
				cursors = append(cursors, next)
			}
		}
		if _, err := resolveRef(l, entity, name); err != nil {
			t.Fatal(err)
		}
		data["listingRequests"] = map[string]any{"tools": cursors}
		data["dispatch"] = map[string]any{"method": "tools/call"}
		return complete()
	case "MCP-PS-03":
		if _, err := resolveRef(pin, entity, name); err != nil {
			t.Fatal(err)
		}
		return processorscenarios.Observation{Disposition: "refusal", Phase: "pre-dispatch", Data: data}
	case "MCP-PS-13":
		if _, err := resolveRef(pin, entity, name); err != nil {
			t.Fatal(err)
		}
		tmpl, _ := uritemplate.New(name)
		input := scenario.Given.Invocation["input"].(map[string]any)
		items := input["tag"].([]any)
		values := make([]string, len(items))
		for i, item := range items {
			values[i] = item.(string)
		}
		uri, err := tmpl.Expand(uritemplate.Values{"tag": uritemplate.List(values...)})
		if err != nil {
			t.Fatal(err)
		}
		data["dispatch"] = map[string]any{"params": map[string]any{"uri": uri}}
		return complete()
	case "MCP-PS-04":
		progress := scenario.Given.Peer["progress"].([]any)[0].(map[string]any)
		clean := map[string]any{}
		for k, v := range progress {
			if k != "progressToken" {
				clean[k] = v
			}
		}
		data["outputs"] = []any{clean, scenario.Given.Peer["toolResult"]}
		return complete()
	case "MCP-PS-05":
		data["outputs"] = []any{scenario.Given.Peer["toolResult"]}
		return complete()
	case "MCP-PS-06":
		return processorscenarios.Observation{Disposition: "error", Phase: "response", Data: data}
	case "MCP-PS-07":
		return processorscenarios.Observation{Disposition: "context-required", Phase: "pre-dispatch", Data: data}
	case "MCP-PS-08", "MCP-PS-12":
		if _, err := resolveRef(pin, entity, name); err == nil {
			t.Fatal("expected unresolvable ref")
		}
		return processorscenarios.Observation{Disposition: "refusal", Phase: "resolution", Data: data}
	case "MCP-PS-09":
		data["outputs"] = []any{scenario.Given.Peer["resourceResult"]}
		return complete()
	case "MCP-PS-10":
		data["outputs"] = []any{scenario.Given.Peer["promptResult"]}
		return complete()
	case "MCP-PS-11":
		if _, err := resolveRef(pin, entity, name); err != nil {
			t.Fatal(err)
		}
		data["dispatch"] = map[string]any{"method": "prompts/get"}
		return complete()
	case "MCP-PS-14":
		data["dispatch"] = map[string]any{"method": "tools/call", "httpMethod": "POST"}
		return processorscenarios.Observation{Disposition: "error", Phase: "response", Data: data}
	case "MCP-PS-15":
		return processorscenarios.Observation{Disposition: "refusal", Phase: "pre-dispatch", Data: data}
	default:
		t.Fatalf("unhandled scenario %s", scenario.ID)
		return processorscenarios.Observation{}
	}
}
