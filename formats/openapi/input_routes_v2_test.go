package openapi

import (
	"reflect"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	openapiclient "github.com/openbindings/openapi-client/go"
)

func TestParseCallerEnvelopeRejectsUnknownTopLevelKey(t *testing.T) {
	if _, err := parseCallerEnvelope(map[string]any{"extra": true}); err == nil {
		t.Fatal("expected the closed caller envelope to reject an unknown key")
	}
}

func TestParseCallerEnvelopeRejectsNonObjectParameters(t *testing.T) {
	if _, err := parseCallerEnvelope(map[string]any{"parameters": []any{}}); err == nil {
		t.Fatal("expected a non-object parameters member to be rejected")
	}
}

func TestEngineInputRejectsUnknownParameterKey(t *testing.T) {
	params := openapi3.Parameters{&openapi3.ParameterRef{Value: &openapi3.Parameter{Name: "q", In: "query"}}}
	routes := planAbstractInputRoutes(params, nil)
	_, err := engineInputForCallerEnvelope(
		map[string]any{"parameters": map[string]any{"other": "x"}},
		params, nil, routes, openapiclient.FullProfile(),
	)
	if err == nil {
		t.Fatal("expected an unknown parameter key to be rejected")
	}
}

func TestQualifiedParameterModeUsesEveryLocation(t *testing.T) {
	params := openapi3.Parameters{
		&openapi3.ParameterRef{Value: &openapi3.Parameter{Name: "id", In: "path"}},
		&openapi3.ParameterRef{Value: &openapi3.Parameter{Name: "id", In: "query"}},
	}
	routes := planAbstractInputRoutes(params, nil)
	_, err := engineInputForCallerEnvelope(
		map[string]any{"parameters": map[string]any{"path/id": "p", "query/id": "q"}},
		params, nil, routes, openapiclient.FullProfile(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engineInputForCallerEnvelope(
		map[string]any{"parameters": map[string]any{"id": "bare"}},
		params, nil, routes, openapiclient.FullProfile(),
	); err == nil {
		t.Fatal("expected qualified mode to reject the bare parameter name")
	}
}

func TestInputTransformConstructsPublicEnvelope(t *testing.T) {
	params := openapi3.Parameters{&openapi3.ParameterRef{Value: &openapi3.Parameter{Name: "q", In: "query"}}}
	plans := []*bodyPlan{{declared: true, required: true, props: map[string]bool{"name": true}, family: familyJSON}}
	routes := planAbstractInputRoutes(params, plans)
	got, err := (openAPIJSONataEvaluator{}).Evaluate(routes.transformExpression(params), map[string]any{
		"q": "term", "name": "Ada",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"parameters": map[string]any{"q": "term"},
		"body":       map[string]any{"name": "Ada"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("transform = %#v, want %#v", got, want)
	}
}

func TestInputTransformEscapesQualifiedParameterNames(t *testing.T) {
	params := openapi3.Parameters{
		&openapi3.ParameterRef{Value: &openapi3.Parameter{Name: "a/b~c", In: "path"}},
		&openapi3.ParameterRef{Value: &openapi3.Parameter{Name: "a/b~c", In: "query"}},
	}
	routes := planAbstractInputRoutes(params, nil)
	got, err := (openAPIJSONataEvaluator{}).Evaluate(routes.transformExpression(params), map[string]any{
		"a/b~c": "p", "a/b~c_2": "q",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"parameters": map[string]any{
		"path/a~1b~0c": "p", "query/a~1b~0c": "q",
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("transform = %#v, want %#v", got, want)
	}
}
