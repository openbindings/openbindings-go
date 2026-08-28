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
	if openAPIBindingSpecRegistry[BindingSpecOpenAPI32].responseComplete {
		t.Fatal("3.2 was warranted before the remaining response and dependency passes completed")
	}
}

func TestOpenAPI32NativeResponseSurfaceRetainsRangeAndOptionalDescription(t *testing.T) {
	operation := &openapi3.Operation{Responses: openapi3.NewResponses()}
	operation.Responses.Set("200", &openapi3.ResponseRef{Value: &openapi3.Response{
		Content: openapi3.Content{
			"application/json":  &openapi3.MediaType{Schema: &openapi3.SchemaRef{Value: &openapi3.Schema{Type: &openapi3.Types{"object"}}}},
			"application/jsonl": &openapi3.MediaType{ItemSchema: &openapi3.SchemaRef{Value: &openapi3.Schema{Type: &openapi3.Types{"object"}}}},
		},
	}})
	operation.Responses.Set("202", &openapi3.ResponseRef{Value: &openapi3.Response{Headers: openapi3.Headers{
		"Content-Encoding": &openapi3.HeaderRef{Value: &openapi3.Header{}},
	}}})
	operation.Responses.Set("2XX", &openapi3.ResponseRef{Value: &openapi3.Response{}})

	if operation.Responses.Len() != 3 {
		t.Fatalf("native responses = %#v", operation.Responses.Map())
	}
	responseRef := operation.Responses.Value("200")
	if responseRef == nil || responseRef.Value == nil || len(responseRef.Value.Content) != 2 || operation.Responses.Value("2XX") == nil {
		t.Fatalf("native response surface = %#v", operation.Responses.Map())
	}
}

func TestOpenAPI32SequentialResponseSynthesisPublishesPerItemContract(t *testing.T) {
	result, err := NewSynthesizer().SynthesizeInterfaceWithCoverage(context.Background(), &synthesize.SynthesizeInput{
		Sources: []synthesize.SynthesizeSource{{
			BindingSpec: BindingSpecOpenAPI32,
			Content: openbindings.TextContent(`{
  "openapi":"3.2.0",
  "info":{"title":"sequential response synthesis","version":"1"},
  "servers":[{"url":"https://api.example"}],
  "paths":{"/events":{"get":{"operationId":"watchEvents","responses":{"200":{"content":{"application/jsonl":{
    "schema":{"type":"array"},
    "itemSchema":{"type":"object","required":["id"],"properties":{"id":{"type":"string"}}}
  }}}}}}}
}`),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	output, _ := result.Interface.Operations["watchEvents"].Output.(map[string]any)
	if output["type"] != "object" {
		t.Fatalf("sequential output = %#v, want itemSchema object", output)
	}
	properties, _ := output["properties"].(map[string]any)
	id, _ := properties["id"].(map[string]any)
	if id["type"] != "string" {
		t.Fatalf("sequential item property = %#v", id)
	}
}
