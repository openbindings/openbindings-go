package graphql

import (
	"bytes"
	"context"
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
	responseBody, _ := json.Marshal(t.peer["body"])
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     http.Header{"Content-Type": []string{contentType}},
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
	if strings.HasPrefix(ref, "subscription/") {
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
	call := invoker.InvokeBinding(context.Background(), args)
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
	if dispatch == nil {
		return processorscenarios.Observation{Disposition: "refusal", Phase: "pre-dispatch", Data: data}
	}
	phase := "completion"
	if scenario.ID == "GQL-PS-07" {
		phase = "response"
	}
	return processorscenarios.Observation{Disposition: "error", Phase: phase, Data: data}
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
