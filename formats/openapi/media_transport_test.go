package openapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"reflect"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/invoke"
)

func TestInvokerPropertyMediaAndTypeless31WireCarriage(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		version     string
		bindingSpec string
		context     map[string]any
		wantType    string
	}{
		{name: "3.0 configured", version: "3.0.4", bindingSpec: BindingSpecOpenAPI30, context: map[string]any{"configuration": map[string]any{"propertyMedia": map[string]any{"file": "image/png"}}}, wantType: "image/png"},
		{name: "3.1 default", version: "3.1.1", bindingSpec: BindingSpecOpenAPI31, wantType: "application/octet-stream"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			spec := `{"openapi":"` + testCase.version + `","info":{"title":"t","version":"1"},"servers":[{"url":"https://example.test"}],"paths":{"/upload":{"post":{"requestBody":{"required":true,"content":{"multipart/form-data":{"schema":{"type":"object","properties":{"file":{}}}}}},"responses":{"204":{"description":"ok"}}}}}}`
			var wireBody []byte
			var wireType string
			capture := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
				wireBody, _ = io.ReadAll(request.Body)
				wireType = request.Header.Get("Content-Type")
				return &http.Response{StatusCode: 204, Status: "204 No Content", Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(nil)), Request: request}, nil
			})
			call := NewInvokerWithClient(&http.Client{Transport: capture}).InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
				Source:   invoke.InvocationSource{BindingSpec: testCase.bindingSpec, Content: openbindings.TextContent(spec)},
				Selector: "#/paths/~1upload/post", Context: testCase.context,
			})
			outputs, invocationErr := driveOutputs(context.Background(), call, map[string]any{"body": map[string]any{"file": "YWJj"}})
			if invocationErr != nil || len(outputs) != 0 {
				t.Fatalf("invoke = %#v, %v", outputs, invocationErr)
			}
			_, params, _ := mime.ParseMediaType(wireType)
			reader := multipart.NewReader(bytes.NewReader(wireBody), params["boundary"])
			part, err := reader.NextPart()
			if err != nil {
				t.Fatal(err)
			}
			partBody, _ := io.ReadAll(part)
			if part.Header.Get("Content-Type") != testCase.wantType || string(partBody) != "abc" {
				t.Fatalf("part = (%q, %q), want (%q, abc)", part.Header.Get("Content-Type"), partBody, testCase.wantType)
			}
		})
	}
}

func TestRequiredPropertyMediaIsPreflightableAndOptionalIsInvocationOnly(t *testing.T) {
	for _, required := range []bool{true, false} {
		t.Run(map[bool]string{true: "required", false: "optional"}[required], func(t *testing.T) {
			requiredJSON, _ := json.Marshal(required)
			spec := `{"openapi":"3.0.4","info":{"title":"t","version":"1"},"servers":[{"url":"https://example.test"}],"paths":{"/upload":{"post":{"requestBody":{"required":` + string(requiredJSON) + `,"content":{"multipart/form-data":{"schema":{"type":"object","properties":{"file":{}}}}}},"responses":{"204":{"description":"ok"}}}}}}`
			dispatches := 0
			capture := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
				dispatches++
				return &http.Response{StatusCode: 204, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(nil)), Request: request}, nil
			})
			invoker := NewInvokerWithClient(&http.Client{Transport: capture})
			args := &invoke.BindingInvocationArgs{Source: invoke.InvocationSource{BindingSpec: BindingSpecOpenAPI30, Content: openbindings.TextContent(spec)}, Selector: "#/paths/~1upload/post"}
			details, err := invoker.PrepareBinding(context.Background(), args)
			if err != nil {
				t.Fatal(err)
			}
			if required {
				if details == nil || len(details.Alternatives) != 1 || len(details.Alternatives[0].Requirements) != 1 {
					t.Fatalf("required propertyMedia details = %#v", details)
				}
				requirement := details.Alternatives[0].Requirements[0]
				if requirement.Extra["point"] != "propertyMedia" || requirement.Extra["path"] != "/file" {
					t.Fatalf("propertyMedia requirement = %#v", requirement)
				}
			} else if details != nil {
				t.Fatalf("optional body preflight unexpectedly requires propertyMedia: %#v", details)
			}
			call := invoker.InvokeBinding(context.Background(), args)
			_, invocationErr := driveSingle(t, call, map[string]any{"body": map[string]any{"file": "YWJj"}})
			if invocationErr == nil || dispatches != 0 {
				t.Fatalf("missing propertyMedia invocation = %v after %d dispatches", invocationErr, dispatches)
			}
			if required && invocationErr.Code != invoke.ErrCodeContextRequired {
				t.Fatalf("required body error = %v, want CONTEXT_REQUIRED", invocationErr)
			}
			if !required && invocationErr.Code != invoke.ErrCodeRefused {
				t.Fatalf("optional supplied body error = %v, want refusal", invocationErr)
			}
		})
	}
}

func TestInvokerResponseBoundaryIsUnaryAndEmptyEmitsNothing(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		status      int
		headers     http.Header
		body        string
		wantOutputs []any
	}{
		{
			name: "missing Content-Type assumes raw octets", content: `"application/octet-stream":{}`,
			status: 200, headers: http.Header{}, body: "abc", wantOutputs: []any{"YWJj"},
		},
		{
			name: "event stream is one unary value", content: `"text/event-stream":{"schema":{"type":"string"}}`,
			status: 200, headers: http.Header{"Content-Type": {"text/event-stream"}}, body: "data: one\n\ndata: two\n\n",
			wantOutputs: []any{"data: one\n\ndata: two\n\n"},
		},
		{
			name: "empty response emits no value", content: `"application/json":{"schema":{"type":"object"}}`,
			status: 200, headers: http.Header{"Content-Type": {"application/json"}}, body: "", wantOutputs: nil,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			spec := `{"openapi":"3.1.1","info":{"title":"t","version":"1"},"servers":[{"url":"https://example.test"}],"paths":{"/x":{"get":{"responses":{"200":{"description":"ok","content":{` + testCase.content + `}}}}}}}`
			transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: testCase.status, Status: http.StatusText(testCase.status), Header: testCase.headers.Clone(),
					Body: io.NopCloser(strings.NewReader(testCase.body)), Request: request,
				}, nil
			})
			call := NewInvokerWithClient(&http.Client{Transport: transport}).InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
				Source:   invoke.InvocationSource{BindingSpec: BindingSpecOpenAPI31, Content: openbindings.TextContent(spec)},
				Selector: "#/paths/~1x/get",
			})
			outputs, invocationErr := driveOutputs(context.Background(), call, nil)
			if invocationErr != nil || !reflect.DeepEqual(outputs, testCase.wantOutputs) {
				t.Fatalf("outputs = %#v, %v; want %#v", outputs, invocationErr, testCase.wantOutputs)
			}
		})
	}
}
