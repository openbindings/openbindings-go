package usage

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/invoke"
	"github.com/openbindings/openbindings-go/processorscenarios"
	"github.com/openbindings/openbindings-go/synthesize"
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

func TestInvocationFidelityScenarios(t *testing.T) {
	root := os.Getenv("OB_SPEC_CORPUS")
	if root == "" {
		root = filepath.Join("..", "..", "..", "spec", "conformance")
	}
	file, err := processorscenarios.LoadPath(
		filepath.Join(root, "invocation-fidelity", "usage.json"),
		"usage",
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
		fixture, _ := scenario.Given.Peer["processResult"].(map[string]any)
		if fixture == nil {
			return ProcessResult{Stdout: "", ExitCode: 0}, nil
		}
		stdout, _ := base64.StdEncoding.DecodeString(stringField(fixture, "stdoutBase64"))
		stderr, _ := base64.StdEncoding.DecodeString(stringField(fixture, "stderrBase64"))
		exitCode := 0
		if value, ok := fixture["exitCode"].(float64); ok {
			exitCode = int(value)
		}
		return ProcessResult{
			Stdout: string(stdout), Stderr: string(stderr), ExitCode: exitCode,
			Signal:          stringField(fixture, "signal"),
			StdoutTruncated: fixture["stdoutTruncated"] == true,
			StderrTruncated: fixture["stderrTruncated"] == true,
		}, nil
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

	source := invoke.InvocationSource{BindingSpec: BindingSpec}
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
	joined := len(scenario.ID) >= len("USAGE-FI-") && scenario.ID[:len("USAGE-FI-")] == "USAGE-FI-"
	var call invoke.Invocation[any, any]
	if joined {
		iface, err := NewSynthesizer().SynthesizeInterface(context.Background(), &synthesize.SynthesizeInput{
			Sources: []synthesize.SynthesizeSource{{BindingSpec: BindingSpec, Location: source.Location, Content: source.Content}},
		})
		if err != nil {
			t.Fatal(err)
		}
		op := invoke.NewOperationInvoker(invoker)
		call = invoke.Invoke(
			context.Background(), op, iface,
			invoke.NewOperationSignature[any, any](usageOperationForRef(t, iface, ref)),
			invoke.WithContext(bindCtx),
		)
	} else {
		call = invoker.InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{Source: source, Ref: ref, Context: bindCtx})
	}
	if present, _ := scenario.Given.Invocation["inputPresent"].(bool); present {
		_ = call.Write(context.Background(), scenario.Given.Invocation["input"])
	}
	_ = call.Close()
	outputs := []any{}
	stream := call.Outputs()
	var terminal *invoke.InvocationError
	for {
		value, err := stream.Read(context.Background())
		if err == nil {
			outputs = append(outputs, value)
			continue
		}
		if err != io.EOF {
			terminal = invoke.AsInvocationError(err)
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
	if joined {
		data["joinedSynthesis"] = true
	}
	delete(data, "artifactDiscoveryProcess")
	if dispatch != nil {
		data["dispatch"] = dispatch
	}
	if terminal == nil {
		return processorscenarios.Observation{Disposition: "complete", Phase: "completion", Data: data}
	}
	if terminal.Code == invoke.ErrCodeContextRequired {
		return processorscenarios.Observation{Disposition: "context-required", Phase: "pre-dispatch", Data: data}
	}
	if joined {
		data["error"] = normalizeUsageScenarioValue(terminal)
		return processorscenarios.Observation{Disposition: "error", Phase: "completion", Data: data}
	}
	phase := "pre-dispatch"
	if scenario.ID == "USAGE-PS-01" || scenario.ID == "USAGE-PS-02" || scenario.ID == "USAGE-PS-13" || scenario.ID == "USAGE-PS-14" || scenario.ID == "USAGE-PS-17" || scenario.ID == "USAGE-PS-18" || scenario.ID == "USAGE-PS-19" {
		phase = "load"
	} else if scenario.ID == "USAGE-PS-03" {
		phase = "resolution"
	}
	return processorscenarios.Observation{Disposition: "refusal", Phase: phase, Data: data}
}

func usageOperationForRef(t *testing.T, iface *openbindings.Interface, ref string) string {
	t.Helper()
	for _, binding := range iface.Bindings {
		if binding.Ref == ref {
			return binding.Operation
		}
	}
	t.Fatalf("synthesized Usage interface has no binding for %q", ref)
	return ""
}

func stringField(value map[string]any, name string) string {
	text, _ := value[name].(string)
	return text
}

func normalizeUsageScenarioValue(value any) any {
	raw, _ := json.Marshal(value)
	var normalized any
	_ = json.Unmarshal(raw, &normalized)
	return normalized
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
