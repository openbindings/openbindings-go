package openapi

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/invoke"
	obsdk "github.com/openbindings/openbindings-go/sdk"
	"github.com/openbindings/openbindings-go/synthesize"
)

func TestAdapterProvidesOneSDKRegistration(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
			Request:    req,
		}, nil
	})}
	adapter := NewAdapterWithOptions(AdapterOptions{
		Invoker:             InvokerOptions{HTTPClient: client},
		SynthesisHTTPClient: client,
	})
	runtime, err := obsdk.New(obsdk.RuntimeOptions{Providers: []obsdk.BindingProvider{adapter}})
	if err != nil {
		t.Fatal(err)
	}
	document := openbindings.TextContent(`{
		"openapi":"3.1.0",
		"info":{"title":"Adapter proof","version":"1"},
		"servers":[{"url":"https://api.example.test"}],
		"paths":{"/ping":{"get":{"operationId":"ping","responses":{"200":{"description":"ok","content":{"application/json":{"schema":{"type":"object"}}}}}}}}
	}`)
	source := openbindings.Source{BindingSpec: BindingSpecOpenAPI31, Content: document}
	inspection, err := runtime.InspectSource(context.Background(), &source)
	if err != nil || !inspection.Exhaustive || len(inspection.Targets) != 1 || inspection.Targets[0].OperationKey != "ping" {
		t.Fatalf("inspection = (%#v, %v)", inspection, err)
	}
	result, err := runtime.SynthesizeInterfaceWithCoverage(context.Background(), &synthesize.SynthesizeInput{
		Sources: []synthesize.SynthesizeSource{{BindingSpec: BindingSpecOpenAPI31, Content: document}},
	})
	if err != nil || !result.Coverage.Exhaustive {
		t.Fatalf("synthesis = (%#v, %v)", result, err)
	}
	call := runtime.Invoke(context.Background(), result.Interface, "ping")
	_ = call.Close()
	output, err := invoke.Single(context.Background(), call.Outputs())
	if err != nil {
		t.Fatal(err)
	}
	if got := output.(map[string]any)["ok"]; got != true {
		t.Fatalf("output = %#v", output)
	}
}
