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

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/processorscenarios"
)

// TestOpenAPINativeDifferential is the independent-client gate for the first
// OpenAPI fidelity slice. The native side uses net/http against a real server;
// the OpenBindings side synthesizes the same artifact and invokes it through
// the operation layer. The comparison deliberately does not reuse the
// OpenAPI invoker's response decoder or failure-evidence builder.
func TestOpenAPINativeDifferential(t *testing.T) {
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
				t.Fatalf("%s peer status is not an integer", scenario.ID)
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
			nativeReq, err := http.NewRequestWithContext(context.Background(), method, server.URL+path, nil)
			if err != nil {
				t.Fatal(err)
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

			iface, err := NewSynthesizer().SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
				Sources: []openbindings.SynthesizeSource{{
					BindingSpec: BindingSpec,
					Content:     content,
				}},
			})
			if err != nil {
				t.Fatalf("synthesis failed: %v", err)
			}
			call := openbindings.Invoke(
				context.Background(),
				openbindings.NewOperationInvoker(NewInvokerWithClient(server.Client())),
				iface,
				openbindings.NewOperationSignature[any, any](openAPIFidelityOperationID(scenario.ID)),
			)
			_ = call.Close()
			outputs, terminal := differentialOutputs(call)

			nativeSucceeded := nativeResp.StatusCode >= 200 && nativeResp.StatusCode < 300
			if nativeSucceeded {
				if terminal != nil {
					t.Fatalf("native request completed but OpenBindings failed: %v", terminal)
				}
				var nativeValue any
				if err := json.Unmarshal(nativeBody, &nativeValue); err != nil {
					t.Fatalf("native JSON decode failed: %v", err)
				}
				if want := []any{nativeValue}; !reflect.DeepEqual(outputs, want) {
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
			evidence, ok := FailureEvidenceFrom(terminal)
			if !ok {
				t.Fatalf("OpenBindings failure omitted typed OpenAPI evidence: %v", terminal)
			}
			if evidence.HTTPResponse.Status != nativeResp.StatusCode {
				t.Errorf("status differential: got %d, native %d", evidence.HTTPResponse.Status, nativeResp.StatusCode)
			}
			if !evidence.HTTPResponse.BodyCaptured || !bytes.Equal(evidence.HTTPResponse.Body, nativeBody) {
				t.Errorf("body differential: got captured=%v %v, native %v", evidence.HTTPResponse.BodyCaptured, evidence.HTTPResponse.Body, nativeBody)
			}
			for name := range differentialPeerHeaders(scenario.Given.Peer) {
				lower := strings.ToLower(name)
				if got, want := evidence.HTTPResponse.Headers[lower], nativeResp.Header.Values(name); !reflect.DeepEqual(got, want) {
					t.Errorf("header %s differential: got %#v, native %#v", name, got, want)
				}
			}
		})
	}
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
	ref, _ := scenario.Given.Binding["ref"].(string)
	const prefix = "#/paths/"
	if !strings.HasPrefix(ref, prefix) {
		t.Fatalf("%s ref %q is outside the bounded paths-operation slice", scenario.ID, ref)
	}
	parts := strings.Split(strings.TrimPrefix(ref, prefix), "/")
	if len(parts) != 2 {
		t.Fatalf("%s ref %q does not identify one paths operation", scenario.ID, ref)
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

func differentialOutputs(call openbindings.Invocation[any, any]) ([]any, error) {
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
