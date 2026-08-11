package openapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func dynamicBodySpec(openapiVersion, mediaType, schema string) string {
	return `{"openapi":` + quoteJSON(openapiVersion) + `,"info":{"title":"dynamic object carriage","version":"1"},"servers":[{"url":"https://example.test"}],"paths":{"/items":{"post":{"operationId":"createItem","parameters":[{"name":"id","in":"query","required":true,"schema":{"type":"string"}}],"requestBody":{"required":true,"content":{` + quoteJSON(mediaType) + `:{"schema":` + schema + `}}},"responses":{"204":{"description":"stored"}}}}}}`
}

func dynamicRoutedInput(payload map[string]any) []any {
	return []any{map[string]any{
		"$openbindings": BindingSpec,
		"value": map[string]any{
			"id":      "query-value",
			"payload": payload,
		},
		"parameters": []any{map[string]any{"in": "query", "name": "id", "field": "id"}},
		"body":       map[string]any{"whole": "payload"},
	}}
}

func invokeRevision5Request(t *testing.T, spec string, input any, observe func(*http.Request)) *openbindings.InvocationError {
	t.Helper()
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		observe(req)
		return &http.Response{
			StatusCode: 204,
			Status:     "204 No Content",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    req,
		}, nil
	})
	call := NewInvokerWithClient(&http.Client{Transport: transport}).InvokeBinding(context.Background(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{BindingSpec: BindingSpec, Content: openbindings.TextContent(spec)},
		Ref:    "#/paths/~1items/post",
	})
	_, invocationErr := driveOutputs(context.Background(), call, input)
	return invocationErr
}

func TestRevision5AdditionalPropertiesFormPreservesIndependentNames(t *testing.T) {
	spec := dynamicBodySpec("3.0.4", "application/x-www-form-urlencoded", `{"type":"object","properties":{"fixed":{"type":"string"}},"additionalProperties":{"type":"string"}}`)
	invocationErr := invokeRevision5Request(t, spec, dynamicRoutedInput(map[string]any{
		"id": "body-value", "extra": "a b", "fixed": "yes",
	}), func(req *http.Request) {
		if got := req.URL.Query().Get("id"); got != "query-value" {
			t.Errorf("query id = %q", got)
		}
		mediaType, _, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/x-www-form-urlencoded" {
			t.Errorf("content type = %q, %v", req.Header.Get("Content-Type"), err)
		}
		body, _ := io.ReadAll(req.Body)
		values, err := url.ParseQuery(string(body))
		if err != nil {
			t.Error(err)
		}
		want := url.Values{"extra": {"a b"}, "fixed": {"yes"}, "id": {"body-value"}}
		if !reflect.DeepEqual(values, want) {
			t.Errorf("form = %#v, want %#v", values, want)
		}
	})
	if invocationErr != nil {
		t.Fatal(invocationErr)
	}

	iface, err := NewSynthesizer().SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{BindingSpec: BindingSpec, Content: openbindings.TextContent(spec)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	binding := iface.Bindings["createItem.openapi"]
	if binding.InputTransform == nil || !strings.Contains(binding.InputTransform.Inline, `"whole":"payload"`) {
		t.Fatalf("input transform = %#v", binding.InputTransform)
	}
	input := iface.Operations["createItem"].Input.(map[string]any)
	properties := input["properties"].(map[string]any)
	if _, ok := properties["id"]; !ok {
		t.Fatalf("input properties omit query id: %#v", properties)
	}
	if !reflect.DeepEqual(properties["payload"], map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"fixed": map[string]any{"type": "string"}},
		"additionalProperties": map[string]any{"type": "string"},
	}) {
		t.Fatalf("payload schema = %#v", properties["payload"])
	}
}

func TestRevision5PatternPropertiesSelectsMultipartMemberSchema(t *testing.T) {
	spec := dynamicBodySpec("3.1.2", "multipart/form-data", `{"type":"object","patternProperties":{"^meta_":{"type":"object","additionalProperties":{"type":"string"}}},"additionalProperties":false}`)
	invocationErr := invokeRevision5Request(t, spec, dynamicRoutedInput(map[string]any{
		"meta_first": map[string]any{"role": "admin"},
	}), func(req *http.Request) {
		_, params, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
		if err != nil {
			t.Fatal(err)
		}
		reader := multipart.NewReader(req.Body, params["boundary"])
		part, err := reader.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		if part.FormName() != "meta_first" || part.Header.Get("Content-Type") != "application/json" {
			t.Errorf("part = %q, %q", part.FormName(), part.Header.Get("Content-Type"))
		}
		body, _ := io.ReadAll(part)
		var value map[string]any
		if err := json.NewDecoder(bytes.NewReader(body)).Decode(&value); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(value, map[string]any{"role": "admin"}) {
			t.Errorf("part value = %#v", value)
		}
	})
	if invocationErr != nil {
		t.Fatal(invocationErr)
	}
}

func TestRevision5DoesNotTreatExplicitAdditionalPropertiesFalseAsDynamic(t *testing.T) {
	spec := dynamicBodySpec("3.1.2", "application/json", `{"type":"object","properties":{"name":{"type":"string"}},"required":["name"],"additionalProperties":false}`)
	iface, err := NewSynthesizer().SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{BindingSpec: BindingSpec, Content: openbindings.TextContent(spec)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	properties := iface.Operations["createItem"].Input.(map[string]any)["properties"].(map[string]any)
	if _, ok := properties["payload"]; ok {
		t.Fatalf("closed named object was treated as dynamic: %#v", properties)
	}
	if _, ok := properties["name"]; !ok {
		t.Fatalf("closed named object lost its finite property: %#v", properties)
	}
}
