package openapi

// Executes the shared `format: byte` carriage case table (stage-3 block 5,
// escalation M4). The identical file, at the identical digest, is executed by
// openbindings-go/formats/openapi, by openapi-client/typescript, and by
// openbindings-ts/packages/openapi against that package's BUILT dist. Three
// request positions ride one table because one declaration governs all three:
// the whole body of an exact non-JSON, non-form selection, a multipart part,
// and an urlencoded property.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"strings"
	"testing"

	"os"

	"github.com/getkin/kin-openapi/openapi3"
)

const formByteCarriageCasesDigest = "37b2cf5eece416504551780469e934a17627b90f1be3e282c7826308ca8d4c0a"

type formByteCarriageCase struct {
	Name                string         `json:"name"`
	OpenAPI             string         `json:"openapi"`
	Lane                string         `json:"lane"`
	Media               string         `json:"media"`
	Kind                string         `json:"kind"`
	PropertyName        string         `json:"propertyName"`
	Schema              map[string]any `json:"schema"`
	Value               string         `json:"value"`
	EncodingContentType string         `json:"encodingContentType"`
	Expect              string         `json:"expect"`
	Basis               string         `json:"basis"`
}

func loadFormByteCarriageTable(t *testing.T) []formByteCarriageCase {
	t.Helper()
	raw, err := os.ReadFile("testdata/format-byte-carriage-cases.json")
	if err != nil {
		t.Fatalf("read case table: %v", err)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != formByteCarriageCasesDigest {
		t.Fatalf("case table digest = %s, want %s (the table is shared byte-for-byte with the twin engines)", got, formByteCarriageCasesDigest)
	}
	var table struct {
		Cases []formByteCarriageCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &table); err != nil {
		t.Fatalf("parse case table: %v", err)
	}
	if len(table.Cases) == 0 {
		t.Fatal("case table is empty")
	}
	return table.Cases
}

// formByteCarriageDocument renders one case as a WHOLE OpenAPI document: the
// shipped loader normalizes the raw tree before the typed model exists, so a
// harness that hands the engine a hand-built MediaType measures an engine the
// project does not ship.
func formByteCarriageDocument(t *testing.T, c formByteCarriageCase) []byte {
	t.Helper()
	mediaObject := map[string]any{}
	if c.Lane == "body" {
		mediaObject["schema"] = c.Schema
	} else {
		mediaObject["schema"] = map[string]any{
			"type":       "object",
			"properties": map[string]any{c.PropertyName: c.Schema},
		}
		if c.EncodingContentType != "" {
			mediaObject["encoding"] = map[string]any{
				c.PropertyName: map[string]any{"contentType": c.EncodingContentType},
			}
		}
	}
	document := map[string]any{
		"openapi": c.OpenAPI,
		"info":    map[string]any{"title": "format: byte carriage case table", "version": "1.0.0"},
		"paths": map[string]any{
			"/send": map[string]any{
				"post": map[string]any{
					"operationId": "send",
					"requestBody": map[string]any{
						"required": true,
						"content":  map[string]any{c.Media: mediaObject},
					},
					"responses": map[string]any{"204": map[string]any{"description": "ok"}},
				},
			},
		},
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("%s: marshal document: %v", c.Name, err)
	}
	return encoded
}

func formByteCarriageDecision(t *testing.T, c formByteCarriageCase) string {
	t.Helper()
	doc, err := loadDocument("", json.RawMessage(formByteCarriageDocument(t, c)))
	if err != nil {
		return "source-refused"
	}
	item := doc.Paths.Find("/send")
	if item == nil || item.Post == nil {
		t.Fatalf("%s: loaded document has no operation", c.Name)
	}
	plans, err := planRequestBodiesFor(doc, item.Post, BindingSpec)
	if err != nil || len(plans) == 0 {
		return "refused"
	}
	media := item.Post.RequestBody.Value.Content[c.Media]
	if media == nil {
		t.Fatalf("%s: loaded document has no %s media type", c.Name, c.Media)
	}
	return "admitted;emit=" + formByteCarriageEmission(t, doc, media, plans[0], c)
}

func formByteCarriageEmission(t *testing.T, doc *openapi3.T, media *openapi3.MediaType, plan *bodyPlan, c formByteCarriageCase) string {
	t.Helper()
	switch c.Lane {
	case "body":
		reader, contentType, err := buildRequestBody(doc, plan, &routedInput{bodyValue: c.Value, bodySet: true})
		if err != nil {
			return "error"
		}
		body, err := io.ReadAll(reader)
		if err != nil {
			return "error"
		}
		return contentType + ":" + string(body)
	case "urlencoded":
		encoded, err := buildURLEncodedBodyForRevision(doc, media, map[string]any{c.PropertyName: c.Value}, BindingSpec)
		if err != nil {
			return "error"
		}
		if encoded == "" {
			return "elided"
		}
		return encoded
	}
	reader, contentType, err := buildMultipartBodyForRevision(doc, media, map[string]any{c.PropertyName: c.Value}, BindingSpec)
	if err != nil {
		return "error"
	}
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "error"
	}
	parts := multipart.NewReader(reader, params["boundary"])
	rendered := []string{}
	for {
		part, err := parts.NextRawPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "error"
		}
		body, err := io.ReadAll(part)
		if err != nil {
			return "error"
		}
		rendered = append(rendered, part.Header.Get("Content-Type")+":"+string(body))
		part.Close()
	}
	if len(rendered) == 0 {
		return "elided"
	}
	return strings.Join(rendered, "&")
}

func TestFormatByteCarriageCaseTable(t *testing.T) {
	for _, c := range loadFormByteCarriageTable(t) {
		t.Run(c.Name, func(t *testing.T) {
			if got := formByteCarriageDecision(t, c); got != c.Expect {
				t.Fatalf("%s: decision = %q, want %q\nbasis: %s", c.Name, got, c.Expect, c.Basis)
			}
		})
	}
}

// TestFormatByteEmitsNoContentTransferEncoding states M4's third half as a
// claim in its own right: §9.2 reads 3.0.4's `byte`-part/Encoding-Object-
// headers equivalence as declaration semantics, so no Content-Transfer-Encoding
// header is emitted unless the artifact itself declares that header. The
// shared table cannot carry the assertion — TypeScript inserts part headers
// after FormData has serialized — so each engine pins it here.
func TestFormatByteEmitsNoContentTransferEncoding(t *testing.T) {
	checked := 0
	for _, c := range loadFormByteCarriageTable(t) {
		if c.Lane != "multipart" || !strings.HasPrefix(c.Expect, "admitted;") {
			continue
		}
		checked++
		doc, err := loadDocument("", json.RawMessage(formByteCarriageDocument(t, c)))
		if err != nil {
			t.Fatalf("%s: load document: %v", c.Name, err)
		}
		media := doc.Paths.Find("/send").Post.RequestBody.Value.Content[c.Media]
		reader, contentType, err := buildMultipartBodyForRevision(doc, media, map[string]any{c.PropertyName: c.Value}, BindingSpec)
		if err != nil {
			t.Fatalf("%s: build body: %v", c.Name, err)
		}
		_, params, err := mime.ParseMediaType(contentType)
		if err != nil {
			t.Fatalf("%s: parse content type: %v", c.Name, err)
		}
		parts := multipart.NewReader(reader, params["boundary"])
		for {
			part, err := parts.NextRawPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("%s: read part: %v", c.Name, err)
			}
			if got := part.Header.Get("Content-Transfer-Encoding"); got != "" {
				t.Fatalf("%s: part carries Content-Transfer-Encoding %q; the artifact declares no such header", c.Name, got)
			}
			part.Close()
		}
	}
	if checked != 32 {
		t.Fatalf("checked %d admitted multipart cells, want 32 (every multipart cell in the table)", checked)
	}
}

func TestOpenAPI30ArtifactDeclaredBase64TransferHeader(t *testing.T) {
	document := func(values string) json.RawMessage {
		return json.RawMessage(`{
			"openapi":"3.0.4","info":{"title":"transfer","version":"1"},
			"paths":{"/send":{"post":{"requestBody":{"required":true,"content":{"multipart/form-data":{
				"schema":{"type":"object","properties":{"payload":{"type":"string","format":"byte"}}},
				"encoding":{"payload":{"headers":{"Content-Transfer-Encoding":{"schema":{"type":"string","enum":[` + values + `]}}}}}
			}}},"responses":{"204":{"description":"ok"}}}}}
		}`)
	}
	for _, testCase := range []struct {
		name, values, wantHeader string
		wantError                bool
	}{
		{name: "admits base64", values: `"base64"`, wantHeader: "base64"},
		{name: "disallows base64", values: `"quoted-printable"`, wantError: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			doc, err := loadDocument("", document(testCase.values))
			if err != nil {
				t.Fatal(err)
			}
			media := doc.Paths.Find("/send").Post.RequestBody.Value.Content["multipart/form-data"]
			reader, contentType, err := buildMultipartBodyForRevision(doc, media, map[string]any{"payload": "YWJj"}, BindingSpecOpenAPI30)
			if testCase.wantError {
				if err == nil || !strings.Contains(err.Error(), "disallows base64") {
					t.Fatalf("transfer-header error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			_, params, _ := mime.ParseMediaType(contentType)
			part, err := multipart.NewReader(reader, params["boundary"]).NextRawPart()
			if err != nil {
				t.Fatal(err)
			}
			if got := part.Header.Get("Content-Transfer-Encoding"); got != testCase.wantHeader {
				t.Fatalf("Content-Transfer-Encoding = %q, want %q", got, testCase.wantHeader)
			}
		})
	}
}

func TestOpenAPI30DescriptiveEncodingHeaderDoesNotEmit(t *testing.T) {
	doc, err := loadDocument("", json.RawMessage(`{
		"openapi":"3.0.4","info":{"title":"headers","version":"1"},
		"paths":{"/send":{"post":{"requestBody":{"required":true,"content":{"multipart/form-data":{
			"schema":{"type":"object","properties":{"payload":{"type":"string"}}},
			"encoding":{"payload":{"headers":{"X-Part-Meta":{"schema":{"type":"string"}}}}}
		}}},"responses":{"204":{"description":"ok"}}}}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	media := doc.Paths.Find("/send").Post.RequestBody.Value.Content["multipart/form-data"]
	reader, contentType, err := buildMultipartBodyForRevision(doc, media, map[string]any{"payload": "hello"}, BindingSpecOpenAPI30)
	if err != nil {
		t.Fatal(err)
	}
	_, params, _ := mime.ParseMediaType(contentType)
	part, err := multipart.NewReader(reader, params["boundary"]).NextRawPart()
	if err != nil {
		t.Fatal(err)
	}
	if got := part.Header.Get("X-Part-Meta"); got != "" {
		t.Fatalf("descriptive Encoding header emitted %q", got)
	}
}

// TestFormatByteIsNotBoundaryDecoded pins the value half against its nearest
// neighbour rather than in isolation: on the 3.0 line `binary` and `byte` take
// the SAME default part Content-Type and differ only in whether the caller's
// string is the OpenBindings canonical Base64 boundary. A regression that
// decoded a `byte` value would keep every content type intact.
func TestFormatByteIsNotBoundaryDecoded(t *testing.T) {
	seen := 0
	for _, c := range loadFormByteCarriageTable(t) {
		if !strings.HasPrefix(c.OpenAPI, "3.0") || c.Kind != "byte" || c.Lane == "urlencoded" {
			continue
		}
		seen++
		if !strings.HasSuffix(c.Expect, ":"+c.Value) {
			t.Fatalf("%s expects %q, which does not end in the artifact-encoded value %q", c.Name, c.Expect, c.Value)
		}
	}
	if seen != 10 {
		t.Fatalf("table carries %d 3.0-line byte cells outside the urlencoded lane, want 10", seen)
	}
}
