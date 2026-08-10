package openapi

import (
	"context"
	"encoding/base64"
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
			observation := runOpenAPIProcessorScenario(t, scenario, file.BindingSpec)
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
		filepath.Join(root, "invocation-fidelity", "openapi.json"),
		"openapi",
		"openbindings.invocation-fidelity-scenarios@1",
	)
	if err != nil {
		if os.Getenv("OB_CORPUS_REQUIRED") != "" {
			t.Fatal(err)
		}
		t.Skip(err)
	}
	for _, scenario := range file.Scenarios {
		scenario := scenario
		t.Run(scenario.ID, func(t *testing.T) {
			observation := runOpenAPIProcessorScenario(t, scenario, file.BindingSpec)
			if _, err := processorscenarios.Match(scenario, observation); err != nil {
				t.Fatal(err)
			}
		})
	}
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
	source := openbindings.InvocationSource{BindingSpec: bindingSpec}
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
	joined := strings.HasPrefix(scenario.ID, "OAPI-FI-")
	var call openbindings.Invocation[any, any]
	if joined {
		iface, err := NewSynthesizer().SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
			Sources: []openbindings.SynthesizeSource{{
				BindingSpec: bindingSpec, Location: source.Location, Content: source.Content,
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		op := openbindings.NewOperationInvoker(NewInvokerWithClient(client))
		op.TransformEvaluator = openAPIJSONataEvaluator{}
		call = openbindings.Invoke(
			context.Background(), op, iface,
			openbindings.NewOperationSignature[any, any](openAPIFidelityOperationID(scenario.ID)),
			openbindings.WithContext(scenarioContext(scenario)),
		)
	} else {
		call = NewInvokerWithClient(client).InvokeBinding(context.Background(), args)
	}
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
	if joined {
		data["joinedSynthesis"] = true
	}
	if len(roundTripper.dispatches) > 0 {
		data["dispatch"] = roundTripper.dispatches[0]
		data["dispatches"] = anySlice(roundTripper.dispatches)
	}
	if terminal == nil {
		trailer := call.Diagnostics().Trailer()
		if values := trailer["x-ob-governing-media"]; len(values) == 1 {
			data["response"] = map[string]any{"governingMedia": values[0]}
		}
		return processorscenarios.Observation{Disposition: "complete", Phase: "completion", Data: data}
	}

	disposition := "refusal"
	normalizedError := normalizedInvocationError(t, terminal)
	if terminal.Code == openbindings.ErrCodeContextRequired {
		disposition = "context-required"
		data["context"] = normalizedError["details"]
	} else if len(roundTripper.dispatches) > 0 {
		disposition = "error"
	}
	phase := openAPIErrorPhase(terminal, len(roundTripper.dispatches) > 0)
	data["error"] = normalizedError
	return processorscenarios.Observation{Disposition: disposition, Phase: phase, Data: data}
}

func openAPIFidelityOperationID(scenarioID string) string {
	return map[string]string{
		"OAPI-FI-01": "fidelityJobs",
		"OAPI-FI-02": "fidelityItems",
		"OAPI-FI-03": "fidelityBinary",
		"OAPI-FI-04": "fidelitySlow",
		"OAPI-FI-05": "fidelitySSEFailure",
		"OAPI-FI-06": "fidelityUploadImage",
		"OAPI-FI-07": "fidelityCreateItem",
		"OAPI-FI-08": "fidelityCreateItemWithContext",
		"OAPI-FI-09": "fidelityImage",
		"OAPI-FI-10": "fidelityDynamicItem",
		"OAPI-FI-11": "fidelityWholeJSON",
	}[scenarioID]
}

func normalizedInvocationError(t *testing.T, terminal *openbindings.InvocationError) map[string]any {
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
