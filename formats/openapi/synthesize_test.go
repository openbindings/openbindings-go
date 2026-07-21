package openapi

import (
	"context"
	"errors"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	openbindings "github.com/openbindings/openbindings-go"
)

func minimalDoc() *openapi3.T {
	doc := &openapi3.T{
		OpenAPI: "3.0.3",
		Info: &openapi3.Info{
			Title:       "Test API",
			Version:     "2.0.0",
			Description: "A test API",
		},
		Paths: openapi3.NewPaths(
			openapi3.WithPath("/users", &openapi3.PathItem{
				Get: &openapi3.Operation{
					OperationID: "listUsers",
					Summary:     "List users",
					Responses:   openapi3.NewResponses(),
				},
				Post: &openapi3.Operation{
					OperationID: "createUser",
					Summary:     "Create a user",
					Responses:   openapi3.NewResponses(),
				},
			}),
		),
	}
	return doc
}

func TestConvertDocToInterface_CopiesMetadata(t *testing.T) {
	doc := minimalDoc()
	iface := convertDocToInterface(doc, "https://example.com/openapi.json", nil)

	if iface.Name != "Test API" {
		t.Errorf("Name = %q, want %q", iface.Name, "Test API")
	}
	if iface.Version != "2.0.0" {
		t.Errorf("Version = %q, want %q", iface.Version, "2.0.0")
	}
	if iface.Description != "A test API" {
		t.Errorf("Description = %q, want %q", iface.Description, "A test API")
	}
}

func TestConvertDocToInterface_CreatesOperations(t *testing.T) {
	doc := minimalDoc()
	iface := convertDocToInterface(doc, "", nil)

	if len(iface.Operations) != 2 {
		t.Fatalf("len(Operations) = %d, want 2", len(iface.Operations))
	}
	if _, ok := iface.Operations["listUsers"]; !ok {
		t.Error("missing operation 'listUsers'")
	}
	if _, ok := iface.Operations["createUser"]; !ok {
		t.Error("missing operation 'createUser'")
	}
}

func TestConvertDocToInterface_CreatesBindingsWithRefs(t *testing.T) {
	doc := minimalDoc()
	iface := convertDocToInterface(doc, "", nil)

	if len(iface.Bindings) != 2 {
		t.Fatalf("len(Bindings) = %d, want 2", len(iface.Bindings))
	}

	// Check that bindings have JSON pointer refs
	for key, binding := range iface.Bindings {
		if binding.Ref == "" {
			t.Errorf("binding %q has empty ref", key)
		}
		if binding.Source != DefaultSourceName {
			t.Errorf("binding %q source = %q, want %q", key, binding.Source, DefaultSourceName)
		}
		// The ref should be parseable
		_, _, err := parseRef(binding.Ref)
		if err != nil {
			t.Errorf("binding %q ref %q is not parseable: %v", key, binding.Ref, err)
		}
	}
}

func TestConvertDocToInterface_CreatesSourceEntry(t *testing.T) {
	doc := minimalDoc()
	iface := convertDocToInterface(doc, "https://example.com/openapi.json", nil)

	src, ok := iface.Sources[DefaultSourceName]
	if !ok {
		t.Fatal("missing source entry for DefaultSourceName")
	}
	if src.Location != "https://example.com/openapi.json" {
		t.Errorf("source Location = %q, want %q", src.Location, "https://example.com/openapi.json")
	}
	if src.BindingSpec != BindingSpec {
		t.Errorf("source bindingSpec = %q, want the exact identifier %q", src.BindingSpec, BindingSpec)
	}
}

func TestConvertDocToInterface_NoPaths(t *testing.T) {
	doc := &openapi3.T{
		OpenAPI: "3.0.3",
		Info:    &openapi3.Info{Title: "Empty", Version: "1.0.0"},
	}
	iface := convertDocToInterface(doc, "", nil)

	if len(iface.Operations) != 0 {
		t.Errorf("len(Operations) = %d, want 0", len(iface.Operations))
	}
	if len(iface.Bindings) != 0 {
		t.Errorf("len(Bindings) = %d, want 0", len(iface.Bindings))
	}
}

func TestConvertDocToInterface_DerivesKeyFromOperationId(t *testing.T) {
	doc := &openapi3.T{
		OpenAPI: "3.0.3",
		Info:    &openapi3.Info{Title: "Test", Version: "1.0.0"},
		Paths: openapi3.NewPaths(
			openapi3.WithPath("/pets", &openapi3.PathItem{
				Get: &openapi3.Operation{
					OperationID: "findPets",
					Responses:   openapi3.NewResponses(),
				},
			}),
		),
	}
	iface := convertDocToInterface(doc, "", nil)

	if _, ok := iface.Operations["findPets"]; !ok {
		t.Errorf("expected operation key 'findPets', got keys: %v", keys(iface.Operations))
	}
}

func TestConvertDocToInterface_DerivesKeyFromPathWhenNoOperationId(t *testing.T) {
	doc := &openapi3.T{
		OpenAPI: "3.0.3",
		Info:    &openapi3.Info{Title: "Test", Version: "1.0.0"},
		Paths: openapi3.NewPaths(
			openapi3.WithPath("/pets", &openapi3.PathItem{
				Get: &openapi3.Operation{
					Summary:   "List pets",
					Responses: openapi3.NewResponses(),
				},
			}),
		),
	}
	iface := convertDocToInterface(doc, "", nil)

	// Should derive key from path + method
	if _, ok := iface.Operations["pets.get"]; !ok {
		t.Errorf("expected operation key 'pets.get', got keys: %v", keys(iface.Operations))
	}
}

func keys[V any](m map[string]V) []string {
	result := make([]string, 0, len(m))
	for k := range m {
		result = append(result, k)
	}
	return result
}

func TestConvertDocToInterface_TranslatesNullableIn30(t *testing.T) {
	yaml := []byte(`openapi: 3.0.3
info: { title: P, version: "1.0.0" }
paths:
  /ability:
    get:
      operationId: abilityList
      responses:
        '200':
          description: OK
          content:
            application/json:
              schema:
                type: object
                properties:
                  count: { type: integer }
                  next: { type: string, nullable: true, format: uri }
                  previous: { type: string, nullable: true, format: uri }
                required: [count]
`)
	doc, err := loadDocument("", yaml)
	if err != nil {
		t.Fatalf("loadDocument: %v", err)
	}
	iface := convertDocToInterface(doc, "", nil)

	op, ok := iface.Operations["abilityList"]
	if !ok {
		t.Fatalf("abilityList operation missing")
	}

	props, ok := op.Output.(map[string]any)["properties"].(map[string]any)
	if !ok {
		t.Fatalf("output.properties missing or wrong type: %#v", op.Output)
	}

	next, ok := props["next"].(map[string]any)
	if !ok {
		t.Fatalf("next property missing")
	}
	gotType, ok := next["type"].([]any)
	if !ok {
		t.Fatalf("next.type expected []any, got %#v", next["type"])
	}
	if len(gotType) != 2 || gotType[0] != "string" || gotType[1] != "null" {
		t.Errorf("next.type = %#v, want [\"string\", \"null\"]", gotType)
	}
	if _, hasNullable := next["nullable"]; hasNullable {
		t.Errorf("next.nullable should have been removed, got %#v", next)
	}

	if iface.Sources["openapi"].BindingSpec != BindingSpec {
		t.Errorf("source.bindingSpec = %q, want %q", iface.Sources["openapi"].BindingSpec, BindingSpec)
	}
}

func TestConvertDocToInterface_Preserves31Verbatim(t *testing.T) {
	yaml := []byte(`openapi: 3.1.0
info: { title: T, version: "1.0.0" }
paths:
  /x:
    get:
      operationId: x
      responses:
        '200':
          description: OK
          content:
            application/json:
              schema:
                type: object
                properties:
                  next: { type: [string, "null"], format: uri }
                  legacy: { type: string, nullable: true }
`)
	doc, err := loadDocument("", yaml)
	if err != nil {
		t.Fatalf("loadDocument: %v", err)
	}
	iface := convertDocToInterface(doc, "", nil)

	op := iface.Operations["x"]
	props := op.Output.(map[string]any)["properties"].(map[string]any)

	legacy := props["legacy"].(map[string]any)
	// In 3.1, nullable: true is an inert annotation; we pass it through.
	if legacy["nullable"] != true {
		t.Errorf("legacy.nullable = %#v, want true (3.1 inert annotation should pass through)", legacy["nullable"])
	}
	if legacy["type"] != "string" {
		t.Errorf("legacy.type = %#v, want \"string\"", legacy["type"])
	}
}

func TestConvertDocToInterface_TranslatesExclusiveMinIn30(t *testing.T) {
	yaml := []byte(`openapi: 3.0.3
info: { title: T, version: "1.0.0" }
paths:
  /q:
    get:
      operationId: q
      parameters:
        - name: page
          in: query
          schema:
            type: integer
            minimum: 0
            exclusiveMinimum: true
            maximum: 100
            exclusiveMaximum: false
      responses:
        '200': { description: OK }
`)
	doc, err := loadDocument("", yaml)
	if err != nil {
		t.Fatalf("loadDocument: %v", err)
	}
	iface := convertDocToInterface(doc, "", nil)

	op := iface.Operations["q"]
	props := op.Input.(map[string]any)["properties"].(map[string]any)
	page := props["page"].(map[string]any)

	if _, hasMin := page["minimum"]; hasMin {
		t.Errorf("page.minimum should have been removed, got %#v", page)
	}
	em, ok := page["exclusiveMinimum"].(float64)
	if !ok || em != 0 {
		t.Errorf("page.exclusiveMinimum = %#v, want 0 (numeric)", page["exclusiveMinimum"])
	}
	if max, ok := page["maximum"].(float64); !ok || max != 100 {
		t.Errorf("page.maximum = %#v, want 100", page["maximum"])
	}
	if _, hasExMax := page["exclusiveMaximum"]; hasExMax {
		t.Errorf("page.exclusiveMaximum (false) should have been removed, got %#v", page)
	}
}

// Content-fed synthesis must emit an invocable source: with no location,
// the provided artifact is embedded (a source needs location or content;
// dropping the content would emit neither).
func TestSynthesizeInterface_ContentOnlyEmbedsSource(t *testing.T) {
	content := `{"openapi":"3.0.3","info":{"title":"T","version":"1.0.0"},"paths":{"/x":{"get":{"operationId":"getX","responses":{"200":{"description":"ok"}}}}}}`
	iface, err := NewSynthesizer().SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{BindingSpec: "openapi@3.0", Content: openbindings.TextContent(content)}},
	})
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	src, ok := iface.Sources[DefaultSourceName]
	if !ok {
		t.Fatal("expected the default source entry")
	}
	if src.Location != "" {
		t.Errorf("content-only synthesis must not invent a location, got %q", src.Location)
	}
	if src.Content == nil {
		t.Fatal("content-fed synthesis must embed the artifact")
	}
	if got, err := openbindings.ContentToBytes(src.Content); err != nil || string(got) != content {
		t.Error("embedded content must be the provided artifact verbatim")
	}
}

// Multi-source composition is implementation-defined; a single-source
// synthesizer refuses extras loudly rather than silently using a subset.
func TestSynthesizeInterface_RefusesMultipleSources(t *testing.T) {
	_, err := NewSynthesizer().SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{
			{BindingSpec: "openapi@3.0", Content: openbindings.TextContent("{}")},
			{BindingSpec: "openapi@3.0", Content: openbindings.TextContent("{}")},
		},
	})
	if !errors.Is(err, openbindings.ErrMultipleSources) {
		t.Fatalf("want ErrMultipleSources, got %v", err)
	}
}

// TestSynthesize_ParamBodyCollisionWarns pins the field-collision rule's
// synthesis half: the merge is deterministic (body schema wins) and never
// silent — a SynthesizerWarning names the field and the delivery rule.
func TestSynthesize_ParamBodyCollisionWarns(t *testing.T) {
	spec := `{
	  "openapi": "3.0.0",
	  "info": {"title": "t", "version": "1"},
	  "paths": {
	    "/users/{id}": {
	      "put": {
	        "operationId": "updateUser",
	        "parameters": [{"name": "id", "in": "path", "required": true, "schema": {"type": "string"}}],
	        "requestBody": {"required": true, "content": {"application/json": {"schema": {
	          "type": "object",
	          "properties": {"id": {"type": "string"}, "name": {"type": "string"}},
	          "required": ["id", "name"]
	        }}}},
	        "responses": {"200": {"description": "ok"}}
	      }
	    }
	  }
	}`
	var warnings []openbindings.SynthesizerWarning
	synth := NewSynthesizer()
	iface, err := synth.SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
		Sources:   []openbindings.SynthesizeSource{{Content: openbindings.TextContent(spec)}},
		OnWarning: func(w openbindings.SynthesizerWarning) { warnings = append(warnings, w) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || warnings[0].Code != "openapi.param_body_collision" {
		t.Fatalf("want one param_body_collision warning, got %v", warnings)
	}
	props, _ := iface.Operations["updateUser"].Input.(map[string]any)["properties"].(map[string]any)
	if _, ok := props["id"]; !ok {
		t.Fatalf("flattened input must carry one id field, got %v", props)
	}
}

// TestSynthesize_MediaSchemaMismatchWarns pins the synthesis half of §9.2's
// degenerate media/schema combination rule (OAPI-P-04): when the produced
// contract's only declared request media cannot carry it — multipart or
// urlencoded selected while the body schema does not flatten, text/plain
// selected while it does — synthesis emits openapi.media_schema_mismatch,
// so authors hear it at synthesis time rather than at first dispatch. A
// co-declared JSON media type is selected instead and silences the warning.
func TestSynthesize_MediaSchemaMismatchWarns(t *testing.T) {
	spec := `{
	  "openapi": "3.1.0",
	  "info": {"title": "t", "version": "1"},
	  "paths": {
	    "/scalar-multipart": {
	      "post": {
	        "operationId": "scalarMultipart",
	        "requestBody": {"required": true, "content": {"multipart/form-data": {"schema": {"type": "string"}}}},
	        "responses": {"200": {"description": "ok"}}
	      }
	    },
	    "/object-text": {
	      "post": {
	        "operationId": "objectText",
	        "requestBody": {"required": true, "content": {"text/plain": {"schema": {"type": "object", "properties": {"a": {"type": "string"}}}}}},
	        "responses": {"200": {"description": "ok"}}
	      }
	    },
	    "/fine": {
	      "post": {
	        "operationId": "fine",
	        "requestBody": {"required": true, "content": {
	          "multipart/form-data": {"schema": {"type": "string"}},
	          "application/json": {"schema": {"type": "string"}}
	        }},
	        "responses": {"200": {"description": "ok"}}
	      }
	    }
	  }
	}`
	var warnings []openbindings.SynthesizerWarning
	synth := NewSynthesizer()
	_, err := synth.SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
		Sources:   []openbindings.SynthesizeSource{{Content: openbindings.TextContent(spec)}},
		OnWarning: func(w openbindings.SynthesizerWarning) { warnings = append(warnings, w) },
	})
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]openbindings.SynthesizerWarning{}
	for _, w := range warnings {
		if w.Code == "openapi.media_schema_mismatch" {
			byPath[w.Path] = w
		}
	}
	if len(byPath) != 2 {
		t.Fatalf("want exactly two media_schema_mismatch warnings (the co-declared-JSON operation is fine), got %v", warnings)
	}
	wantMultipart := `request media selection (OAPI-P-04) lands on multipart/form-data, but the declared body schema does not flatten (no properties and no explicit object type): openbindings.openapi@1 defines no request carriage for this combination; a conformant invoker refuses this operation before dispatch`
	if w := byPath["operations.scalarMultipart.input"]; w.Message != wantMultipart {
		t.Errorf("multipart warning = %q, want %q", w.Message, wantMultipart)
	}
	wantText := `request media selection (OAPI-P-04) lands on text/plain, but the declared body schema flattens (an object contract): openbindings.openapi@1 defines no request carriage for this combination; a conformant invoker refuses this operation before dispatch`
	if w := byPath["operations.objectText.input"]; w.Message != wantText {
		t.Errorf("text warning = %q, want %q", w.Message, wantText)
	}
}

// TestSynthesize_TypelessBodyWrapsSynthetic pins the contract half of the
// §9.1 declaration-only object determination: a TYPELESS request-body
// schema — neither `properties` nor an explicit object type — is
// non-object, so the published contract carries it under the synthetic
// `body` property (required when the artifact declares the body required);
// a schema declaring `properties` WITHOUT a type is object by declaration
// and flattens by property name. planRequestBody (media.go) routes the wire
// with the same predicate (bodySchemaFlattens), so contract and wire cannot
// disagree.
func TestSynthesize_TypelessBodyWrapsSynthetic(t *testing.T) {
	spec := `{
	  "openapi": "3.1.0",
	  "info": {"title": "t", "version": "1"},
	  "paths": {
	    "/opaque": {
	      "post": {
	        "operationId": "sendOpaque",
	        "requestBody": {"required": true, "content": {"application/json": {"schema": {"description": "opaque payload"}}}},
	        "responses": {"200": {"description": "ok"}}
	      }
	    },
	    "/named": {
	      "post": {
	        "operationId": "sendNamed",
	        "requestBody": {"required": true, "content": {"application/json": {"schema": {"properties": {"name": {"type": "string"}}}}}},
	        "responses": {"200": {"description": "ok"}}
	      }
	    }
	  }
	}`
	synth := NewSynthesizer()
	iface, err := synth.SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{Content: openbindings.TextContent(spec)}},
	})
	if err != nil {
		t.Fatal(err)
	}

	opaque, _ := iface.Operations["sendOpaque"].Input.(map[string]any)
	opaqueProps, _ := opaque["properties"].(map[string]any)
	if _, ok := opaqueProps["body"]; !ok || len(opaqueProps) != 1 {
		t.Errorf("typeless body must wrap under the synthetic body property alone, got %v", opaqueProps)
	}
	if req := stringSlice(opaque["required"]); len(req) != 1 || req[0] != "body" {
		t.Errorf("required-body artifact must require the synthetic body property, got %v", req)
	}

	named, _ := iface.Operations["sendNamed"].Input.(map[string]any)
	namedProps, _ := named["properties"].(map[string]any)
	if _, ok := namedProps["name"]; !ok {
		t.Errorf("properties-without-type body must flatten by property name, got %v", namedProps)
	}
	if _, ok := namedProps["body"]; ok {
		t.Errorf("properties-without-type body must not wrap synthetically, got %v", namedProps)
	}
}
