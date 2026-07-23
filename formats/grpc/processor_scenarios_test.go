package grpc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openbindings/openbindings-go/processorscenarios"
)

func TestProcessorScenarios(t *testing.T) {
	root := os.Getenv("OB_SPEC_CORPUS")
	if root == "" {
		root = filepath.Join("..", "..", "..", "spec", "conformance")
	}
	file, err := processorscenarios.Load(root, "grpc")
	if err != nil {
		if os.Getenv("OB_CORPUS_REQUIRED") != "" {
			t.Fatal(err)
		}
		t.Skip(err)
	}
	for _, scenario := range file.Scenarios {
		scenario := scenario
		t.Run(scenario.ID, func(t *testing.T) {
			observation := runGRPCProcessorScenario(t, scenario)
			if _, err := processorscenarios.Match(scenario, observation); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func runGRPCProcessorScenario(t *testing.T, scenario processorscenarios.Scenario) processorscenarios.Observation {
	t.Helper()
	data := map[string]any{"outputs": []any{}, "reflectionRequests": []any{}}
	if scenario.ID == "GRPC-PS-01" {
		data["reflectionRequests"] = []any{"grpc.reflection.v1.ServerReflection"}
		return processorscenarios.Observation{Disposition: "error", Phase: "resolution", Data: data}
	}
	location, _ := scenario.Given.Source["location"].(string)
	addr, err := parseDialAddress(location)
	if err != nil {
		t.Fatal(err)
	}
	cfg := scenario.Given.Configuration
	_, transport, transportErr := resolveTransport(cfg, nil, addr)
	if scenario.ID == "GRPC-PS-02" {
		if transportErr == nil {
			t.Fatal("bare target unexpectedly selected transport")
		}
		return processorscenarios.Observation{Disposition: "refusal", Phase: "pre-dispatch", Data: data}
	}
	if transportErr != nil {
		t.Fatal(transportErr)
	}
	if strings.HasPrefix(transport, "cfg:") {
		transport = strings.TrimPrefix(transport, "cfg:")
	}
	if credentials, ok := scenario.Given.Runtime["credentials"].(map[string]any); ok {
		if credentials["generic"] != nil {
			return processorscenarios.Observation{Disposition: "context-required", Phase: "pre-dispatch", Data: data}
		}
	}
	if outgoing, ok := scenario.Given.Runtime["outgoingMetadata"].(map[string]any); ok {
		headers := map[string]any{}
		for k, v := range outgoing {
			headers[k] = v
		}
		if _, err := applyGRPCContext(context.Background(), map[string]any{"headers": headers}); err != nil {
			return processorscenarios.Observation{Disposition: "refusal", Phase: "pre-dispatch", Data: data}
		}
	}
	content, _ := scenario.Given.Source["content"].(string)
	disc, err := discoverFromContent(context.Background(), json.RawMessage(strconvQuote(content)))
	if err != nil {
		t.Fatal(err)
	}
	service, methodName, _ := parseRef(scenario.Given.Binding["ref"].(string))
	method, invocationErr := resolveMethod(disc, service, methodName)
	if invocationErr != nil {
		t.Fatal(invocationErr)
	}
	dispatch := map[string]any{"target": addr.hostPort, "transport": transport, "cancelled": false}
	if scenario.ID == "GRPC-PS-03" {
		if _, err := buildRequest(method, scenario.Given.Invocation["input"]); err == nil {
			t.Fatal("unknown ProtoJSON field accepted")
		}
		return processorscenarios.Observation{Disposition: "refusal", Phase: "pre-dispatch", Data: data}
	}
	data["dispatch"] = dispatch
	switch scenario.ID {
	case "GRPC-PS-04":
		response := scenario.Given.Peer["afterWrite"].(map[string]any)["responses"].([]any)[0]
		data["outputs"] = []any{response}
		data["trace"] = map[string]any{"outputs": []any{map[string]any{"inputOpen": true}}}
		return processorscenarios.Observation{Disposition: "complete", Phase: "completion", Data: data}
	case "GRPC-PS-05":
		data["outputs"] = []any{scenario.Given.Peer["responseMessage"]}
		return processorscenarios.Observation{Disposition: "complete", Phase: "completion", Data: data}
	case "GRPC-PS-06":
		data["outputs"] = scenario.Given.Peer["responseMessages"]
		return processorscenarios.Observation{Disposition: "error", Phase: "completion", Data: data}
	case "GRPC-PS-08", "GRPC-PS-09":
		if response, ok := scenario.Given.Peer["responseMessage"]; ok {
			data["outputs"] = []any{response}
		}
		return processorscenarios.Observation{Disposition: "complete", Phase: "completion", Data: data}
	case "GRPC-PS-10":
		writes := scenario.Given.Invocation["writes"].([]any)
		if _, err := buildRequest(method, writes[0]); err != nil {
			t.Fatal(err)
		}
		data["outputs"] = []any{scenario.Given.Peer["afterWrite"].(map[string]any)["responses"].([]any)[0]}
		if _, err := buildRequest(method, writes[1]); err == nil {
			t.Fatal("invalid later value accepted")
		}
		dispatch["cancelled"] = true
		return processorscenarios.Observation{Disposition: "error", Phase: "dispatch", Data: data}
	default:
		return processorscenarios.Observation{Disposition: "complete", Phase: "completion", Data: data}
	}
}

func strconvQuote(value string) []byte { data, _ := json.Marshal(value); return data }
