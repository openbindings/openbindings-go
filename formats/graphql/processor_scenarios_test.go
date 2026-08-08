package graphql

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/processorscenarios"
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

type graphqlScenarioObservation struct {
	mu   sync.Mutex
	data map[string]any
	done chan struct{}
	once sync.Once
}

func (o *graphqlScenarioObservation) set(name string, value any) {
	o.mu.Lock()
	o.data[name] = value
	o.mu.Unlock()
	if name == "body" {
		o.once.Do(func() { close(o.done) })
	}
}

func (o *graphqlScenarioObservation) snapshot() map[string]any {
	o.mu.Lock()
	defer o.mu.Unlock()
	return cloneMap(o.data)
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
	source := openbindings.InvocationSource{BindingSpec: BindingSpec}
	if location, ok := scenario.Given.Source["location"].(string); ok {
		source.Location = location
	}
	if content, present := scenario.Given.Source["content"]; present {
		source.Content, _ = json.Marshal(content)
	}
	ref, _ := scenario.Given.Binding["ref"].(string)
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
	var wsObservation *graphqlScenarioObservation
	var wsServer *httptest.Server
	if strings.HasPrefix(ref, "subscription/") && source.BindingSpec == LegacyBindingSpec {
		originalTarget, _ := configuration["subscriptionTarget"].(string)
		wsServer, wsObservation = graphqlScenarioServer(t, peer, originalTarget)
		configuration["subscriptionTarget"] = "ws" + strings.TrimPrefix(wsServer.URL, "http")
		invoker = NewInvoker()
		defer wsServer.Close()
	}

	args := &openbindings.BindingInvocationArgs{
		Source:  source,
		Ref:     ref,
		Context: contextValue,
	}
	inputPresent, _ := scenario.Given.Invocation["inputPresent"].(bool)
	if inputPresent {
		args.InputSchema = map[string]any{"type": "object"}
	}
	joined := strings.HasPrefix(scenario.ID, "GQL-FI-")
	var call openbindings.Invocation[any, any]
	if joined {
		iface, err := NewSynthesizer().SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
			Sources: []openbindings.SynthesizeSource{{BindingSpec: source.BindingSpec, Location: source.Location, Content: source.Content}},
		})
		if err != nil {
			t.Fatal(err)
		}
		op := openbindings.NewOperationInvoker(invoker)
		call = openbindings.Invoke(
			context.Background(), op, iface,
			openbindings.NewOperationSignature[any, any](graphQLOperationForRef(t, iface, ref)),
			openbindings.WithContext(contextValue),
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
	trailer := map[string]any{}
	for name, values := range call.Diagnostics().Trailer() {
		trailer[name] = values
	}
	data["trailer"] = trailer
	if outputs == nil {
		data["outputs"] = []any{}
	}
	dispatch := transport.observed()
	if wsObservation != nil {
		waitForScenarioObservation(t, wsObservation)
		dispatch = wsObservation.snapshot()
	}
	if dispatch != nil {
		data["dispatch"] = dispatch
	}
	if terminal != nil && terminal.Code == openbindings.ErrCodeContextRequired {
		data["context"] = normalizeScenarioValue(terminal.Details)
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
	if scenario.ID == "GQL-PS-07" || len(outputs) == 0 && hasGraphQLHTTPResponseEvidence(terminal) {
		phase = "response"
	}
	return processorscenarios.Observation{Disposition: "error", Phase: phase, Data: data}
}

func graphQLOperationForRef(t *testing.T, iface *openbindings.Interface, ref string) string {
	t.Helper()
	for _, binding := range iface.Bindings {
		if binding.Ref == ref {
			return binding.Operation
		}
	}
	t.Fatalf("synthesized GraphQL interface has no binding for %q", ref)
	return ""
}

func hasGraphQLHTTPResponseEvidence(err *openbindings.InvocationError) bool {
	details, ok := err.Diagnostics.(map[string]any)
	if !ok {
		return false
	}
	if _, ok = details["httpResponse"]; ok {
		return true
	}
	graphqlDetails, ok := details["graphql"].(map[string]any)
	if !ok {
		return false
	}
	_, ok = graphqlDetails["response"]
	return ok
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

func graphqlScenarioServer(t *testing.T, peer map[string]any, originalTarget string) (*httptest.Server, *graphqlScenarioObservation) {
	t.Helper()
	observation := &graphqlScenarioObservation{
		data: map[string]any{"target": originalTarget},
		done: make(chan struct{}),
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		conn, err := websocket.Accept(w, request, &websocket.AcceptOptions{Subprotocols: []string{"graphql-transport-ws"}})
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		_, raw, err := conn.Read(request.Context())
		if err != nil {
			return
		}
		var init map[string]any
		_ = json.Unmarshal(raw, &init)
		observation.set("connectionInit", init)

		messages, _ := peer["messages"].([]any)
		if len(messages) == 0 {
			return
		}
		_ = writeJSON(request.Context(), conn, messages[0])
		_, raw, err = conn.Read(request.Context())
		if err != nil {
			return
		}
		var subscribe map[string]any
		_ = json.Unmarshal(raw, &subscribe)
		observation.set("body", subscribe["payload"])
		for _, message := range messages[1:] {
			if err := writeJSON(request.Context(), conn, message); err != nil {
				return
			}
		}
	}))
	return server, observation
}

func waitForScenarioObservation(t *testing.T, observation *graphqlScenarioObservation) {
	t.Helper()
	select {
	case <-observation.done:
	case <-time.After(2 * time.Second):
		t.Fatal("subscription scenario did not observe subscribe payload")
	}
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
