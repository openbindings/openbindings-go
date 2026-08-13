package connect

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
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/processorscenarios"
)

type connectScenarioTransport struct {
	peer           map[string]any
	dispatches     []map[string]any
	endStreamCount int
}

func (r *connectScenarioTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(req.Body)
	dispatch := map[string]any{
		"method":  req.Method,
		"url":     req.URL.String(),
		"headers": normalizedConnectHeaders(req.Header),
	}
	if strings.HasPrefix(req.Header.Get("Content-Type"), streamingContentType) {
		messages := []any{}
		reader := bytes.NewReader(body)
		for reader.Len() > 0 {
			flags, payload, err := readConnectEnvelope(reader, 10<<20)
			if err != nil {
				break
			}
			if flags&connectFlagEndStream != 0 {
				r.endStreamCount++
				continue
			}
			var value any
			if json.Unmarshal(payload, &value) == nil {
				messages = append(messages, map[string]any{"message": value})
			}
		}
		dispatch["requestEnvelopes"] = messages
	} else if len(body) > 0 {
		var value any
		if json.Unmarshal(body, &value) == nil {
			dispatch["body"] = value
		}
	}
	r.dispatches = append(r.dispatches, dispatch)

	status := http.StatusOK
	headers := http.Header{}
	var responseBody []byte
	if value, ok := numberAsInt(r.peer["status"]); ok {
		status = value
	}
	if raw, ok := r.peer["headers"].(map[string]any); ok {
		for name, value := range raw {
			headers.Set(name, stringValue(value))
		}
	}
	if envelopes, ok := r.peer["envelopes"].([]any); ok {
		headers.Set("Content-Type", streamingContentType)
		var framed bytes.Buffer
		for _, raw := range envelopes {
			item, _ := raw.(map[string]any)
			if value, present := item["message"]; present {
				payload, _ := json.Marshal(value)
				_ = writeConnectEnvelope(&framed, 0, payload)
			}
			if value, present := item["endStream"]; present {
				payload, _ := json.Marshal(value)
				_ = writeConnectEnvelope(&framed, connectFlagEndStream, payload)
			}
			if value, present := item["endStreamBase64"].(string); present {
				payload, _ := base64.StdEncoding.DecodeString(value)
				_ = writeConnectEnvelope(&framed, connectFlagEndStream, payload)
			}
		}
		responseBody = framed.Bytes()
	} else {
		if value, present := r.peer["bodyBase64"].(string); present {
			responseBody, _ = base64.StdEncoding.DecodeString(value)
		} else if value, present := r.peer["body"]; present {
			if text, ok := value.(string); ok && text == "" {
				responseBody = nil
			} else {
				responseBody, _ = json.Marshal(value)
			}
		} else {
			responseBody = []byte("{}")
		}
		if contentType, ok := r.peer["contentType"].(string); ok {
			headers.Set("Content-Type", contentType)
		} else {
			headers.Set("Content-Type", "application/json")
		}
	}
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     headers,
		Body:       io.NopCloser(bytes.NewReader(responseBody)),
		Request:    req,
	}, nil
}

func TestInvocationFidelityScenarios(t *testing.T) {
	root := os.Getenv("OB_SPEC_CORPUS")
	if root == "" {
		root = filepath.Join("..", "..", "..", "spec", "conformance")
	}
	file, err := processorscenarios.LoadPath(
		filepath.Join(root, "invocation-fidelity", "connect.json"),
		"connect",
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
			observation := runConnectProcessorScenario(t, scenario)
			if _, err := processorscenarios.Match(scenario, observation); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestProcessorScenarios(t *testing.T) {
	root := os.Getenv("OB_SPEC_CORPUS")
	if root == "" {
		root = filepath.Join("..", "..", "..", "spec", "conformance")
	}
	file, err := processorscenarios.Load(root, "connect")
	if err != nil {
		if os.Getenv("OB_CORPUS_REQUIRED") != "" {
			t.Fatal(err)
		}
		t.Skip(err)
	}
	for _, scenario := range file.Scenarios {
		scenario := scenario
		t.Run(scenario.ID, func(t *testing.T) {
			observation := runConnectProcessorScenario(t, scenario)
			if _, err := processorscenarios.Match(scenario, observation); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func runConnectProcessorScenario(t *testing.T, scenario processorscenarios.Scenario) processorscenarios.Observation {
	t.Helper()
	transport := &connectScenarioTransport{peer: scenario.Given.Peer}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	fullDuplex := true
	if versions, ok := scenario.Given.Runtime["availableHttpVersions"].([]any); ok {
		fullDuplex = false
		for _, version := range versions {
			if version == "2" {
				fullDuplex = true
			}
		}
	}
	invoker := NewInvokerWithClient(client).WithFullDuplexTransport(fullDuplex)
	source := openbindings.InvocationSource{BindingSpec: BindingSpec}
	if location, ok := scenario.Given.Source["location"].(string); ok {
		source.Location = location
	}
	if content, present := scenario.Given.Source["content"]; present {
		source.Content, _ = json.Marshal(content)
	}
	ctx := map[string]any{}
	if scenario.Given.Configuration != nil {
		ctx["configuration"] = scenario.Given.Configuration
	}
	if metadata, ok := scenario.Given.Runtime["requestMetadata"].(map[string]any); ok {
		ctx["headers"] = metadata
	}
	if credentials, ok := scenario.Given.Runtime["credentials"].(map[string]any); ok {
		if generic, ok := credentials["generic"].(string); ok {
			ctx["apiKey"] = generic
		}
	}
	ref, _ := scenario.Given.Binding["ref"].(string)
	joined := strings.HasPrefix(scenario.ID, "CONN-FI-")
	var call openbindings.Invocation[any, any]
	if joined {
		iface, err := NewSynthesizer().SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
			Sources: []openbindings.SynthesizeSource{{BindingSpec: BindingSpec, Location: source.Location, Content: source.Content}},
		})
		if err != nil {
			t.Fatal(err)
		}
		op := openbindings.NewOperationInvoker(invoker)
		call = openbindings.Invoke(
			context.Background(), op, iface,
			openbindings.NewOperationSignature[any, any](connectOperationForRef(t, iface, ref)),
			openbindings.WithContext(ctx),
		)
	} else {
		call = invoker.InvokeBinding(context.Background(), &openbindings.BindingInvocationArgs{Source: source, Ref: ref, Context: ctx})
	}
	if writes, ok := scenario.Given.Invocation["writes"].([]any); ok {
		for _, value := range writes {
			_ = call.Write(context.Background(), value)
		}
	} else if present, _ := scenario.Given.Invocation["inputPresent"].(bool); present {
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
		"schemaMode":         scenario.Given.Source["content"] != nil,
		"reflectionRequests": []any{},
		"outputs":            outputs,
		"dispatches":         anySlice(transport.dispatches),
		"peer":               map[string]any{"endStreamCount": transport.endStreamCount},
	}
	if joined {
		data["joinedSynthesis"] = true
	}
	if len(transport.dispatches) > 0 {
		data["dispatch"] = transport.dispatches[0]
	}
	if terminal == nil {
		return processorscenarios.Observation{Disposition: "complete", Phase: "completion", Data: data}
	}
	data["error"] = normalizedConnectInvocationError(t, terminal)
	if terminal.Code == openbindings.ErrCodeContextRequired {
		return processorscenarios.Observation{Disposition: "context-required", Phase: "pre-dispatch", Data: data}
	}
	if len(transport.dispatches) == 0 {
		return processorscenarios.Observation{Disposition: "refusal", Phase: "pre-dispatch", Data: data}
	}
	phase := "completion"
	// Before any value has crossed, a dispatched invocation's failure belongs
	// to response processing. Once a value has crossed, a later terminal is a
	// completion failure. Derive this from observation instead of scenario IDs.
	if len(outputs) == 0 {
		phase = "response"
	}
	return processorscenarios.Observation{Disposition: "error", Phase: phase, Data: data}
}

func connectOperationForRef(t *testing.T, iface *openbindings.Interface, ref string) string {
	t.Helper()
	for _, binding := range iface.Bindings {
		if binding.Ref == ref {
			return binding.Operation
		}
	}
	t.Fatalf("synthesized Connect interface has no binding for %q", ref)
	return ""
}

func normalizedConnectInvocationError(t *testing.T, err *openbindings.InvocationError) map[string]any {
	t.Helper()
	encoded, marshalErr := json.Marshal(err)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	var result map[string]any
	if unmarshalErr := json.Unmarshal(encoded, &result); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	return result
}

func normalizedConnectHeaders(headers http.Header) map[string]any {
	out := map[string]any{}
	for name, values := range headers {
		if len(values) > 0 {
			out[strings.ToLower(name)] = values[0]
		}
	}
	return out
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func numberAsInt(value any) (int, bool) {
	number, ok := value.(float64)
	return int(number), ok && number == float64(int(number))
}

func anySlice(values []map[string]any) []any {
	result := make([]any, len(values))
	for i := range values {
		result[i] = values[i]
	}
	return result
}
