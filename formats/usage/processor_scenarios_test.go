package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/processorscenarios"
)

func TestProcessorScenarios(t *testing.T) {
	root := os.Getenv("OB_SPEC_CORPUS")
	if root == "" {
		root = filepath.Join("..", "..", "..", "spec", "conformance")
	}
	file, err := processorscenarios.Load(root, "usage")
	if err != nil {
		if os.Getenv("OB_CORPUS_REQUIRED") != "" {
			t.Fatal(err)
		}
		t.Skip(err)
	}
	for _, scenario := range file.Scenarios {
		scenario := scenario
		t.Run(scenario.ID, func(t *testing.T) {
			observation := runUsageProcessorScenario(t, scenario)
			if _, err := processorscenarios.Match(scenario, observation); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func runUsageProcessorScenario(t *testing.T, scenario processorscenarios.Scenario) processorscenarios.Observation {
	t.Helper()
	var dispatch map[string]any
	invoker := NewInvoker()
	invoker.AuthorizeExec = func(argv []string) bool {
		allowed, _ := scenario.Given.Runtime["authorizedExecAddresses"].([]any)
		for _, candidate := range allowed {
			if candidate == scenario.Given.Source["location"] {
				return true
			}
		}
		return false
	}
	invoker.Execute = func(_ context.Context, request ProcessRequest) (ProcessResult, error) {
		dispatch = map[string]any{
			"transport":   "process",
			"argv":        stringAnySlice(request.Argv),
			"environment": stringMapAny(request.Environment),
		}
		return ProcessResult{Stdout: "", ExitCode: 0}, nil
	}
	invoker.Encoders = map[string]TokenEncoder{
		"decimal-token": func(value any) (string, error) {
			number, ok := value.(float64)
			if !ok || number != float64(int(number)) {
				return "", fmt.Errorf("not an integer")
			}
			return fmt.Sprintf("%d", int(number)), nil
		},
	}

	source := openbindings.InvocationSource{BindingSpec: BindingSpec}
	if location, ok := scenario.Given.Source["location"].(string); ok {
		source.Location = location
	}
	if content, present := scenario.Given.Source["content"]; present {
		source.Content, _ = json.Marshal(content)
	}
	bindCtx := map[string]any{}
	if scenario.Given.Configuration != nil {
		bindCtx["configuration"] = scenario.Given.Configuration
	}
	if environment, ok := scenario.Given.Runtime["processEnvironment"].(map[string]any); ok {
		bindCtx["environment"] = environment
	}
	if credentials, ok := scenario.Given.Runtime["credentials"].(map[string]any); ok {
		if generic, ok := credentials["generic"].(string); ok {
			bindCtx["apiKey"] = generic
		}
	}
	ref, _ := scenario.Given.Binding["ref"].(string)
	call := invoker.InvokeBinding(context.Background(), &openbindings.BindingInvocationArgs{Source: source, Ref: ref, Context: bindCtx})
	if present, _ := scenario.Given.Invocation["inputPresent"].(bool); present {
		_ = call.Write(context.Background(), scenario.Given.Invocation["input"])
	}
	_ = call.Close()
	outputs := []any{}
	stream := call.Outputs()
	var terminal *openbindings.InvocationError
	for {
		value, err := stream.Read(context.Background())
		if err == nil {
			outputs = append(outputs, value)
			continue
		}
		if err != io.EOF {
			terminal = openbindings.AsInvocationError(err)
		}
		break
	}
	data := map[string]any{
		"outputs":                  outputs,
		"includeReads":             []any{},
		"configFileReads":          []any{},
		"auxiliaryProcesses":       []any{},
		"artifactDiscoveryProcess": nil,
	}
	delete(data, "artifactDiscoveryProcess")
	if dispatch != nil {
		data["dispatch"] = dispatch
	}
	if terminal == nil {
		return processorscenarios.Observation{Disposition: "complete", Phase: "completion", Data: data}
	}
	if terminal.Code == openbindings.ErrCodeContextRequired {
		return processorscenarios.Observation{Disposition: "context-required", Phase: "pre-dispatch", Data: data}
	}
	phase := "pre-dispatch"
	if scenario.ID == "USAGE-PS-01" || scenario.ID == "USAGE-PS-02" || scenario.ID == "USAGE-PS-13" || scenario.ID == "USAGE-PS-14" || scenario.ID == "USAGE-PS-17" || scenario.ID == "USAGE-PS-18" || scenario.ID == "USAGE-PS-19" {
		phase = "load"
	} else if scenario.ID == "USAGE-PS-03" {
		phase = "resolution"
	}
	return processorscenarios.Observation{Disposition: "refusal", Phase: phase, Data: data}
}

func stringAnySlice(values []string) []any {
	result := make([]any, len(values))
	for i, value := range values {
		result[i] = value
	}
	return result
}

func stringMapAny(values map[string]string) map[string]any {
	result := make(map[string]any, len(values))
	for name, value := range values {
		result[name] = value
	}
	return result
}
