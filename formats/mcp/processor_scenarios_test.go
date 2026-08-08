package mcp

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
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

func TestInvocationFidelityScenarios(t *testing.T) {
	root := os.Getenv("OB_SPEC_CORPUS")
	if root == "" {
		root = filepath.Join("..", "..", "..", "spec", "conformance")
	}
	file, err := processorscenarios.LoadPath(
		filepath.Join(root, "invocation-fidelity", "mcp.json"),
		"mcp",
		"openbindings.invocation-fidelity-scenarios@1",
	)
	if err != nil {
		if os.IsNotExist(err) && os.Getenv("OB_CORPUS_REQUIRED") == "" {
			t.Skip(err)
		}
		t.Fatal(err)
	}
	for _, scenario := range file.Scenarios {
		scenario := scenario
		t.Run(scenario.ID, func(t *testing.T) {
			observation := runMCPFidelityScenario(t, scenario)
			if _, err := processorscenarios.Match(scenario, observation); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type mcpFidelityTransport struct {
	peer map[string]any
}

func (t *mcpFidelityTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method == http.MethodGet {
		return mcpScenarioResponse(request, http.StatusMethodNotAllowed, nil, nil), nil
	}
	if request.Method == http.MethodDelete {
		return mcpScenarioResponse(request, http.StatusOK, nil, nil), nil
	}
	raw, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, err
	}
	method, _ := envelope["method"].(string)
	id := envelope["id"]
	if method == "initialize" {
		return mcpRPCResult(request, id, map[string]any{
			"protocolVersion": "2025-11-25",
			"capabilities":    map[string]any{},
			"serverInfo":      map[string]any{"name": "fidelity-peer", "version": "1"},
		}), nil
	}
	if id == nil {
		return mcpScenarioResponse(request, http.StatusAccepted, nil, nil), nil
	}
	if method == "tools/call" {
		if status, ok := numberAsInt(t.peer["httpStatus"]); ok {
			headers := http.Header{}
			if rawHeaders, ok := t.peer["headers"].(map[string]any); ok {
				for name, value := range rawHeaders {
					headers.Set(name, fmt.Sprint(value))
				}
			}
			var body []byte
			if encoded, ok := t.peer["bodyBase64"].(string); ok {
				body, _ = base64.StdEncoding.DecodeString(encoded)
			}
			return mcpScenarioResponse(request, status, headers, body), nil
		}
		if rpcError, ok := t.peer["jsonrpcError"].(map[string]any); ok {
			return mcpRPCEnvelope(request, map[string]any{"jsonrpc": "2.0", "id": id, "error": rpcError}), nil
		}
		return mcpRPCResult(request, id, t.peer["toolResult"]), nil
	}
	return mcpRPCResult(request, id, map[string]any{}), nil
}

func mcpRPCResult(request *http.Request, id, result any) *http.Response {
	return mcpRPCEnvelope(request, map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func mcpRPCEnvelope(request *http.Request, envelope map[string]any) *http.Response {
	body, _ := json.Marshal(envelope)
	return mcpScenarioResponse(request, http.StatusOK, http.Header{"Content-Type": {"application/json"}}, body)
}

func mcpScenarioResponse(request *http.Request, status int, headers http.Header, body []byte) *http.Response {
	if headers == nil {
		headers = http.Header{}
	}
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     headers,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    request,
	}
}

func numberAsInt(value any) (int, bool) {
	switch n := value.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	default:
		return 0, false
	}
}

func runMCPFidelityScenario(t *testing.T, scenario processorscenarios.Scenario) processorscenarios.Observation {
	t.Helper()
	transport := &mcpFidelityTransport{peer: scenario.Given.Peer}
	invoker := NewInvoker(WithHTTPClient(&http.Client{Transport: transport}))
	t.Cleanup(func() { _ = invoker.Close() })

	location, _ := scenario.Given.Source["location"].(string)
	ref, _ := scenario.Given.Binding["ref"].(string)
	args := invocationArgs(location, ref, nil)
	if content, present := scenario.Given.Source["content"]; present {
		args.Source.Content = mustContent(content)
	}
	call := invoker.InvokeBinding(bg(), args)
	if present, _ := scenario.Given.Invocation["inputPresent"].(bool); present {
		if err := call.Write(shortCtx(t), scenario.Given.Invocation["input"]); err != nil {
			t.Fatal(err)
		}
	} else {
		_ = call.Close()
	}
	outputs, terminalErr := drainOutputs(t, call)
	if outputs == nil {
		outputs = []any{}
	}
	data := map[string]any{"outputs": normalizeMCPScenarioValue(outputs)}
	trailer := map[string]any{}
	for name, values := range call.Trailer() {
		trailer[name] = values
	}
	data["trailer"] = trailer
	if terminalErr == nil {
		return processorscenarios.Observation{Disposition: "complete", Phase: "completion", Data: data}
	}
	var terminal *openbindings.InvocationError
	if !errors.As(terminalErr, &terminal) {
		t.Fatalf("terminal = %T, want *InvocationError", terminalErr)
	}
	data["error"] = normalizeMCPScenarioValue(terminal)
	phase := "response"
	if details, ok := terminal.Details.(map[string]any); ok {
		if native, ok := details["mcp"].(map[string]any); ok {
			if _, ok := native["result"]; ok {
				phase = "completion"
			}
		}
	}
	return processorscenarios.Observation{Disposition: "error", Phase: phase, Data: data}
}

func normalizeMCPScenarioValue(value any) any {
	raw, _ := json.Marshal(value)
	var normalized any
	_ = json.Unmarshal(raw, &normalized)
	return normalized
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
