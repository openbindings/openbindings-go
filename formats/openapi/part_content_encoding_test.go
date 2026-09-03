package openapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"os"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

// partContentEncodingCasesDigest pins the frozen twin case table. The identical
// file is executed by openbindings-go/formats/openapi, by
// openapi-client/go, by openapi-client/typescript, and by
// openbindings-ts/packages/openapi against that package's BUILT dist; changing
// it in one engine without the others fails here.
const partContentEncodingCasesDigest = "a8812162d3d51b1d869821a733e29b47e240f8a8e00319c721d3fbdcdf52fa64"

type partContentEncodingTable struct {
	Comment string                    `json:"$comment"`
	Bases   map[string]string         `json:"bases"`
	Cases   []partContentEncodingCase `json:"cases"`
}

type partContentEncodingCase struct {
	Name                string `json:"name"`
	OpenAPI             string `json:"openapi"`
	Media               string `json:"media"`
	Lane                string `json:"lane"`
	Kind                string `json:"kind"`
	Keyword             string `json:"keyword"`
	DeclaresString      bool   `json:"declaresString"`
	EncodingContentType string `json:"encodingContentType"`
	PropertyName        string `json:"propertyName"`
	PropertySchema      any    `json:"propertySchema"`
	Value               any    `json:"value"`
	Expect              string `json:"expect"`
	BasisKey            string `json:"basisKey"`
}

func loadPartContentEncodingTable(t *testing.T) partContentEncodingTable {
	t.Helper()
	raw, err := os.ReadFile("testdata/part-content-encoding-cases.json")
	if err != nil {
		t.Fatalf("read case table: %v", err)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != partContentEncodingCasesDigest {
		t.Fatalf("case table digest = %s, want %s (the table is shared byte-for-byte with the twin engines)", got, partContentEncodingCasesDigest)
	}
	var table partContentEncodingTable
	if err := json.Unmarshal(raw, &table); err != nil {
		t.Fatalf("parse case table: %v", err)
	}
	if len(table.Cases) == 0 {
		t.Fatal("case table is empty")
	}
	return table
}

// partContentEncodingDocument renders one case as a WHOLE OpenAPI document. The
// document, and not a hand-built openapi3.MediaType, is what the engine has to
// be given: the shipped loader normalizes the raw tree before kin-openapi's
// typed model exists, and a harness that unmarshals a MediaType directly
// measures an engine the project does not ship.
func partContentEncodingDocument(t *testing.T, c partContentEncodingCase) []byte {
	t.Helper()
	media := map[string]any{
		"schema": map[string]any{
			"type":       "object",
			"properties": map[string]any{c.PropertyName: c.PropertySchema},
		},
	}
	if c.EncodingContentType != "" {
		media["encoding"] = map[string]any{
			c.PropertyName: map[string]any{"contentType": c.EncodingContentType},
		}
	}
	document := map[string]any{
		"openapi": c.OpenAPI,
		"info":    map[string]any{"title": "part content encoding case table", "version": "1.0.0"},
		"paths": map[string]any{
			"/form": map[string]any{
				"post": map[string]any{
					"operationId": "postForm",
					"requestBody": map[string]any{
						"required": true,
						"content":  map[string]any{c.Media: media},
					},
					"responses": map[string]any{"200": map[string]any{"description": "ok"}},
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

// partContentEncodingDecision runs one cell exactly as the twin engines run it.
// Refusal messages are each implementation's own surface, so only the decision
// itself crosses the twin boundary.
func partContentEncodingDecision(t *testing.T, c partContentEncodingCase) string {
	t.Helper()
	doc, err := loadDocument("", json.RawMessage(partContentEncodingDocument(t, c)))
	if err != nil {
		return "source-refused"
	}
	item := doc.Paths.Find("/form")
	if item == nil || item.Post == nil {
		t.Fatalf("%s: loaded document has no form operation", c.Name)
	}
	plans, err := planRequestBodiesFor(doc, item.Post, BindingSpec)
	if err != nil {
		return "refused"
	}
	for _, plan := range plans {
		if len(plan.propertyMedia) > 0 {
			return "missing-required-choice"
		}
	}
	media := item.Post.RequestBody.Value.Content[c.Media]
	if media == nil {
		t.Fatalf("%s: loaded document has no %s media type", c.Name, c.Media)
	}
	return "admitted;emit=" + partContentEncodingEmission(doc, media, c)
}

func partContentEncodingEmission(doc *openapi3.T, media *openapi3.MediaType, c partContentEncodingCase) string {
	fields := map[string]any{c.PropertyName: c.Value}
	if c.Media == "application/x-www-form-urlencoded" {
		encoded, err := buildURLEncodedBodyForRevision(doc, media, fields, BindingSpec)
		if err != nil {
			return "error"
		}
		if encoded == "" {
			return "elided"
		}
		return encoded
	}
	reader, contentType, err := buildMultipartBodyForRevision(doc, media, fields, BindingSpec)
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
		// Content-Transfer-Encoding is deliberately outside the shared
		// rendering: the TypeScript engine writes parts through FormData and
		// cannot emit it at all. The dedicated transfer-header test pins the
		// 3.1 no-emission rule on this side.
		rendered = append(rendered, part.Header.Get("Content-Type")+":"+string(body))
		part.Close()
	}
	if len(rendered) == 0 {
		return "elided"
	}
	return strings.Join(rendered, "&")
}

func TestPartContentEncodingCaseTable(t *testing.T) {
	table := loadPartContentEncodingTable(t)
	if len(table.Cases) != 768 {
		t.Fatalf("case table has %d cells, want 768", len(table.Cases))
	}
	for _, c := range table.Cases {
		if got := partContentEncodingDecision(t, c); got != c.Expect {
			t.Errorf("%s: decision = %q, want %q\nbasis: %s", c.Name, got, c.Expect, table.Bases[c.BasisKey])
		}
	}
}

// TestContentEncodingChangesOnlyTheDeclaredStringRow states the claim the table
// exists for as a claim in its own right, rather than leaving it implicit in
// 768 cells. It is EXECUTED, not read off the table's own expectations: the
// claim is about the engine, so reverting the implementation has to turn this
// red too.
//
// Adding `contentEncoding` to a part changes its decision ONLY where the part
// declares `type: string` — three of 192 pairs, being multipart on each of the
// three accepted 3.1 editions with no Encoding Object contentType, where the
// text/plain row gives way to application/octet-stream. Adding
// `contentMediaType` changes NO decision anywhere, which is 3.1.1 and 3.1.2
// saying "the Encoding Object's contentType defaulting rules do not take the
// Schema Object's contentMediaType into account" and [JSON Schema 2020-12]
// Section 8.4 conditioning the keyword on a string instance.
func TestContentEncodingChangesOnlyTheDeclaredStringRow(t *testing.T) {
	table := loadPartContentEncodingTable(t)
	plain := map[string]partContentEncodingCase{}
	for _, c := range table.Cases {
		if c.Keyword == "plain" {
			plain[c.OpenAPI+"|"+c.Media+"|"+c.Kind+"|"+c.EncodingContentType] = c
		}
	}
	encodingPairs, encodingChanged := 0, 0
	mediaTypePairs, mediaTypeChanged := 0, 0
	for _, c := range table.Cases {
		if c.Keyword != "contentEncoding" && c.Keyword != "contentMediaType" {
			continue
		}
		base, ok := plain[c.OpenAPI+"|"+c.Media+"|"+c.Kind+"|"+c.EncodingContentType]
		if !ok {
			t.Fatalf("%s: no |plain twin", c.Name)
		}
		changed := partContentEncodingDecision(t, c) != partContentEncodingDecision(t, base)
		if c.Keyword == "contentEncoding" {
			encodingPairs++
			if changed {
				encodingChanged++
				if !c.DeclaresString {
					t.Errorf("%s: contentEncoding changed the decision on a part that declares no string", c.Name)
				}
			}
			continue
		}
		mediaTypePairs++
		if changed {
			mediaTypeChanged++
			t.Errorf("%s: contentMediaType changed the decision, which no accepted edition's defaulting rules do", c.Name)
		}
	}
	if encodingPairs != 192 || mediaTypePairs != 192 {
		t.Fatalf("pairs = %d contentEncoding / %d contentMediaType, want 192 each", encodingPairs, mediaTypePairs)
	}
	if encodingChanged != 3 {
		t.Errorf("contentEncoding changed %d of 192 decisions, want 3", encodingChanged)
	}
	if mediaTypeChanged != 0 {
		t.Errorf("contentMediaType changed %d of 192 decisions, want 0", mediaTypeChanged)
	}
}

// OAS 3.1 contentEncoding describes the artifact string; it never instructs
// this binding to synthesize a Content-Transfer-Encoding part header.
func TestRevision3PartContentEncodingEmitsNoTransferHeader(t *testing.T) {
	for _, test := range []struct {
		name   string
		schema map[string]any
		value  any
		want   string
	}{
		{"declared string", map[string]any{"type": "string", "contentEncoding": "base64"}, "abc", ""},
		{"declared integer", map[string]any{"type": "integer", "contentEncoding": "base64"}, 7, ""},
		{"declared object", map[string]any{"type": "object", "contentEncoding": "base64"}, map[string]any{"k": "v"}, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := partContentEncodingCase{
				Name: test.name, OpenAPI: "3.1.1", Media: "multipart/form-data",
				PropertyName: "p", PropertySchema: test.schema, Value: test.value,
			}
			doc, err := loadDocument("", json.RawMessage(partContentEncodingDocument(t, c)))
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			media := doc.Paths.Find("/form").Post.RequestBody.Value.Content[c.Media]
			reader, contentType, err := buildMultipartBodyForRevision(doc, media, map[string]any{"p": test.value}, BindingSpec)
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			_, params, err := mime.ParseMediaType(contentType)
			if err != nil {
				t.Fatalf("content type: %v", err)
			}
			part, err := multipart.NewReader(reader, params["boundary"]).NextRawPart()
			if err != nil {
				t.Fatalf("read part: %v", err)
			}
			if got := part.Header.Get("Content-Transfer-Encoding"); got != test.want {
				t.Fatalf("Content-Transfer-Encoding = %q, want %q", got, test.want)
			}
		})
	}
}
