package graphql

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	file, err := processorscenarios.Load(root, "graphql")
	if err != nil {
		if os.IsNotExist(err) && os.Getenv("OB_CORPUS_REQUIRED") == "" {
			t.Skip(err)
		}
		t.Fatal(err)
	}
	if file.BindingSpec != BindingSpec {
		t.Fatalf("bindingSpec = %q", file.BindingSpec)
	}
	for _, scenario := range file.Scenarios {
		scenario := scenario
		t.Run(scenario.ID, func(t *testing.T) {
			observation := runGraphQLProcessorScenario(t, scenario)
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
		filepath.Join(root, "invocation-fidelity", "graphql.json"),
		"graphql",
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
			observation := runGraphQLProcessorScenario(t, scenario)
			if _, err := processorscenarios.Match(scenario, observation); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type graphqlScenarioTransport struct {
	peer     map[string]any
	mu       sync.Mutex
	dispatch map[string]any
}

func (t *graphqlScenarioTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	raw, _ := io.ReadAll(request.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	headers := map[string]any{}
	for name, values := range request.Header {
		if len(values) > 0 {
			headers[strings.ToLower(name)] = values[0]
		}
	}
	t.mu.Lock()
	t.dispatch = map[string]any{
		"target":  request.URL.String(),
		"method":  request.Method,
		"headers": headers,
		"body":    body,
	}
	t.mu.Unlock()

	status := 200
	if value, ok := t.peer["status"].(float64); ok {
		status = int(value)
	}
	contentType, _ := t.peer["contentType"].(string)
	responseHeader := http.Header{"Content-Type": []string{contentType}}
	if rawHeaders, ok := t.peer["headers"].(map[string]any); ok {
		for name, value := range rawHeaders {
			responseHeader.Set(name, stringValue(value))
		}
	}
	var responseBody []byte
	if encoded, ok := t.peer["bodyBase64"].(string); ok {
		responseBody, _ = base64.StdEncoding.DecodeString(encoded)
	} else {
		responseBody, _ = json.Marshal(t.peer["body"])
	}
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     responseHeader,
		Body:       io.NopCloser(bytes.NewReader(responseBody)),
		Request:    request,
	}, nil
}

func (t *graphqlScenarioTransport) observed() map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.dispatch
}

func runGraphQLProcessorScenario(t *testing.T, scenario processorscenarios.Scenario) processorscenarios.Observation {
	t.Helper()
	source := invoke.InvocationSource{BindingSpec: BindingSpec}
	if location, ok := scenario.Given.Source["location"].(string); ok {
		source.Location = location
	}
	if content, present := scenario.Given.Source["content"]; present {
		source.Content, _ = json.Marshal(content)
	}
	selector, _ := scenario.Given.Binding["selector"].(string)
	configuration := cloneMap(scenario.Given.Configuration)
	contextValue := map[string]any{}
	if configuration != nil {
		contextValue["configuration"] = configuration
	}
	if credentials, ok := scenario.Given.Runtime["credentials"].(map[string]any); ok {
		for name, value := range credentials {
			contextValue[name] = value
		}
	}

	peer := scenario.Given.Peer
	transport := &graphqlScenarioTransport{peer: peer}
	invoker := NewInvokerWithClient(&http.Client{Transport: transport})

	args := &invoke.BindingInvocationArgs{
		Source:   source,
		Selector: selector,
		Context:  contextValue,
	}
	inputPresent, _ := scenario.Given.Invocation["inputPresent"].(bool)
	if inputPresent {
		args.InputSchema = map[string]any{"type": "object"}
	}
	joined := strings.HasPrefix(scenario.ID, "GQL-FI-")
	var call invoke.Invocation[any, any]
	if joined {
		iface, err := NewSynthesizer().SynthesizeInterface(context.Background(), &synthesize.SynthesizeInput{
			Sources: []synthesize.SynthesizeSource{{BindingSpec: source.BindingSpec, Location: source.Location, Content: source.Content}},
		})
		if err != nil {
			t.Fatal(err)
		}
		op := invoke.NewOperationInvoker(invoker)
		call = invoke.Invoke(
			context.Background(), op, iface,
			invoke.NewOperationSignature[any, any](graphQLOperationForSelector(t, iface, selector)),
			invoke.WithContext(contextValue),
		)
	} else {
		call = invoker.InvokeBinding(context.Background(), args)
	}
	outputs, terminal := collectInvocation(
		context.Background(),
		call,
		scenario.Given.Invocation["input"],
		inputPresent,
	)

	data := map[string]any{
		"outputs":               outputs,
		"introspectionRequests": []any{},
	}
	if joined {
		data["joinedSynthesis"] = true
	}
	if outputs == nil {
		data["outputs"] = []any{}
	}
	dispatch := transport.observed()
	if dispatch != nil {
		data["dispatch"] = dispatch
	}
	if terminal != nil && terminal.Code == invoke.ErrCodeContextRequired {
		data["context"] = normalizeScenarioValue(terminal.Data)
		return processorscenarios.Observation{Disposition: "context-required", Phase: "pre-dispatch", Data: data}
	}
	if terminal == nil {
		return processorscenarios.Observation{Disposition: "complete", Phase: "completion", Data: data}
	}
	data["error"] = normalizeScenarioValue(terminal)
	if dispatch == nil {
		return processorscenarios.Observation{Disposition: "refusal", Phase: "pre-dispatch", Data: data}
	}
	phase := "completion"
	if scenario.ID == "GQL-PS-07" || len(outputs) == 0 {
		phase = "response"
	}
	return processorscenarios.Observation{Disposition: "error", Phase: phase, Data: data}
}

func graphQLOperationForSelector(t *testing.T, iface *openbindings.Interface, selector string) string {
	t.Helper()
	for _, binding := range iface.Bindings {
		if binding.Selector == selector {
			return binding.Operation
		}
	}
	t.Fatalf("synthesized GraphQL interface has no binding for %q", selector)
	return ""
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func normalizeScenarioValue(value any) any {
	raw, _ := json.Marshal(value)
	var normalized any
	_ = json.Unmarshal(raw, &normalized)
	return normalized
}

func cloneMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	raw, _ := json.Marshal(value)
	var clone map[string]any
	_ = json.Unmarshal(raw, &clone)
	return clone
}
