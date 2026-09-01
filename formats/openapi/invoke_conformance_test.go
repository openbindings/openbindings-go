package openapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/invoke"
	"github.com/openbindings/openbindings-go/synthesize"
)

// Integration tests driving the current invoker against local httptest
// servers. Historical development-profile cases are not published binding
// specification revisions.

func invokeWith(t *testing.T, spec, selector string, input any) (any, *invoke.InvocationError) {
	return invokeWithContext(t, spec, selector, nil, input)
}

func invokeWithParameterConversion(t *testing.T, spec, selector string, input any) (any, *invoke.InvocationError) {
	t.Helper()
	spec = withDeclaredJSONResponses(t, spec)
	call := NewInvokerWithOptions(RuntimeOptions{ParameterConversion: func(value any) (string, error) {
		return fmt.Sprint(value), nil
	}}).InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
		Source:   invoke.InvocationSource{BindingSpec: bindingSpecForTestDocument(spec), Content: openbindings.TextContent(spec)},
		Selector: selector,
	})
	return driveSingle(t, call, input)
}

// invokeWithParameterConversionAndContext drives one invocation with both a
// converter and binding context, which the propertyMedia probes need: the
// choice is context and the sibling scalar still routes through the converter.
func invokeWithParameterConversionAndContext(t *testing.T, spec, selector string, bindingContext map[string]any, input any) (any, *invoke.InvocationError) {
	t.Helper()
	spec = withDeclaredJSONResponses(t, spec)
	call := NewInvokerWithOptions(RuntimeOptions{ParameterConversion: func(value any) (string, error) {
		return fmt.Sprint(value), nil
	}}).InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
		Source:   invoke.InvocationSource{BindingSpec: bindingSpecForTestDocument(spec), Content: openbindings.TextContent(spec)},
		Selector: selector,
		Context:  bindingContext,
	})
	return driveSingle(t, call, input)
}

func invokeWithContext(t *testing.T, spec, selector string, bindingContext map[string]any, input any) (any, *invoke.InvocationError) {
	t.Helper()
	spec = withDeclaredJSONResponses(t, spec)
	call := NewInvoker().InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
		Source:   invoke.InvocationSource{BindingSpec: bindingSpecForTestDocument(spec), Content: openbindings.TextContent(spec)},
		Selector: selector,
		Context:  bindingContext,
	})
	return driveSingle(t, call, input)
}

func invokeWithBindingSpec(t *testing.T, bindingSpec, spec, selector string, input any) (any, *invoke.InvocationError) {
	t.Helper()
	spec = withDeclaredJSONResponses(t, spec)
	call := NewInvoker().InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
		Source:   invoke.InvocationSource{BindingSpec: bindingSpec, Content: openbindings.TextContent(spec)},
		Selector: selector,
	})
	return driveSingle(t, call, input)
}

// Most routing fixtures predate OAPI-P-07 and are not response-negotiation
// tests. Give their otherwise-minimal Response Objects the concrete JSON
// declaration their fake server already emits; fixtures with explicit
// content remain untouched.
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

// ---------------------------------------------------------------------------
// OAPI-D-03 — selector shape at the invoke boundary
// ---------------------------------------------------------------------------

// An uppercase selector method is non-conformant: refused with ERR_INVALID_SELECTOR,
// never case-folded to a match.
func TestInvoke_UppercaseRefMethodRefused(t *testing.T) {
	srv, requests := countingServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	_, ierr := invokeWith(t, widgetSpec(srv.URL), "#/paths/~1session/GET", nil)
	if ierr == nil || ierr.Code != invoke.ErrCodeInvalidSelector {
		t.Fatalf("expected ERR_INVALID_SELECTOR for an uppercase method, got %v", ierr)
	}
	if requests.Load() != 0 {
		t.Error("refusal must precede dispatch")
	}
}

// A path item that is a $ref (3.1 components.pathItems) resolves before the
// method segment evaluates (OAPI-D-03: OAS reference resolution, not raw
// JSON traversal).
func TestInvoke_SelectorResolvesPathItemRef(t *testing.T) {
	var gotPath string
	srv, _ := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	spec := fmt.Sprintf(`{
	  "openapi": "3.1.0",
	  "info": {"title": "t", "version": "1"},
	  "servers": [{"url": %q}],
	  "paths": {"/shared": {"$ref": "#/components/pathItems/Shared"}},
	  "components": {"pathItems": {"Shared": {
	    "get": {"operationId": "sharedGet", "responses": {"200": {"description": "ok"}}}
	  }}}
	}`, srv.URL)

	out, ierr := invokeWith(t, spec, "#/paths/~1shared/get", nil)
	if ierr != nil {
		t.Fatalf("invoke: %s: %s", ierr.Code, ierr.Error())
	}
	if m, _ := out.(map[string]any); m["ok"] != true {
		t.Errorf("out = %v", out)
	}
	if gotPath != "/shared" {
		t.Errorf("path = %q", gotPath)
	}
}

// ---------------------------------------------------------------------------
// OAPI-P-01 / §3 / §6 — accepted editions, duplicate keys, self-containment
// ---------------------------------------------------------------------------

func TestLoadDocument_VersionDiscrimination(t *testing.T) {
	for _, v := range []string{"3.0.0", "3.0.1", "3.0.2", "3.0.3", "3.0.4", "3.1.0", "3.1.1", "3.1.2"} {
		doc, err := loadDocument("", openbindings.TextContent(fmt.Sprintf(`{"openapi": %q, "info": {"title": "t", "version": "1"}, "paths": {}}`, v)))
		if err != nil || doc == nil {
			t.Errorf("version %s must load, got %v", v, err)
		}
	}

	rejected := []string{
		`{"swagger": "2.0", "info": {"title": "t", "version": "1"}, "paths": {}}`,
		`{"openapi": "3.0.5", "info": {"title": "t", "version": "1"}, "paths": {}}`,
		`{"openapi": "3.1.3", "info": {"title": "t", "version": "1"}, "paths": {}}`,
		`{"openapi": "3.2.0", "info": {"title": "t", "version": "1"}, "paths": {}}`,
	}
	for _, content := range rejected {
		_, err := loadDocument("", openbindings.TextContent(content))
		if err == nil {
			t.Errorf("loadDocument(%s) = %v, want loud family-specific edition refusal", content, err)
		}
	}
}

// §3: duplicate mapping keys in string content are refused loudly (the YAML
// 1.2 layer enforces this).
func TestLoadDocument_DuplicateYAMLKeysRefused(t *testing.T) {
	content := "openapi: 3.0.3\ninfo: {title: t, version: '1'}\npaths:\n  /a:\n    get:\n      operationId: one\n      responses: {'200': {description: ok}}\n  /a:\n    post:\n      operationId: two\n      responses: {'200': {description: ok}}\n"
	if _, err := loadDocument("", openbindings.TextContent(content)); err == nil {
		t.Fatal("duplicate mapping keys must refuse loudly (OAPI-P-01/§3)")
	}
}

// §6: embedded content with no co-present location must be self-contained; a
// relative external $ref fails with a readable error (and never a silent
// working-directory file read).
func TestLoadDocument_NoLocationRelativeRefReadableError(t *testing.T) {
	content := `{"openapi": "3.0.3", "info": {"title": "t", "version": "1"},
	  "paths": {"/a": {"get": {"operationId": "x", "responses": {"200": {"description": "ok",
	    "content": {"application/json": {"schema": {"$ref": "shared.json#/Thing"}}}}}}}}}`
	_, err := loadDocument("", openbindings.TextContent(content))
	if err == nil {
		t.Fatal("a relative external $ref with no base URI must fail")
	}
	if !strings.Contains(err.Error(), "self-contained") {
		t.Errorf("error should explain the self-containment requirement, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// OAPI-P-03 — flattened-model refusals at the invoke boundary
// ---------------------------------------------------------------------------

func TestInvoke_CaseFoldingHeaderCollisionRefused(t *testing.T) {
	srv, requests := countingServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	spec := fmt.Sprintf(`{
	  "openapi": "3.0.3", "info": {"title": "t", "version": "1"},
	  "servers": [{"url": %q}],
	  "paths": {"/items": {"get": {
	    "operationId": "get",
	    "parameters": [
	      {"name": "X-ID", "in": "header", "schema": {"type": "string"}},
	      {"name": "x-id", "in": "header", "schema": {"type": "string"}}
	    ],
	    "responses": {"200": {"description": "ok"}}
	  }}}
	}`, srv.URL)
	_, ierr := invokeWithBindingSpec(t, bindingSpecForTestDocument(spec), spec, "#/paths/~1items/get", map[string]any{})
	if ierr == nil || ierr.Code != invoke.ErrCodeRefused {
		t.Fatalf("expected the unflattenable refusal, got %v", ierr)
	}
	if ierr.HasData() {
		t.Errorf("flattening diagnostics crossed as abstract data: %#v", ierr.Data)
	}
	if requests.Load() != 0 {
		t.Error("refusal must precede dispatch")
	}
}

// A field matching no declared parameter is refused pre-dispatch when the
// operation declares no request body — loud, naming the offenders.
func TestInvoke_UnmatchedFieldsRefusedWithoutBody(t *testing.T) {
	srv, requests := countingServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	_, ierr := invokeWith(t, widgetSpec(srv.URL), "#/paths/~1session/get", map[string]any{"parameters": map[string]any{
		"session_id": "s", "bogus": 1,
	}})
	if ierr == nil || ierr.Code != invoke.ErrCodeRefused {
		t.Fatalf("expected ERR_VALIDATION_FAILED, got %v", ierr)
	}
	if ierr.HasData() {
		t.Errorf("validation diagnostics crossed as abstract data: %#v", ierr.Data)
	}
	if requests.Load() != 0 {
		t.Error("refusal must precede dispatch")
	}
}

// §9.1 (OAPI-P-03): with a NON-OBJECT declared request body, the flattened
// contract carries only parameters and the synthetic `body` property — an
// input field matching neither has no destination and refuses pre-dispatch,
// loudly, naming the unroutable field (same species of refusal as the
// no-body unmatched case above).
func TestInvoke_UnmatchedFieldRefusedForNonObjectBody(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"string JSON body", `{"application/json": {"schema": {"type": "string"}}}`},
		{"array JSON body", `{"application/json": {"schema": {"type": "array", "items": {"type": "integer"}}}}`},
		{"binary-in-JSON body (3.1 contentEncoding)", `{"application/json": {"schema": {"type": "string", "contentEncoding": "base64"}}}`},
		{"text/plain body", `{"text/plain": {"schema": {"type": "string"}}}`},
		// §9.1's object determination is by declaration alone: a TYPELESS
		// schema — neither `properties` nor an explicit object type — is
		// non-object, so the refusal fires for it exactly as for arrays
		// and scalars.
		{"typeless JSON body (bare schema)", `{"application/json": {"schema": {}}}`},
		{"typeless JSON body (description-only schema)", `{"application/json": {"schema": {"description": "opaque payload"}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, requests := countingServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
			spec := fmt.Sprintf(`{
			  "openapi": "3.1.0", "info": {"title": "t", "version": "1"},
			  "servers": [{"url": %q}],
			  "paths": {"/echo": {"post": {
			    "operationId": "echo",
			    "requestBody": {"required": true, "content": %s},
			    "responses": {"200": {"description": "ok"}}
			  }}}
			}`, srv.URL, tc.content)
			_, ierr := invokeWith(t, spec, "#/paths/~1echo/post", map[string]any{
				"parameters": map[string]any{"stray": 1},
				"body":       "x",
			})
			if ierr == nil || ierr.Code != invoke.ErrCodeRefused {
				t.Fatalf("expected ERR_VALIDATION_FAILED, got %v", ierr)
			}
			if ierr.HasData() {
				t.Errorf("routing diagnostics crossed as abstract data: %#v", ierr.Data)
			}
			if requests.Load() != 0 {
				t.Error("refusal must precede dispatch")
			}
		})
	}
}

// §9.1 (OAPI-P-03): a TYPELESS request-body schema — declaring neither
// `properties` nor an explicit object type; a bare {} or a description-only
// schema — is non-object by declaration alone, so the flattened contract
// carries the synthetic `body` property and, at the wire, that property's
// value IS the request body, unwrapped. The published contract and the
// invoker share one determination (bodySchemaFlattens): a caller following
// the contract must never see its value double-wrapped as {"body": X}.
func TestInvoke_TypelessBodyRidesSyntheticBodyUnwrapped(t *testing.T) {
	cases := []struct {
		name   string
		schema string
	}{
		{"bare schema", `{}`},
		{"description-only schema", `{"description": "opaque payload"}`},
		// A 3.1 two-element type array is not an EXPLICIT object type
		// (only the single-element form is): synthetic, like typeless.
		{"nullable object without properties", `{"type": ["object", "null"]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody []byte
			srv, _ := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
				gotBody, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{}`))
			})
			spec := fmt.Sprintf(`{
			  "openapi": "3.1.0", "info": {"title": "t", "version": "1"},
			  "servers": [{"url": %q}],
			  "paths": {"/echo": {"post": {
			    "operationId": "echo",
			    "requestBody": {"required": true, "content": {"application/json": {"schema": %s}}},
			    "responses": {"200": {"description": "ok"}}
			  }}}
			}`, srv.URL, tc.schema)
			_, ierr := invokeWith(t, spec, "#/paths/~1echo/post", map[string]any{"body": map[string]any{"k": "v"}})
			if ierr != nil {
				t.Fatalf("invoke: %s: %s", ierr.Code, ierr.Error())
			}
			if string(gotBody) != `{"k":"v"}` {
				t.Errorf("wire body = %s, want the synthetic body property's value unwrapped: {\"k\":\"v\"}", gotBody)
			}
		})
	}
}

// §9.1 (OAPI-P-03): the other half of the declaration-only determination —
// a schema declaring `properties` WITHOUT a type is object by declaration,
// so it flattens by property name exactly as the synthesized contract
// publishes it; the fix for the typeless case must not overshoot into
// wrapping properties-carrying schemas.
func TestInvoke_PropertiesWithoutTypeStillFlattens(t *testing.T) {
	var gotBody []byte
	srv, _ := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	spec := fmt.Sprintf(`{
	  "openapi": "3.1.0", "info": {"title": "t", "version": "1"},
	  "servers": [{"url": %q}],
	  "paths": {"/w": {"post": {
	    "operationId": "makeW",
	    "requestBody": {"required": true, "content": {"application/json": {"schema": {
	      "properties": {"name": {"type": "string"}}
	    }}}},
	    "responses": {"200": {"description": "ok"}}
	  }}}
	}`, srv.URL)
	_, ierr := invokeWith(t, spec, "#/paths/~1w/post", map[string]any{"body": map[string]any{"name": "x"}})
	if ierr != nil {
		t.Fatalf("invoke: %s: %s", ierr.Code, ierr.Error())
	}
	if string(gotBody) != `{"name":"x"}` {
		t.Errorf("wire body = %s, want the flattened property carried by name: {\"name\":\"x\"}", gotBody)
	}
}

func TestInvoke_EnvelopeShapedApplicationBodyRemainsApplicationData(t *testing.T) {
	var got map[string]any
	srv, _ := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	spec := fmt.Sprintf(`{
	  "openapi": "3.1.0",
	  "info": {"title": "t", "version": "1"},
	  "servers": [{"url": %q}],
	  "paths": {"/objects": {"post": {
	    "requestBody": {"required": true, "content": {"application/json": {"schema": {
	      "type": "object",
	      "properties": {
	        "$openbindings": {"type": "string"},
	        "value": {"type": "object"},
	        "parameters": {"type": "array"},
	        "body": {"type": "object"}
	      }
	    }}}},
	    "responses": {"200": {"description": "ok"}}
	  }}}
	}`, srv.URL)
	applicationBody := map[string]any{
		"$openbindings": BindingSpec,
		"value":         map[string]any{"application": true},
		"parameters":    []any{},
		"body":          map[string]any{"application": true},
	}
	input := map[string]any{"body": applicationBody}

	if _, ierr := invokeWith(t, spec, "#/paths/~1objects/post", input); ierr != nil {
		t.Fatalf("invoke: %s: %s", ierr.Code, ierr.Error())
	}
	if !reflect.DeepEqual(got, applicationBody) {
		t.Fatalf("request body = %#v, want application input %#v", got, applicationBody)
	}
}

func TestInvoke_CallerEnvelopeCarriesJSONBodyWithoutSchemaValidation(t *testing.T) {
	var bodies []string
	srv, _ := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	spec := fmt.Sprintf(`{
	  "openapi": "3.1.2", "info": {"title": "t", "version": "1"},
	  "servers": [{"url": %q}],
	  "paths": {"/objects": {"post": {
	    "operationId": "postObject",
	    "requestBody": {"required": true, "content": {"application/json": {"schema": {
	      "type": "object", "properties": {"name": {"type": "string"}}, "required": ["name"]
	    }}}},
	    "responses": {"200": {"description": "ok", "content": {"application/json": {"schema": {"type": "object"}}}}}
	  }}}
	}`, srv.URL)
	if _, err := loadDocumentForBindingSpec("", openbindings.TextContent(spec), BindingSpecOpenAPI31); err != nil {
		t.Fatalf("test artifact: %v", err)
	}
	for _, value := range []any{nil, "scalar"} {
		if _, invocationErr := invokeWith(t, spec, "#/paths/~1objects/post", map[string]any{"body": value}); invocationErr != nil {
			t.Fatalf("body %#v was validated instead of carried: %v", value, invocationErr)
		}
	}
	if want := []string{"null", `"scalar"`}; !reflect.DeepEqual(bodies, want) {
		t.Fatalf("wire bodies = %#v, want %#v", bodies, want)
	}
}

// §9.1 (OAPI-P-03): with an OBJECT body, a field matching no declared
// parameter or body property joins the body value BEFORE encoding selection
// and rides whatever encoding the body rides — JSON here, exactly like
// declared fields.
func TestInvoke_PassthroughRidesJSONBodyEncoding(t *testing.T) {
	var gotCT string
	var gotBody []byte
	srv, _ := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	spec := fmt.Sprintf(`{
	  "openapi": "3.0.3", "info": {"title": "t", "version": "1"},
	  "servers": [{"url": %q}],
	  "paths": {"/w": {"post": {
	    "operationId": "makeW",
	    "requestBody": {"required": true, "content": {"application/json": {"schema": {
	      "type": "object", "properties": {"name": {"type": "string"}}
	    }}}},
	    "responses": {"200": {"description": "ok"}}
	  }}}
	}`, srv.URL)
	_, ierr := invokeWith(t, spec, "#/paths/~1w/post", map[string]any{"body": map[string]any{"name": "x", "extra": "y"}})
	if ierr != nil {
		t.Fatalf("invoke: %s: %s", ierr.Code, ierr.Error())
	}
	if gotCT != "application/json" {
		t.Errorf("Content-Type = %q", gotCT)
	}
	if string(gotBody) != `{"extra":"y","name":"x"}` {
		t.Errorf("body = %s, want the passthrough field joined into the JSON body", gotBody)
	}
}

// Multipart members need declaration-defined part carriage. Unknown members
// fail closed instead of inheriting an invented codec.
func TestInvoke_UndeclaredMultipartMembersRefused(t *testing.T) {
	srv, requests := countingServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	spec := fmt.Sprintf(`{
	  "openapi": "3.0.3", "info": {"title": "t", "version": "1"},
	  "servers": [{"url": %q}],
	  "paths": {"/upload": {"post": {
	    "operationId": "upload",
	    "requestBody": {"required": true, "content": {"multipart/form-data": {"schema": {
	      "type": "object", "properties": {"description": {"type": "string"}}
	    }}}},
	    "responses": {"200": {"description": "ok"}}
	  }}}
	}`, srv.URL)
	_, ierr := invokeWithBindingSpec(t, bindingSpecForTestDocument(spec), spec, "#/paths/~1upload/post", map[string]any{"body": map[string]any{
		"description": "d", "note": "urgent", "meta": map[string]any{"k": "v"},
	}})
	if ierr == nil || ierr.Code != invoke.ErrCodeRefused {
		t.Fatalf("expected declaration-defined multipart refusal, got %v", ierr)
	}
	if ierr.HasData() {
		t.Fatalf("multipart routing evidence crossed as abstract data: %#v", ierr.Data)
	}
	if requests.Load() != 0 {
		t.Fatal("multipart refusal must precede dispatch")
	}
}

// Form-urlencoded members likewise need declared serialization.
func TestInvoke_UndeclaredURLEncodedMembersRefused(t *testing.T) {
	srv, requests := countingServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	spec := fmt.Sprintf(`{
	  "openapi": "3.0.3", "info": {"title": "t", "version": "1"},
	  "servers": [{"url": %q}],
	  "paths": {"/form": {"post": {
	    "operationId": "postForm",
	    "requestBody": {"required": true, "content": {"application/x-www-form-urlencoded": {"schema": {
	      "type": "object", "properties": {"name": {"type": "string"}}
	    }}}},
	    "responses": {"200": {"description": "ok"}}
	  }}}
	}`, srv.URL)
	_, ierr := invokeWithBindingSpec(t, bindingSpecForTestDocument(spec), spec, "#/paths/~1form/post", map[string]any{"body": map[string]any{"name": "a b", "extra": "y"}})
	if ierr == nil || ierr.Code != invoke.ErrCodeRefused {
		t.Fatalf("expected declaration-defined urlencoded refusal, got %v", ierr)
	}
	if ierr.HasData() {
		t.Fatalf("urlencoded routing evidence crossed as abstract data: %#v", ierr.Data)
	}
	if requests.Load() != 0 {
		t.Fatal("urlencoded refusal must precede dispatch")
	}
}

// A supplied input missing a declared path parameter always refuses before
// dispatch (§9.1); other missing required members are the server's business.
func TestInvoke_SuppliedInputMissingPathParamRefuses(t *testing.T) {
	srv, requests := countingServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	spec := fmt.Sprintf(`{
	  "openapi": "3.0.3", "info": {"title": "t", "version": "1"},
	  "servers": [{"url": %q}],
	  "paths": {"/w/{id}": {"post": {
	    "operationId": "makeW",
	    "parameters": [{"name": "id", "in": "path", "required": true, "schema": {"type": "string"}}],
	    "requestBody": {"content": {"application/json": {"schema": {"type": "object", "properties": {"name": {"type": "string"}}, "required": ["name"]}}}},
	    "responses": {"200": {"description": "ok"}}
	  }}}
	}`, srv.URL)
	_, ierr := invokeWith(t, spec, "#/paths/~1w~1{id}/post", map[string]any{"body": map[string]any{"name": "x"}})
	if ierr == nil || ierr.Code != invoke.ErrCodeRefused {
		t.Fatalf("expected ERR_REFUSED for the unfilled path template, got %v", ierr)
	}
	if requests.Load() != 0 {
		t.Error("refusal must precede dispatch")
	}

	// A supplied input missing a required BODY member is sent as-is.
	var gotBody []byte
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv2.Close()
	spec2 := strings.ReplaceAll(spec, srv.URL, srv2.URL)
	if _, ierr := invokeWith(t, spec2, "#/paths/~1w~1{id}/post", map[string]any{"parameters": map[string]any{"id": "7"}}); ierr != nil {
		t.Fatalf("missing required body member must be the server's business, got %v", ierr)
	}
	if string(gotBody) != "" {
		t.Errorf("no body fields and a non-required requestBody: body must be omitted, got %q", gotBody)
	}
}

// Style/explode serialization over the wire: an exploded form array repeats
// the parameter; deepObject brackets its members.
func TestInvoke_QueryStyleSerializationOnTheWire(t *testing.T) {
	var gotRawQuery string
	srv, _ := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotRawQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	spec := fmt.Sprintf(`{
	  "openapi": "3.0.3", "info": {"title": "t", "version": "1"},
	  "servers": [{"url": %q}],
	  "paths": {"/search": {"get": {
	    "operationId": "search",
	    "parameters": [
	      {"name": "tags", "in": "query", "schema": {"type": "array", "items": {"type": "string"}}},
	      {"name": "flat", "in": "query", "style": "form", "explode": false, "schema": {"type": "array", "items": {"type": "string"}}},
	      {"name": "filter", "in": "query", "style": "deepObject", "explode": true, "schema": {"type": "object"}}
	    ],
	    "responses": {"200": {"description": "ok"}}
	  }}}
	}`, srv.URL)
	_, ierr := invokeWithParameterConversion(t, spec, "#/paths/~1search/get", map[string]any{"parameters": map[string]any{
		"tags":   []any{"a", "b"},
		"flat":   []any{"x", "y"},
		"filter": map[string]any{"kind": "big", "size": float64(2)},
	}})
	if ierr != nil {
		t.Fatalf("invoke: %s: %s", ierr.Code, ierr.Error())
	}
	// Declaration order: tags (form explode default), flat, filter.
	want := "tags=a&tags=b&flat=x,y&filter[kind]=big&filter[size]=2"
	if gotRawQuery != want {
		t.Errorf("raw query = %q, want %q", gotRawQuery, want)
	}
}

// Matrix/label path styles substitute their full expansions into the
// template.
func TestInvoke_PathStyleSerializationOnTheWire(t *testing.T) {
	var gotURI string
	srv, _ := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotURI = r.URL.RequestURI()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	spec := fmt.Sprintf(`{
	  "openapi": "3.0.3", "info": {"title": "t", "version": "1"},
	  "servers": [{"url": %q}],
	  "paths": {"/map/{coords}": {"get": {
	    "operationId": "map",
	    "parameters": [
	      {"name": "coords", "in": "path", "required": true, "style": "matrix", "explode": false, "schema": {"type": "array", "items": {"type": "number"}}}
	    ],
	    "responses": {"200": {"description": "ok"}}
	  }}}
	}`, srv.URL)
	_, ierr := invokeWithParameterConversion(t, spec, "#/paths/~1map~1{coords}/get", map[string]any{"parameters": map[string]any{
		"coords": []any{float64(50.4), float64(4.32)},
	}})
	if ierr != nil {
		t.Fatalf("invoke: %s: %s", ierr.Code, ierr.Error())
	}
	if gotURI != "/map/;coords=50.4,4.32" {
		t.Errorf("URI = %q, want /map/;coords=50.4,4.32", gotURI)
	}
}

// ---------------------------------------------------------------------------
// OAPI-P-04 — media selection and bodies on the wire
// ---------------------------------------------------------------------------

// The lexicographically least +json type is selected when exact
// application/json is absent, and the caller's requestMedia choice selects
// the concrete +json declaration that rides as the request Content-Type.
func TestInvoke_PlusJSONSelected(t *testing.T) {
	var gotCT string
	var gotBody []byte
	srv, _ := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	})
	spec := fmt.Sprintf(`{
	  "openapi": "3.0.3", "info": {"title": "t", "version": "1"},
	  "servers": [{"url": %q}],
	  "paths": {"/things": {"post": {
	    "operationId": "makeThing",
	    "requestBody": {"required": true, "content": {
	      "application/vnd.b+json": {"schema": {"type": "object"}},
	      "application/vnd.a+json": {"schema": {"type": "object"}}
	    }},
	    "responses": {"201": {"description": "ok", "content": {"application/json": {"schema": {}}}}}
	  }}}
	}`, srv.URL)
	_, ierr := invokeWithContext(t, spec, "#/paths/~1things/post", map[string]any{
		"configuration": map[string]any{"requestMedia": "application/vnd.a+json"},
	}, map[string]any{"body": map[string]any{"k": "v"}})
	if ierr != nil {
		t.Fatalf("invoke: %s: %s", ierr.Code, ierr.Error())
	}
	if gotCT != "application/vnd.a+json" {
		t.Errorf("Content-Type = %q, want the configured +json type", gotCT)
	}
	if string(gotBody) != `{"k":"v"}` {
		t.Errorf("body = %s", gotBody)
	}
}

func TestInvoke_BinaryOnlyBodyCarriesExactOctets(t *testing.T) {
	var gotBody []byte
	srv, requests := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	spec := fmt.Sprintf(`{
	  "openapi": "3.0.3", "info": {"title": "t", "version": "1"},
	  "servers": [{"url": %q}],
	  "paths": {"/blob": {"post": {
	    "operationId": "putBlob",
	    "requestBody": {"required": true, "content": {"application/octet-stream": {"schema": {"type": "string", "format": "binary"}}}},
	    "responses": {"200": {"description": "ok"}}
	  }}}
	}`, srv.URL)
	_, ierr := invokeWithBindingSpec(t, bindingSpecForTestDocument(spec), spec, "#/paths/~1blob/post", map[string]any{"body": "AAEC"})
	if ierr != nil {
		t.Fatalf("binary invocation failed: %v", ierr)
	}
	if requests.Load() != 1 || !reflect.DeepEqual(gotBody, []byte{0, 1, 2}) {
		t.Fatalf("binary dispatch count/body = %d/%v", requests.Load(), gotBody)
	}
}

// §9.2 (OAPI-P-04): a degenerate media/schema combination — selection
// landing on multipart/form-data or application/x-www-form-urlencoded
// while the declared body schema does not flatten (§9.1's declaration-only
// determination: no properties and no explicit object type), or on
// text/plain while it does — has no OAS-defined wire form and refuses
// pre-dispatch rather than inventing carriage.
func TestInvoke_DegenerateMediaSchemaCombinationRefused(t *testing.T) {
	cases := []struct {
		name    string
		content string
		input   map[string]any
	}{
		{
			"multipart-only with a scalar schema",
			`{"multipart/form-data": {"schema": {"type": "string"}}}`,
			map[string]any{"body": "x"},
		},
		{
			// §9.1's determination is declaration-only: a TYPELESS schema
			// (neither `properties` nor an explicit object type) does not
			// flatten, so the refusal fires for it exactly as for scalars.
			"multipart-only with a typeless schema",
			`{"multipart/form-data": {"schema": {"description": "opaque"}}}`,
			map[string]any{"body": "x"},
		},
		{
			"urlencoded-only with a scalar schema",
			`{"application/x-www-form-urlencoded": {"schema": {"type": "integer"}}}`,
			map[string]any{"body": float64(1)},
		},
		{
			"text-only with an object schema",
			`{"text/plain": {"schema": {"type": "object", "properties": {"a": {"type": "string"}}}}}`,
			map[string]any{"body": map[string]any{"a": "x"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, requests := countingServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
			spec := fmt.Sprintf(`{
			  "openapi": "3.1.0", "info": {"title": "t", "version": "1"},
			  "servers": [{"url": %q}],
			  "paths": {"/op": {"post": {
			    "operationId": "op",
			    "requestBody": {"required": true, "content": %s},
			    "responses": {"200": {"description": "ok"}}
			  }}}
			}`, srv.URL, tc.content)
			_, ierr := invokeWith(t, spec, "#/paths/~1op/post", tc.input)
			if ierr == nil || ierr.Code != invoke.ErrCodeSourceConfigError {
				t.Fatalf("expected the degenerate-combination refusal, got %v", ierr)
			}
			if ierr.HasData() {
				t.Errorf("media-planning diagnostics crossed as abstract data: %#v", ierr.Data)
			}
			if requests.Load() != 0 {
				t.Error("refusal must precede dispatch")
			}
		})
	}
}

// The degenerate-combination refusal reaches only artifacts declaring NO
// JSON-family request media: a caller-selected co-declared JSON media type
// carries the same scalar shape with no refusal.
func TestInvoke_DegenerateCombinationUnreachableWithJSONCoDeclared(t *testing.T) {
	var gotCT string
	var gotBody []byte
	srv, requests := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	spec := fmt.Sprintf(`{
	  "openapi": "3.1.0", "info": {"title": "t", "version": "1"},
	  "servers": [{"url": %q}],
	  "paths": {"/op": {"post": {
	    "operationId": "op",
	    "requestBody": {"required": true, "content": {
	      "multipart/form-data": {"schema": {"type": "string"}},
	      "application/json": {"schema": {"type": "string"}}
	    }},
	    "responses": {"200": {"description": "ok"}}
	  }}}
	}`, srv.URL)
	_, ierr := invokeWithContext(t, spec, "#/paths/~1op/post", map[string]any{
		"configuration": map[string]any{"requestMedia": "application/json"},
	}, map[string]any{"body": "x"})
	if ierr != nil {
		t.Fatalf("invoke: %s: %s", ierr.Code, ierr.Error())
	}
	if gotCT != "application/json" || string(gotBody) != `"x"` {
		t.Errorf("request = (%q, %q), want application/json with the synthetic body %q", gotCT, gotBody, `"x"`)
	}
	if requests.Load() != 1 {
		t.Errorf("requests = %d, want 1", requests.Load())
	}
}

// urlencoded selection serializes fields per the OAS encoding rules. This
// artifact writes no Encoding Object, so every accepted edition puts both
// properties on the CONTENT path: `name` takes the string row's text/plain and
// rides as-is with its space spelled `+` per RFC 1866 section 8.2.1, and `ids`
// takes the array row and rides as one field carrying its JSON image. Before
// 2026-08-17 a legacyOpenAPIFormEncoding predicate put a 3.0.3 artifact on the
// RFC6570-style path instead and this test expected `ids=1&ids=2&name=a%20st`;
// see design/openapi-30-urlencoded-default-lane-ruling.md. The array's
// application/json default is the engines' own convention on an open cell
// (corpus-lab/OPENAPI-RUNTIME.md, "The array-items part default"), pinned here
// as observed rather than claimed as authority.
func TestInvoke_URLEncodedBodyOnTheWire(t *testing.T) {
	var gotCT string
	var gotBody []byte
	srv, _ := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	spec := fmt.Sprintf(`{
	  "openapi": "3.0.3", "info": {"title": "t", "version": "1"},
	  "servers": [{"url": %q}],
	  "paths": {"/form": {"post": {
	    "operationId": "postForm",
	    "requestBody": {"required": true, "content": {"application/x-www-form-urlencoded": {"schema": {
	      "type": "object",
	      "properties": {"name": {"type": "string"}, "ids": {"type": "array", "items": {"type": "integer"}}}
	    }}}},
	    "responses": {"200": {"description": "ok"}}
	  }}}
	}`, srv.URL)
	input := map[string]any{"body": map[string]any{
		"name": "a b", "ids": []any{float64(1), float64(2)},
	}}

	// R4 (ratified 2026-09-01): `ids` is an array of integers on the CONTENT
	// lane, so it rides one field as a whole compound and its item-type
	// default is text/plain, under which no accepted edition defines an
	// array's bytes. With no `propertyMedia` choice the invocation refuses
	// before dispatch as the context-required species. Before R4 this emitted
	// `ids=%5B%221%22%2C%222%22%5D` -- container JSON no edition selected,
	// with the integers stringified on the way through.
	if _, ierr := invokeWithParameterConversion(t, spec, "#/paths/~1form/post", input); ierr == nil {
		t.Fatal("invoke without a propertyMedia choice succeeded, want the context-required refusal")
	} else if ierr.Code != "CONTEXT_REQUIRED" {
		t.Fatalf("invoke: %s: %s", ierr.Code, ierr.Error())
	}

	// With the choice supplied the operation dispatches, and the chosen lane
	// -- not a container default -- decides the bytes.
	_, ierr := invokeWithParameterConversionAndContext(t, spec, "#/paths/~1form/post", map[string]any{
		"configuration": map[string]any{"propertyMedia": map[string]any{"ids": "application/json"}},
	}, input)
	if ierr != nil {
		t.Fatalf("invoke: %s: %#v", ierr.Code, ierr)
	}
	if gotCT != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type = %q", gotCT)
	}
	// The array's items reach the wire as `["1","2"]`, not `[1,2]`: this
	// adapter runs the parameterConversion converter over them before the
	// engine sees the value. Given the same document and input the engine
	// alone emits `ids=%5B1%2C2%5D`. Section 9.3's pin says the converter is
	// "scoped to the `schema`-form and RFC 6570-style paths", which the
	// content lane is not, so the adapter is the side that looks wrong -- but
	// the behavior predates R4 and is unchanged by it, so this assertion
	// records what ships today rather than asserting a fix this change did not
	// make. Queued separately.
	if string(gotBody) != "ids=%5B%221%22%2C%222%22%5D&name=a+b" {
		t.Errorf("body = %q", gotBody)
	}
}

// Synthetic body unwrap on the wire: with an array body schema, the caller's
// `body` field IS the request body.
func TestInvoke_SyntheticBodyUnwrapOnTheWire(t *testing.T) {
	var gotBody []byte
	srv, _ := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	spec := fmt.Sprintf(`{
	  "openapi": "3.0.3", "info": {"title": "t", "version": "1"},
	  "servers": [{"url": %q}],
	  "paths": {"/batch": {"post": {
	    "operationId": "batch",
	    "requestBody": {"required": true, "content": {"application/json": {"schema": {"type": "array", "items": {"type": "integer"}}}}},
	    "responses": {"200": {"description": "ok"}}
	  }}}
	}`, srv.URL)
	_, ierr := invokeWith(t, spec, "#/paths/~1batch/post", map[string]any{"body": []any{float64(1), float64(2)}})
	if ierr != nil {
		t.Fatalf("invoke: %s: %s", ierr.Code, ierr.Error())
	}
	if string(gotBody) != "[1,2]" {
		t.Errorf("wire body = %q, want the unwrapped array [1,2]", gotBody)
	}
}

// text/plain selection: a string body rides verbatim and the declared
// response media governs decoding without producing an Accept header.
func TestInvoke_TextPlainBody(t *testing.T) {
	var gotCT string
	var gotBody []byte
	srv, _ := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "pong")
	})
	spec := fmt.Sprintf(`{
	  "openapi": "3.0.3", "info": {"title": "t", "version": "1"},
	  "servers": [{"url": %q}],
	  "paths": {"/echo": {"post": {
	    "operationId": "echo",
	    "requestBody": {"required": true, "content": {"text/plain": {"schema": {"type": "string"}}}},
	    "responses": {"200": {"description": "ok", "content": {"text/plain": {"schema": {"type": "string"}}}}}
	  }}}
	}`, srv.URL)
	out, ierr := invokeWith(t, spec, "#/paths/~1echo/post", map[string]any{"body": "ping"})
	if ierr != nil {
		t.Fatalf("invoke: %s: %s", ierr.Code, ierr.Error())
	}
	if gotCT != "text/plain" || string(gotBody) != "ping" {
		t.Errorf("request = (%q, %q), want text/plain ping", gotCT, gotBody)
	}
	if out != "pong" {
		t.Errorf("out = %v, want the text lane's string", out)
	}

	// The selection condition: a non-string body value refuses pre-dispatch.
	_, ierr = invokeWith(t, spec, "#/paths/~1echo/post", map[string]any{"body": float64(1)})
	if ierr == nil || ierr.Code != invoke.ErrCodeRefused {
		t.Fatalf("expected the text/plain condition refusal, got %v", ierr)
	}
}

// Response-media declarations govern decoding and synthesis, but the binding
// never emits them as an Accept header.
func TestInvoke_NoAcceptHeader(t *testing.T) {
	var gotAccept string
	srv, _ := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	spec := fmt.Sprintf(`{
	  "openapi": "3.0.3", "info": {"title": "t", "version": "1"},
	  "servers": [{"url": %q}],
	  "paths": {"/csvjson": {"get": {
	    "operationId": "dual",
	    "responses": {
	      "200": {"description": "ok", "content": {"application/json": {}, "text/csv": {}}},
	      "404": {"description": "nope", "content": {"application/problem+json": {}}}
	    }
	  }}}
	}`, srv.URL)
	if _, ierr := invokeWith(t, spec, "#/paths/~1csvjson/get", nil); ierr != nil {
		t.Fatalf("invoke: %v", ierr)
	}
	if gotAccept != "" {
		t.Errorf("Accept = %q, want absent", gotAccept)
	}

	// Absent any declaration: no Accept header is invented. Drive this one
	// directly so the routing-fixture helper does not add response media.
	srv2, _ := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotAccept = r.Header.Get("Accept")
		w.WriteHeader(200)
	})
	noMediaSpec := fmt.Sprintf(`{
	  "openapi": "3.0.3", "info": {"title": "t", "version": "1"},
	  "servers": [{"url": %q}],
	  "paths": {"/session": {"get": {
	    "operationId": "getSession",
	    "responses": {"200": {"description": "ok"}}
	  }}}
	}`, srv2.URL)
	call := NewInvoker().InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
		Source:   invoke.InvocationSource{BindingSpec: BindingSpec, Content: openbindings.TextContent(noMediaSpec)},
		Selector: "#/paths/~1session/get",
	})
	outputs, ierr := driveOutputs(context.Background(), call, nil)
	if ierr != nil || len(outputs) != 0 {
		t.Fatalf("invoke: outputs=%v error=%v", outputs, ierr)
	}
	if gotAccept != "" {
		t.Errorf("Accept = %q, want omission", gotAccept)
	}
}

// ---------------------------------------------------------------------------
// OAPI-P-06 / §8 — interaction shape bounded by declaration
// ---------------------------------------------------------------------------

// A text/event-stream response on an operation that is NOT streaming-capable
// is a protocol error, never a silent reclassification.
func TestInvoke_UndeclaredSSEIsProtocolError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "data: hi\n\n")
	}))
	defer srv.Close()

	_, ierr := invokeWith(t, widgetSpec(srv.URL), "#/paths/~1session/get", nil)
	if ierr == nil || ierr.Code != invoke.ErrCodeExecutionFailed {
		t.Fatalf("expected ERR_PROTOCOL for an undeclared event-stream response, got %v", ierr)
	}
}

// An operation declaring BOTH a JSON success and text/event-stream is
// streaming-capable; the response's Content-Type framing selects the shape.
func TestInvoke_DeclaredBothShapesSelectByFraming(t *testing.T) {
	dualSpec := func(url string) string {
		return fmt.Sprintf(`{
		  "openapi": "3.0.3", "info": {"title": "t", "version": "1"},
		  "servers": [{"url": %q}],
		  "paths": {"/dual": {"get": {
		    "operationId": "dual",
		    "responses": {"200": {"description": "ok", "content": {
		      "application/json": {"schema": {"type": "object"}},
		      "text/event-stream": {"schema": {"type": "string"}}
		    }}}
		  }}}
		}`, url)
	}

	// SSE framing remains one unary body at the operation boundary.
	sseSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "data: one\n\ndata: two\n\n")
	}))
	defer sseSrv.Close()
	call := NewInvoker().InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
		Source:   invoke.InvocationSource{BindingSpec: BindingSpec, Content: openbindings.TextContent(dualSpec(sseSrv.URL))},
		Selector: "#/paths/~1dual/get",
	})
	events, ierr := driveOutputs(context.Background(), call, nil)
	if ierr != nil {
		t.Fatalf("stream: %v", ierr)
	}
	if len(events) != 1 || events[0] != "data: one\n\ndata: two\n\n" {
		t.Fatalf("events = %v, want one complete event-stream body", events)
	}

	// JSON framing → unary.
	jsonSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"mode":"unary"}`)
	}))
	defer jsonSrv.Close()
	out, ierr2 := invokeWith(t, dualSpec(jsonSrv.URL), "#/paths/~1dual/get", nil)
	if ierr2 != nil {
		t.Fatalf("unary: %v", ierr2)
	}
	if m, _ := out.(map[string]any); m["mode"] != "unary" {
		t.Errorf("out = %v", out)
	}
}

// Event framing, including empty data and incomplete final blocks, remains
// opaque inside the one response body value.
func TestInvoke_SSEFramingRemainsOpaque(t *testing.T) {
	want := ": comment only\n\nevent: tick\nid: 7\n\ndata: first\n\ndata:\n\ndata: third\n\ndata: incomplete-final-event"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, ": comment only\n\n")           // comment-only: nothing
		_, _ = io.WriteString(w, "event: tick\nid: 7\n\n")       // fields-only: nothing
		_, _ = io.WriteString(w, "data: first\n\n")              // emits "first"
		_, _ = io.WriteString(w, "data:\n\n")                    // lone empty data line: emits ""
		_, _ = io.WriteString(w, "data: third\n\n")              // emits "third"
		_, _ = io.WriteString(w, "data: incomplete-final-event") // no blank line: discarded
	}))
	defer srv.Close()

	events, ierr := driveOutputs(context.Background(), sseCall(srv), nil)
	if ierr != nil {
		t.Fatalf("stream: %v", ierr)
	}
	if len(events) != 1 || events[0] != want {
		t.Fatalf("events = %#v, want one opaque body", events)
	}
}

// CRLF and lone-CR bytes are preserved in the unary body.
func TestInvoke_SSECarriageReturnBytesPreserved(t *testing.T) {
	want := "data: crlf\r\n\r\ndata: cr\r\r"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "data: crlf\r\n\r\n")
		_, _ = io.WriteString(w, "data: cr\r\r")
	}))
	defer srv.Close()

	events, ierr := driveOutputs(context.Background(), sseCall(srv), nil)
	if ierr != nil {
		t.Fatalf("stream: %v", ierr)
	}
	if len(events) != 1 || events[0] != want {
		t.Fatalf("events = %#v, want preserved body", events)
	}
}

// ---------------------------------------------------------------------------
// OAPI-P-07 — decode: charset handling and the 204 rule
// ---------------------------------------------------------------------------

func TestDecode_CharsetHandling(t *testing.T) {
	// latin-1 declared: bytes transcode.
	dec := decodeByContentTypeFor("text/plain; charset=iso-8859-1", BindingSpecOpenAPI31)
	out, err := dec(invoke.InvokeSite{}, invoke.RawResult{Body: []byte{0xe9}})
	if err != nil || out != "é" {
		t.Errorf("latin-1 = (%v, %v), want é", out, err)
	}

	// Invalid UTF-8 under the default charset is a loud decode error.
	dec = decodeByContentTypeFor("text/plain", BindingSpecOpenAPI31)
	if _, err := dec(invoke.InvokeSite{}, invoke.RawResult{Body: []byte{0xff, 0xfe}}); err == nil {
		t.Error("invalid UTF-8 must be a loud decode error, never mojibake")
	}

	// An undecodable declared charset is loud (override at the decode point).
	dec = decodeByContentTypeFor("text/plain; charset=shift_jis", BindingSpecOpenAPI31)
	if _, err := dec(invoke.InvokeSite{}, invoke.RawResult{Body: []byte("x")}); err == nil {
		t.Error("an unsupported charset must be a loud decode error")
	}

	// The builtin decoder returns nil for absent bytes; the invocation layer
	// interprets that as zero output values rather than a JSON null result.
	dec = decodeByContentTypeFor("application/json", BindingSpecOpenAPI31)
	out, err = dec(invoke.InvokeSite{}, invoke.RawResult{Body: nil})
	if err != nil || out != nil {
		t.Errorf("empty body = (%v, %v), want null", out, err)
	}
}

// ---------------------------------------------------------------------------
// OAPI-P-10 — channel assembly
// ---------------------------------------------------------------------------

// Declared cookie parameters and cookie-riding credentials merge into ONE
// Cookie header: parameters in declaration order, credentials appended after.
func TestInvoke_CookieChannelAssembly(t *testing.T) {
	var gotCookie string
	srv, _ := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotCookie = r.Header.Get("Cookie")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	spec := fmt.Sprintf(`{
	  "openapi": "3.0.3", "info": {"title": "t", "version": "1"},
	  "servers": [{"url": %q}],
	  "paths": {"/sess": {"get": {
	    "operationId": "sess",
	    "security": [{"cookieKey": []}],
	    "parameters": [
	      {"name": "zeta", "in": "cookie", "schema": {"type": "string"}},
	      {"name": "alpha", "in": "cookie", "schema": {"type": "string"}}
	    ],
	    "responses": {"200": {"description": "ok", "content": {"application/json": {"schema": {}}}}}
	  }}},
	  "components": {"securitySchemes": {
	    "cookieKey": {"type": "apiKey", "in": "cookie", "name": "auth_token"}
	  }}
	}`, srv.URL)

	call := NewInvoker().InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
		Source:   invoke.InvocationSource{BindingSpec: bindingSpecForTestDocument(spec), Content: openbindings.TextContent(spec)},
		Selector: "#/paths/~1sess/get",
		Context:  map[string]any{"apiKeys": map[string]any{"cookieKey": "secret"}},
	})
	if _, ierr := driveSingle(t, call, map[string]any{"parameters": map[string]any{"zeta": "z", "alpha": "a"}}); ierr != nil {
		t.Fatalf("invoke: %s: %s", ierr.Code, ierr.Error())
	}
	// ONE header: declared params in declaration order (zeta before alpha),
	// the credential appended after.
	if gotCookie != "zeta=z; alpha=a; auth_token=secret" {
		t.Errorf("Cookie = %q, want zeta=z; alpha=a; auth_token=secret", gotCookie)
	}
}

// A name collision between a credential and a caller-populated declared
// parameter on the same channel refuses before dispatch.
func TestInvoke_CredentialCollisionRefused(t *testing.T) {
	channels := []struct {
		name  string
		in    string
		param string
	}{
		{"header", "header", "X-Api-Key"},
		{"query", "query", "api_key"},
		{"cookie", "cookie", "session"},
	}
	for _, tc := range channels {
		t.Run(tc.name, func(t *testing.T) {
			srv, requests := countingServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
			spec := fmt.Sprintf(`{
			  "openapi": "3.0.3", "info": {"title": "t", "version": "1"},
			  "servers": [{"url": %q}],
			  "paths": {"/x": {"get": {
			    "operationId": "x",
			    "security": [{"key": []}],
			    "parameters": [{"name": %q, "in": %q, "schema": {"type": "string"}}],
			    "responses": {"200": {"description": "ok"}}
			  }}},
			  "components": {"securitySchemes": {
			    "key": {"type": "apiKey", "in": %q, "name": %q}
			  }}
			}`, srv.URL, tc.param, tc.in, tc.in, tc.param)

			call := NewInvoker().InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
				Source:   invoke.InvocationSource{BindingSpec: bindingSpecForTestDocument(spec), Content: openbindings.TextContent(spec)},
				Selector: "#/paths/~1x/get",
				Context:  map[string]any{"apiKey": "cred"},
			})
			_, ierr := driveSingle(t, call, map[string]any{"parameters": map[string]any{tc.param: "caller-value"}})
			if ierr == nil || ierr.Code != invoke.ErrCodeRefused {
				t.Fatalf("expected the OAPI-P-10 collision refusal, got %v", ierr)
			}
			if ierr.HasData() {
				t.Errorf("credential-collision evidence crossed as abstract data: %#v", ierr.Data)
			}
			if requests.Load() != 0 {
				t.Error("refusal must precede dispatch")
			}
		})
	}
}

// Security Requirement Objects are alternatives, not a pool of schemes. The
// explicit choice applies exactly one complete alternative and does not leak
// another satisfiable credential onto the wire.
func TestInvoke_SecurityORSelectsOneCompleteAlternative(t *testing.T) {
	var gotHeader string
	var gotQuery string
	srv, _ := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Header-Key")
		gotQuery = r.URL.Query().Get("query_key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	spec := fmt.Sprintf(`{
	  "openapi": "3.1.0", "info": {"title": "t", "version": "1"},
	  "servers": [{"url": %q}],
	  "paths": {"/x": {"get": {
	    "operationId": "x",
	    "security": [{"headerKey": []}, {"queryKey": []}],
	    "responses": {"200": {"description": "ok", "content": {"application/json": {"schema": {}}}}}
	  }}},
	  "components": {"securitySchemes": {
	    "headerKey": {"type": "apiKey", "in": "header", "name": "X-Header-Key"},
	    "queryKey": {"type": "apiKey", "in": "query", "name": "query_key"}
	  }}
	}`, srv.URL)

	call := NewInvoker().InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
		Source:   invoke.InvocationSource{BindingSpec: bindingSpecForTestDocument(spec), Content: openbindings.TextContent(spec)},
		Selector: "#/paths/~1x/get",
		Context: map[string]any{"apiKeys": map[string]any{
			"headerKey": "header-secret",
			"queryKey":  "query-secret",
		}, "configuration": map[string]any{"security": map[string]any{"index": 0}}},
	})
	if _, ierr := driveSingle(t, call, nil); ierr != nil {
		t.Fatalf("invoke: %s: %s", ierr.Code, ierr.Error())
	}
	if gotHeader != "header-secret" {
		t.Errorf("selected alternative header = %q, want header-secret", gotHeader)
	}
	if gotQuery != "" {
		t.Errorf("unselected alternative credential leaked into query: %q", gotQuery)
	}
}

// A credential satisfying only part of one AND alternative must not be mixed
// with a separate complete OR alternative. Only the latter reaches the wire.
func TestInvoke_SecurityORDoesNotCombineIncompleteAlternativeFragments(t *testing.T) {
	var gotFirstHeader string
	var gotSecondHeader string
	var gotQuery string
	srv, _ := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotFirstHeader = r.Header.Get("X-First-Key")
		gotSecondHeader = r.Header.Get("X-Second-Key")
		gotQuery = r.URL.Query().Get("query_key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	spec := fmt.Sprintf(`{
	  "openapi": "3.0.3", "info": {"title": "t", "version": "1"},
	  "servers": [{"url": %q}],
	  "paths": {"/x": {"get": {
	    "operationId": "x",
	    "security": [{"firstHeader": [], "secondHeader": []}, {"queryKey": []}],
	    "responses": {"200": {"description": "ok", "content": {"application/json": {"schema": {}}}}}
	  }}},
	  "components": {"securitySchemes": {
	    "firstHeader": {"type": "apiKey", "in": "header", "name": "X-First-Key"},
	    "secondHeader": {"type": "apiKey", "in": "header", "name": "X-Second-Key"},
	    "queryKey": {"type": "apiKey", "in": "query", "name": "query_key"}
	  }}
	}`, srv.URL)

	call := NewInvoker().InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
		Source:   invoke.InvocationSource{BindingSpec: bindingSpecForTestDocument(spec), Content: openbindings.TextContent(spec)},
		Selector: "#/paths/~1x/get",
		Context: map[string]any{"apiKeys": map[string]any{
			"firstHeader": "incomplete-fragment",
			"queryKey":    "complete-alternative",
		}, "configuration": map[string]any{"security": map[string]any{"index": 1}}},
	})
	if _, ierr := driveSingle(t, call, nil); ierr != nil {
		t.Fatalf("invoke: %s: %s", ierr.Code, ierr.Error())
	}
	if gotFirstHeader != "" || gotSecondHeader != "" {
		t.Errorf("incomplete alternative leaked headers: first=%q second=%q", gotFirstHeader, gotSecondHeader)
	}
	if gotQuery != "complete-alternative" {
		t.Errorf("complete alternative query credential = %q, want complete-alternative", gotQuery)
	}
}

// Credentials in ambient context have no wire meaning unless the artifact's
// effective security declaration selects them.
func TestInvoke_NoDeclaredSecurityDoesNotSendContextCredentials(t *testing.T) {
	var gotAuthorization string
	srv, _ := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	spec := fmt.Sprintf(`{
	  "openapi": "3.0.3", "info": {"title": "t", "version": "1"},
	  "servers": [{"url": %q}],
	  "paths": {"/x": {"get": {
	    "operationId": "x",
	    "responses": {"200": {"description": "ok", "content": {"application/json": {"schema": {}}}}}
	  }}}
	}`, srv.URL)

	call := NewInvoker().InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
		Source:   invoke.InvocationSource{BindingSpec: bindingSpecForTestDocument(spec), Content: openbindings.TextContent(spec)},
		Selector: "#/paths/~1x/get",
		Context: map[string]any{
			"bearerToken": "unrelated-bearer",
			"apiKey":      "unrelated-key",
			"basic":       map[string]any{"username": "u", "password": "p"},
		},
	})
	if _, ierr := driveSingle(t, call, nil); ierr != nil {
		t.Fatalf("invoke: %s: %s", ierr.Code, ierr.Error())
	}
	if gotAuthorization != "" {
		t.Errorf("undeclared credential leaked into Authorization: %q", gotAuthorization)
	}
}

func TestInvoke_RawCookieConflictsRefused(t *testing.T) {
	for _, edition := range []string{"3.0.4", "3.1.2"} {
		edition := edition
		t.Run("structured cookie parameter "+edition, func(t *testing.T) {
			srv, requests := countingServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
			spec := fmt.Sprintf(`{
			  "openapi": %q, "info": {"title": "t", "version": "1"},
			  "servers": [{"url": %q}],
			  "paths": {"/x": {"get": {
			    "operationId": "x",
			    "parameters": [
			      {"name": "Cookie", "in": "header", "schema": {"type": "string"}},
			      {"name": "session", "in": "cookie", "schema": {"type": "string"}}
			    ],
			    "responses": {"200": {"description": "ok"}}
			  }}}
			}`, edition, srv.URL)
			call := NewInvoker().InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
				Source:   invoke.InvocationSource{BindingSpec: bindingSpecForTestDocument(spec), Content: openbindings.TextContent(spec)},
				Selector: "#/paths/~1x/get",
			})
			_, ierr := driveSingle(t, call, map[string]any{"parameters": map[string]any{
				"Cookie": "raw=1", "session": "structured",
			}})
			if ierr == nil || ierr.Code != invoke.ErrCodeRefused {
				t.Fatalf("expected invocation-time P-02 refusal, got %v", ierr)
			}
			if ierr.HasData() {
				t.Fatalf("cookie-collision evidence crossed as abstract data: %#v", ierr.Data)
			}
			if requests.Load() != 0 {
				t.Error("refusal must precede dispatch")
			}
		})
	}

	t.Run("selected cookie credential", func(t *testing.T) {
		srv, requests := countingServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
		spec := fmt.Sprintf(`{
		  "openapi": "3.0.3", "info": {"title": "t", "version": "1"},
		  "servers": [{"url": %q}],
		  "paths": {"/x": {"get": {
		    "operationId": "x",
		    "security": [{"cookieKey": []}],
		    "parameters": [{"name": "Cookie", "in": "header", "schema": {"type": "string"}}],
		    "responses": {"200": {"description": "ok"}}
		  }}},
		  "components": {"securitySchemes": {
		    "cookieKey": {"type": "apiKey", "in": "cookie", "name": "auth_token"}
		  }}
		}`, srv.URL)
		call := NewInvoker().InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
			Source:   invoke.InvocationSource{BindingSpec: bindingSpecForTestDocument(spec), Content: openbindings.TextContent(spec)},
			Selector: "#/paths/~1x/get",
			Context:  map[string]any{"apiKeys": map[string]any{"cookieKey": "secret"}},
		})
		_, ierr := driveSingle(t, call, map[string]any{"parameters": map[string]any{"Cookie": "raw=1"}})
		if ierr == nil || ierr.Code != invoke.ErrCodeRefused {
			t.Fatalf("expected validation OAPI-P-10 refusal, got %v", ierr)
		}
		if ierr.HasData() {
			t.Fatalf("cookie-credential evidence crossed as abstract data: %#v", ierr.Data)
		}
		if requests.Load() != 0 {
			t.Error("refusal must precede dispatch")
		}
	})
}

func TestPrepareBinding_FiltersCollidingSecurityAlternative(t *testing.T) {
	srv, requests := countingServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	spec := fmt.Sprintf(`{
	  "openapi": "3.1.0", "info": {"title": "t", "version": "1"},
	  "servers": [{"url": %q}],
	  "paths": {"/x": {"get": {
	    "operationId": "x",
	    "security": [{"cookieKey": []}, {"bearer": []}],
	    "parameters": [{"name": "Cookie", "in": "header", "schema": {"type": "string"}}],
	    "responses": {"200": {"description": "ok"}}
	  }}},
	  "components": {"securitySchemes": {
	    "cookieKey": {"type": "apiKey", "in": "cookie", "name": "session"},
	    "bearer": {"type": "http", "scheme": "bearer"}
	  }}
	}`, srv.URL)

	invoker := NewInvoker()
	args := &invoke.BindingInvocationArgs{
		Source:   invoke.InvocationSource{BindingSpec: bindingSpecForTestDocument(spec), Content: openbindings.TextContent(spec)},
		Selector: "#/paths/~1x/get",
	}
	details, err := invoker.PrepareBinding(context.Background(), args)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if details == nil || len(details.Alternatives) != 1 || len(details.Alternatives[0].Requirements) != 1 {
		t.Fatalf("challenge = %+v, want the security selection requirement", details)
	}
	req := details.Alternatives[0].Requirements[0]
	if req.Type != "config.value" || req.Extra["point"] != "security" || req.Extra["path"] != "/index" {
		t.Errorf("selection requirement = %+v, want configuration.security index", req)
	}
	args.Context = map[string]any{"configuration": map[string]any{"security": map[string]any{"index": 1}}}
	details, err = invoker.PrepareBinding(context.Background(), args)
	if err != nil || details == nil || len(details.Alternatives) != 1 || len(details.Alternatives[0].Requirements) != 1 {
		t.Fatalf("selected challenge = %+v, %v; want bearer prerequisite", details, err)
	}
	req = details.Alternatives[0].Requirements[0]
	if req.Type != "auth.bearer" || req.Name != "bearer" {
		t.Errorf("selected requirement = %+v, want named auth.bearer", req)
	}
	if requests.Load() != 0 {
		t.Error("prepareBinding must perform no network I/O")
	}
}

func TestInvoke_AllMissingSecurityAlternativesRefuseBeforeDispatch(t *testing.T) {
	srv, requests := countingServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	spec := fmt.Sprintf(`{
	  "openapi": "3.1.0", "info": {"title": "t", "version": "1"},
	  "servers": [{"url": %q}],
	  "paths": {"/x": {"get": {
	    "operationId": "x",
	    "security": [{"missingA": []}, {"missingB": []}],
	    "responses": {"200": {"description": "ok"}}
	  }}}
	}`, srv.URL)
	call := NewInvoker().InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
		Source:   invoke.InvocationSource{BindingSpec: bindingSpecForTestDocument(spec), Content: openbindings.TextContent(spec)},
		Selector: "#/paths/~1x/get",
	})
	_, ierr := driveSingle(t, call, nil)
	if ierr == nil || ierr.Code != invoke.ErrCodeRefused {
		t.Fatalf("expected closed security-configuration refusal, got %v", ierr)
	}
	if ierr.HasData() {
		t.Fatalf("invalid security-declaration evidence crossed as abstract data: %#v", ierr.Data)
	}
	if requests.Load() != 0 {
		t.Error("invalid security declaration must refuse before dispatch")
	}
}

func TestCustomDocumentDialectExcludesTheWholeSource(t *testing.T) {
	srv, requests := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	spec := fmt.Sprintf(`{
	  "openapi": "3.1.0",
	  "jsonSchemaDialect": "https://example.test/custom-dialect",
	  "info": {"title": "custom dialect", "version": "1"},
	  "servers": [{"url": %q}],
	  "paths": {"/x": {"get": {
	    "operationId": "x",
	    "responses": {"200": {"description": "ok", "content": {"application/json": {}}}}
	  }}}
	}`, srv.URL)
	call := NewInvoker().InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
		Source:   invoke.InvocationSource{BindingSpec: bindingSpecForTestDocument(spec), Content: openbindings.TextContent(spec)},
		Selector: "#/paths/~1x/get",
	})
	if _, ierr := driveSingle(t, call, nil); ierr == nil || ierr.Code != invoke.ErrCodeRefused {
		t.Fatalf("custom document dialect invocation error = %v, want ERR_REFUSED", ierr)
	}
	if requests.Load() != 0 {
		t.Fatalf("excluded source dispatched %d requests", requests.Load())
	}
	_, err := NewSynthesizer().SynthesizeInterface(context.Background(), &synthesize.SynthesizeInput{
		Sources: []synthesize.SynthesizeSource{{BindingSpec: bindingSpecForTestDocument(spec), Content: openbindings.TextContent(spec)}},
	})
	if err == nil || !strings.Contains(err.Error(), "whole-source exclusion") {
		t.Fatalf("custom document dialect synthesis error = %v", err)
	}
}

func TestInvoke_RawCookieContextHeaderConflictsStructuredSources(t *testing.T) {
	t.Run("structured context cookies", func(t *testing.T) {
		srv, requests := countingServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
		spec := fmt.Sprintf(`{
		  "openapi": "3.1.0", "info": {"title": "t", "version": "1"},
		  "servers": [{"url": %q}],
		  "paths": {"/x": {"get": {"operationId": "x", "responses": {"200": {"description": "ok"}}}}}
		}`, srv.URL)
		call := NewInvoker().InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
			Source:   invoke.InvocationSource{BindingSpec: bindingSpecForTestDocument(spec), Content: openbindings.TextContent(spec)},
			Selector: "#/paths/~1x/get",
			Context: map[string]any{
				"headers": map[string]any{"Cookie": "raw=1"},
				"cookies": map[string]any{"session": "structured"},
			},
		})
		_, ierr := driveSingle(t, call, nil)
		if ierr == nil || ierr.Code != invoke.ErrCodeRefused {
			t.Fatalf("expected Cookie context collision, got %v", ierr)
		}
		if ierr.HasData() {
			t.Fatalf("cookie-context evidence crossed as abstract data: %#v", ierr.Data)
		}
		if requests.Load() != 0 {
			t.Error("Cookie collision must refuse before dispatch")
		}
	})

	t.Run("selected cookie credential", func(t *testing.T) {
		srv, requests := countingServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
		spec := fmt.Sprintf(`{
		  "openapi": "3.1.0", "info": {"title": "t", "version": "1"},
		  "servers": [{"url": %q}],
		  "paths": {"/x": {"get": {
		    "operationId": "x", "security": [{"cookieKey": []}],
		    "responses": {"200": {"description": "ok"}}
		  }}},
		  "components": {"securitySchemes": {"cookieKey": {"type": "apiKey", "in": "cookie", "name": "session"}}}
		}`, srv.URL)
		call := NewInvoker().InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
			Source:   invoke.InvocationSource{BindingSpec: bindingSpecForTestDocument(spec), Content: openbindings.TextContent(spec)},
			Selector: "#/paths/~1x/get",
			Context: map[string]any{
				"headers": map[string]any{"cookie": "raw=1"},
				"apiKeys": map[string]any{"cookieKey": "secret"},
			},
		})
		_, ierr := driveSingle(t, call, nil)
		if ierr == nil || ierr.Code != invoke.ErrCodeRefused {
			t.Fatalf("expected Cookie credential/context collision, got %v", ierr)
		}
		if ierr.HasData() {
			t.Fatalf("cookie-credential evidence crossed as abstract data: %#v", ierr.Data)
		}
		if requests.Load() != 0 {
			t.Error("Cookie collision must refuse before dispatch")
		}
	})
}

func TestPrepareBindingAllowsCookieDeclarationsAndReturnsAuthChallenge(t *testing.T) {
	spec := `{
	  "openapi": "3.1.0", "info": {"title": "t", "version": "1"},
	  "servers": [{"url": "https://api.example.com"}],
	  "paths": {"/x": {"get": {
	    "operationId": "x", "security": [{"bearer": []}],
	    "parameters": [
	      {"name": "Cookie", "in": "header", "schema": {"type": "string"}},
	      {"name": "session", "in": "cookie", "schema": {"type": "string"}}
	    ],
	    "responses": {"200": {"description": "ok"}}
	  }}},
	  "components": {"securitySchemes": {"bearer": {"type": "http", "scheme": "bearer"}}}
	}`
	details, err := NewInvoker().PrepareBinding(context.Background(), &invoke.BindingInvocationArgs{
		Source:   invoke.InvocationSource{BindingSpec: bindingSpecForTestDocument(spec), Content: openbindings.TextContent(spec)},
		Selector: "#/paths/~1x/get",
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if details == nil || len(details.Alternatives) != 1 || len(details.Alternatives[0].Requirements) != 1 {
		t.Fatalf("challenge = %+v, want bearer prerequisite", details)
	}
	requirement := details.Alternatives[0].Requirements[0]
	if requirement.Type != "auth.bearer" || requirement.Name != "bearer" {
		t.Fatalf("requirement = %+v, want named auth.bearer", requirement)
	}
}

func TestInvoke_ProcessorOwnedHeaderParametersRefused(t *testing.T) {
	for _, name := range []string{"Host", "Content-Length"} {
		t.Run(name, func(t *testing.T) {
			srv, requests := countingServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
			spec := fmt.Sprintf(`{
			  "openapi": "3.0.3", "info": {"title": "t", "version": "1"},
			  "servers": [{"url": %q}],
			  "paths": {"/x": {"get": {
			    "operationId": "x",
			    "parameters": [{"name": %q, "in": "header", "schema": {"type": "string"}}],
			    "responses": {"200": {"description": "ok"}}
			  }}}
			}`, srv.URL, name)
			call := NewInvoker().InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
				Source:   invoke.InvocationSource{BindingSpec: bindingSpecForTestDocument(spec), Content: openbindings.TextContent(spec)},
				Selector: "#/paths/~1x/get",
			})
			_, ierr := driveSingle(t, call, map[string]any{})
			if ierr == nil || ierr.Code != invoke.ErrCodeRefused {
				t.Fatalf("expected source-config OAPI-P-10 refusal, got %v", ierr)
			}
			if ierr.HasData() {
				t.Fatalf("processor-owned header evidence crossed as abstract data: %#v", ierr.Data)
			}
			if requests.Load() != 0 {
				t.Error("refusal must precede dispatch")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// OAPI-P-05 — servers end to end
// ---------------------------------------------------------------------------

// The configuration point selects the target end to end.
func TestInvoke_ServerConfigurationPoint(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()

	// The document declares an unrelated (unreachable) server; the consumer
	// configuration supplies the real base URL outright.
	spec := `{
	  "openapi": "3.0.3", "info": {"title": "t", "version": "1"},
	  "servers": [{"url": "https://unreachable.invalid"}],
	  "paths": {"/ping": {"get": {"operationId": "ping", "responses": {"200": {
	    "description": "ok", "content": {"application/json": {"schema": {}}}
	  }}}}}
	}`
	call := NewInvoker().InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
		Source:   invoke.InvocationSource{BindingSpec: bindingSpecForTestDocument(spec), Content: openbindings.TextContent(spec)},
		Selector: "#/paths/~1ping/get",
		Context:  map[string]any{"configuration": map[string]any{"server": map[string]any{"baseUrl": srv.URL}}},
	})
	if _, ierr := driveSingle(t, call, nil); ierr != nil {
		t.Fatalf("invoke: %s: %s", ierr.Code, ierr.Error())
	}
	if hits != 1 {
		t.Errorf("configured server hits = %d, want 1", hits)
	}
}
