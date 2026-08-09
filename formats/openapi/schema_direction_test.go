package openapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
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

	assertRequired(t, requestContent, []string{"client"}, []string{"server"})
	assertRequired(t, responseContent, []string{"server"}, []string{"client"})
	if _, ok := schemaProperties(t, requestContent)["server"]; ok {
		t.Error("request contentSchema retained readOnly property")
	}
	if _, ok := schemaProperties(t, responseContent)["client"]; ok {
		t.Error("response contentSchema retained writeOnly property")
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

			result, err := NewSynthesizer().SynthesizeInterfaceWithCoverage(context.Background(), &openbindings.SynthesizeInput{
				Sources: []openbindings.SynthesizeSource{{BindingSpec: BindingSpec, Content: json.RawMessage(doc)}},
			})
			if err != nil {
				t.Fatalf("synthesize: %v", err)
			}
			op := result.Interface.Operations["upsertUser"]
			input := schemaValueMap(t, op.Input)
			output := schemaValueMap(t, op.Output)

			inputProps := schemaProperties(t, input)
			for _, name := range []string{"filter", "password", "clientSecret", "displayName", "nested", "directory", "manager"} {
				if _, ok := inputProps[name]; !ok {
					t.Errorf("input omitted request property %q: %#v", name, inputProps)
				}
			}
			for _, name := range []string{"id", "serverNote", "neverDirectional"} {
				if _, ok := inputProps[name]; ok {
					t.Errorf("input retained readOnly property %q", name)
				}
			}
			assertRequired(t, input, []string{"filter", "password", "clientSecret", "displayName", "nested", "directory", "manager"}, []string{"id", "serverNote", "neverDirectional"})

			filter := inputProps["filter"].(map[string]any)
			filterProps := schemaProperties(t, filter)
			if _, ok := filterProps["serverGenerated"]; ok {
				t.Error("request parameter schema retained nested readOnly property")
			}
			if _, ok := filterProps["clientProvided"]; !ok {
				t.Error("request parameter schema omitted writeOnly property")
			}
			assertRequired(t, filter, []string{"clientProvided", "ordinary"}, []string{"serverGenerated"})

			inputNested := inputProps["nested"].(map[string]any)["items"].(map[string]any)
			assertDirectionalLeaf(t, inputNested, "createdAt", "draft", true)
			inputMapValue := inputProps["directory"].(map[string]any)["additionalProperties"].(map[string]any)
			assertDirectionalLeaf(t, inputMapValue, "createdAt", "draft", true)

			outputProfile := findSchemaMap(t, output, func(schema map[string]any) bool {
				_, ok := schemaPropertiesIfPresent(schema)["displayName"]
				return ok
			})
			outputProfileProps := schemaProperties(t, outputProfile)
			if _, ok := outputProfileProps["serverNote"]; !ok {
				t.Error("response omitted readOnly property serverNote")
			}
			for _, name := range []string{"clientSecret", "neverDirectional"} {
				if _, ok := outputProfileProps[name]; ok {
					t.Errorf("response retained writeOnly property %q", name)
				}
			}
			assertRequired(t, outputProfile, []string{"serverNote", "displayName", "nested", "directory"}, []string{"clientSecret", "neverDirectional"})

			outputIdentity := findSchemaMap(t, output, func(schema map[string]any) bool {
				_, ok := schemaPropertiesIfPresent(schema)["id"]
				return ok
			})
			outputIdentityProps := schemaProperties(t, outputIdentity)
			if _, ok := outputIdentityProps["password"]; ok {
				t.Error("response retained writeOnly property password")
			}
			assertRequired(t, outputIdentity, []string{"id", "manager"}, []string{"password"})

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
			assertRequired(t, outputRoot, []string{"id", "serverNote", "displayName", "nested", "directory", "manager"}, []string{"password", "clientSecret", "neverDirectional"})

			inputJSON, _ := json.Marshal(input)
			outputJSON, _ := json.Marshal(output)
			if !strings.Contains(string(inputJSON), "#/operations/upsertUser/input/$defs/User") {
				t.Fatalf("input lost stable recursive $defs identity: %s", inputJSON)
			}
			if !strings.Contains(string(outputJSON), "#/operations/upsertUser/output/$defs/User") {
				t.Fatalf("output lost stable recursive $defs identity: %s", outputJSON)
			}
			if strings.Contains(string(inputJSON), `"readOnly":true`) {
				t.Errorf("request projection retained readOnly annotation/property: %s", inputJSON)
			}
			if strings.Contains(string(outputJSON), `"writeOnly":true`) {
				t.Errorf("response projection retained writeOnly annotation/property: %s", outputJSON)
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
	if request {
		if _, ok := properties[readOnlyName]; ok {
			t.Errorf("request retained readOnly property %q", readOnlyName)
		}
		if _, ok := properties[writeOnlyName]; !ok {
			t.Errorf("request omitted writeOnly property %q", writeOnlyName)
		}
		assertRequired(t, schema, []string{writeOnlyName, "label"}, []string{readOnlyName})
		return
	}
	if _, ok := properties[readOnlyName]; !ok {
		t.Errorf("response omitted readOnly property %q", readOnlyName)
	}
	if _, ok := properties[writeOnlyName]; ok {
		t.Errorf("response retained writeOnly property %q", writeOnlyName)
	}
	assertRequired(t, schema, []string{readOnlyName, "label"}, []string{writeOnlyName})
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
