package openapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	openbindings "github.com/openbindings/openbindings-go"
)

func revision4ResponseSpec(openapiVersion, content string) string {
	return `{"openapi":` + quoteJSON(openapiVersion) + `,"info":{"title":"response carriage","version":"1"},"servers":[{"url":"https://example.test"}],"paths":{"/payload":{"get":{"operationId":"getPayload","responses":{"200":{"description":"payload","content":` + content + `}}}}}}`
}

func quoteJSON(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func invokeRevision4Response(t *testing.T, spec, bindingSpec, contentType string, body []byte) ([]any, *openbindings.InvocationError) {
	t.Helper()
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": {contentType}},
			Body:       io.NopCloser(strings.NewReader(string(body))),
			Request:    req,
		}, nil
	})
	call := NewInvokerWithClient(&http.Client{Transport: transport}).InvokeBinding(context.Background(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{BindingSpec: bindingSpec, Content: openbindings.TextContent(spec)},
		Ref:    "#/paths/~1payload/get",
	})
	return driveOutputs(context.Background(), call, nil)
}

func TestRevision4RawResponseAndSynthesisBoundary(t *testing.T) {
	spec := revision4ResponseSpec("3.0.4", `{"image/png":{"schema":{"type":"string","format":"binary"}}}`)
	outputs, invocationErr := invokeRevision4Response(t, spec, BindingSpec, "image/png", []byte{0, 1, 254, 255})
	if invocationErr != nil || !reflect.DeepEqual(outputs, []any{"AAH+/w=="}) {
		t.Fatalf("binary outputs = %#v, %v", outputs, invocationErr)
	}
	iface, err := NewSynthesizer().SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{BindingSpec: BindingSpec, Content: openbindings.TextContent(spec)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"type": "string", "contentEncoding": "base64"}
	if !reflect.DeepEqual(iface.Operations["getPayload"].Output, want) {
		t.Fatalf("output schema = %#v, want %#v", iface.Operations["getPayload"].Output, want)
	}
}

func TestRevision4ResponseRangeSelectsConcreteRawAndJSONLanes(t *testing.T) {
	rawSpec := revision4ResponseSpec("3.1.2", `{"image/*":{}}`)
	outputs, invocationErr := invokeRevision4Response(t, rawSpec, BindingSpec, "image/png", []byte{222, 173, 190, 239})
	if invocationErr != nil || !reflect.DeepEqual(outputs, []any{"3q2+7w=="}) {
		t.Fatalf("range raw outputs = %#v, %v", outputs, invocationErr)
	}

	jsonSpec := revision4ResponseSpec("3.1.2", `{"application/*":{"schema":{"type":"object","properties":{"ok":{"type":"boolean"}}}}}`)
	outputs, invocationErr = invokeRevision4Response(t, jsonSpec, BindingSpec, "application/json", []byte(`{"ok":true}`))
	if invocationErr != nil || !reflect.DeepEqual(outputs, []any{map[string]any{"ok": true}}) {
		t.Fatalf("range JSON outputs = %#v, %v", outputs, invocationErr)
	}
}

func TestRevision4ResponseRangeSpecificity(t *testing.T) {
	response := &openapi3.Response{Content: openapi3.Content{
		"*/*; profile=v1":     emptyMedia(),
		"image/*; profile=v1": emptyMedia(),
		"image/png":           emptyMedia(),
	}}
	match, err := governingResponseMediaMatchFor(response, "image/png; profile=v1", BindingSpec)
	if err != nil || match.key != "image/png" {
		t.Fatalf("range selection = %#v, %v", match, err)
	}
}

func TestRevision4ParameterizedRangeConfersPossibleStreamingCapability(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromData([]byte(revision4ResponseSpec("3.1.2", `{"text/*; profile=events":{}}`)))
	if err != nil {
		t.Fatal(err)
	}
	op := doc.Paths.Find("/payload").Get
	if !isStreamingCapableFor(op, BindingSpec) {
		t.Fatal("the candidate should recognize a parameterized text range as possibly event-stream")
	}
}
