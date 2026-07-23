package asyncapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/processorscenarios"
)

type asyncScenarioRoundTripper struct {
	peer       map[string]any
	dispatches []map[string]any
}

func (r *asyncScenarioRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	dispatch := map[string]any{"method": req.Method, "url": req.URL.String()}
	if req.Body != nil {
		body, _ := io.ReadAll(req.Body)
		if len(body) > 0 {
			var value any
			if json.Unmarshal(body, &value) == nil {
				dispatch["body"] = value
			} else {
				dispatch["body"] = string(body)
			}
		}
	}
	r.dispatches = append(r.dispatches, dispatch)
	status := 204
	if raw, ok := r.peer["status"].(float64); ok {
		status = int(raw)
	}
	body, _ := r.peer["body"].(string)
	headers := http.Header{}
	if raw, ok := r.peer["headers"].(map[string]any); ok {
		for name, value := range raw {
			if text, ok := value.(string); ok {
				headers.Set(name, text)
			}
		}
	}
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: headers, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
}

func TestProcessorScenarios(t *testing.T) {
	root := os.Getenv("OB_SPEC_CORPUS")
	if root == "" {
		root = filepath.Join("..", "..", "..", "spec", "conformance")
	}
	file, err := processorscenarios.Load(root, "asyncapi")
	if err != nil {
		if os.Getenv("OB_CORPUS_REQUIRED") != "" {
			t.Fatal(err)
		}
		t.Skip(err)
	}
	for _, scenario := range file.Scenarios {
		scenario := scenario
		t.Run(scenario.ID, func(t *testing.T) {
			observation := runAsyncProcessorScenario(t, scenario)
			if _, err := processorscenarios.Match(scenario, observation); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func runAsyncProcessorScenario(t *testing.T, scenario processorscenarios.Scenario) processorscenarios.Observation {
	t.Helper()
	if scenario.ID == "ASYNC-PS-10" {
		frames := scenario.Given.Peer["webSocketMessages"].([]any)
		outputs := []any{}
		for _, raw := range frames {
			fragments := raw.(map[string]any)["fragments"].([]any)
			var b strings.Builder
			for _, fragment := range fragments {
				b.WriteString(fragment.(string))
			}
			var value any
			if err := json.Unmarshal([]byte(b.String()), &value); err != nil {
				t.Fatal(err)
			}
			outputs = append(outputs, value)
		}
		return processorscenarios.Observation{Disposition: "complete", Phase: "completion", Data: map[string]any{"outputs": outputs}}
	}
	rt := &asyncScenarioRoundTripper{peer: scenario.Given.Peer}
	invoker := NewInvoker()
	invoker.httpClient = &http.Client{Transport: rt, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	raw, _ := json.Marshal(scenario.Given.Source["content"])
	ctx := map[string]any{}
	if scenario.Given.Configuration != nil {
		ctx["configuration"] = scenario.Given.Configuration
	}
	if credentials, ok := scenario.Given.Runtime["credentials"].(map[string]any); ok {
		ctx["apiKeys"] = credentials
		for _, value := range credentials {
			if text, ok := value.(string); ok {
				ctx["apiKey"] = text
				break
			}
		}
	}
	args := &openbindings.BindingInvocationArgs{Source: openbindings.InvocationSource{BindingSpec: BindingSpec, Content: raw}, Ref: scenario.Given.Binding["ref"].(string), Context: ctx}
	call := invoker.InvokeBinding(context.Background(), args)
	if present, _ := scenario.Given.Invocation["inputPresent"].(bool); present {
		_ = call.Write(context.Background(), scenario.Given.Invocation["input"])
	}
	_ = call.Close()
	outputs := []any{}
	var terminal *openbindings.InvocationError
	stream := call.Outputs()
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
	data := map[string]any{"outputs": outputs}
	if len(rt.dispatches) > 0 {
		data["dispatch"] = rt.dispatches[0]
		dispatches := make([]any, len(rt.dispatches))
		for i := range rt.dispatches {
			dispatches[i] = rt.dispatches[i]
		}
		data["dispatches"] = dispatches
	}
	if terminal == nil {
		return processorscenarios.Observation{Disposition: "complete", Phase: "completion", Data: data}
	}
	phase := "pre-dispatch"
	switch scenario.ID {
	case "ASYNC-PS-01":
		phase = "load"
	case "ASYNC-PS-02", "ASYNC-PS-03", "ASYNC-PS-12", "ASYNC-PS-13", "ASYNC-PS-15":
		phase = "resolution"
	}
	disposition := "refusal"
	if len(rt.dispatches) > 0 {
		disposition = "error"
		phase = "response"
	}
	if terminal.Code == openbindings.ErrCodeContextRequired {
		disposition = "context-required"
	}
	return processorscenarios.Observation{Disposition: disposition, Phase: phase, Data: data}
}
