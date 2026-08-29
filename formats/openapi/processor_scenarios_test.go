package openapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openbindings/openbindings-go/invoke"
	"github.com/openbindings/openbindings-go/processorscenarios"
	"github.com/openbindings/openbindings-go/synthesize"
)

type scenarioRoundTripper struct {
	peer       map[string]any
	resources  map[string]any
	dispatches []map[string]any
}

func (r *scenarioRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if resource, ok := r.resources[req.URL.String()]; ok {
		var body []byte
		if text, ok := resource.(string); ok {
			body = []byte(text)
		} else {
			body, _ = json.Marshal(resource)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     http.StatusText(http.StatusOK),
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader(string(body))),
			Request:    req,
		}, nil
	}
	dispatch := map[string]any{
		"method":  req.Method,
		"url":     req.URL.String(),
		"headers": normalizedHeaders(req.Header),
	}
	if req.Body != nil {
		body, _ := io.ReadAll(req.Body)
		dispatch["bodyBase64"] = base64.StdEncoding.EncodeToString(body)
		dispatch["byteLength"] = len(body)
		if len(body) > 0 {
			var parsed any
			if json.Unmarshal(body, &parsed) == nil {
				dispatch["body"] = parsed
			} else {
				dispatch["body"] = string(body)
			}
		}
	}
	r.dispatches = append(r.dispatches, dispatch)

	status := 599
	body := []byte{}
	headers := http.Header{}
	if r.peer != nil {
		if value, ok := numberAsInt(r.peer["status"]); ok {
			status = value
		}
		if value, ok := r.peer["bodyBase64"].(string); ok {
			if decoded, err := base64.StdEncoding.DecodeString(value); err == nil {
				body = decoded
			}
		} else if value, ok := r.peer["body"].(string); ok {
			body = []byte(value)
		}
		if raw, ok := r.peer["headers"].(map[string]any); ok {
			for name, value := range raw {
				if text, ok := value.(string); ok {
					headers.Set(name, text)
				}
			}
		}
	}
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     headers,
		Body:       io.NopCloser(strings.NewReader(string(body))),
		Request:    req,
	}, nil
}

func TestProcessorScenarios(t *testing.T) {
	root := os.Getenv("OB_SPEC_CORPUS")
	if root == "" {
		root = filepath.Join("..", "..", "..", "spec", "conformance")
	}
	for _, family := range []struct {
		name   string
		wanted map[string]bool
	}{
		{
			name: "openapi-2.0",
			wanted: map[string]bool{
				"OAPI20-PS-01": true,
				"OAPI20-PS-02": true,
				"OAPI20-PS-03": true,
				"OAPI20-PS-04": true,
				"OAPI20-PS-05": true,
				"OAPI20-PS-06": true,
				"OAPI20-PS-07": true,
				"OAPI20-PS-08": true,
				"OAPI20-PS-09": true,
				"OAPI20-PS-10": true,
				"OAPI20-PS-11": true,
				"OAPI20-PS-12": true,
				"OAPI20-PS-13": true,
				"OAPI20-PS-14": true,
				"OAPI20-PS-15": true,
				"OAPI20-PS-16": true,
				"OAPI20-PS-17": true,
				"OAPI20-PS-18": true,
				"OAPI20-PS-19": true,
				"OAPI20-PS-20": true,
				"OAPI20-PS-21": true,
				"OAPI20-PS-22": true,
				"OAPI20-PS-23": true,
				"OAPI20-PS-24": true,
				"OAPI20-PS-25": true,
				"OAPI20-PS-26": true,
				"OAPI20-PS-27": true,
				"OAPI20-PS-28": true,
				"OAPI20-PS-29": true,
				"OAPI20-PS-30": true,
				"OAPI20-PS-31": true,
				"OAPI20-PS-32": true,
				"OAPI20-PS-33": true,
				"OAPI20-PS-34": true,
				"OAPI20-PS-35": true,
				"OAPI20-PS-36": true,
				"OAPI20-PS-37": true,
				"OAPI20-PS-38": true,
				"OAPI20-PS-39": true,
				"OAPI20-PS-40": true,
				"OAPI20-PS-41": true,
				"OAPI20-PS-42": true,
				"OAPI20-PS-43": true,
				"OAPI20-PS-44": true,
				"OAPI20-PS-45": true,
				"OAPI20-PS-46": true,
				"OAPI20-PS-47": true,
				"OAPI20-PS-48": true,
				"OAPI20-PS-49": true,
				"OAPI20-PS-50": true,
				"OAPI20-PS-51": true,
				"OAPI20-PS-52": true,
				"OAPI20-PS-53": true,
				"OAPI20-PS-54": true,
				"OAPI20-PS-55": true,
				"OAPI20-PS-56": true,
				"OAPI20-PS-57": true,
				"OAPI20-PS-58": true,
				"OAPI20-PS-59": true,
				"OAPI20-PS-60": true,
				"OAPI20-PS-61": true,
				"OAPI20-PS-62": true,
				"OAPI20-PS-63": true,
				"OAPI20-PS-64": true,
				"OAPI20-PS-65": true,
				"OAPI20-PS-66": true,
				"OAPI20-PS-67": true,
				"OAPI20-PS-68": true,
				"OAPI20-PS-69": true,
				"OAPI20-PS-70": true,
				"OAPI20-PS-71": true,
				"OAPI20-PS-72": true,
				"OAPI20-PS-73": true,
				"OAPI20-PS-74": true,
				"OAPI20-PS-75": true,
				"OAPI20-PS-76": true,
				"OAPI20-PS-77": true,
				"OAPI20-PS-78": true,
				"OAPI20-PS-79": true,
				"OAPI20-PS-80": true,
				"OAPI20-PS-81": true,
				"OAPI20-PS-82": true,
				"OAPI20-PS-83": true,
				"OAPI20-PS-84": true,
				"OAPI20-PS-85": true,
				"OAPI20-PS-86": true,
				"OAPI20-PS-87": true,
				"OAPI20-PS-88": true,
				"OAPI20-PS-89": true,
				"OAPI20-PS-90": true,
				"OAPI20-PS-91": true,
				"OAPI20-PS-92": true,
				"OAPI20-PS-93": true,
				"OAPI20-PS-94": true,
			},
		},
		{name: "openapi-3.0"},
		{name: "openapi-3.1"},
		{name: "openapi-3.2"},
	} {
		family := family
		t.Run(family.name, func(t *testing.T) {
			file, err := loadOpenAPIProcessorScenarioFile(
				filepath.Join(root, "binding-specs", "processor", family.name+".json"),
				family.name,
				"openbindings.binding-spec-processor-scenarios@2",
			)
			if err != nil {
				if os.Getenv("OB_CORPUS_REQUIRED") != "" {
					t.Fatal(err)
				}
				t.Skip(err)
			}
			ran := 0
			for _, scenario := range file.Scenarios {
				if family.wanted != nil && !family.wanted[scenario.ID] {
					continue
				}
				ran++
				scenario := scenario
				t.Run(scenario.ID, func(t *testing.T) {
					observation := runOpenAPIProcessorScenario(t, scenario, file.BindingSpec)
					if _, err := processorscenarios.Match(scenario, observation); err != nil {
						t.Fatal(err)
					}
				})
			}
			if ran != len(file.Scenarios) {
				t.Fatalf("ran %d of %d processor scenarios", ran, len(file.Scenarios))
			}
			t.Logf("executed %d of %d processor scenarios", ran, len(file.Scenarios))
		})
	}
}

func TestInvocationFidelityScenarios(t *testing.T) {
	root := os.Getenv("OB_SPEC_CORPUS")
	if root == "" {
		root = filepath.Join("..", "..", "..", "spec", "conformance")
	}
	for _, entry := range loadOpenAPIFidelityScenarios(t, root) {
		scenario := entry.Scenario
		t.Run(scenario.ID, func(t *testing.T) {
			observation := runOpenAPIProcessorScenario(t, scenario, entry.BindingSpec)
			if _, err := processorscenarios.Match(scenario, observation); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type openAPIFidelityScenario struct {
	processorscenarios.Scenario
	BindingSpec string
}

func loadOpenAPIFidelityScenarios(t *testing.T, root string) []openAPIFidelityScenario {
	t.Helper()
	var scenarios []openAPIFidelityScenario
	for _, family := range []string{"openapi-3.0", "openapi-3.1"} {
		file, err := processorscenarios.LoadPath(
			filepath.Join(root, "invocation-fidelity", family+".json"),
			family,
			"openbindings.invocation-fidelity-scenarios@1",
		)
		if err != nil {
			if os.Getenv("OB_CORPUS_REQUIRED") != "" {
				t.Fatal(err)
			}
			t.Skip(err)
		}
		for _, scenario := range file.Scenarios {
			scenarios = append(scenarios, openAPIFidelityScenario{
				Scenario:    scenario,
				BindingSpec: file.BindingSpec,
			})
		}
	}
	return scenarios
}

func runOpenAPIProcessorScenario(t *testing.T, scenario processorscenarios.Scenario, bindingSpec string) processorscenarios.Observation {
	t.Helper()
	roundTripper := &scenarioRoundTripper{peer: scenario.Given.Peer, resources: scenario.Given.Resources}
	client := &http.Client{
		Transport: roundTripper,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	source := invoke.InvocationSource{BindingSpec: bindingSpec}
	if location, ok := scenario.Given.Source["location"].(string); ok {
		source.Location = location
	}
	if content, present := scenario.Given.Source["content"]; present {
		raw, err := json.Marshal(content)
		if err != nil {
			t.Fatal(err)
		}
		source.Content = raw
	}
	selector, _ := scenario.Given.Binding["selector"].(string)
	args := &invoke.BindingInvocationArgs{
		Source:   source,
		Selector: selector,
		Context:  scenarioContext(scenario),
	}
	if limit, ok := scenario.Given.Runtime["maxDeliveryUnitBytes"].(float64); ok {
		args.MaxDeliveryUnitBytes = int64(limit)
	}
	joined := isOpenAPIFidelityScenario(scenario.ID)
	processor := scenarioOpenAPIInvoker(client, scenario)
	var call invoke.Invocation[any, any]
	if joined {
		iface, err := NewSynthesizer().SynthesizeInterface(context.Background(), &synthesize.SynthesizeInput{
			Sources: []synthesize.SynthesizeSource{{
				BindingSpec: bindingSpec, Location: source.Location, Content: source.Content,
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		op := invoke.NewOperationInvoker(processor)
		op.TransformEvaluator = openAPIJSONataEvaluator{}
		call = invoke.Invoke(
			context.Background(), op, iface,
			invoke.NewOperationSignature[any, any](openAPIFidelityOperationID(scenario.ID)),
			invoke.WithContext(scenarioContext(scenario)),
		)
	} else {
		call = processor.InvokeBinding(context.Background(), args)
	}
	if present, _ := scenario.Given.Invocation["inputPresent"].(bool); present {
		if err := call.Write(context.Background(), scenario.Given.Invocation["input"]); err != nil {
			// The output terminal remains authoritative and is read below.
		}
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

	data := map[string]any{"outputs": outputs}
	if joined {
		data["joinedSynthesis"] = true
	}
	if len(roundTripper.dispatches) > 0 {
		data["dispatch"] = roundTripper.dispatches[0]
		data["dispatches"] = anySlice(roundTripper.dispatches)
	}
	if terminal == nil {
		return processorscenarios.Observation{Disposition: "complete", Phase: "completion", Data: data}
	}

	disposition := "refusal"
	normalizedError := normalizedInvocationError(t, terminal)
	if terminal.Code == invoke.ErrCodeContextRequired {
		disposition = "context-required"
		data["context"] = normalizedError["data"]
	} else if len(roundTripper.dispatches) > 0 {
		disposition = "error"
	}
	phase := openAPIErrorPhase(terminal, len(roundTripper.dispatches) > 0)
	data["error"] = normalizedError
	return processorscenarios.Observation{Disposition: disposition, Phase: phase, Data: data}
}

func scenarioOpenAPIInvoker(client *http.Client, scenario processorscenarios.Scenario) *Invoker {
	options := RuntimeOptions{
		HTTPClient:          client,
		ParameterConversion: scenarioParameterConversion(scenario),
	}
	if declared, ok := scenario.Given.Runtime["requestContentCodings"].(map[string]any); ok {
		options.RequestContentCodings = map[string]ContentEncoder{}
		for token, raw := range declared {
			if raw == "reverse" {
				options.RequestContentCodings[token] = func(input []byte) ([]byte, error) {
					output := append([]byte(nil), input...)
					for left, right := 0, len(output)-1; left < right; left, right = left+1, right-1 {
						output[left], output[right] = output[right], output[left]
					}
					return output, nil
				}
			}
		}
	}
	if declared, ok := scenario.Given.Runtime["responseContentCodings"].(map[string]any); ok {
		options.ResponseContentCodings = map[string]ContentDecoder{}
		for token, raw := range declared {
			token := token
			switch raw {
			case "reverse":
				options.ResponseContentCodings[token] = func(input []byte) ([]byte, error) {
					output := append([]byte(nil), input...)
					for left, right := 0, len(output)-1; left < right; left, right = left+1, right-1 {
						output[left], output[right] = output[right], output[left]
					}
					return output, nil
				}
			case "unwrap":
				options.ResponseContentCodings[token] = func(input []byte) ([]byte, error) {
					prefix := token + "("
					if !strings.HasPrefix(string(input), prefix) || !strings.HasSuffix(string(input), ")") {
						return nil, fmt.Errorf("%s response coding cannot unwrap representation", token)
					}
					return append([]byte(nil), input[len(prefix):len(input)-1]...), nil
				}
			}
		}
	}
	return NewInvokerWithOptions(options)
}

func openAPIFidelityOperationID(scenarioID string) string {
	return map[string]string{
		"OAPI30-FI-06": "fidelityUploadImage",
		"OAPI30-FI-10": "fidelityDynamicItem",
		"OAPI30-FI-12": "fidelityStoreArchive",
		"OAPI31-FI-01": "fidelityJobs",
		"OAPI31-FI-02": "fidelityItems",
		"OAPI31-FI-03": "fidelityBinary",
		"OAPI31-FI-04": "fidelitySlow",
		"OAPI31-FI-07": "fidelityCreateItem",
		"OAPI31-FI-08": "fidelityCreateItemWithContext",
		"OAPI31-FI-11": "fidelityWholeJSON",
	}[scenarioID]
}

func isOpenAPIFidelityScenario(scenarioID string) bool {
	return strings.HasPrefix(scenarioID, "OAPI30-FI-") ||
		strings.HasPrefix(scenarioID, "OAPI31-FI-")
}

func normalizedInvocationError(t *testing.T, terminal *invoke.InvocationError) map[string]any {
	t.Helper()
	raw, err := json.Marshal(terminal)
	if err != nil {
		t.Fatal(err)
	}
	var normalized map[string]any
	if err := json.Unmarshal(raw, &normalized); err != nil {
		t.Fatal(err)
	}
	return normalized
}

func scenarioContext(scenario processorscenarios.Scenario) map[string]any {
	ctx := map[string]any{}
	if scenario.Given.Configuration != nil {
		ctx["configuration"] = scenario.Given.Configuration
	}
	if credentials, ok := scenario.Given.Runtime["credentials"].(map[string]any); ok {
		ctx["apiKeys"] = credentials
		if strings.HasPrefix(scenario.ID, "OAPI20-") {
			ctx["credentials"] = credentials
		}
	}
	return ctx
}

func openAPIErrorPhase(err *invoke.InvocationError, dispatched bool) string {
	if dispatched {
		return "response"
	}
	if err.Code == invoke.ErrCodeSourceLoadFailed {
		return "load"
	}
	if err.Code == invoke.ErrCodeInvalidSelector || err.Code == invoke.ErrCodeSelectorNotFound {
		return "resolution"
	}
	return "pre-dispatch"
}

func normalizedHeaders(headers http.Header) map[string]any {
	out := map[string]any{}
	for name, values := range headers {
		if len(values) == 0 {
			continue
		}
		out[name] = values[0]
		out[strings.ToLower(name)] = values[0]
	}
	return out
}

func numberAsInt(value any) (int, bool) {
	switch number := value.(type) {
	case int:
		return number, true
	case float64:
		return int(number), number == float64(int(number))
	case json.Number:
		integer, err := number.Int64()
		return int(integer), err == nil && int64(int(integer)) == integer
	default:
		return 0, false
	}
}

func loadOpenAPIProcessorScenarioFile(path, family, format string) (*processorscenarios.File, error) {
	file, err := processorscenarios.LoadPath(path, family, format)
	if err != nil || family != "openapi-2.0" {
		return file, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var exact processorscenarios.File
	if err := decoder.Decode(&exact); err != nil {
		return nil, err
	}
	return &exact, nil
}

func scenarioParameterConversion(scenario processorscenarios.Scenario) ParameterConversion {
	raw, ok := scenario.Given.Configuration["parameterConversion"].(map[string]any)
	if !ok {
		return nil
	}
	return func(value any) (string, error) {
		encoded, err := json.Marshal(value)
		if err != nil {
			return "", err
		}
		converted, present := raw[string(encoded)].(string)
		if !present {
			return "", fmt.Errorf("parameterConversion has no result for %s", encoded)
		}
		return converted, nil
	}
}

func anySlice(values []map[string]any) []any {
	out := make([]any, len(values))
	for i := range values {
		out[i] = values[i]
	}
	return out
}
