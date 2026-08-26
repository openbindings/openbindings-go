package openapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/openbindings/openbindings-go/invoke"
	"github.com/openbindings/openbindings-go/synthesize"

	"github.com/recolabs/gnata"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/processorscenarios"
)

// TestOpenAPINativeDifferential is the independent-client gate for the first
// OpenAPI fidelity slice. The native side uses net/http against a real server;
// the OpenBindings side synthesizes the same artifact and invokes it through
// the operation layer. The comparison deliberately does not reuse the
// OpenAPI invoker's response decoder or failure-evidence builder.
func TestOpenAPINativeDifferential(t *testing.T) {
	t.Skip("N10/M7 migrates the invocation-fidelity corpus from the retired OpenAPI token")
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
			peerBody := differentialPeerBody(t, scenario.Given.Peer)
			status, ok := numberAsInt(scenario.Given.Peer["status"])
			if !ok {
				t.Skip("scenario intentionally completes before a native HTTP exchange")
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				for name, value := range differentialPeerHeaders(scenario.Given.Peer) {
					if text, ok := value.(string); ok {
						w.Header().Set(name, text)
					}
				}
				w.WriteHeader(status)
				_, _ = w.Write(peerBody)
			}))
			t.Cleanup(server.Close)

			content := differentialArtifact(t, scenario, server.URL)
			method, path := differentialNativeTarget(t, scenario)
			var nativeRequestBody io.Reader
			nativeContentType := ""
			switch scenario.ID {
			case "OAPI-FI-06":
				input, _ := scenario.Given.Invocation["input"].(map[string]any)
				encoded, _ := input[syntheticBodyProperty].(string)
				decoded, decodeErr := base64.StdEncoding.DecodeString(encoded)
				if decodeErr != nil {
					t.Fatal(decodeErr)
				}
				nativeRequestBody = bytes.NewReader(decoded)
				nativeContentType = "image/png"
			case "OAPI-FI-07":
				encoded, encodeErr := json.Marshal(scenario.Given.Invocation["input"])
				if encodeErr != nil {
					t.Fatal(encodeErr)
				}
				nativeRequestBody = bytes.NewReader(encoded)
				nativeContentType, _ = scenario.Given.Configuration["requestMedia"].(string)
			}
			nativeReq, err := http.NewRequestWithContext(context.Background(), method, server.URL+path, nativeRequestBody)
			if err != nil {
				t.Fatal(err)
			}
			if nativeContentType != "" {
				nativeReq.Header.Set("Content-Type", nativeContentType)
			}
			nativeResp, err := server.Client().Do(nativeReq)
			if err != nil {
				t.Fatalf("native request failed: %v", err)
			}
			nativeBody, err := io.ReadAll(nativeResp.Body)
			_ = nativeResp.Body.Close()
			if err != nil {
				t.Fatalf("read native response: %v", err)
			}

			iface, err := NewSynthesizer().SynthesizeInterface(context.Background(), &synthesize.SynthesizeInput{
				Sources: []synthesize.SynthesizeSource{{
					BindingSpec: file.BindingSpec,
					Content:     content,
				}},
			})
			if err != nil {
				t.Fatalf("synthesis failed: %v", err)
			}
			opInvoker := invoke.NewOperationInvoker(NewInvokerWithClient(server.Client()))
			opInvoker.TransformEvaluator = openAPIJSONataEvaluator{}
			call := invoke.Invoke(
				context.Background(),
				opInvoker,
				iface,
				invoke.NewOperationSignature[any, any](openAPIFidelityOperationID(scenario.ID)),
				invoke.WithContext(scenarioContext(scenario)),
			)
			if present, _ := scenario.Given.Invocation["inputPresent"].(bool); present {
				_ = call.Write(context.Background(), scenario.Given.Invocation["input"])
			}
			_ = call.Close()
			outputs, terminal := differentialOutputs(call)

			nativeSucceeded := nativeResp.StatusCode >= 200 && nativeResp.StatusCode < 300
			if nativeSucceeded {
				if terminal != nil {
					t.Fatalf("native request completed but OpenBindings failed: %v", terminal)
				}
				if len(nativeBody) == 0 {
					if len(outputs) != 0 {
						t.Fatalf("empty native response produced outputs %#v", outputs)
					}
					return
				}
				var want any
				for _, alternative := range scenario.Expected {
					if alternative.Disposition != "complete" {
						continue
					}
					for _, assertion := range alternative.Assertions {
						if assertion.Path == "/outputs" {
							want = assertion.Equals
						}
					}
				}
				if want == nil || !reflect.DeepEqual(outputs, want) {
					t.Fatalf("output differential\ngot:  %#v\nwant: %#v", outputs, want)
				}
				return
			}

			if terminal == nil {
				t.Fatalf("native request failed with HTTP %d but OpenBindings completed: outputs=%#v", nativeResp.StatusCode, outputs)
			}
			if len(outputs) != 0 {
				t.Fatalf("failure emitted operation outputs: %#v", outputs)
			}
			terminalError := invoke.AsInvocationError(terminal)
			if terminalError.Code != invoke.ErrCodeExecutionFailed {
				t.Fatalf("native unsuccessful completion mapped to %q", terminalError.Code)
			}
		})
	}
}

// TestOpenAPIV2CollisionDifferential proves the first revision-2 fidelity
// repair end to end. Three independently authored values named "id" (path,
// query, and JSON body) remain independently writable through a
// protocol-neutral synthesized operation and arrive at the server exactly as
// native HTTP code sends them.
func TestOpenAPIV2CollisionDifferential(t *testing.T) {
	type observation struct {
		method  string
		pathID  string
		queryID string
		body    map[string]any
	}
	observations := make(chan observation, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		observations <- observation{
			method:  r.Method,
			pathID:  strings.TrimPrefix(r.URL.Path, "/items/"),
			queryID: r.URL.Query().Get("id"),
			body:    body,
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(server.Close)

	nativeBody := strings.NewReader(`{"id":"body-value","name":"widget"}`)
	nativeReq, err := http.NewRequest(http.MethodPost, server.URL+"/items/path-value?id=query-value", nativeBody)
	if err != nil {
		t.Fatal(err)
	}
	nativeReq.Header.Set("Content-Type", "application/json")
	nativeResp, err := server.Client().Do(nativeReq)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, nativeResp.Body)
	_ = nativeResp.Body.Close()
	want := <-observations

	spec := fmt.Sprintf(`{
	  "openapi":"3.1.0",
	  "info":{"title":"collision","version":"1"},
	  "servers":[{"url":%q}],
	  "paths":{"/items/{id}":{"post":{
	    "operationId":"createItem",
	    "parameters":[
	      {"name":"id","in":"path","required":true,"description":"resource identifier","schema":{"type":"string"}},
	      {"name":"id","in":"query","description":"request correlation identifier","schema":{"type":"string"}}
	    ],
	    "requestBody":{"required":true,"content":{"application/json":{"schema":{
	      "type":"object",
	      "properties":{"id":{"type":"string","description":"body identifier"},"name":{"type":"string"}},
	      "required":["id","name"]
	    }}}},
	    "responses":{
	      "200":{
	        "description":"ok",
	        "content":{"application/json":{"schema":{"type":"object","properties":{"ok":{"type":"boolean"}}}}}
	      }
	    }
	  }}}
	}`, server.URL)
	iface, err := NewSynthesizer().SynthesizeInterface(context.Background(), &synthesize.SynthesizeInput{
		Sources: []synthesize.SynthesizeSource{{BindingSpec: bindingSpecForTestDocument(spec), Content: openbindings.TextContent(spec)}},
	})
	if err != nil {
		t.Fatalf("revision-2 synthesis failed: %v", err)
	}
	binding := iface.Bindings["createItem.openapi"]
	if binding.InputTransform == nil {
		t.Fatal("collision-preserving synthesis omitted its binding-private input transform")
	}
	props := iface.Operations["createItem"].Input.(map[string]any)["properties"].(map[string]any)
	for _, field := range []string{"id", "id_2", "id_3", "name"} {
		if _, ok := props[field]; !ok {
			t.Fatalf("synthesized operation input omitted abstract field %q: %#v", field, props)
		}
	}

	opInvoker := invoke.NewOperationInvoker(NewInvokerWithClient(server.Client()))
	opInvoker.TransformEvaluator = openAPIJSONataEvaluator{}
	call := invoke.Invoke(
		context.Background(),
		opInvoker,
		iface,
		invoke.NewOperationSignature[any, any]("createItem"),
	)
	if err := call.Write(context.Background(), map[string]any{
		"id": "path-value", "id_2": "query-value", "id_3": "body-value", "name": "widget",
	}); err != nil {
		t.Fatalf("write operation input: %v", err)
	}
	_ = call.Close()
	outputs, terminal := differentialOutputs(call)
	if terminal != nil {
		t.Fatalf("OpenBindings invocation failed: %v", terminal)
	}
	if !reflect.DeepEqual(outputs, []any{map[string]any{"ok": true}}) {
		t.Fatalf("unexpected outputs: %#v", outputs)
	}
	got := <-observations
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("native/OB request differential\ngot:  %#v\nwant: %#v", got, want)
	}
}

// TestOpenAPIAllOfMultipartDifferential is the minimized wild-corpus
// regression for OAPI-P-03's rule that allOf contributes its recursive body
// property union. Synthesis and invocation must agree on those properties;
// neither may invent a literal multipart part named "body".
func TestOpenAPIAllOfMultipartDifferential(t *testing.T) {
	type observation struct {
		transaction string
		fileName    string
		file        []byte
		hasBodyPart bool
	}
	observations := make(chan observation, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse multipart request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			t.Errorf("read multipart file: %v (values=%#v files=%#v)", err, r.MultipartForm.Value, r.MultipartForm.File)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		fileBytes, err := io.ReadAll(file)
		_ = file.Close()
		if err != nil {
			t.Errorf("read multipart file bytes: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, bodyValue := r.MultipartForm.Value["body"]
		_, bodyFile := r.MultipartForm.File["body"]
		observations <- observation{
			transaction: r.FormValue("transaction"),
			fileName:    r.FormValue("fileName"),
			file:        fileBytes,
			hasBodyPart: bodyValue || bodyFile,
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	spec := fmt.Sprintf(`{
	  "openapi":"3.0.3",
	  "info":{"title":"multipart allOf","version":"1"},
	  "servers":[{"url":%q}],
	  "paths":{"/upload":{"post":{
	    "operationId":"uploadAllOf",
	    "requestBody":{"required":true,"content":{"multipart/form-data":{"schema":{"allOf":[
	      {"type":"object","properties":{"transaction":{"type":"string"},"fileName":{"type":"string"}},"required":["transaction","fileName"]},
	      {"type":"object","properties":{"file":{"type":"string","format":"binary"}},"required":["file"]}
	    ]}}}},
	    "responses":{"204":{"description":"done"}}
	  }}}
	}`, server.URL)
	iface, err := NewSynthesizer().SynthesizeInterface(context.Background(), &synthesize.SynthesizeInput{
		Sources: []synthesize.SynthesizeSource{{BindingSpec: bindingSpecForTestDocument(spec), Content: openbindings.TextContent(spec)}},
	})
	if err != nil {
		t.Fatalf("synthesis failed: %v", err)
	}
	input, ok := iface.Operations["uploadAllOf"].Input.(map[string]any)
	if !ok {
		t.Fatalf("operation input is not an object schema: %#v", iface.Operations["uploadAllOf"].Input)
	}
	properties, _ := input["properties"].(map[string]any)
	for _, name := range []string{"transaction", "fileName", "file"} {
		if _, present := properties[name]; !present {
			t.Fatalf("synthesized input omitted allOf property %q: %#v", name, properties)
		}
	}
	if _, present := properties["body"]; present {
		t.Fatalf("synthesized input invented a body wrapper: %#v", properties)
	}

	opInvoker := invoke.NewOperationInvoker(NewInvokerWithClient(server.Client()))
	opInvoker.TransformEvaluator = openAPIJSONataEvaluator{}
	call := invoke.Invoke(
		context.Background(),
		opInvoker,
		iface,
		invoke.NewOperationSignature[any, any]("uploadAllOf"),
	)
	if err := call.Write(context.Background(), map[string]any{
		"transaction": "tx-1", "fileName": "a.bin", "file": "AQID",
	}); err != nil {
		t.Fatalf("write operation input: %v", err)
	}
	_ = call.Close()
	outputs, terminal := differentialOutputs(call)
	if terminal != nil {
		t.Fatalf("invocation failed: %v", terminal)
	}
	if len(outputs) != 0 {
		t.Fatalf("unexpected outputs: %#v", outputs)
	}
	got := <-observations
	want := observation{transaction: "tx-1", fileName: "a.bin", file: []byte{1, 2, 3}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("multipart allOf differential\ngot:  %#v\nwant: %#v", got, want)
	}
}

type openAPIJSONataEvaluator struct{}

func (openAPIJSONataEvaluator) Evaluate(expression string, data any) (any, error) {
	compiled, err := gnata.Compile(expression)
	if err != nil {
		return nil, err
	}
	input, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	result, err := compiled.EvalBytes(context.Background(), input)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, invoke.ErrTransformUndefined
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	var normalized any
	if err := json.Unmarshal(raw, &normalized); err != nil {
		return nil, err
	}
	return normalized, nil
}

func differentialArtifact(t *testing.T, scenario processorscenarios.Scenario, serverURL string) json.RawMessage {
	t.Helper()
	content, ok := scenario.Given.Source["content"].(map[string]any)
	if !ok {
		t.Fatalf("%s source content is not an object", scenario.ID)
	}
	raw, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	var cloned map[string]any
	if err := json.Unmarshal(raw, &cloned); err != nil {
		t.Fatal(err)
	}
	cloned["servers"] = []any{map[string]any{"url": serverURL}}
	raw, err = json.Marshal(cloned)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func differentialNativeTarget(t *testing.T, scenario processorscenarios.Scenario) (string, string) {
	t.Helper()
	selector, _ := scenario.Given.Binding["selector"].(string)
	const prefix = "#/paths/"
	if !strings.HasPrefix(selector, prefix) {
		t.Fatalf("%s selector %q is outside the bounded paths-operation slice", scenario.ID, selector)
	}
	parts := strings.Split(strings.TrimPrefix(selector, prefix), "/")
	if len(parts) != 2 {
		t.Fatalf("%s selector %q does not identify one paths operation", scenario.ID, selector)
	}
	path := strings.ReplaceAll(strings.ReplaceAll(parts[0], "~1", "/"), "~0", "~")
	return strings.ToUpper(parts[1]), path
}

func differentialPeerBody(t *testing.T, peer map[string]any) []byte {
	t.Helper()
	if encoded, ok := peer["bodyBase64"].(string); ok {
		body, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	if body, ok := peer["body"].(string); ok {
		return []byte(body)
	}
	return nil
}

func differentialPeerHeaders(peer map[string]any) map[string]any {
	headers, _ := peer["headers"].(map[string]any)
	return headers
}

func differentialOutputs(call invoke.Invocation[any, any]) ([]any, error) {
	outputs := []any{}
	stream := call.Outputs()
	for {
		value, err := stream.Read(context.Background())
		if err == nil {
			outputs = append(outputs, value)
			continue
		}
		if err == io.EOF {
			return outputs, nil
		}
		return outputs, fmt.Errorf("%w", err)
	}
}
