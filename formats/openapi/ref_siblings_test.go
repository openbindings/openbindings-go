package openapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func synthesizeRefSiblingFixture(t *testing.T, client *http.Client, location, document string) *openbindings.Interface {
	t.Helper()
	synthesizer := NewSynthesizerWithClient(client)
	iface, err := synthesizer.SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{
			BindingSpec: BindingSpec,
			Location:    location,
			Content:     openbindings.TextContent(document),
		}},
	})
	if err != nil {
		t.Fatalf("synthesize fixture: %v", err)
	}
	return iface
}

func TestOpenAPI31RefSiblingsComposeAtDirectAndNestedSchemaPositions(t *testing.T) {
	doc := `openapi: 3.1.2
info: {title: Ref siblings, version: "1"}
paths:
  /direct:
    get:
      operationId: direct
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/Base"
                description: direct sibling
                allOf:
                  - {maxProperties: 2}
  /nested:
    get:
      operationId: nested
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
                properties:
                  value:
                    $ref: "#/components/schemas/Base"
                    description: nested sibling
                    allOf:
                      - {minProperties: 1}
components:
  schemas:
    Base:
      type: object
      description: target annotation
      properties:
        id: {type: string}
`
	iface := synthesizeRefSiblingFixture(t, http.DefaultClient, "", doc)

	direct := iface.Operations["direct"].Output.(map[string]any)
	if direct["description"] != "direct sibling" {
		t.Fatalf("direct sibling annotation = %#v", direct["description"])
	}
	directAllOf := direct["allOf"].([]any)
	if len(directAllOf) != 2 {
		t.Fatalf("direct allOf = %#v, want target plus authored sibling applicator", directAllOf)
	}
	target := directAllOf[0].(map[string]any)
	if target["description"] != "target annotation" || target["type"] != "object" {
		t.Fatalf("direct referenced semantics were not preserved: %#v", target)
	}
	if directAllOf[1].(map[string]any)["maxProperties"] != float64(2) {
		t.Fatalf("direct sibling applicator was not preserved: %#v", directAllOf[1])
	}

	nested := iface.Operations["nested"].Output.(map[string]any)["properties"].(map[string]any)["value"].(map[string]any)
	if nested["description"] != "nested sibling" {
		t.Fatalf("nested sibling annotation = %#v", nested["description"])
	}
	nestedAllOf := nested["allOf"].([]any)
	if len(nestedAllOf) != 2 || nestedAllOf[0].(map[string]any)["description"] != "target annotation" {
		t.Fatalf("nested target and sibling did not compose: %#v", nestedAllOf)
	}
	if nestedAllOf[1].(map[string]any)["minProperties"] != float64(1) {
		t.Fatalf("nested sibling applicator was not preserved: %#v", nestedAllOf[1])
	}
}

func TestOpenAPI30SchemaRefSiblingsAreIgnored(t *testing.T) {
	doc := `openapi: 3.0.3
info: {title: Ref siblings, version: "1"}
paths:
  /value:
    get:
      operationId: value
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/Base"
                description: ignored sibling
                allOf:
                  - {maxLength: 2}
components:
  schemas:
    Base:
      type: string
      description: target annotation
      minLength: 5
`
	iface := synthesizeRefSiblingFixture(t, http.DefaultClient, "", doc)
	output := iface.Operations["value"].Output.(map[string]any)
	if output["description"] != "target annotation" || output["minLength"] != float64(5) {
		t.Fatalf("3.0 target semantics were not retained: %#v", output)
	}
	if _, present := output["allOf"]; present || output["description"] == "ignored sibling" {
		t.Fatalf("3.0 schema-ref siblings were applied: %#v", output)
	}
}

func TestPrepareDocUsesTheSameInlineRefSiblingNormalizer(t *testing.T) {
	doc := `openapi: 3.0.3
info: {title: Prepare siblings, version: "1"}
paths:
  /value:
    get:
      operationId: value
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/Base"
                description: ignored at the reference site
components:
  schemas:
    Base: {type: string, description: component description}
`
	loaded := NewInvoker().prepareDoc("", openbindings.TextContent(doc))
	if loaded == nil {
		t.Fatal("prepareDoc rejected a locally resolvable inline document")
	}
	schema := loaded.Paths.Find("/value").Get.Responses.Value("200").Value.Content["application/json"].Schema.Value
	if schema.Description != "component description" {
		t.Fatalf("prepareDoc diverged from invocation's 3.0 ref semantics: %q", schema.Description)
	}
}

func TestOpenAPI30ReferenceObjectSiblingsAreIgnored(t *testing.T) {
	doc := `openapi: 3.0.3
info: {title: Reference siblings, version: "1"}
paths:
  /value:
    get:
      operationId: value
      parameters:
        - $ref: "#/components/parameters/Trace"
          description: ignored reference sibling
      responses: {"204": {description: ok}}
components:
  parameters:
    Trace:
      name: trace
      in: query
      description: target parameter
      schema: {type: string}
`
	iface := synthesizeRefSiblingFixture(t, http.DefaultClient, "", doc)
	trace := iface.Operations["value"].Input.(map[string]any)["properties"].(map[string]any)["trace"].(map[string]any)
	if trace["description"] != "target parameter" || trace["type"] != "string" {
		t.Fatalf("3.0 Reference Object sibling changed its target: %#v", trace)
	}
}

func TestOpenAPI31ExternalNestedRefSiblingsCompose(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://description.example/schemas.yaml" {
			return nil, errors.New("unexpected artifact request: " + req.URL.String())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{},
			Body: io.NopCloser(strings.NewReader(`Base:
  type: string
  description: external target
  minLength: 3
Alias:
  $ref: "#/Base"
  description: external sibling
  allOf:
    - {maxLength: 8}
UntouchedData:
  value:
    $ref: "#/Base"
    description: ordinary data
`)),
			Request: req,
		}, nil
	})}
	doc := `openapi: 3.1.2
info: {title: External ref siblings, version: "1"}
paths:
  /value:
    get:
      operationId: value
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema: {$ref: "./schemas.yaml#/Alias"}
`
	iface := synthesizeRefSiblingFixture(t, client, "https://description.example/openapi.yaml", doc)
	output := iface.Operations["value"].Output.(map[string]any)
	if output["description"] != "external sibling" {
		t.Fatalf("external sibling annotation = %#v", output["description"])
	}
	allOf := output["allOf"].([]any)
	if len(allOf) != 2 {
		t.Fatalf("external composed allOf = %#v", allOf)
	}
	target := allOf[0].(map[string]any)
	if target["description"] != "external target" || target["minLength"] != float64(3) {
		t.Fatalf("external referenced semantics were lost: %#v", target)
	}
	if allOf[1].(map[string]any)["maxLength"] != float64(8) {
		t.Fatalf("external sibling applicator was lost: %#v", allOf[1])
	}
}

func TestRedirectFinalRetrievalURIControlsRelativeIDAndNestedRef(t *testing.T) {
	const original = "https://description.example/openapi.yaml"
	const final = "https://cdn.example/contracts/openapi.yaml"
	const child = "https://cdn.example/contracts/schemas/child.yaml"
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		respond := func(body, responseURL string) *http.Response {
			responseRequest, _ := http.NewRequestWithContext(req.Context(), http.MethodGet, responseURL, nil)
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: responseRequest}
		}
		switch req.URL.String() {
		case original:
			return respond(`openapi: 3.1.2
info: {title: Redirect base, version: "1"}
paths:
  /value:
    get:
      operationId: value
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                $id: schemas/base.yaml
                $ref: child.yaml#/Thing
                description: site sibling
`, final), nil
		case child:
			return respond(`Thing:
  $ref: "#/Base"
  description: child sibling
Base: {type: string, minLength: 3}
`, child), nil
		default:
			return nil, errors.New("unexpected artifact request: " + req.URL.String())
		}
	})}
	iface, err := NewSynthesizerWithClient(client).SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{BindingSpec: BindingSpec, Location: original}},
	})
	if err != nil {
		t.Fatalf("synthesize redirected artifact: %v", err)
	}
	output := iface.Operations["value"].Output.(map[string]any)
	if output["description"] != "site sibling" {
		t.Fatalf("site sibling lost after redirect: %#v", output)
	}
	encoded, _ := json.Marshal(output)
	if !strings.Contains(string(encoded), `"minLength":3`) || !strings.Contains(string(encoded), `"child sibling"`) {
		t.Fatalf("nested ref did not use the final retrieval URI: %s", encoded)
	}
}

func TestExternalPointerTargetUsesEffectiveNestedIDBaseForRelativeRef(t *testing.T) {
	const bundle = "https://description.example/bundle.json"
	const child = "https://description.example/nested/child.yaml"
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		var body string
		switch req.URL.String() {
		case bundle:
			body = `{
			  "$defs": {
			    "Bundle": {
			      "$id": "nested/base.json",
			      "$defs": {
			        "Target": {
			          "type": "object",
			          "properties": {
			            "child": {
			              "$ref": "child.yaml#/Child",
			              "description": "site child"
			            }
			          }
			        }
			      }
			    }
			  }
			}`
		case child:
			body = `{"Child":{"type":"string","minLength":2}}`
		default:
			return nil, errors.New("unexpected artifact request: " + req.URL.String())
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})}
	doc := `openapi: 3.1.2
info: {title: Nested pointer base, version: "1"}
paths:
  /value:
    get:
      operationId: value
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                $ref: "https://description.example/bundle.json#/$defs/Bundle/$defs/Target"
`
	iface := synthesizeRefSiblingFixture(t, client, "", doc)
	output := iface.Operations["value"].Output.(map[string]any)
	childSchema := output["properties"].(map[string]any)["child"].(map[string]any)
	encoded, _ := json.Marshal(childSchema)
	if childSchema["description"] != "site child" || !strings.Contains(string(encoded), `"minLength":2`) {
		t.Fatalf("nested-$id pointer target used the physical resource base: %#v", childSchema)
	}
}

func TestRefSiblingNormalizationDoesNotRewriteExamplesExtensionsOrData(t *testing.T) {
	doc := `openapi: 3.1.2
info: {title: Opaque data, version: "1"}
paths:
  /value:
    get:
      operationId: value
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
                example:
                  $ref: "#/components/schemas/Target"
                  description: example data
                x-payload:
                  schema:
                    $ref: "#/components/schemas/Target"
                    description: extension data
              examples:
                sample:
                  value:
                    schema:
                      $ref: "#/components/schemas/Target"
                      description: media example data
components:
  schemas:
    Target: {type: string}
`
	loaded, err := loadDocument("", openbindings.TextContent(doc))
	if err != nil {
		t.Fatalf("load document: %v", err)
	}
	media := loaded.Paths.Find("/value").Get.Responses.Value("200").Value.Content["application/json"]
	schema := media.Schema.Value
	wantExample := map[string]any{"$ref": "#/components/schemas/Target", "description": "example data"}
	if !reflect.DeepEqual(schema.Example, wantExample) {
		t.Fatalf("schema example was rewritten: %#v", schema.Example)
	}
	extension := schema.Extensions["x-payload"].(map[string]any)["schema"]
	wantExtension := map[string]any{"$ref": "#/components/schemas/Target", "description": "extension data"}
	if !reflect.DeepEqual(extension, wantExtension) {
		t.Fatalf("extension data was rewritten: %#v", extension)
	}
	mediaValue := media.Examples["sample"].Value.Value.(map[string]any)["schema"]
	wantMediaValue := map[string]any{"$ref": "#/components/schemas/Target", "description": "media example data"}
	if !reflect.DeepEqual(mediaValue, wantMediaValue) {
		t.Fatalf("media example data was rewritten: %#v", mediaValue)
	}
}

func TestOpenAPI31UnsupportedJSONSchemaDialectRefusesBeforeSynthesis(t *testing.T) {
	unsupported := `openapi: 3.1.2
jsonSchemaDialect: https://example.test/unknown-dialect
info: {title: Unsupported dialect, version: "1"}
paths:
  /value:
    get:
      operationId: value
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/Target"
                description: cannot safely compose
components:
  schemas:
    Target: {type: string}
`
	_, err := NewSynthesizer().SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{BindingSpec: BindingSpec, Content: openbindings.TextContent(unsupported)}},
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported OpenAPI 3.1 schema dialect") {
		t.Fatalf("synthesis error = %v, want unsupported-dialect refusal", err)
	}

	withoutSiblings := strings.Replace(unsupported, "\n                description: cannot safely compose", "", 1)
	if _, err := NewSynthesizer().SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{BindingSpec: BindingSpec, Content: openbindings.TextContent(withoutSiblings)}},
	}); err == nil || !strings.Contains(err.Error(), "unsupported OpenAPI 3.1 schema dialect") {
		t.Fatalf("unsupported dialect without ref siblings must also refuse, got %v", err)
	}
}

func TestOpenAPI31UnsupportedPerSchemaDialectRefusesBeforeSynthesis(t *testing.T) {
	doc := `openapi: 3.1.2
info: {title: Per-schema dialect, version: "1"}
paths:
  /value:
    get:
      operationId: value
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                $schema: https://example.test/custom-dialect
                type: string
`
	_, err := NewSynthesizer().SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{BindingSpec: BindingSpec, Content: openbindings.TextContent(doc)}},
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported OpenAPI 3.1 schema dialect") || !strings.Contains(err.Error(), "OBI-D-06") {
		t.Fatalf("synthesis error = %v, want per-schema dialect refusal", err)
	}
}

func TestOpenAPI31UnusedCustomPerSchemaDialectDoesNotBlockOtherOperations(t *testing.T) {
	doc := `openapi: 3.1.2
info: {title: Unused dialect, version: "1"}
paths:
  /value:
    get:
      operationId: value
      responses:
        "200":
          description: ok
          content: {application/json: {schema: {type: string}}}
components:
  schemas:
    Base: {type: integer}
    Unused:
      $schema: https://example.test/custom-dialect
      $ref: "#/components/schemas/Base"
      x-custom-constraint: true
`
	iface, err := NewSynthesizer().SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{BindingSpec: BindingSpec, Content: openbindings.TextContent(doc)}},
	})
	if err != nil {
		t.Fatalf("unused custom-dialect component blocked a representable operation: %v", err)
	}
	if iface.Operations["value"].Output == nil {
		t.Fatal("representable operation output was not synthesized")
	}
}

func TestInspectSourceListsSchemaFreeCustomDocumentDialectOperation(t *testing.T) {
	doc := `openapi: 3.1.2
jsonSchemaDialect: https://example.test/custom-dialect
info: {title: Inspect custom dialect, version: "1"}
paths:
  /health:
    get:
      operationId: health
      responses: {"204": {description: ok}}
`
	inspection, err := NewSynthesizer().InspectSource(context.Background(), &openbindings.Source{
		BindingSpec: BindingSpec,
		Content:     openbindings.TextContent(doc),
	})
	if err != nil {
		t.Fatalf("schema-free inspection should use artifact-native loading: %v", err)
	}
	if len(inspection.Targets) != 1 || inspection.Targets[0].OperationKey != "health" {
		t.Fatalf("inspection targets = %+v", inspection.Targets)
	}
}

func TestCustomDocumentDialectGatesOnlyOperationsThatProjectIt(t *testing.T) {
	doc := `openapi: 3.1.2
jsonSchemaDialect: https://example.test/custom-dialect
info: {title: Mixed dialect operations, version: "1"}
paths:
  /schema-free:
    get:
      operationId: schemaFree
      responses: {"204": {description: ok}}
  /supported:
    get:
      operationId: supported
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                $schema: https://json-schema.org/draft/2020-12/schema
                type: string
  /unsupported:
    get:
      operationId: unsupported
      responses:
        "200":
          description: ok
          content: {application/json: {schema: {type: string}}}
`
	inspection, err := NewSynthesizer().InspectSource(context.Background(), &openbindings.Source{
		BindingSpec: BindingSpec,
		Content:     openbindings.TextContent(doc),
	})
	if err != nil {
		t.Fatalf("tolerant inspection: %v", err)
	}
	keys := map[string]bool{}
	for _, target := range inspection.Targets {
		keys[target.OperationKey] = true
		if target.OperationKey == "unsupported" {
			t.Fatalf("inspection published a false 2020-12 contract: %+v", target)
		}
	}
	if !keys["schemaFree"] || !keys["supported"] || len(keys) != 2 {
		t.Fatalf("inspection operation keys = %v", keys)
	}
	_, err = NewSynthesizer().SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{BindingSpec: BindingSpec, Content: openbindings.TextContent(doc)}},
	})
	if err == nil || !strings.Contains(err.Error(), "OBI-D-06") {
		t.Fatalf("strict synthesis must refuse the unsupported projected operation, got %v", err)
	}
}

func TestRefSiblingFixtureDocumentsRemainJSONSerializable(t *testing.T) {
	// A narrow guard for the raw-normalization boundary: its JSON round trip
	// must still yield ordinary OBI JSON values, never typed loader internals.
	iface := synthesizeRefSiblingFixture(t, http.DefaultClient, "", `openapi: 3.1.2
info: {title: JSON, version: "1"}
paths: {/v: {get: {operationId: v, responses: {"200": {description: ok, content: {application/json: {schema: {type: string}}}}}}}}
`)
	if _, err := json.Marshal(iface); err != nil {
		t.Fatalf("marshal synthesized interface: %v", err)
	}
}

func TestSchemaAnchorResolutionIgnoresExamplesAndExtensions(t *testing.T) {
	var root any
	err := json.Unmarshal([]byte(`{
	  "openapi":"3.1.2",
	  "info":{"title":"Anchor scope","version":"1"},
	  "x-fake":{"$anchor":"Value","type":"integer"},
	  "components":{
	    "examples":{"Fake":{"value":{"$anchor":"Value","type":"integer"}}},
	    "schemas":{"Real":{"$anchor":"Value","type":"string","maxLength":10}}
	  },
	  "paths":{}
	}`), &root)
	if err != nil {
		t.Fatal(err)
	}
	targetValue, ok := rawFragmentTarget(root, "Value", rawSchemaTarget)
	if !ok {
		t.Fatal("schema anchor was not indexed")
	}
	target := targetValue.(map[string]any)
	if target["type"] != "string" || target["maxLength"] != float64(10) {
		t.Fatalf("anchor resolved outside a Schema Object position: %#v", target)
	}
}

func TestSchemaAnchorsAreIndexedWithinTheirIDResourceScope(t *testing.T) {
	var root any
	err := json.Unmarshal([]byte(`{
	  "openapi":"3.1.2",
	  "info":{"title":"Anchor resources","version":"1"},
	  "components":{"schemas":{
	    "Parent":{"$anchor":"A","type":"string"},
	    "Nested":{"$id":"nested/schema.json","$defs":{"Child":{"$anchor":"A","type":"integer"}}}
	  }},
	  "paths":{}
	}`), &root)
	if err != nil {
		t.Fatal(err)
	}
	base, _ := url.Parse("https://description.example/contracts/openapi.json")
	scopes := rawSchemaResourceScopes(root, base)
	parent, _, ok := scopes["https://description.example/contracts/openapi.json"].fragment("A")
	if !ok || parent.(map[string]any)["type"] != "string" {
		t.Fatalf("parent #A crossed into nested $id resource: %#v", parent)
	}
	nested, _, ok := scopes["https://description.example/contracts/nested/schema.json"].fragment("A")
	if !ok || nested.(map[string]any)["type"] != "integer" {
		t.Fatalf("nested resource anchor was not indexed under its resolved $id: %#v", nested)
	}
}

func TestOpenAPI31ReferenceMetadataIsSiteLocal(t *testing.T) {
	doc := `openapi: 3.1.2
info: {title: Reference metadata, version: "1"}
paths:
  /a:
    get:
      operationId: a
      parameters:
        - {$ref: "#/components/parameters/Shared", description: site A}
      responses: {"204": {description: ok}}
  /b:
    get:
      operationId: b
      parameters:
        - {$ref: "#/components/parameters/Shared", description: site B}
      responses: {"204": {description: ok}}
components:
  parameters:
    Shared:
      name: value
      in: query
      description: component description
      schema: {type: string}
`
	iface := synthesizeRefSiblingFixture(t, http.DefaultClient, "", doc)
	description := func(operation string) any {
		input := iface.Operations[operation].Input.(map[string]any)
		return input["properties"].(map[string]any)["value"].(map[string]any)["description"]
	}
	if got := description("a"); got != "site A" {
		t.Fatalf("site A description = %#v", got)
	}
	if got := description("b"); got != "site B" {
		t.Fatalf("site B description = %#v", got)
	}
}

func TestOpenAPI31SecuritySchemeReferenceDescriptionsAreSiteLocal(t *testing.T) {
	doc := `openapi: 3.1.2
info: {title: Security reference metadata, version: "1"}
servers: [{url: https://api.example.com}]
paths:
  /a:
    get:
      operationId: a
      security: [{SiteA: []}]
      responses: {"204": {description: ok}}
  /b:
    get:
      operationId: b
      security: [{SiteB: []}]
      responses: {"204": {description: ok}}
components:
  securitySchemes:
    Base: {type: http, scheme: bearer, description: base description}
    SiteA: {$ref: "#/components/securitySchemes/Base", description: site A}
    SiteB: {$ref: "#/components/securitySchemes/Base", description: site B}
`
	loaded, err := loadDocument("", openbindings.TextContent(doc))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	description := func(path string) string {
		op := loaded.Paths.Find(path).Get
		details := requiredContext(loaded, op, nil, "https://api.example.com", nil)
		if details == nil || len(details.Alternatives) != 1 || len(details.Alternatives[0].Requirements) != 1 {
			t.Fatalf("%s challenge = %+v", path, details)
		}
		return details.Alternatives[0].Requirements[0].Description
	}
	if got := description("/a"); got != "site A" {
		t.Fatalf("site A description = %q", got)
	}
	if got := description("/b"); got != "site B" {
		t.Fatalf("site B description = %q", got)
	}
}
