package openapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

// Integration tests keyed to openbindings.openapi@1 rules, driving the
// invoker against local httptest servers.

func invokeWith(t *testing.T, spec, ref string, input any) (any, *openbindings.InvocationError) {
	t.Helper()
	call := NewInvoker().InvokeBinding(context.Background(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{BindingSpec: BindingSpec, Content: openbindings.TextContent(spec)},
		Ref:    ref,
	})
	return driveSingle(t, call, input)
}

// ---------------------------------------------------------------------------
// OAPI-D-03 — ref shape at the invoke boundary
// ---------------------------------------------------------------------------

// An uppercase ref method is non-conformant: refused with ERR_INVALID_REF,
// never case-folded to a match.
func TestInvoke_UppercaseRefMethodRefused(t *testing.T) {
	srv, requests := countingServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	_, ierr := invokeWith(t, widgetSpec(srv.URL), "#/paths/~1session/GET", nil)
	if ierr == nil || ierr.Code != openbindings.ErrCodeInvalidRef {
		t.Fatalf("expected ERR_INVALID_REF for an uppercase method, got %v", ierr)
	}
	if requests.Load() != 0 {
		t.Error("refusal must precede dispatch")
	}
}

// A path item that is a $ref (3.1 components.pathItems) resolves before the
// method segment evaluates (OAPI-D-03: OAS reference resolution, not raw
// JSON traversal).
func TestInvoke_RefResolvesPathItemRef(t *testing.T) {
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
		t.Fatalf("invoke: %s: %s", ierr.Code, ierr.Message)
	}
	if m, _ := out.(map[string]any); m["ok"] != true {
		t.Errorf("out = %v", out)
	}
	if gotPath != "/shared" {
		t.Errorf("path = %q", gotPath)
	}
}

// ---------------------------------------------------------------------------
// OAPI-P-01 / §3 / §6 — accepted lines, duplicate keys, self-containment
// ---------------------------------------------------------------------------

func TestLoadDocument_VersionDiscrimination(t *testing.T) {
	cases := []struct {
		name    string
		content string
		wantErr string
	}{
		{"swagger 2.0", `{"swagger": "2.0", "info": {"title": "t", "version": "1"}, "paths": {}}`, "OAPI-P-01"},
		{"3.2 line", `{"openapi": "3.2.0", "info": {"title": "t", "version": "1"}, "paths": {}}`, "OAPI-P-01"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadDocument("", openbindings.TextContent(tc.content))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("loadDocument = %v, want loud OAPI-P-01 refusal", err)
			}
		})
	}
	// Accepted lines load.
	for _, v := range []string{"3.0.3", "3.1.0"} {
		doc, err := loadDocument("", openbindings.TextContent(fmt.Sprintf(`{"openapi": %q, "info": {"title": "t", "version": "1"}, "paths": {}}`, v)))
		if err != nil || doc == nil {
			t.Errorf("version %s must load, got %v", v, err)
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

func TestInvoke_UnflattenableOperationRefused(t *testing.T) {
	srv, requests := countingServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	spec := fmt.Sprintf(`{
	  "openapi": "3.0.3", "info": {"title": "t", "version": "1"},
	  "servers": [{"url": %q}],
	  "paths": {"/items/{id}": {"get": {
	    "operationId": "get",
	    "parameters": [
	      {"name": "id", "in": "path", "required": true, "schema": {"type": "string"}},
	      {"name": "id", "in": "query", "schema": {"type": "string"}}
	    ],
	    "responses": {"200": {"description": "ok"}}
	  }}}
	}`, srv.URL)
	_, ierr := invokeWith(t, spec, "#/paths/~1items~1{id}/get", map[string]any{"id": "1"})
	if ierr == nil || ierr.Code != openbindings.ErrCodeSourceConfigError {
		t.Fatalf("expected the unflattenable refusal, got %v", ierr)
	}
	if !strings.Contains(ierr.Message, "unflattenable") {
		t.Errorf("message should name the unflattenable rule, got %q", ierr.Message)
	}
	if requests.Load() != 0 {
		t.Error("refusal must precede dispatch")
	}
}

// A field matching no declared parameter is refused pre-dispatch when the
// operation declares no request body — loud, naming the offenders.
func TestInvoke_UnmatchedFieldsRefusedWithoutBody(t *testing.T) {
	srv, requests := countingServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	_, ierr := invokeWith(t, widgetSpec(srv.URL), "#/paths/~1session/get", map[string]any{
		"session_id": "s", "bogus": 1,
	})
	if ierr == nil || ierr.Code != openbindings.ErrCodeValidationFailed {
		t.Fatalf("expected ERR_VALIDATION_FAILED, got %v", ierr)
	}
	if !strings.Contains(ierr.Message, "bogus") {
		t.Errorf("refusal must list the offending fields, got %q", ierr.Message)
	}
	if requests.Load() != 0 {
		t.Error("refusal must precede dispatch")
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
	_, ierr := invokeWith(t, spec, "#/paths/~1w~1{id}/post", map[string]any{"name": "x"})
	if ierr == nil || ierr.Code != openbindings.ErrCodeMissingInput {
		t.Fatalf("expected ERR_MISSING_INPUT for the unfilled path template, got %v", ierr)
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
	if _, ierr := invokeWith(t, spec2, "#/paths/~1w~1{id}/post", map[string]any{"id": "7"}); ierr != nil {
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
	_, ierr := invokeWith(t, spec, "#/paths/~1search/get", map[string]any{
		"tags":   []any{"a", "b"},
		"flat":   []any{"x", "y"},
		"filter": map[string]any{"kind": "big", "size": float64(2)},
	})
	if ierr != nil {
		t.Fatalf("invoke: %s: %s", ierr.Code, ierr.Message)
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
	_, ierr := invokeWith(t, spec, "#/paths/~1map~1{coords}/get", map[string]any{
		"coords": []any{float64(50.4), float64(4.32)},
	})
	if ierr != nil {
		t.Fatalf("invoke: %s: %s", ierr.Code, ierr.Message)
	}
	if gotURI != "/map/;coords=50.4,4.32" {
		t.Errorf("URI = %q, want /map/;coords=50.4,4.32", gotURI)
	}
}

// ---------------------------------------------------------------------------
// OAPI-P-04 — media selection and bodies on the wire
// ---------------------------------------------------------------------------

// The lexicographically least +json type is selected when exact
// application/json is absent, and rides as the request Content-Type.
func TestInvoke_PlusJSONSelected(t *testing.T) {
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
	  "paths": {"/things": {"post": {
	    "operationId": "makeThing",
	    "requestBody": {"required": true, "content": {
	      "application/vnd.b+json": {"schema": {"type": "object"}},
	      "application/vnd.a+json": {"schema": {"type": "object"}}
	    }},
	    "responses": {"201": {"description": "ok"}}
	  }}}
	}`, srv.URL)
	_, ierr := invokeWith(t, spec, "#/paths/~1things/post", map[string]any{"k": "v"})
	if ierr != nil {
		t.Fatalf("invoke: %s: %s", ierr.Code, ierr.Message)
	}
	if gotCT != "application/vnd.a+json" {
		t.Errorf("Content-Type = %q, want the lexicographically least +json type", gotCT)
	}
	if string(gotBody) != `{"k":"v"}` {
		t.Errorf("body = %s", gotBody)
	}
}

// An operation declaring only out-of-family request media refuses
// pre-dispatch with zero I/O.
func TestInvoke_BinaryOnlyBodyRefused(t *testing.T) {
	srv, requests := countingServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	spec := fmt.Sprintf(`{
	  "openapi": "3.0.3", "info": {"title": "t", "version": "1"},
	  "servers": [{"url": %q}],
	  "paths": {"/blob": {"post": {
	    "operationId": "putBlob",
	    "requestBody": {"required": true, "content": {"application/octet-stream": {"schema": {"type": "string", "format": "binary"}}}},
	    "responses": {"200": {"description": "ok"}}
	  }}}
	}`, srv.URL)
	_, ierr := invokeWith(t, spec, "#/paths/~1blob/post", map[string]any{"body": "x"})
	if ierr == nil || ierr.Code != openbindings.ErrCodeSourceConfigError {
		t.Fatalf("expected the out-of-family refusal, got %v", ierr)
	}
	if requests.Load() != 0 {
		t.Error("refusal must precede dispatch and input consumption where knowable")
	}
}

// urlencoded selection serializes fields per the OAS encoding rules.
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
	_, ierr := invokeWith(t, spec, "#/paths/~1form/post", map[string]any{
		"name": "a b", "ids": []any{float64(1), float64(2)},
	})
	if ierr != nil {
		t.Fatalf("invoke: %s: %s", ierr.Code, ierr.Message)
	}
	if gotCT != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type = %q", gotCT)
	}
	if string(gotBody) != "ids=1&ids=2&name=a%20b" {
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
		t.Fatalf("invoke: %s: %s", ierr.Code, ierr.Message)
	}
	if string(gotBody) != "[1,2]" {
		t.Errorf("wire body = %q, want the unwrapped array [1,2]", gotBody)
	}
}

// text/plain selection: a string body rides verbatim; the Accept header
// reflects the declared success media.
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
		t.Fatalf("invoke: %s: %s", ierr.Code, ierr.Message)
	}
	if gotCT != "text/plain" || string(gotBody) != "ping" {
		t.Errorf("request = (%q, %q), want text/plain ping", gotCT, gotBody)
	}
	if out != "pong" {
		t.Errorf("out = %v, want the text lane's string", out)
	}

	// The selection condition: a non-string body value refuses pre-dispatch.
	_, ierr = invokeWith(t, spec, "#/paths/~1echo/post", map[string]any{"body": float64(1)})
	if ierr == nil || ierr.Code != openbindings.ErrCodeValidationFailed {
		t.Fatalf("expected the text/plain condition refusal, got %v", ierr)
	}
}

// The Accept header carries the declared success media; membership is
// normative (§9.2).
func TestInvoke_AcceptHeaderMembership(t *testing.T) {
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
	if !strings.Contains(gotAccept, "application/json") || !strings.Contains(gotAccept, "text/csv") {
		t.Errorf("Accept = %q, must contain both declared success media", gotAccept)
	}
	if strings.Contains(gotAccept, "problem+json") {
		t.Errorf("Accept = %q, must not contain failure-response media", gotAccept)
	}

	// Absent any declaration: application/json.
	srv2, _ := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotAccept = r.Header.Get("Accept")
		w.WriteHeader(200)
	})
	if _, ierr := invokeWith(t, widgetSpec(srv2.URL), "#/paths/~1session/get", nil); ierr != nil {
		t.Fatalf("invoke: %v", ierr)
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept = %q, want the application/json default", gotAccept)
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
	if ierr == nil || ierr.Code != openbindings.ErrCodeProtocol {
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
		      "text/event-stream": {}
		    }}}
		  }}}
		}`, url)
	}

	// SSE framing → server-streaming.
	sseSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "data: one\n\ndata: two\n\n")
	}))
	defer sseSrv.Close()
	call := NewInvoker().InvokeBinding(context.Background(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{BindingSpec: BindingSpec, Content: openbindings.TextContent(dualSpec(sseSrv.URL))},
		Ref:    "#/paths/~1dual/get",
	})
	events, ierr := driveOutputs(context.Background(), call, nil)
	if ierr != nil {
		t.Fatalf("stream: %v", ierr)
	}
	if len(events) != 2 || events[0] != "one" || events[1] != "two" {
		t.Fatalf("events = %v, want [one two]", events)
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

// WHATWG extraction: comment-only, empty-data, and event/id-only events emit
// nothing; an incomplete final event is discarded.
func TestInvoke_SSEEmptyEventsEmitNothing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, ": comment only\n\n")           // comment-only: nothing
		_, _ = io.WriteString(w, "event: tick\nid: 7\n\n")       // fields-only: nothing
		_, _ = io.WriteString(w, "data:\n\n")                    // empty-data: nothing
		_, _ = io.WriteString(w, "data: real\n\n")               // emits "real"
		_, _ = io.WriteString(w, "data: incomplete-final-event") // no blank line: discarded
	}))
	defer srv.Close()

	events, ierr := driveOutputs(context.Background(), sseCall(srv), nil)
	if ierr != nil {
		t.Fatalf("stream: %v", ierr)
	}
	if len(events) != 1 || events[0] != "real" {
		t.Fatalf("events = %v, want exactly [real]", events)
	}
}

// CRLF and lone-CR line endings are valid event-stream line terminators.
func TestInvoke_SSECarriageReturnLineEndings(t *testing.T) {
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
	if len(events) != 2 || events[0] != "crlf" || events[1] != "cr" {
		t.Fatalf("events = %v, want [crlf cr]", events)
	}
}

// ---------------------------------------------------------------------------
// OAPI-P-07 — decode: charset handling and the 204 rule
// ---------------------------------------------------------------------------

func TestDecode_CharsetHandling(t *testing.T) {
	// latin-1 declared: bytes transcode.
	dec := decodeByContentType("text/plain; charset=iso-8859-1")
	out, err := dec(openbindings.InvokeSite{}, openbindings.RawResult{Body: []byte{0xe9}})
	if err != nil || out != "é" {
		t.Errorf("latin-1 = (%v, %v), want é", out, err)
	}

	// Invalid UTF-8 under the default charset is a loud decode error.
	dec = decodeByContentType("text/plain")
	if _, err := dec(openbindings.InvokeSite{}, openbindings.RawResult{Body: []byte{0xff, 0xfe}}); err == nil {
		t.Error("invalid UTF-8 must be a loud decode error, never mojibake")
	}

	// An undecodable declared charset is loud (override at the decode point).
	dec = decodeByContentType("text/plain; charset=shift_jis")
	if _, err := dec(openbindings.InvokeSite{}, openbindings.RawResult{Body: []byte("x")}); err == nil {
		t.Error("an unsupported charset must be a loud decode error")
	}

	// Empty body (204 included) yields null on every lane.
	dec = decodeByContentType("application/json")
	out, err = dec(openbindings.InvokeSite{}, openbindings.RawResult{Body: nil})
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
	    "responses": {"200": {"description": "ok"}}
	  }}},
	  "components": {"securitySchemes": {
	    "cookieKey": {"type": "apiKey", "in": "cookie", "name": "auth_token"}
	  }}
	}`, srv.URL)

	call := NewInvoker().InvokeBinding(context.Background(), &openbindings.BindingInvocationArgs{
		Source:  openbindings.InvocationSource{BindingSpec: BindingSpec, Content: openbindings.TextContent(spec)},
		Ref:     "#/paths/~1sess/get",
		Context: map[string]any{"apiKeys": map[string]any{"cookieKey": "secret"}},
	})
	if _, ierr := driveSingle(t, call, map[string]any{"zeta": "z", "alpha": "a"}); ierr != nil {
		t.Fatalf("invoke: %s: %s", ierr.Code, ierr.Message)
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

			call := NewInvoker().InvokeBinding(context.Background(), &openbindings.BindingInvocationArgs{
				Source:  openbindings.InvocationSource{BindingSpec: BindingSpec, Content: openbindings.TextContent(spec)},
				Ref:     "#/paths/~1x/get",
				Context: map[string]any{"apiKey": "cred"},
			})
			_, ierr := driveSingle(t, call, map[string]any{tc.param: "caller-value"})
			if ierr == nil || ierr.Code != openbindings.ErrCodeValidationFailed {
				t.Fatalf("expected the OAPI-P-10 collision refusal, got %v", ierr)
			}
			if !strings.Contains(ierr.Message, "OAPI-P-10") {
				t.Errorf("message should cite the rule, got %q", ierr.Message)
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
	  "paths": {"/ping": {"get": {"operationId": "ping", "responses": {"200": {"description": "ok"}}}}}
	}`
	call := NewInvoker().InvokeBinding(context.Background(), &openbindings.BindingInvocationArgs{
		Source:  openbindings.InvocationSource{BindingSpec: BindingSpec, Content: openbindings.TextContent(spec)},
		Ref:     "#/paths/~1ping/get",
		Context: map[string]any{"configuration": map[string]any{"server": map[string]any{"baseUrl": srv.URL}}},
	})
	if _, ierr := driveSingle(t, call, nil); ierr != nil {
		t.Fatalf("invoke: %s: %s", ierr.Code, ierr.Message)
	}
	if hits != 1 {
		t.Errorf("configured server hits = %d, want 1", hits)
	}
}
