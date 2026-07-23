package openapi

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

type scenarioRoundTripper struct {
	peer       map[string]any
	dispatches []map[string]any
}

func (r *scenarioRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	dispatch := map[string]any{
		"method":  req.Method,
		"url":     req.URL.String(),
		"headers": normalizedHeaders(req.Header),
	}
	if req.Body != nil {
		body, _ := io.ReadAll(req.Body)
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
	body := ""
	headers := http.Header{}
	if r.peer != nil {
		if value, ok := numberAsInt(r.peer["status"]); ok {
			status = value
		}
		if value, ok := r.peer["body"].(string); ok {
			body = value
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
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}, nil
}

func TestProcessorScenarios(t *testing.T) {
	root := os.Getenv("OB_SPEC_CORPUS")
	if root == "" {
		root = filepath.Join("..", "..", "..", "spec", "conformance")
	}
	file, err := processorscenarios.Load(root, "openapi")
	if err != nil {
		if os.Getenv("OB_CORPUS_REQUIRED") != "" {
			t.Fatal(err)
		}
		t.Skip(err)
	}
	for _, scenario := range file.Scenarios {
		scenario := scenario
		t.Run(scenario.ID, func(t *testing.T) {
			observation := runOpenAPIProcessorScenario(t, scenario)
			if _, err := processorscenarios.Match(scenario, observation); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func runOpenAPIProcessorScenario(t *testing.T, scenario processorscenarios.Scenario) processorscenarios.Observation {
	t.Helper()
	roundTripper := &scenarioRoundTripper{peer: scenario.Given.Peer}
	client := &http.Client{
		Transport: roundTripper,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	source := openbindings.InvocationSource{BindingSpec: BindingSpec}
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
	ref, _ := scenario.Given.Binding["ref"].(string)
	args := &openbindings.BindingInvocationArgs{
		Source:  source,
		Ref:     ref,
		Context: scenarioContext(scenario),
	}
	call := NewInvokerWithClient(client).InvokeBinding(context.Background(), args)
	if present, _ := scenario.Given.Invocation["inputPresent"].(bool); present {
		if err := call.Write(context.Background(), scenario.Given.Invocation["input"]); err != nil {
			// The output terminal remains authoritative and is read below.
		}
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

	data := map[string]any{"outputs": outputs}
	if len(roundTripper.dispatches) > 0 {
		data["dispatch"] = roundTripper.dispatches[0]
		data["dispatches"] = anySlice(roundTripper.dispatches)
	}
	if terminal == nil {
		trailer := call.Trailer()
		if values := trailer["x-ob-governing-media"]; len(values) == 1 {
			data["response"] = map[string]any{"governingMedia": values[0]}
		}
		return processorscenarios.Observation{Disposition: "complete", Phase: "completion", Data: data}
	}

	disposition := "refusal"
	if terminal.Code == openbindings.ErrCodeContextRequired {
		disposition = "context-required"
	} else if len(roundTripper.dispatches) > 0 {
		disposition = "error"
	}
	phase := openAPIErrorPhase(terminal, len(roundTripper.dispatches) > 0)
	return processorscenarios.Observation{Disposition: disposition, Phase: phase, Data: data}
}

func scenarioContext(scenario processorscenarios.Scenario) map[string]any {
	ctx := map[string]any{}
	if scenario.Given.Configuration != nil {
		ctx["configuration"] = scenario.Given.Configuration
	}
	if credentials, ok := scenario.Given.Runtime["credentials"].(map[string]any); ok {
		ctx["apiKeys"] = credentials
	}
	return ctx
}

func openAPIErrorPhase(err *openbindings.InvocationError, dispatched bool) string {
	if dispatched {
		return "response"
	}
	if err.Code == openbindings.ErrCodeSourceLoadFailed {
		return "load"
	}
	if err.Code == openbindings.ErrCodeInvalidRef || err.Code == openbindings.ErrCodeRefNotFound ||
		strings.Contains(err.Message, "unflattenable") || strings.Contains(err.Message, "normalized collision") ||
		strings.Contains(err.Message, "different locations") {
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
	default:
		return 0, false
	}
}

func anySlice(values []map[string]any) []any {
	out := make([]any, len(values))
	for i := range values {
		out[i] = values[i]
	}
	return out
}
