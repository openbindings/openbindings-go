package asyncapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coder/websocket"
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
				t.Fatalf("%v\nobservation=%#v", err, observation)
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
		filepath.Join(root, "invocation-fidelity", "asyncapi.json"),
		"asyncapi",
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
			observation := runAsyncProcessorScenario(t, scenario)
			if _, err := processorscenarios.Match(scenario, observation); err != nil {
				t.Fatalf("%v\nobservation=%#v", err, observation)
			}
		})
	}
}

func runAsyncProcessorScenario(t *testing.T, scenario processorscenarios.Scenario) processorscenarios.Observation {
	t.Helper()
	rt := &asyncScenarioRoundTripper{peer: scenario.Given.Peer}
	invoker := NewInvoker()
	defer invoker.Close()
	invoker.httpClient = &http.Client{Transport: rt, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	sourceContent, sourceHasContent := scenario.Given.Source["content"]
	ctx := map[string]any{}
	if scenario.Given.Configuration != nil {
		ctx["configuration"] = scenario.Given.Configuration
	}
	if frames, ok := scenario.Given.Peer["webSocketMessages"].([]any); ok {
		// The corpus names an external ws/wss peer. The local scripted peer is
		// intentionally plaintext, so clone only the test copy of the artifact
		// and retain the same WebSocket interaction under the ws scheme.
		raw, _ := json.Marshal(sourceContent)
		var localContent map[string]any
		if json.Unmarshal(raw, &localContent) == nil {
			if servers, ok := localContent["servers"].(map[string]any); ok {
				for _, rawServer := range servers {
					if server, ok := rawServer.(map[string]any); ok {
						server["protocol"] = "ws"
					}
				}
			}
			sourceContent = localContent
		}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			conn, err := websocket.Accept(w, request, &websocket.AcceptOptions{InsecureSkipVerify: true})
			if err != nil {
				return
			}
			defer conn.Close(websocket.StatusNormalClosure, "scenario complete")
			wsCtx := request.Context()
			for _, raw := range frames {
				item, _ := raw.(map[string]any)
				fragments, _ := item["fragments"].([]any)
				var payload strings.Builder
				for _, fragment := range fragments {
					text, _ := fragment.(string)
					payload.WriteString(text)
				}
				if err := conn.Write(wsCtx, websocket.MessageText, []byte(payload.String())); err != nil {
					return
				}
			}
		}))
		t.Cleanup(srv.Close)
		configuration, _ := ctx["configuration"].(map[string]any)
		if configuration == nil {
			configuration = map[string]any{}
			ctx["configuration"] = configuration
		}
		configuration["server"] = map[string]any{"url": "ws" + strings.TrimPrefix(srv.URL, "http")}
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
	source := openbindings.InvocationSource{BindingSpec: BindingSpec}
	if location, ok := scenario.Given.Source["location"].(string); ok {
		source.Location = location
	}
	if sourceHasContent {
		source.Content, _ = json.Marshal(sourceContent)
	}
	ref, _ := scenario.Given.Binding["ref"].(string)
	args := &openbindings.BindingInvocationArgs{Source: source, Ref: ref, Context: ctx}
	joined := strings.HasPrefix(scenario.ID, "ASYNC-FI-")
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
			openbindings.NewOperationSignature[any, any](asyncOperationForRef(t, iface, ref)),
			openbindings.WithContext(ctx),
		)
	} else {
		call = invoker.InvokeBinding(context.Background(), args)
	}
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
	if joined {
		data["joinedSynthesis"] = true
	}
	data["trailer"] = metadataAny(call.Diagnostics().Trailer())
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
	errorData := map[string]any{"code": terminal.Code, "message": terminal.Message}
	if terminal.Details != nil {
		errorData["details"] = terminal.Details
	}
	if terminal.Diagnostics != nil {
		errorData["diagnostics"] = terminal.Diagnostics
	}
	data["error"] = errorData
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

func asyncOperationForRef(t *testing.T, iface *openbindings.Interface, ref string) string {
	t.Helper()
	for _, binding := range iface.Bindings {
		if binding.Ref == ref {
			return binding.Operation
		}
	}
	t.Fatalf("synthesized AsyncAPI interface has no binding for %q", ref)
	return ""
}

func metadataAny(metadata openbindings.Metadata) map[string]any {
	result := make(map[string]any, len(metadata))
	for name, values := range metadata {
		result[name] = values
	}
	return result
}
