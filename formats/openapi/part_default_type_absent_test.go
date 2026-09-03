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

// partDefaultTypeAbsentCasesDigest pins the frozen twin case table. The
// identical file is executed by openbindings-go/formats/openapi, by
// openapi-client/typescript, and by openbindings-ts/packages/openapi against
// that package's BUILT dist; changing it in one engine without the others
// fails here.
const partDefaultTypeAbsentCasesDigest = "0b7d98d4aaa2372f538b27857619a3b996e2967157ca81fc65816a03ed75a3f2"

type partDefaultTypeAbsentTable struct {
	Comment string                      `json:"$comment"`
	Cases   []partDefaultTypeAbsentCase `json:"cases"`
}

type partDefaultTypeAbsentCase struct {
	Name           string `json:"name"`
	OpenAPI        string `json:"openapi"`
	Media          string `json:"media"`
	Lane           string `json:"lane"`
	Kind           string `json:"kind"`
	DeclaresType   bool   `json:"declaresType"`
	PropertyName   string `json:"propertyName"`
	PropertySchema any    `json:"propertySchema"`
	Value          any    `json:"value"`
	Expect         string `json:"expect"`
	Basis          string `json:"basis"`
}

func loadPartDefaultTypeAbsentTable(t *testing.T) []partDefaultTypeAbsentCase {
	t.Helper()
	raw, err := os.ReadFile("testdata/part-default-type-absent-cases.json")
	if err != nil {
		t.Fatalf("read case table: %v", err)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != partDefaultTypeAbsentCasesDigest {
		t.Fatalf("case table digest = %s, want %s (the table is shared byte-for-byte with the twin engines)", got, partDefaultTypeAbsentCasesDigest)
	}
	var table partDefaultTypeAbsentTable
	if err := json.Unmarshal(raw, &table); err != nil {
		t.Fatalf("parse case table: %v", err)
	}
	if len(table.Cases) == 0 {
		t.Fatal("case table is empty")
	}
	return table.Cases
}

// partDefaultTypeAbsentDocument renders one case as a WHOLE OpenAPI document.
// The document, and not a hand-built openapi3.MediaType, is what the engine
// has to be given: the shipped loader normalizes the raw tree before
// kin-openapi's typed model exists, and a harness that unmarshals a MediaType
// directly measures an engine the project does not ship.
func partDefaultTypeAbsentDocument(t *testing.T, c partDefaultTypeAbsentCase) []byte {
	t.Helper()
	document := map[string]any{
		"openapi": c.OpenAPI,
		"info":    map[string]any{"title": "type-absent part default case table", "version": "1.0.0"},
		"paths": map[string]any{
			"/form": map[string]any{
				"post": map[string]any{
					"operationId": "postForm",
					"requestBody": map[string]any{
						"required": true,
						"content": map[string]any{
							c.Media: map[string]any{
								"schema": map[string]any{
									"type":       "object",
									"properties": map[string]any{c.PropertyName: c.PropertySchema},
								},
							},
						},
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

// partDefaultTypeAbsentDecision renders one cell exactly as the twin engines
// render it. Refusal messages are each implementation's own surface, so only
// the decision itself crosses the twin boundary.
func partDefaultTypeAbsentDecision(t *testing.T, c partDefaultTypeAbsentCase) string {
	t.Helper()
	doc, err := loadDocument("", json.RawMessage(partDefaultTypeAbsentDocument(t, c)))
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
	if plansRequirePropertyMedia(plans) {
		return "missing-required-choice"
	}
	media := item.Post.RequestBody.Value.Content[c.Media]
	if media == nil {
		t.Fatalf("%s: loaded document has no %s media type", c.Name, c.Media)
	}
	return "admitted;emit=" + partDefaultTypeAbsentEmission(t, doc, media, c)
}

func partDefaultTypeAbsentEmission(t *testing.T, doc *openapi3.T, media *openapi3.MediaType, c partDefaultTypeAbsentCase) string {
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

func TestPartDefaultTypeAbsentCaseTable(t *testing.T) {
	for _, c := range loadPartDefaultTypeAbsentTable(t) {
		if got := partDefaultTypeAbsentDecision(t, c); got != c.Expect {
			t.Errorf("%s: decision = %q, want %q\nbasis: %s", c.Name, got, c.Expect, c.Basis)
		}
	}
}

// TestTypeAbsentPartUsesTheFamilyDecision states the corrected family split
// independently of the table expectations: 3.1 typeless multipart parts are
// admitted, while 3.0 typeless multipart parts remain represented and require
// propertyMedia. Boolean schemas are source-invalid on 3.0, and this rule does
// not invent a typeless raw lane for urlencoded bodies.
func TestTypeAbsentPartUsesTheFamilyDecision(t *testing.T) {
	// EXECUTED, not read off the table's own expectations: the claim is about
	// the engine, so a revert of the implementation has to turn this red too.
	checked := 0
	for _, c := range loadPartDefaultTypeAbsentTable(t) {
		got := partDefaultTypeAbsentDecision(t, c)
		want := ""
		switch {
		case c.DeclaresType:
			want = "admitted"
		case c.Media == "multipart/form-data" && strings.HasPrefix(c.OpenAPI, "3.1"):
			want = "admitted"
		case c.Media == "multipart/form-data" && strings.HasPrefix(c.OpenAPI, "3.0") && c.Kind != "boolean-literal-true":
			want = "missing-required-choice"
		case c.Media == "application/x-www-form-urlencoded" && strings.HasPrefix(c.OpenAPI, "3.0") && c.Kind != "boolean-literal-true":
			// OA-F8: the 3.0 line states no row for a typeless declaration on
			// either content-based lane, and openbindings.openapi-3.0@1 §9.3
			// requires propertyMedia "on the content-based form-urlencoded
			// path and for a multipart part alike". The 3.1 line keeps its
			// refusal on this lane on its own ground (an octet-stream row with
			// no boundary defined here).
			want = "missing-required-choice"
		default:
			want = "refused"
		}
		decision := "refused"
		if strings.HasPrefix(got, "admitted;") {
			decision = "admitted"
		} else if got == "missing-required-choice" {
			decision = got
		}
		if decision != want {
			t.Errorf("%s: decision class = %q, want %q (decision %q)\nbasis: %s", c.Name, decision, want, got, c.Basis)
		}
		checked++
	}
	if checked != 128 {
		t.Fatalf("checked %d cells, want 128", checked)
	}
}

func plansRequirePropertyMedia(plans []*bodyPlan) bool {
	for _, plan := range plans {
		if plan != nil && len(plan.propertyMedia) > 0 {
			return true
		}
	}
	return false
}
