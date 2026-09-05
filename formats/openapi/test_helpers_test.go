package openapi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/openbindings/openbindings-go/invoke"
)

const BindingSpec = BindingSpecOpenAPI30

func bindingSpecForTestDocument(content any) string {
	var text string
	switch value := content.(type) {
	case string:
		text = value
	case []byte:
		text = string(value)
	case json.RawMessage:
		text = string(value)
	default:
		encoded, _ := json.Marshal(value)
		text = string(encoded)
	}
	if strings.Contains(text, `"openapi":"3.2`) || strings.Contains(text, `"openapi": "3.2`) || strings.Contains(text, "openapi: 3.2") {
		return BindingSpecOpenAPI32
	}
	if strings.Contains(text, `"openapi":"3.1`) || strings.Contains(text, `"openapi": "3.1`) || strings.Contains(text, "openapi: 3.1") {
		return BindingSpecOpenAPI31
	}
	return BindingSpecOpenAPI30
}

// withDeclaredJSONResponses keeps transport-oriented fixtures focused on
// their subject by giving an otherwise minimal Response Object the JSON lane
// emitted by its fake peer.
func withDeclaredJSONResponses(t *testing.T, spec string) string {
	t.Helper()
	var document any
	if err := json.Unmarshal([]byte(spec), &document); err != nil {
		return spec
	}
	var visit func(any)
	visit = func(value any) {
		switch node := value.(type) {
		case map[string]any:
			if responses, ok := node["responses"].(map[string]any); ok {
				for _, raw := range responses {
					response, ok := raw.(map[string]any)
					if !ok || response["$ref"] != nil || response["content"] != nil {
						continue
					}
					response["content"] = map[string]any{"application/json": map[string]any{"schema": map[string]any{}}}
				}
			}
			for _, child := range node {
				visit(child)
			}
		case []any:
			for _, child := range node {
				visit(child)
			}
		}
	}
	visit(document)
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func swagger20RequirementByPoint(details *invoke.ContextRequiredDetails, point string) *invoke.ContextRequirement {
	if details == nil {
		return nil
	}
	for _, alternative := range details.Alternatives {
		for index, requirement := range alternative.Requirements {
			if requirement.Extra["point"] == point {
				return &alternative.Requirements[index]
			}
		}
	}
	return nil
}
