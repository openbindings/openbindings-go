package openapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func TestOperationRequirementSynthesizedOpenAPI(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "task_1"})
	}))
	defer server.Close()

	inputSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"title": map[string]any{"type": "string"},
		},
		"required": []any{"title"},
	}
	outputSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id": map[string]any{"type": "string"},
		},
		"required": []any{"id"},
	}
	required := &openbindings.Interface{
		OpenBindings: "0.2.0",
		Operations: map[string]openbindings.Operation{
			"example.tasks.create": {
				Input:  inputSchema,
				Output: outputSchema,
			},
		},
	}
	spec := map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":   "Tasks",
			"version": "1.0.0",
		},
		"servers": []any{map[string]any{"url": server.URL}},
		"paths": map[string]any{
			"/todos": map[string]any{
				"post": map[string]any{
					"operationId": "example.tasks.create",
					"requestBody": map[string]any{
						"required": true,
						"content": map[string]any{
							"application/json": map[string]any{"schema": inputSchema},
						},
					},
					"responses": map[string]any{
						"201": map[string]any{
							"description": "Created",
							"content": map[string]any{
								"application/json": map[string]any{"schema": outputSchema},
							},
						},
					},
				},
			},
		},
	}
	specContent, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := NewSynthesizer().SynthesizeInterface(
		context.Background(),
		&openbindings.SynthesizeInput{
			Sources: []openbindings.SynthesizeSource{{
				BindingSpec: BindingSpec,
				Content:     specContent,
			}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	signature := openbindings.NewOperationSignature[map[string]any, map[string]any]("example.tasks.create")
	requirement, err := openbindings.NewOperationRequirement(required, signature)
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := openbindings.ResolveOperationRequirement(
		context.Background(),
		requirement,
		[]openbindings.OperationImplementation{{
			Interface: candidate,
			Invoker:   openbindings.NewOperationInvoker(NewInvoker()),
			Label:     "tasks-api",
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Status != openbindings.OperationRequirementAvailable {
		t.Fatalf("resolution = %#v", resolution)
	}

	call := resolution.Match.Invoke(context.Background())
	if err := call.Write(context.Background(), map[string]any{
		"title": "Ship the operation layer",
	}); err != nil {
		t.Fatal(err)
	}
	output, err := openbindings.Single(context.Background(), call.Outputs())
	if err != nil {
		t.Fatal(err)
	}
	if output["id"] != "task_1" {
		t.Fatalf("output = %#v", output)
	}
	if gotMethod != http.MethodPost || gotPath != "/todos" {
		t.Fatalf("request = %s %s", gotMethod, gotPath)
	}
	if gotBody["title"] != "Ship the operation layer" {
		t.Fatalf("body = %#v", gotBody)
	}
}
