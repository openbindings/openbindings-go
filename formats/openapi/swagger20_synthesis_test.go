package openapi

import (
	"context"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/invoke"
	"github.com/openbindings/openbindings-go/synthesize"
)

func TestSwagger20SynthesisEmitsFlatContractEnvelopeTransformAndCoverage(t *testing.T) {
	artifact := `{
  "swagger":"2.0","info":{"title":"Create","version":"1"},
  "host":"api.example","schemes":["https","wss"],
  "consumes":["application/json","application/vnd.example+json"],"produces":["application/json"],
  "securityDefinitions":{
    "bad":{"type":"apiKey","in":"query","name":"id"},
    "good":{"type":"apiKey","in":"header","name":"X-Key"}
  },
  "paths":{"/pets/{id}":{"post":{
    "operationId":"create pet","summary":"Create one","security":[{"bad":[]},{"good":[]}],
    "parameters":[
      {"name":"id","in":"path","required":true,"type":"string"},
      {"name":"id","in":"query","type":"string"},
      {"name":"payload","in":"body","required":true,"schema":{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}}
    ],
    "responses":{"201":{"description":"created","headers":{"Location":{"type":"string"}},"schema":{"type":"object","properties":{"id":{"type":"string"}}}}}
  }}}
}`
	result, err := NewSynthesizer().SynthesizeInterfaceWithCoverage(context.Background(), &synthesize.SynthesizeInput{
		Sources: []synthesize.SynthesizeSource{{BindingSpec: BindingSpecOpenAPI20, Content: openbindings.TextContent(artifact)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	op, ok := result.Interface.Operations["create_pet"]
	if !ok || op.Input == nil || op.Output == nil || op.Description != "Create one" {
		t.Fatalf("operation = %#v", op)
	}
	binding := result.Interface.Bindings["create_pet.openapi"]
	if binding.InputTransform == nil {
		t.Fatal("missing inputTransform")
	}
	value, err := (openAPIJSONataEvaluator{}).Evaluate(binding.InputTransform.Inline, map[string]any{
		"path/id": "7", "query/id": "lookup", "body": map[string]any{"name": "Ada"},
	})
	if err != nil {
		t.Fatal(err)
	}
	envelope, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("transform result = %#v", value)
	}
	parameters, _ := envelope["parameters"].(map[string]any)
	if parameters["path/id"] != "7" || parameters["query/id"] != "lookup" {
		t.Fatalf("parameters = %#v", parameters)
	}
	if body, _ := envelope["body"].(map[string]any); body["name"] != "Ada" {
		t.Fatalf("body = %#v", envelope["body"])
	}
	if result.Coverage.Exhaustive != true || result.Coverage.FullyRepresented {
		t.Fatalf("coverage = %#v", result.Coverage)
	}
	var target, requestAlternatives, serverExclusions, securityExclusions int
	for _, entry := range result.Coverage.Entries {
		switch {
		case entry.Scope == synthesize.SynthesisCoverageTarget:
			target++
			if !stringSliceContains(entry.Requirements, "configuration.requestMedia") || !stringSliceContains(entry.Requirements, "configuration.security") {
				t.Errorf("target requirements = %v", entry.Requirements)
			}
		case entry.SourceRef == "#/consumes/0" || entry.SourceRef == "#/consumes/1":
			requestAlternatives++
		case entry.SourceRef == "#/schemes/1":
			serverExclusions++
		case entry.SourceRef == "#/paths/~1pets~1{id}/post/security/0" || entry.SourceRef == "#/paths/~1pets~1{id}/post/security/1":
			securityExclusions++
		}
	}
	if target != 1 || requestAlternatives != 2 || serverExclusions != 1 || securityExclusions != 1 {
		t.Fatalf("coverage unit counts = target %d request %d server exclusions %d security exclusions %d: %#v", target, requestAlternatives, serverExclusions, securityExclusions, result.Coverage.Entries)
	}
}

func TestSwagger20PrepareBindingReportsSelectedCredentialRequirement(t *testing.T) {
	artifact := `{"swagger":"2.0","info":{"title":"Auth","version":"1"},"host":"api.example","schemes":["https"],"securityDefinitions":{"key":{"type":"apiKey","in":"header","name":"X-Key"}},"security":[{"key":[]}],"paths":{"/x":{"get":{"responses":{"204":{"description":"ok"}}}}}}`
	args := &invoke.BindingInvocationArgs{
		Source:   invoke.InvocationSource{BindingSpec: BindingSpecOpenAPI20, Content: openbindings.TextContent(artifact)},
		Selector: "#/paths/~1x/get",
	}
	details, err := NewInvoker().PrepareBinding(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if details == nil || len(details.Alternatives) != 1 || len(details.Alternatives[0].Requirements) != 1 {
		t.Fatalf("details = %#v", details)
	}
	requirement := details.Alternatives[0].Requirements[0]
	if requirement.Type != "auth.apiKey" || requirement.Name != "key" {
		t.Fatalf("requirement = %#v", requirement)
	}
	args.Context = map[string]any{"credentials": map[string]any{"key": "secret"}}
	if details, err := NewInvoker().PrepareBinding(context.Background(), args); err != nil || details != nil {
		t.Fatalf("satisfied prepare = (%#v, %v)", details, err)
	}
}

func stringSliceContains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
