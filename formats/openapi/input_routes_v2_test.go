package openapi

import (
	"reflect"
	"sort"
	"strings"
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

// The routed envelope handed to the standalone engine is built with the
// engine's own discriminator — key "$openapi", marker "openapi-client.routed@1"
// — and the TypeScript SDK builds the identical object
// (input-routes-v2.test.ts, "builds the routed envelope with the standalone
// client's own discriminator, as the Go twin does"). Filed 2026-08-20 in
// binding-fidelity.md as a parity defect with "no test catches it"; the
// media_v3/v5/v6 tests pin only the absence of the OBI SDK's former key, this
// pins the positive object. The envelope never enters an OBI: the emitted
// transform is the flat caller envelope and carries neither key nor marker.
func TestEngineInputBuildsTheStandaloneEnvelope(t *testing.T) {
	params := openapi3.Parameters{&openapi3.ParameterRef{Value: &openapi3.Parameter{Name: "q", In: "query"}}}
	plans := []*bodyPlan{{declared: true, required: true, props: map[string]bool{"name": true}, family: familyJSON}}
	routes := planAbstractInputRoutes(params, plans)
	profile := openapiclient.FullProfile()
	if profile.InputRouteKey != "$openapi" || profile.InputRouteMarker != "openapi-client.routed@1" {
		t.Fatalf("profile envelope = %q/%q, want $openapi/openapi-client.routed@1", profile.InputRouteKey, profile.InputRouteMarker)
	}
	got, err := engineInputForCallerEnvelope(
		map[string]any{"parameters": map[string]any{"q": "term"}, "body": map[string]any{"name": "Ada"}},
		params, plans, routes, profile,
	)
	if err != nil {
		t.Fatal(err)
	}
	items, ok := got.([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("routed envelope = %#v, want a one-item array", got)
	}
	item, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("routed envelope item = %#v, want an object", items[0])
	}
	if item["$openapi"] != "openapi-client.routed@1" {
		t.Fatalf("item[$openapi] = %#v, want openapi-client.routed@1", item["$openapi"])
	}
	if _, leaked := item["$openbindings"]; leaked {
		t.Fatal("routed envelope carries the OBI SDK's former $openbindings key")
	}
	keys := make([]string, 0, len(item))
	for k := range item {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if want := []string{"$openapi", "body", "parameters", "value"}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("routed envelope keys = %v, want %v", keys, want)
	}
	transform := routes.transformExpression(params)
	for _, forbidden := range []string{"$openbindings", "$openapi", "openapi-client.routed@1"} {
		if strings.Contains(transform, forbidden) {
			t.Fatalf("emitted transform carries engine-private %q: %s", forbidden, transform)
		}
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
