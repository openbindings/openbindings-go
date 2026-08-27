package openapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/openbindings/openbindings-go/synthesize"
)

func TestProjectOpenAPISchemaProjectsContentSchema(t *testing.T) {
	schema := map[string]any{
		"type":             "string",
		"contentMediaType": "application/json",
		"contentSchema": map[string]any{
			"type":     "object",
			"required": []any{"server", "client"},
			"properties": map[string]any{
				"server": map[string]any{"type": "string", "readOnly": true},
				"client": map[string]any{"type": "string", "writeOnly": true},
			},
		},
	}

	request := projectOpenAPISchema(schema, openAPIRequestSchema, nil)
	response := projectOpenAPISchema(schema, openAPIResponseSchema, nil)
	requestContent := request["contentSchema"].(map[string]any)
	responseContent := response["contentSchema"].(map[string]any)

	for direction, content := range map[string]map[string]any{"request": requestContent, "response": responseContent} {
		assertRequired(t, content, []string{"server", "client"}, nil)
		properties := schemaProperties(t, content)
		if len(properties) != 2 {
			t.Errorf("%s contentSchema properties = %v, want both annotated members", direction, properties)
		}
	}
}

func TestSynthesisProjectsDirectionalSchemas(t *testing.T) {
	for _, version := range []string{"3.0.3", "3.1.0"} {
		t.Run(version, func(t *testing.T) {
			doc := fmt.Sprintf(`{
			  "openapi": %q,
			  "info": {"title": "Directional API", "version": "1"},
			  "servers": [{"url": "https://api.example.test"}],
			  "components": {"schemas": {
			    "Filter": {
			      "type": "object",
			      "required": ["serverGenerated", "clientProvided", "ordinary"],
			      "properties": {
			        "serverGenerated": {"type": "string", "readOnly": true},
			        "clientProvided": {"type": "string", "writeOnly": true},
			        "ordinary": {"type": "string"}
			      }
			    },
			    "Profile": {
			      "type": "object",
			      "required": ["serverNote", "clientSecret", "displayName", "nested", "directory", "neverDirectional"],
			      "properties": {
			        "serverNote": {"type": "string", "readOnly": true},
			        "clientSecret": {"type": "string", "writeOnly": true},
			        "displayName": {"type": "string"},
			        "neverDirectional": {"type": "string", "readOnly": true, "writeOnly": true},
			        "nested": {
			          "type": "array",
			          "items": {
			            "type": "object",
			            "required": ["createdAt", "draft", "label"],
			            "properties": {
			              "createdAt": {"type": "string", "readOnly": true},
			              "draft": {"type": "string", "writeOnly": true},
			              "label": {"type": "string"}
			            }
			          }
			        },
			        "directory": {
			          "type": "object",
			          "additionalProperties": {
			            "type": "object",
			            "required": ["createdAt", "draft", "label"],
			            "properties": {
			              "createdAt": {"type": "string", "readOnly": true},
			              "draft": {"type": "string", "writeOnly": true},
			              "label": {"type": "string"}
			            }
			          }
			        }
			      }
			    },
			    "User": {
			      "type": "object",
			      "required": ["id", "password", "serverNote", "clientSecret", "displayName", "nested", "directory", "manager", "neverDirectional"],
			      "allOf": [
			        {"$ref": "#/components/schemas/Profile"},
			        {
			          "type": "object",
			          "required": ["id", "password", "manager"],
			          "properties": {
			            "id": {"type": "string", "readOnly": true},
			            "password": {"type": "string", "writeOnly": true},
			            "manager": {"$ref": "#/components/schemas/User"}
			          }
			        }
			      ]
			    }
			  }},
			  "paths": {"/users": {"post": {
			    "operationId": "upsertUser",
			    "parameters": [{
			      "name": "filter", "in": "query", "required": true,
			      "schema": {"$ref": "#/components/schemas/Filter"}
			    }],
			    "requestBody": {"required": true, "content": {
			      "application/json": {"schema": {"$ref": "#/components/schemas/User"}}
			    }},
			    "responses": {"200": {"description": "ok", "content": {
			      "application/json": {"schema": {"$ref": "#/components/schemas/User"}}
			    }}}
			  }}}
			}`, version)

			result, err := NewSynthesizer().SynthesizeInterfaceWithCoverage(context.Background(), &synthesize.SynthesizeInput{
				Sources: []synthesize.SynthesizeSource{{BindingSpec: bindingSpecForTestDocument(doc), Content: json.RawMessage(doc)}},
			})
			if err != nil {
				t.Fatalf("synthesize: %v", err)
			}
			op := result.Interface.Operations["upsertUser"]
			input := schemaValueMap(t, op.Input)
			output := schemaValueMap(t, op.Output)

			inputProps := schemaProperties(t, input)
			for _, name := range []string{"filter", "id", "password", "serverNote", "clientSecret", "displayName", "nested", "directory", "manager", "neverDirectional"} {
				if _, ok := inputProps[name]; !ok {
					t.Errorf("input omitted request property %q: %#v", name, inputProps)
				}
			}
			assertRequired(t, input, []string{"filter", "id", "password", "serverNote", "clientSecret", "displayName", "nested", "directory", "manager", "neverDirectional"}, nil)

			filter := inputProps["filter"].(map[string]any)
			filterProps := schemaProperties(t, filter)
			for _, name := range []string{"serverGenerated", "clientProvided", "ordinary"} {
				if _, ok := filterProps[name]; !ok {
					t.Errorf("request parameter schema omitted annotated property %q", name)
				}
			}
			assertRequired(t, filter, []string{"serverGenerated", "clientProvided", "ordinary"}, nil)

			inputNested := inputProps["nested"].(map[string]any)["items"].(map[string]any)
			assertDirectionalLeaf(t, inputNested, "createdAt", "draft", true)
			inputMapValue := inputProps["directory"].(map[string]any)["additionalProperties"].(map[string]any)
			assertDirectionalLeaf(t, inputMapValue, "createdAt", "draft", true)

			outputProfile := findSchemaMap(t, output, func(schema map[string]any) bool {
				_, ok := schemaPropertiesIfPresent(schema)["displayName"]
				return ok
			})
			outputProfileProps := schemaProperties(t, outputProfile)
			for _, name := range []string{"serverNote", "clientSecret", "displayName", "nested", "directory", "neverDirectional"} {
				if _, ok := outputProfileProps[name]; !ok {
					t.Errorf("response omitted annotated property %q", name)
				}
			}
			assertRequired(t, outputProfile, []string{"serverNote", "clientSecret", "displayName", "nested", "directory", "neverDirectional"}, nil)

			outputIdentity := findSchemaMap(t, output, func(schema map[string]any) bool {
				_, ok := schemaPropertiesIfPresent(schema)["id"]
				return ok
			})
			outputIdentityProps := schemaProperties(t, outputIdentity)
			if _, ok := outputIdentityProps["password"]; !ok {
				t.Error("response omitted writeOnly-annotated password")
			}
			assertRequired(t, outputIdentity, []string{"id", "password", "manager"}, nil)

			outputNested := findSchemaMap(t, output, func(schema map[string]any) bool {
				_, ok := schemaPropertiesIfPresent(schema)["label"]
				return ok
			})
			assertDirectionalLeaf(t, outputNested, "createdAt", "draft", false)
			outputMap := findSchemaMap(t, output, func(schema map[string]any) bool {
				additional, ok := schema["additionalProperties"].(map[string]any)
				if !ok {
					return false
				}
				_, hasLabel := schemaPropertiesIfPresent(additional)["label"]
				return hasLabel
			})["additionalProperties"].(map[string]any)
			assertDirectionalLeaf(t, outputMap, "createdAt", "draft", false)

			outputRoot := findSchemaMap(t, output, func(schema map[string]any) bool {
				for _, name := range stringSlice(schema["required"]) {
					if name == "manager" {
						return true
					}
				}
				return false
			})
			assertRequired(t, outputRoot, []string{"id", "password", "serverNote", "clientSecret", "displayName", "nested", "directory", "manager", "neverDirectional"}, nil)

			inputJSON, _ := json.Marshal(input)
			outputJSON, _ := json.Marshal(output)
			if !strings.Contains(string(inputJSON), "#/operations/upsertUser/input/$defs/User") {
				t.Fatalf("input lost stable recursive $defs identity: %s", inputJSON)
			}
			if !strings.Contains(string(outputJSON), "#/operations/upsertUser/output/$defs/User") {
				t.Fatalf("output lost stable recursive $defs identity: %s", outputJSON)
			}
			if !strings.Contains(string(inputJSON), `"readOnly":true`) || !strings.Contains(string(inputJSON), `"writeOnly":true`) {
				t.Errorf("request schema lost readOnly/writeOnly annotations: %s", inputJSON)
			}
			if !strings.Contains(string(outputJSON), `"readOnly":true`) || !strings.Contains(string(outputJSON), `"writeOnly":true`) {
				t.Errorf("response schema lost readOnly/writeOnly annotations: %s", outputJSON)
			}
		})
	}
}

func schemaValueMap(t *testing.T, schema any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	return result
}

func schemaProperties(t *testing.T, schema map[string]any) map[string]any {
	t.Helper()
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema has no properties map: %#v", schema)
	}
	return properties
}

func schemaPropertiesIfPresent(schema map[string]any) map[string]any {
	properties, _ := schema["properties"].(map[string]any)
	return properties
}

func assertRequired(t *testing.T, schema map[string]any, present, absent []string) {
	t.Helper()
	required := map[string]bool{}
	for _, name := range stringSlice(schema["required"]) {
		required[name] = true
	}
	for _, name := range present {
		if !required[name] {
			t.Errorf("required omitted %q: %v", name, required)
		}
	}
	for _, name := range absent {
		if required[name] {
			t.Errorf("required retained excluded property %q: %v", name, required)
		}
	}
}

func assertDirectionalLeaf(t *testing.T, schema map[string]any, readOnlyName, writeOnlyName string, request bool) {
	t.Helper()
	properties := schemaProperties(t, schema)
	if _, ok := properties[readOnlyName]; !ok {
		t.Errorf("schema omitted readOnly-annotated property %q", readOnlyName)
	}
	if _, ok := properties[writeOnlyName]; !ok {
		t.Errorf("schema omitted writeOnly-annotated property %q", writeOnlyName)
	}
	assertRequired(t, schema, []string{readOnlyName, writeOnlyName, "label"}, nil)
}

func findSchemaMap(t *testing.T, root any, predicate func(map[string]any) bool) map[string]any {
	t.Helper()
	pending := []any{root}
	for len(pending) > 0 {
		value := pending[0]
		pending = pending[1:]
		switch node := value.(type) {
		case map[string]any:
			if predicate(node) {
				return node
			}
			for _, child := range node {
				pending = append(pending, child)
			}
		case []any:
			pending = append(pending, node...)
		}
	}
	t.Fatal("expected schema fragment was not found")
	return nil
}
