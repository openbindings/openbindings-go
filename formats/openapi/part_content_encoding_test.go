package openapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
// openapi-client/typescript/src, and by openbindings-ts/packages/openapi
// against that package's BUILT dist; changing it in one engine without the
// others fails here.
const partContentEncodingCasesDigest = "06647fe967dbc2d7f6739fa718b79c1f7bb45bcc8ccc7faf4113836fb469b605"

type partContentEncodingTable struct {
	Comment string                    `json:"$comment"`
	Cases   []partContentEncodingCase `json:"cases"`
}

type partContentEncodingCase struct {
	Name                string         `json:"name"`
	OpenAPI             string         `json:"openapi"`
	Media               string         `json:"media"`
	Part                string         `json:"part"`
	ContentEncoding     bool           `json:"contentEncoding"`
	PropertySchema      map[string]any `json:"propertySchema"`
	EncodingContentType *string        `json:"encodingContentType"`
	PropertyName        string         `json:"propertyName"`
	Value               any            `json:"value"`
	Expect              string         `json:"expect"`
	Basis               string         `json:"basis"`
}

func loadPartContentEncodingTable(t *testing.T) []partContentEncodingCase {
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
	return table.Cases
}

// partContentEncodingDocument renders one case as a WHOLE OpenAPI document.
// The document, and not a hand-built openapi3.MediaType, is what the engine has
// to be given: the shipped loader normalizes the raw tree before kin-openapi's
// typed model exists, and a harness that unmarshals a MediaType directly
// measures an engine the project does not ship.
func partContentEncodingDocument(t *testing.T, c partContentEncodingCase) []byte {
	t.Helper()
	mediaObject := map[string]any{
		"schema": map[string]any{
			"type":       "object",
			"properties": map[string]any{c.PropertyName: c.PropertySchema},
		},
	}
	if c.EncodingContentType != nil {
		mediaObject["encoding"] = map[string]any{
			c.PropertyName: map[string]any{"contentType": *c.EncodingContentType},
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
						"content":  map[string]any{c.Media: mediaObject},
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

// partContentEncodingDecision renders one cell exactly as the twin engines
// render it. Refusal messages are each implementation's own surface, so only
// the decision itself crosses the twin boundary.
func partContentEncodingDecision(t *testing.T, c partContentEncodingCase) string {
	t.Helper()
	doc, err := loadDocument("", json.RawMessage(partContentEncodingDocument(t, c)))
	if err != nil {
		t.Fatalf("%s: load document: %v", c.Name, err)
	}
	item := doc.Paths.Find("/form")
	if item == nil || item.Post == nil {
		t.Fatalf("%s: loaded document has no form operation", c.Name)
	}
	if _, err := planRequestBodiesFor(doc, item.Post, BindingSpec); err != nil {
		return "refused"
	}
	media := item.Post.RequestBody.Value.Content[c.Media]
	if media == nil {
		t.Fatalf("%s: loaded document has no %s media type", c.Name, c.Media)
	}
	return "admitted;emit=" + partContentEncodingEmission(t, doc, media, c)
}

func partContentEncodingEmission(t *testing.T, doc *openapi3.T, media *openapi3.MediaType, c partContentEncodingCase) string {
	t.Helper()
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
		rendered = append(rendered, part.Header.Get("Content-Type")+":"+string(body))
		part.Close()
	}
	if len(rendered) == 0 {
		return "elided"
	}
	return strings.Join(rendered, "&")
}

func TestPartContentEncodingCaseTable(t *testing.T) {
	for _, c := range loadPartContentEncodingTable(t) {
		t.Run(c.Name, func(t *testing.T) {
			if got := partContentEncodingDecision(t, c); got != c.Expect {
				t.Fatalf("%s: decision = %q, want %q\nbasis: %s", c.Name, got, c.Expect, c.Basis)
			}
		})
	}
}

// TestContentEncodingChangesOnlyTheStringRow states the invariant the table
// exists for as a claim in its own right, rather than leaving it implicit in
// 216 cells: with no Encoding Object, adding `contentEncoding` to a part
// schema changes the part's decision ONLY where an accepted edition says it
// does.
//
//   - 3.1 line: only `type: string` (the encoded-string row every 3.1 edition
//     states) and the typeless part (whose 3.1.1/3.1.2 row is
//     application/octet-stream, a boundary this revision does not define —
//     open item F-O1-10 item 2). Every declared non-string kind decides
//     identically with and without the keyword.
//   - 3.0 line: nothing at all. `contentEncoding` is not in that line's
//     Schema Object dialect.
func TestContentEncodingChangesOnlyTheStringRow(t *testing.T) {
	// EXECUTED, not read off the table's own expectations: the claim is about
	// the engine, so a revert of the implementation has to turn this red too.
	decisions := map[string]string{}
	for _, c := range loadPartContentEncodingTable(t) {
		if c.EncodingContentType != nil {
			continue
		}
		decisions[c.Name] = partContentEncodingDecision(t, c)
	}
	editions := []string{"3.0.0", "3.0.1", "3.0.2", "3.0.3", "3.0.4", "3.1.0", "3.1.1", "3.1.2"}
	kinds := []string{"string", "integer", "number", "boolean", "typeless", "array-of-string"}
	pairs, moved := 0, 0
	for _, edition := range editions {
		is31 := strings.HasPrefix(edition, "3.1")
		for _, media := range []string{"multipart", "urlencoded"} {
			for _, kind := range kinds {
				plain, plainOK := decisions[fmt.Sprintf("%s|%s|%s|plain", edition, media, kind)]
				encoded, encodedOK := decisions[fmt.Sprintf("%s|%s|%s|contentEncoding", edition, media, kind)]
				if !plainOK || !encodedOK {
					t.Fatalf("case table is missing the %s|%s|%s pair", edition, media, kind)
				}
				pairs++
				mayMove := is31 && (kind == "string" || kind == "typeless")
				if plain == encoded {
					continue
				}
				moved++
				if !mayMove {
					t.Errorf("%s|%s|%s: contentEncoding changed the decision (%q -> %q), but no accepted edition keys that kind's row on it",
						edition, media, kind, plain, encoded)
				}
			}
		}
	}
	if pairs != 96 {
		t.Fatalf("compared %d pairs, want 96 (8 editions x 2 media x 6 kinds)", pairs)
	}
	// multipart `string` on each 3.1 edition, plus the typeless cell on both
	// media on each 3.1 edition. On the urlencoded lane a string part carries
	// its characters either way, so the row is not observable there.
	if moved != 9 {
		t.Fatalf("contentEncoding moved %d of %d pairs, want 9", moved, pairs)
	}
	for _, edition := range []string{"3.1.0", "3.1.1", "3.1.2"} {
		plain := decisions[edition+"|multipart|string|plain"]
		encoded := decisions[edition+"|multipart|string|contentEncoding"]
		if !strings.Contains(plain, "text/plain") || !strings.Contains(encoded, "application/octet-stream") {
			t.Fatalf("%s multipart string: plain = %q, encoded = %q; the encoded-string row is not being exercised", edition, plain, encoded)
		}
	}
}
