package openapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/invoke"
)

func TestMediaTransportAppliesCodingStacksAndOmitsAccept(t *testing.T) {
	document := loadTransportTestDocument(t, `{
      "openapi":"3.1.1","info":{"title":"t","version":"1"},
      "paths":{"/x":{"post":{
        "parameters":[{"name":"Content-Encoding","in":"header","required":true,"schema":{"type":"string","enum":["first, second"]}}],
        "responses":{"200":{"description":"ok","headers":{
          "Content-Encoding":{"required":true,"schema":{"type":"string","enum":["first, second"]}},
          "X-Required":{"required":true,"schema":{"type":"string"}}
        },"content":{"text/plain":{"schema":{"type":"string"}}}}}
      }}}}`)
	operation := document.Paths.Find("/x").Post
	var requestBody []byte
	var requestHeader http.Header
	decodeOrder := []string{}
	transport := rawCookieBridgeTransport{
		base: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			requestBody, _ = io.ReadAll(request.Body)
			requestHeader = request.Header.Clone()
			return &http.Response{
				StatusCode: 200, Status: "200 OK", Request: request,
				Header: http.Header{
					"Content-Type":     {"text/plain"},
					"Content-Encoding": {"first, second"},
					"X-Required":       {"yes"},
				},
				Body: io.NopCloser(strings.NewReader("second(first(payload))")),
			}, nil
		}),
		requestCodings: map[string]ContentEncoder{
			"first":  func(value []byte) ([]byte, error) { return []byte("first(" + string(value) + ")"), nil },
			"second": func(value []byte) ([]byte, error) { return []byte("second(" + string(value) + ")"), nil },
		},
		responseCodings: map[string]ContentDecoder{
			"first": func(value []byte) ([]byte, error) {
				decodeOrder = append(decodeOrder, "first")
				return unwrapCoding(value, "first")
			},
			"second": func(value []byte) ([]byte, error) {
				decodeOrder = append(decodeOrder, "second")
				return unwrapCoding(value, "second")
			},
		},
	}
	request, _ := http.NewRequest(http.MethodPost, "https://example.test/x", strings.NewReader("payload"))
	request.Header.Set("Accept", "text/plain")
	request.Header.Set("Content-Encoding", "first, second")
	request = request.WithContext(context.WithValue(request.Context(), mediaGovernanceContextKey{}, &mediaGovernance{
		document: document, operation: operation, parameters: operation.Parameters, bindingSpec: BindingSpecOpenAPI31,
	}))
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	decoded, _ := io.ReadAll(response.Body)
	if string(requestBody) != "second(first(payload))" {
		t.Fatalf("encoded request body = %q", requestBody)
	}
	if requestHeader.Get("Accept") != "" {
		t.Fatalf("wire request carried Accept: %q", requestHeader.Get("Accept"))
	}
	if string(decoded) != "payload" || !reflect.DeepEqual(decodeOrder, []string{"second", "first"}) {
		t.Fatalf("decoded response/order = %q, %v", decoded, decodeOrder)
	}
}

func TestMediaTransportBuffersFiniteEventStreamAsUnary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(response, "data: one\n\ndata: two\n\n")
	}))
	defer server.Close()

	document := loadTransportTestDocument(t, `{"openapi":"3.1.1","info":{"title":"t","version":"1"},"paths":{"/x":{"get":{"responses":{"200":{"description":"ok","content":{"text/event-stream":{"schema":{"type":"string"}}}}}}}}}`)
	request, _ := http.NewRequest(http.MethodGet, server.URL+"/x", nil)
	governance := &mediaGovernance{document: document, operation: document.Paths.Find("/x").Get, bindingSpec: BindingSpecOpenAPI31}
	request = request.WithContext(context.WithValue(request.Context(), mediaGovernanceContextKey{}, governance))
	transport := rawCookieBridgeTransport{base: http.DefaultTransport}
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	if response.Header.Get("Content-Type") != unaryEventStreamType || string(body) != "data: one\n\ndata: two\n\n" {
		t.Fatalf("unary event response = (%q, %q)", response.Header.Get("Content-Type"), body)
	}
}

func unwrapCoding(value []byte, name string) ([]byte, error) {
	prefix, suffix := name+"(", ")"
	if !strings.HasPrefix(string(value), prefix) || !strings.HasSuffix(string(value), suffix) {
		return nil, io.ErrUnexpectedEOF
	}
	return value[len(prefix) : len(value)-len(suffix)], nil
}

func TestMediaTransportCodingAndResponseGovernanceFailures(t *testing.T) {
	document := loadTransportTestDocument(t, `{
      "openapi":"3.1.1","info":{"title":"t","version":"1"},
      "paths":{"/x":{"get":{"responses":{
        "200":{"description":"ok","headers":{"X-Required":{"required":true,"schema":{"type":"string"}}},"content":{"application/octet-stream":{}}},
        "default":{"description":"fallback"}
      }}}}}`)
	operation := document.Paths.Find("/x").Get
	governance := &mediaGovernance{document: document, operation: operation, bindingSpec: BindingSpecOpenAPI31}
	request, _ := http.NewRequest(http.MethodGet, "https://example.test/x", nil)
	request = request.WithContext(context.WithValue(request.Context(), mediaGovernanceContextKey{}, governance))

	missingRequired := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("x")), Request: request}
	if _, err := applyResponseGovernance(request, missingRequired, nil); err == nil || !strings.Contains(err.Error(), "X-Required") {
		t.Fatalf("missing required header = %v", err)
	}

	requestCoding, _ := http.NewRequest(http.MethodPost, "https://example.test/x", strings.NewReader("x"))
	requestCoding.Header.Set("Content-Encoding", "br")
	requestCoding = requestCoding.WithContext(context.WithValue(requestCoding.Context(), mediaGovernanceContextKey{}, governance))
	if err := applyRequestContentCodings(requestCoding, nil); err == nil || !strings.Contains(err.Error(), "governing Header Parameter") {
		t.Fatalf("ungoverned request coding = %v", err)
	}

	ungovernedResponse := &http.Response{
		StatusCode: 200, Header: http.Header{"X-Required": {"yes"}, "Content-Encoding": {"br"}},
		Body: io.NopCloser(strings.NewReader("x")), Request: request,
	}
	if _, err := applyResponseGovernance(request, ungovernedResponse, nil); err == nil || !strings.Contains(err.Error(), "Header Object") {
		t.Fatalf("ungoverned response coding = %v", err)
	}

	undeclaredDocument := loadTransportTestDocument(t, `{"openapi":"3.1.1","info":{"title":"t","version":"1"},"paths":{"/x":{"get":{"responses":{"204":{"description":"empty"}}}}}}`)
	undeclared := &mediaGovernance{document: undeclaredDocument, operation: undeclaredDocument.Paths.Find("/x").Get, bindingSpec: BindingSpecOpenAPI31}
	undeclaredRequest := request.WithContext(context.WithValue(request.Context(), mediaGovernanceContextKey{}, undeclared))
	response := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("x")), Request: undeclaredRequest}
	if _, err := applyResponseGovernance(undeclaredRequest, response, nil); err == nil || !strings.Contains(err.Error(), "no governing Response Object") {
		t.Fatalf("non-empty undeclared response = %v", err)
	}
}

func TestResponseGovernanceAssumesOctetsAndDefinesHEADAsEmpty(t *testing.T) {
	document := loadTransportTestDocument(t, `{"openapi":"3.1.1","info":{"title":"t","version":"1"},"paths":{"/x":{"head":{"responses":{"200":{"description":"ok","content":{"application/octet-stream":{}}}}}}}}`)
	operation := document.Paths.Find("/x").Head
	governance := &mediaGovernance{document: document, operation: operation, bindingSpec: BindingSpecOpenAPI31}
	request, _ := http.NewRequest(http.MethodHead, "https://example.test/x", nil)
	request = request.WithContext(context.WithValue(request.Context(), mediaGovernanceContextKey{}, governance))
	response := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("ignored")), Request: request}
	governed, err := applyResponseGovernance(request, response, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(governed.Body)
	if len(body) != 0 || !governance.emptyResponse.Load() {
		t.Fatalf("HEAD governed body = %q, empty=%v", body, governance.emptyResponse.Load())
	}

	getRequest, _ := http.NewRequest(http.MethodGet, "https://example.test/x", nil)
	getRequest = getRequest.WithContext(context.WithValue(getRequest.Context(), mediaGovernanceContextKey{}, governance))
	response = &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("octets")), Request: getRequest}
	governed, err = applyResponseGovernance(getRequest, response, nil)
	if err != nil {
		t.Fatal(err)
	}
	if governed.Header.Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("assumed Content-Type = %q", governed.Header.Get("Content-Type"))
	}
}

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

func loadTransportTestDocument(t *testing.T, source string) *openapi3.T {
	t.Helper()
	document, err := loadDocument("", json.RawMessage(source))
	if err != nil {
		t.Fatal(err)
	}
	return document
}
