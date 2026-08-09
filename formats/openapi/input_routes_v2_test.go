package openapi

import (
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

func TestParseRoutedEnvelopeRejectsSharedDestinationField(t *testing.T) {
	_, err := parseRoutedEnvelope([]any{map[string]any{
		"$openbindings": BindingSpecV2,
		"value":         map[string]any{"shared": "x"},
		"parameters": []any{
			map[string]any{"in": "path", "name": "id", "field": "shared"},
			map[string]any{"in": "query", "name": "id", "field": "shared"},
		},
		"body": map[string]any{},
	}})
	if err == nil || !strings.Contains(err.Error(), "more than one destination") {
		t.Fatalf("expected shared-field refusal, got %v", err)
	}
}

func TestParseRoutedEnvelopeRequiresExactDescriptorShape(t *testing.T) {
	_, err := parseRoutedEnvelope([]any{map[string]any{
		"$openbindings": BindingSpecV2,
		"value":         map[string]any{},
		"parameters":    []any{},
		"body":          map[string]any{},
		"extra":         true,
	}})
	if err == nil || !strings.Contains(err.Error(), "exactly") {
		t.Fatalf("expected exact-shape refusal, got %v", err)
	}
}

func TestParseRoutedEnvelopeLeavesMarkerShapedObjectFlat(t *testing.T) {
	envelope, err := parseRoutedEnvelope(map[string]any{
		"$openbindings": BindingSpecV2,
		"value":         map[string]any{"application": true},
	})
	if err != nil || envelope != nil {
		t.Fatalf("marker-shaped application object parsed as private envelope: %#v, %v", envelope, err)
	}
}

func TestValidateEnvelopeRoutesRejectsUnknownIdentity(t *testing.T) {
	envelope := &routedEnvelope{
		value:      map[string]any{},
		parameters: []abstractParameterRoute{{In: "query", Name: "id", Field: "queryID"}},
		bodyFields: map[string]string{},
	}
	params := openapi3.Parameters{
		&openapi3.ParameterRef{Value: &openapi3.Parameter{In: "path", Name: "id"}},
	}
	if err := validateEnvelopeRoutes(params, nil, envelope); err == nil || !strings.Contains(err.Error(), "does not identify") {
		t.Fatalf("expected unknown-identity refusal, got %v", err)
	}
}

func TestRouteEnvelopeDoesNotLeakAlternateCandidateMapping(t *testing.T) {
	envelope := &routedEnvelope{
		value:      map[string]any{"renamed": "x"},
		bodyFields: map[string]string{"id": "renamed"},
	}
	plan := &bodyPlan{declared: true, family: familyJSON, props: map[string]bool{"name": true}}
	_, err := routeEnvelope(nil, envelope, "/items", plan)
	if err == nil || !strings.Contains(err.Error(), "no destination") {
		t.Fatalf("expected selected-candidate refusal, got %v", err)
	}
}
