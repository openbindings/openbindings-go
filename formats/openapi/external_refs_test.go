package openapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func TestLoadDocumentUsesInjectedResolverForCompleteReferenceClosure(t *testing.T) {
	requests := map[string]int{}
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		requests[req.URL.String()]++
		body := "missing"
		status := http.StatusNotFound
		if req.URL.String() == "https://description.example/path-item.yaml" {
			status = http.StatusOK
			body = `post:
  parameters: [{$ref: "#/Trace"}]
  requestBody: {$ref: "#/Create"}
  responses:
    "200": {$ref: "#/Created"}
Trace: {name: trace, in: query, schema: {type: string}}
Create:
  required: true
  content: {application/json: {schema: {type: object}}}
Created: {description: ok}
`
		}
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})}

	root := openbindings.TextContent(`openapi: 3.1.2
info: {title: External, version: "1"}
paths:
  /items: {$ref: "./path-item.yaml"}
`)
	doc, err := loadDocumentWithResolver(
		context.Background(), client,
		"https://description.example/openapi.yaml", root,
	)
	if err != nil {
		t.Fatalf("loadDocumentWithResolver: %v", err)
	}
	post := doc.Paths.Find("/items").Post
	if post == nil || len(post.Parameters) != 1 || post.Parameters[0].Value == nil {
		t.Fatalf("external operation parameter was not resolved: %#v", post)
	}
	if got := post.Parameters[0].Value.Name; got != "trace" {
		t.Fatalf("parameter name = %q, want trace", got)
	}
	if post.RequestBody == nil || post.RequestBody.Value == nil || !post.RequestBody.Value.Required {
		t.Fatal("external operation request body was not resolved")
	}
	if response := post.Responses.Value("200"); response == nil || response.Value == nil || *response.Value.Description != "ok" {
		t.Fatal("external operation response was not resolved")
	}
	if requests["https://description.example/path-item.yaml"] != 1 {
		t.Fatalf("external document fetches = %d, want one", requests["https://description.example/path-item.yaml"])
	}
}

func TestSynthesizerUsesInjectedResolver(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://description.example/path-item.yaml" {
			return nil, errors.New("unexpected artifact request: " + req.URL.String())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{},
			Body: io.NopCloser(strings.NewReader(`get:
  operationId: externalGet
  responses: {"200": {description: ok}}
`)),
			Request: req,
		}, nil
	})}
	iface, err := NewSynthesizerWithClient(client).SynthesizeInterface(
		context.Background(),
		&openbindings.SynthesizeInput{Sources: []openbindings.SynthesizeSource{{
			BindingSpec: BindingSpec,
			Location:    "https://description.example/openapi.yaml",
			Content: openbindings.TextContent(`openapi: 3.1.2
info: {title: External, version: "1"}
paths: {/items: {$ref: "./path-item.yaml"}}
`),
		}}},
	)
	if err != nil {
		t.Fatalf("SynthesizeInterface: %v", err)
	}
	if _, ok := iface.Operations["externalGet"]; !ok {
		t.Fatalf("synthesized operations = %#v, want externalGet", iface.Operations)
	}
}

func TestSynthesizerInternalizesExternalSchemaClosure(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://description.example/schemas.yaml" {
			return nil, errors.New("unexpected artifact request: " + req.URL.String())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{},
			Body: io.NopCloser(strings.NewReader(`Thing:
  type: object
  required: [name]
  properties:
    name: {$ref: "#/Name"}
Name: {type: string}
`)),
			Request: req,
		}, nil
	})}
	iface, err := NewSynthesizerWithClient(client).SynthesizeInterface(
		context.Background(),
		&openbindings.SynthesizeInput{Sources: []openbindings.SynthesizeSource{{
			BindingSpec: BindingSpec,
			Location:    "https://description.example/openapi.yaml",
			Content: openbindings.TextContent(`openapi: 3.1.2
info: {title: External schemas, version: "1"}
paths:
  /items:
    post:
      operationId: createItem
      requestBody:
        required: true
        content:
          application/json:
            schema: {$ref: "./schemas.yaml#/Thing"}
      responses: {"204": {description: ok}}
`),
		}}},
	)
	if err != nil {
		t.Fatalf("SynthesizeInterface: %v", err)
	}
	encoded, err := json.Marshal(iface.Operations["createItem"].Input)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"$ref"`) {
		t.Fatalf("external reference escaped into OBI operation schema: %s", encoded)
	}
	if !strings.Contains(string(encoded), `"name":{"type":"string"}`) {
		t.Fatalf("external schema closure was not materialized: %s", encoded)
	}
}

func TestLoadDocumentPropagatesCancellationToArtifactResolver(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := loadDocumentWithResolver(
		ctx, client,
		"https://description.example/openapi.yaml",
		nil,
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("load error = %v, want context.Canceled", err)
	}
}

func TestLoadDocumentUsesFinalRetrievalURIForNestedRelativeRefs(t *testing.T) {
	var requested []string
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		requested = append(requested, req.URL.String())
		responseRequest := req
		body := "missing"
		status := http.StatusNotFound
		switch req.URL.String() {
		case "https://description.example/alias.yaml":
			status = http.StatusOK
			body = `get:
  responses:
    "200":
      description: ok
      content:
        application/json:
          schema: {$ref: "./actual.yaml"}
`
			responseRequest = req.Clone(req.Context())
			responseRequest.URL, _ = url.Parse("https://cdn.example/nested/alias.yaml")
		case "https://cdn.example/nested/actual.yaml":
			status = http.StatusOK
			body = `type: string`
		}
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    responseRequest,
		}, nil
	})}
	doc, err := loadDocumentWithResolver(
		context.Background(), client,
		"https://description.example/openapi.yaml",
		openbindings.TextContent(`openapi: 3.1.2
info: {title: Redirected external, version: "1"}
paths: {/items: {$ref: "./alias.yaml"}}
`),
	)
	if err != nil {
		t.Fatalf("loadDocumentWithResolver: %v (requests: %v)", err, requested)
	}
	item := doc.Paths.Find("/items")
	if item == nil || item.Get == nil {
		t.Fatalf("redirected nested Path Item was not resolved (requests: %v)", requested)
	}
	schema := item.Get.Responses.Value("200").Value.Content["application/json"].Schema
	if schema == nil || schema.Value == nil || schema.Value.Type == nil || !schema.Value.Type.Is("string") {
		t.Fatalf("nested schema was not resolved from the final retrieval URI (requests: %v)", requested)
	}
}
