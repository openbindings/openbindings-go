package openapi

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

const revision7ByteSpec = `{"openapi":"3.0.4","info":{"title":"schema-omitted bytes","version":"1"},"servers":[{"url":"https://example.test"}],"paths":{"/archive":{"post":{"operationId":"storeArchive","requestBody":{"required":true,"content":{"application/octet-stream":{}}},"responses":{"200":{"description":"stored","content":{"application/octet-stream":{}}}}}}}}`

const revision7ResponseOnlySpec = `{"openapi":"3.0.4","info":{"title":"schema-omitted response","version":"1"},"servers":[{"url":"https://example.test"}],"paths":{"/archive":{"get":{"operationId":"readArchive","responses":{"200":{"description":"archive","content":{"application/octet-stream":{}}}}}}}}`

func TestRevision7SchemaOmittedOAS30BytesRoundTripAndSynthesis(t *testing.T) {
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		if req.Header.Get("Content-Type") != "application/octet-stream" || !reflect.DeepEqual(body, []byte{0, 1, 254, 255}) {
			t.Fatalf("request = content-type %q body %v", req.Header.Get("Content-Type"), body)
		}
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": {"application/octet-stream"}},
			Body:       io.NopCloser(strings.NewReader(string([]byte{222, 173, 190, 239}))),
			Request:    req,
		}, nil
	})
	call := NewInvokerWithClient(&http.Client{Transport: transport}).InvokeBinding(context.Background(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{BindingSpec: BindingSpecV7, Content: openbindings.TextContent(revision7ByteSpec)},
		Ref:    "#/paths/~1archive/post",
	})
	outputs, invocationErr := driveOutputs(context.Background(), call, map[string]any{"body": "AAH+/w=="})
	if invocationErr != nil || !reflect.DeepEqual(outputs, []any{"3q2+7w=="}) {
		t.Fatalf("outputs = %#v, %v", outputs, invocationErr)
	}

	iface, err := NewSynthesizer().SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{BindingSpec: BindingSpecV7, Content: openbindings.TextContent(revision7ByteSpec)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	operation := iface.Operations["storeArchive"]
	wantBoundary := map[string]any{"type": "string", "contentEncoding": "base64"}
	input, _ := operation.Input.(map[string]any)
	properties, _ := input["properties"].(map[string]any)
	if !reflect.DeepEqual(properties[syntheticBodyProperty], wantBoundary) || !reflect.DeepEqual(operation.Output, wantBoundary) {
		t.Fatalf("schemas = input %#v output %#v", operation.Input, operation.Output)
	}
}

func TestRevision7KeepsRevision6SchemaOmittedOAS30RequestExclusionImmutable(t *testing.T) {
	_, err := NewSynthesizer().SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{BindingSpec: BindingSpecV6, Content: openbindings.TextContent(revision7ByteSpec)}},
	})
	if err == nil || !strings.Contains(err.Error(), "outside the families") {
		t.Fatalf("revision 6 synthesis error = %v", err)
	}
}

func TestRevision7KeepsRevision6SchemaOmittedOAS30ResponseTextLaneImmutable(t *testing.T) {
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": {"application/octet-stream"}},
			Body:       io.NopCloser(strings.NewReader("raw")),
			Request:    req,
		}, nil
	})
	call := NewInvokerWithClient(&http.Client{Transport: transport}).InvokeBinding(context.Background(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{BindingSpec: BindingSpecV6, Content: openbindings.TextContent(revision7ResponseOnlySpec)},
		Ref:    "#/paths/~1archive/get",
	})
	outputs, invocationErr := driveOutputs(context.Background(), call, nil)
	if invocationErr != nil || !reflect.DeepEqual(outputs, []any{"raw"}) {
		t.Fatalf("revision 6 outputs = %#v, %v", outputs, invocationErr)
	}
}
