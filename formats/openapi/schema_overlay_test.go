package openapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func synthesizeOverlayFixture(t *testing.T, synthesizer *Synthesizer, location, document string) openbindings.Interface {
	t.Helper()
	iface, err := synthesizer.SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{
			BindingSpec: BindingSpec,
			Location:    location,
			Content:     openbindings.TextContent(document),
		}},
	})
	if err != nil {
		t.Fatalf("SynthesizeInterface: %v", err)
	}
	return *iface
}

func TestSynthesisRestoresDirectAndNestedAuthorialSchemaPresence(t *testing.T) {
	iface := synthesizeOverlayFixture(t, NewSynthesizer(), "", `openapi: 3.1.2
info: {title: Presence, version: "1"}
paths:
  /value:
    get:
      operationId: value
      parameters:
        - name: marker
          in: query
          schema: {type: string, description: "", minLength: 0, default: null}
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
                description: ""
                deprecated: false
                $defs:
                  Authorial: {type: string, description: "", default: null}
                properties:
                  annotated:
                    type: string
                    description: ""
                    minLength: 0
                    default: null
                    example: null
                    const: null
                    examples: []
                    x-null: null
                    x-empty-string: ""
                    x-empty-map: {}
                    x-empty-array: []
                    x-zero: 0
                    x-false: false
                  emptyObject:
                    type: object
                    properties: {}
                  absent:
                    type: string
                  composed:
                    $ref: "#/components/schemas/Base"
                    description: ""
                    default: null
components:
  schemas:
    Base: {type: string, pattern: ""}
`)
	input := iface.Operations["value"].Input.(map[string]any)
	inputMarker := input["properties"].(map[string]any)["marker"].(map[string]any)
	if value, present := inputMarker["default"]; !present || value != nil {
		t.Errorf("input default = %#v (present %v), want authored null", value, present)
	}
	if value, present := inputMarker["description"]; !present || value != "" {
		t.Errorf("input empty description = %#v (present %v)", value, present)
	}
	if value, present := inputMarker["minLength"]; !present || value != float64(0) && value != 0 {
		t.Errorf("input minLength = %#v (present %v), want authored zero", value, present)
	}
	output := iface.Operations["value"].Output.(map[string]any)
	if value, present := output["description"]; !present || value != "" {
		t.Fatalf("direct empty description = %#v (present %v), want authored empty string", value, present)
	}
	if value, present := output["deprecated"]; !present || value != false {
		t.Fatalf("direct false annotation = %#v (present %v), want authored false", value, present)
	}
	authorialDef := output["$defs"].(map[string]any)["Authorial"].(map[string]any)
	if value, present := authorialDef["default"]; !present || value != nil {
		t.Errorf("nested $defs default = %#v (present %v), want authored null", value, present)
	}
	if value, present := authorialDef["description"]; !present || value != "" {
		t.Errorf("nested $defs description = %#v (present %v)", value, present)
	}
	properties := output["properties"].(map[string]any)
	annotated := properties["annotated"].(map[string]any)
	for _, key := range []string{"default", "example", "const", "x-null"} {
		if value, present := annotated[key]; !present || value != nil {
			t.Errorf("%s = %#v (present %v), want authored null", key, value, present)
		}
	}
	if value, present := annotated["description"]; !present || value != "" {
		t.Errorf("nested empty description = %#v (present %v)", value, present)
	}
	if value, present := annotated["minLength"]; !present || value != float64(0) && value != 0 {
		t.Errorf("minLength = %#v (present %v), want authored zero", value, present)
	}
	if examples, present := annotated["examples"].([]any); !present || len(examples) != 0 {
		t.Errorf("examples = %#v (present %v), want authored empty array", annotated["examples"], present)
	}
	if value := annotated["x-empty-string"]; value != "" {
		t.Errorf("x-empty-string = %#v", value)
	}
	if value := annotated["x-zero"]; value != 0 && value != float64(0) {
		t.Errorf("x-zero = %#v", value)
	}
	if value := annotated["x-false"]; value != false {
		t.Errorf("x-false = %#v", value)
	}
	if value, ok := annotated["x-empty-map"].(map[string]any); !ok || len(value) != 0 {
		t.Errorf("x-empty-map = %#v", annotated["x-empty-map"])
	}
	if value, ok := annotated["x-empty-array"].([]any); !ok || len(value) != 0 {
		t.Errorf("x-empty-array = %#v", annotated["x-empty-array"])
	}
	if value, ok := properties["emptyObject"].(map[string]any)["properties"].(map[string]any); !ok || len(value) != 0 {
		t.Errorf("emptyObject.properties = %#v, want authored empty map", properties["emptyObject"])
	}
	absent := properties["absent"].(map[string]any)
	for _, key := range []string{"default", "example", "description", "minLength", "deprecated"} {
		if _, invented := absent[key]; invented {
			t.Errorf("absent schema invented %q: %#v", key, absent)
		}
	}
	composed := properties["composed"].(map[string]any)
	composedTarget := composed["allOf"].([]any)[0].(map[string]any)
	if composedTarget["type"] != "string" {
		t.Errorf("3.1 ref sibling target was not composed: %#v", composed)
	}
	if value, present := composed["description"]; !present || value != "" {
		t.Errorf("composed description = %#v (present %v), want authored empty string", value, present)
	}
	if value, present := composedTarget["pattern"]; !present || value != "" {
		t.Errorf("composed target pattern = %#v (present %v), want authored empty string", value, present)
	}
	if value, present := composed["default"]; !present || value != nil {
		t.Errorf("composed default = %#v (present %v), want authored null", value, present)
	}
	assertNoSchemaOverlayMarker(t, iface)
}

func TestSynthesisRestoresExternalAndRecursiveSchemaPresenceWithoutExtraFetch(t *testing.T) {
	var requests atomic.Int32
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://description.example/schemas.yaml" {
			return nil, fmt.Errorf("unexpected artifact request: %s", req.URL)
		}
		requests.Add(1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{},
			Body: io.NopCloser(strings.NewReader(`Envelope:
  type: object
  description: ""
  properties:
    value: {type: string, minLength: 0, default: null, example: null}
    next: {$ref: "#/Envelope"}
`)),
			Request: req,
		}, nil
	})}
	iface := synthesizeOverlayFixture(t, NewSynthesizerWithClient(client), "https://description.example/openapi.yaml", `openapi: 3.1.2
info: {title: External presence, version: "1"}
paths:
  /value:
    get:
      operationId: externalValue
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema: {$ref: "./schemas.yaml#/Envelope"}
`)
	if got := requests.Load(); got != 1 {
		t.Fatalf("external closure fetches = %d, want exactly one", got)
	}
	output := iface.Operations["externalValue"].Output.(map[string]any)
	if value, present := output["description"]; !present || value != "" {
		t.Fatalf("external empty description = %#v (present %v)", value, present)
	}
	definitions, ok := output["$defs"].(map[string]any)
	if !ok {
		t.Fatalf("recursive external output has no definitions: %#v", output)
	}
	if len(definitions) != 1 {
		t.Fatalf("recursive external definitions = %#v, want one Envelope definition", definitions)
	}
	var definitionName string
	var envelope map[string]any
	for name, definition := range definitions {
		definitionName = name
		envelope = definition.(map[string]any)
	}
	value := envelope["properties"].(map[string]any)["value"].(map[string]any)
	if defaultValue, present := value["default"]; !present || defaultValue != nil {
		t.Errorf("recursive definition default = %#v (present %v), want null", defaultValue, present)
	}
	if exampleValue, present := value["example"]; !present || exampleValue != nil {
		t.Errorf("recursive definition example = %#v (present %v), want null", exampleValue, present)
	}
	if minimum, present := value["minLength"]; !present || minimum != float64(0) && minimum != 0 {
		t.Errorf("recursive definition minLength = %#v (present %v), want zero", minimum, present)
	}
	next := envelope["properties"].(map[string]any)["next"].(map[string]any)
	wantRef := "#/operations/externalValue/output/$defs/" + escapeJSONPointerSegment(definitionName)
	if next["$ref"] != wantRef {
		t.Errorf("recursive ref = %#v", next)
	}
	assertNoSchemaOverlayMarker(t, iface)
}

func TestSynthesisOverlayNeverRestoresIgnoredOpenAPI30RefSiblings(t *testing.T) {
	iface := synthesizeOverlayFixture(t, NewSynthesizer(), "", `openapi: 3.0.3
info: {title: Ref siblings, version: "1"}
paths:
  /value:
    get:
      operationId: ignoredSiblings
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/Value"
                description: ""
                default: null
                x-ignored: false
components:
  schemas:
    Value: {type: string, description: target, example: null, minLength: 0}
`)
	output := iface.Operations["ignoredSiblings"].Output.(map[string]any)
	if output["description"] != "target" {
		t.Fatalf("resolved target = %#v, want target description", output)
	}
	if value, present := output["example"]; !present || value != nil {
		t.Fatalf("target example = %#v (present %v), want authored explicit null", value, present)
	}
	if value, present := output["minLength"]; !present || value != float64(0) && value != 0 {
		t.Fatalf("target minLength = %#v (present %v), want authored zero", value, present)
	}
	for _, key := range []string{"default", "x-ignored"} {
		if _, restored := output[key]; restored {
			t.Errorf("ignored OAS 3.0 ref sibling %q was restored: %#v", key, output)
		}
	}
	assertNoSchemaOverlayMarker(t, iface)
}

func TestSynthesisOverlayOwnershipIsConcurrentAndCollisionSafe(t *testing.T) {
	const runs = 24
	synthesizer := NewSynthesizer()
	errors := make(chan error, runs)
	var wait sync.WaitGroup
	for index := 0; index < runs; index++ {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			// Deliberately use the collector's former predictable token spelling.
			// Ownership is by the load-local sidecar, never by guessing from the
			// raw value.
			authored := fmt.Sprintf("schema-%d", index+1)
			document := fmt.Sprintf(`{"openapi":"3.1.2","info":{"title":"Race","version":"1"},"paths":{"/value":{"get":{"operationId":"value","responses":{"200":{"description":"ok","content":{"application/json":{"schema":{"type":"string","default":null,%q:%q}}}}}}}}}`, schemaOverlayMarker, authored)
			iface, err := synthesizer.SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
				Sources: []openbindings.SynthesizeSource{{BindingSpec: BindingSpec, Content: openbindings.TextContent(document)}},
			})
			if err != nil {
				errors <- err
				return
			}
			output := iface.Operations["value"].Output.(map[string]any)
			if output[schemaOverlayMarker] != authored {
				errors <- fmt.Errorf("run %d marker collision restored %#v, want %q", index, output[schemaOverlayMarker], authored)
				return
			}
			if value, present := output["default"]; !present || value != nil {
				errors <- fmt.Errorf("run %d default = %#v (present %v)", index, value, present)
			}
		}()
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
}

func TestSynthesisOverlayCoversEncodingHeaderSchemasWithLoadLocalMarkers(t *testing.T) {
	first := newRawSchemaOverlayCollector()
	firstSchema := map[string]any{"default": nil}
	if !first.markRawSchema(firstSchema) {
		t.Fatal("first schema was not marked")
	}
	foreignMarker := firstSchema[schemaOverlayMarker].(string)
	second := newRawSchemaOverlayCollector()
	secondSchema := map[string]any{"default": nil}
	if !second.markRawSchema(secondSchema) {
		t.Fatal("second schema was not marked")
	}
	if currentMarker := secondSchema[schemaOverlayMarker]; currentMarker == foreignMarker {
		t.Fatalf("independent synthesis loads reused marker %q", foreignMarker)
	}

	document := fmt.Sprintf(`openapi: 3.1.2
info: {title: Encoding header presence, version: "1"}
paths:
  /upload:
    post:
      operationId: upload
      requestBody:
        required: true
        content:
          multipart/form-data:
            schema: {type: object, default: null}
            encoding:
              value:
                headers:
                  X-Meta:
                    schema:
                      type: string
                      example: null
                      %s: %q
      responses:
        "204": {description: no content}
`, schemaOverlayMarker, foreignMarker)
	doc, _, _, err := loadDocumentForSynthesis(context.Background(), nil, "", openbindings.TextContent(document))
	if err != nil {
		t.Fatalf("loadDocumentForSynthesis: %v", err)
	}
	pathItem := doc.Paths.Find("/upload")
	if pathItem == nil || pathItem.Post == nil || pathItem.Post.RequestBody == nil || pathItem.Post.RequestBody.Value == nil {
		t.Fatalf("loaded request body is incomplete: %#v", pathItem)
	}
	media := pathItem.Post.RequestBody.Value.Content["multipart/form-data"]
	header := media.Encoding["value"].Headers["X-Meta"]
	if header == nil || header.Value == nil || header.Value.Schema == nil || header.Value.Schema.Value == nil {
		t.Fatalf("loaded encoding header schema is incomplete: %#v", header)
	}
	extensions := header.Value.Schema.Value.Extensions
	if value, present := extensions["example"]; !present || value != nil {
		t.Errorf("encoding header example = %#v (present %v), want authored null", value, present)
	}
	if value := extensions[schemaOverlayMarker]; value != foreignMarker {
		t.Errorf("encoding header authored marker = %#v, want %q", value, foreignMarker)
	}
	if _, invented := extensions["default"]; invented {
		t.Errorf("encoding header inherited a foreign overlay: %#v", extensions)
	}
}

func assertNoSchemaOverlayMarker(t *testing.T, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"`+schemaOverlayMarker+`":`) {
		t.Fatalf("private schema-overlay marker escaped synthesis: %s", encoded)
	}
}
