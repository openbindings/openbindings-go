package openapi

import (
	"context"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/synthesize"
)

func TestOpenAPI32RequestSurfaceSynthesisEmitsContractsAndEditionSelectors(t *testing.T) {
	result, err := NewSynthesizer().SynthesizeInterfaceWithCoverage(context.Background(), &synthesize.SynthesizeInput{
		Sources: []synthesize.SynthesizeSource{{
			BindingSpec: BindingSpecOpenAPI32,
			Content: openbindings.TextContent(`{
  "openapi":"3.2.0",
  "info":{"title":"request synthesis","version":"1"},
  "servers":[{"url":"https://api.example"}],
  "paths":{
    "/items/{id}":{
	  "parameters":[{"name":"id","in":"path","required":true,"schema":{"type":"string"}}],
      "query":{"operationId":"queryItem","responses":{"200":{"description":"ok","content":{"application/json":{"schema":{"type":"object"}}}}}},
      "additionalOperations":{"MiXeD":{"operationId":"mixedItem","requestBody":{"required":true,"content":{"application/jsonl":{"itemSchema":{"type":"object","properties":{"name":{"type":"string"}}}}}},"responses":{"204":{"description":"ok"}}}}
    }
  }
}`),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.Interface == nil {
		t.Fatal("synthesis returned no interface")
	}
	for _, key := range []string{"queryItem", "mixedItem"} {
		if _, ok := result.Interface.Operations[key]; !ok {
			t.Errorf("operation %q was not emitted", key)
		}
	}
	queryBinding := result.Interface.Bindings["queryItem."+DefaultSourceName]
	mixedBinding := result.Interface.Bindings["mixedItem."+DefaultSourceName]
	if queryBinding.Selector != "#/paths/~1items~1{id}/query" {
		t.Errorf("QUERY selector = %q", queryBinding.Selector)
	}
	if mixedBinding.Selector != "#/paths/~1items~1{id}/additionalOperations/MiXeD" {
		t.Errorf("additional selector = %q", mixedBinding.Selector)
	}
	if queryBinding.InputTransform == nil || mixedBinding.InputTransform == nil {
		t.Fatal("request-bearing 3.2 bindings did not emit inputTransform")
	}
	if result.Interface.Operations["queryItem"].Output == nil {
		t.Fatal("plain unary 3.1-equivalent response contract was not emitted")
	}
	if openAPI32M6ResponseSeams[0].name == "" || openAPIBindingSpecRegistry[BindingSpecOpenAPI32].responseComplete {
		t.Fatal("3.2 response seams or unwarranted capability gate were lost")
	}
}

func TestOpenAPI32UnaryResponseBridgeKeepsOnlyExplicitEquivalentCells(t *testing.T) {
	description := "ok"
	operation := &openapi3.Operation{Responses: openapi3.NewResponses()}
	operation.Responses.Set("200", &openapi3.ResponseRef{Value: &openapi3.Response{
		Description: &description,
		Content: openapi3.Content{
			"application/json":  &openapi3.MediaType{Schema: &openapi3.SchemaRef{Value: &openapi3.Schema{Type: &openapi3.Types{"object"}}}},
			"application/jsonl": &openapi3.MediaType{ItemSchema: &openapi3.SchemaRef{Value: &openapi3.Schema{Type: &openapi3.Types{"object"}}}},
		},
	}})
	operation.Responses.Set("201", &openapi3.ResponseRef{Ref: "#/components/responses/Created", Value: &openapi3.Response{
		Description: &description,
		Content:     openapi3.Content{"application/jsonl": &openapi3.MediaType{ItemSchema: &openapi3.SchemaRef{Value: &openapi3.Schema{Type: &openapi3.Types{"object"}}}}},
	}})
	operation.Responses.Set("202", &openapi3.ResponseRef{Value: &openapi3.Response{Description: &description, Headers: openapi3.Headers{
		"Content-Encoding": &openapi3.HeaderRef{Value: &openapi3.Header{}},
	}}})
	operation.Responses.Set("2XX", &openapi3.ResponseRef{Value: &openapi3.Response{Description: &description}})

	bridged := openAPI32UnaryResponseBridgeOperation(operation)
	if bridged.Responses.Len() != 1 {
		t.Fatalf("bridged responses = %#v", bridged.Responses.Map())
	}
	responseRef := bridged.Responses.Value("200")
	if responseRef == nil || responseRef.Value == nil || len(responseRef.Value.Content) != 1 || responseRef.Value.Content["application/json"] == nil {
		t.Fatalf("bridged 200 response = %#v", responseRef)
	}
}
