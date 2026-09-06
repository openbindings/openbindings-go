package openapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	openapiclient "github.com/openbindings/openapi-client/go"
	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/invoke"
	"github.com/openbindings/openbindings-go/synthesize"
)

func TestNativeSecuritySelectionRequirementUsesBindingContextShape(t *testing.T) {
	requirements, err := nativeBindingRequirements(&openapiclient.ConfigurationRequirements{
		Target: "https://api.example.test",
		Alternatives: []openapiclient.ConfigurationAlternative{{Requirements: []openapiclient.ConfigurationRequirement{{
			Kind:          openapiclient.RequirementOption,
			Name:          "SecurityAlternative",
			AllowedValues: []any{0, 1},
		}}}},
	})
	if err != nil || requirements == nil || len(requirements.Alternatives) != 1 || len(requirements.Alternatives[0].Requirements) != 1 {
		t.Fatalf("translated requirements = %#v, err=%v", requirements, err)
	}
	requirement := requirements.Alternatives[0].Requirements[0]
	if requirement.Type != "config.value" || requirement.Extra["point"] != "security" || requirement.Extra["path"] != "/index" {
		t.Fatalf("translated security requirement = %#v", requirement)
	}
}

func TestPreparedDependencySynthesizedOpenAPI(t *testing.T) {
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
		Dependencies: map[string]openbindings.DependencyEntry{
			"creation": {
				Operation:    "example.tasks.create",
				BindingSpecs: []string{BindingSpecOpenAPI31},
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
		&synthesize.SynthesizeInput{
			Sources: []synthesize.SynthesizeSource{{
				BindingSpec: bindingSpecForTestDocument(specContent),
				Content:     specContent,
			}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	consumer, err := openbindings.PrepareInterface(required)
	if err != nil {
		t.Fatal(err)
	}
	providerInterface, err := openbindings.PrepareInterface(candidate)
	if err != nil {
		t.Fatal(err)
	}
	opInvoker := invoke.NewOperationInvoker(NewInvoker())
	opInvoker.TransformEvaluator = openAPIJSONataEvaluator{}
	provider, err := invoke.PrepareProvider(invoke.PreparedProviderOptions{
		Key:       "tasks-api",
		Interface: providerInterface,
		Runtime:   opInvoker,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	session, err := invoke.NewCompositionSession(invoke.CompositionSessionOptions{
		Consumer: consumer,
		Providers: []invoke.ProviderRegistration{{
			Provider: provider,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	operation := invoke.NewOperationSignature[map[string]any, map[string]any]("example.tasks.create")
	dependency := invoke.NewDependencySignatureForOperation("creation", operation)
	resolution, err := invoke.ResolveDependency(context.Background(), session, dependency)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Status != invoke.DependencyAvailable {
		t.Fatalf("resolution = %#v", resolution)
	}

	call := resolution.Route.Invoke(context.Background())
	if err := call.Write(context.Background(), map[string]any{
		"title": "Ship the operation layer",
	}); err != nil {
		t.Fatal(err)
	}
	output, err := invoke.Single(context.Background(), call.Outputs())
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
