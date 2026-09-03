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

// arrayItemsPartDefaultCasesDigest pins the frozen twin case table. The
// identical file is executed by openbindings-go/formats/openapi and by
// openapi-client/typescript/src; changing it in one engine without the others
// fails here.
const arrayItemsPartDefaultCasesDigest = "974af7c73561d7c54d03ce5fa39df694cb036ac9ce40d0050d9098b9d402444f"

type arrayItemsPartDefaultTable struct {
	Comment string                      `json:"$comment"`
	Cases   []arrayItemsPartDefaultCase `json:"cases"`
}

type arrayItemsPartDefaultCase struct {
	Name                string         `json:"name"`
	OpenAPI             string         `json:"openapi"`
	Media               string         `json:"media"`
	Items               string         `json:"items"`
	ItemsSchema         map[string]any `json:"itemsSchema"`
	PropertyName        string         `json:"propertyName"`
	Value               []any          `json:"value"`
	DerivedFrom         string         `json:"derivedFrom"`
	Expect              string         `json:"expect"`
	WriteLane           string         `json:"writeLane"`
	NonArrayValue       any            `json:"nonArrayValue"`
	NonArrayValueExpect string         `json:"nonArrayValueExpect"`
	Basis               string         `json:"basis"`
}

func loadArrayItemsPartDefaultTable(t *testing.T) []arrayItemsPartDefaultCase {
	t.Helper()
	raw, err := os.ReadFile("testdata/array-items-part-default-cases.json")
	if err != nil {
		t.Fatalf("read case table: %v", err)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != arrayItemsPartDefaultCasesDigest {
		t.Fatalf("case table digest = %s, want %s (the table is shared byte-for-byte with the two twin engines)", got, arrayItemsPartDefaultCasesDigest)
	}
	var table arrayItemsPartDefaultTable
	if err := json.Unmarshal(raw, &table); err != nil {
		t.Fatalf("parse case table: %v", err)
	}
	if len(table.Cases) == 0 {
		t.Fatal("case table is empty")
	}
	return table.Cases
}

// arrayItemsPartDefaultDocument renders one case as a WHOLE OpenAPI document.
// The document, and not a hand-built openapi3.MediaType, is what the engine has
// to be given: the shipped loader normalizes the raw tree before kin-openapi's
// typed model exists (ref_siblings.go's normalizeContent), and a harness that
// unmarshals a MediaType directly measures an engine the project does not ship.
func arrayItemsPartDefaultDocument(t *testing.T, c arrayItemsPartDefaultCase) []byte {
	t.Helper()
	document := map[string]any{
		"openapi": c.OpenAPI,
		"info":    map[string]any{"title": "array items part default case table", "version": "1.0.0"},
		"paths": map[string]any{
			"/form": map[string]any{
				"post": map[string]any{
					"operationId": "postForm",
					"requestBody": map[string]any{
						"required": true,
						"content": map[string]any{c.Media: map[string]any{
							"schema": map[string]any{
								"type": "object",
								"properties": map[string]any{
									c.PropertyName: map[string]any{"type": "array", "items": c.ItemsSchema},
								},
							},
						}},
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

// arrayItemsPartDefaultDecision renders one cell exactly as the twin engines
// render it. Refusal messages are each implementation's own surface, so only
// the decision itself crosses the twin boundary.
func arrayItemsPartDefaultDecision(t *testing.T, c arrayItemsPartDefaultCase) string {
	t.Helper()
	doc, err := loadDocument("", json.RawMessage(arrayItemsPartDefaultDocument(t, c)))
	if err != nil {
		t.Fatalf("%s: load document: %v", c.Name, err)
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
	return "admitted;emit=" + arrayItemsPartDefaultEmission(t, doc, media, c)
}

func arrayItemsPartDefaultEmission(t *testing.T, doc *openapi3.T, media *openapi3.MediaType, c arrayItemsPartDefaultCase) string {
	t.Helper()
	return arrayItemsPartDefaultRendering(t, doc, media, c, map[string]any{c.PropertyName: c.Value})
}

// arrayItemsPartDefaultRendering is the emission renderer with the supplied
// fields as a parameter, so one rendering serves both the table's declared
// array value and its nonArrayValue row.
func arrayItemsPartDefaultRendering(t *testing.T, doc *openapi3.T, media *openapi3.MediaType, c arrayItemsPartDefaultCase, fields map[string]any) string {
	t.Helper()
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
	rendered := make([]string, 0, len(c.Value))
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

// TestArrayItemsPartDefaultCaseTable executes the shared table.
//
// Its multipart expectations come from upstream authority text: every accepted
// OAS edition derives an array property's default part Content-Type from the
// ITEMS schema, never from the array. Its urlencoded content-lane cells are
// pinned AS OBSERVED and carry basis "OPEN" — see the table's own $comment and
// corpus-lab/OPENAPI-RUNTIME.md.
func TestArrayItemsPartDefaultCaseTable(t *testing.T) {
	for _, c := range loadArrayItemsPartDefaultTable(t) {
		t.Run(c.Name, func(t *testing.T) {
			if got := arrayItemsPartDefaultDecision(t, c); got != c.Expect {
				t.Fatalf("%s: decision = %q, want %q\nbasis: %s", c.Name, got, c.Expect, c.Basis)
			}
		})
	}
}

// TestArrayItemsPartDefaultRefusesANonArrayValue runs every multipart cell a
// second time with a value that is not an array.
//
// the openapi family specifications §9.2 says of the form lanes: "An invalid value or a
// member for which the resolved schema leaves no faithful form carriage refuses
// before dispatch." An array property's multipart carriage is one part per
// element, so a value with no elements has no faithful carriage at all. The
// engines used to fall through to the WHOLE-property schema and send one
// application/json part carrying the invalid value — a part the declaration
// never described, emitted silently.
func TestArrayItemsPartDefaultRefusesANonArrayValue(t *testing.T) {
	executed := 0
	for _, c := range loadArrayItemsPartDefaultTable(t) {
		if c.NonArrayValueExpect == "" {
			continue
		}
		executed++
		t.Run(c.Name, func(t *testing.T) {
			if c.Media != "multipart/form-data" {
				t.Fatalf("%s: nonArrayValueExpect is defined for the multipart lane only", c.Name)
			}
			doc, err := loadDocument("", json.RawMessage(arrayItemsPartDefaultDocument(t, c)))
			if err != nil {
				t.Fatalf("%s: load document: %v", c.Name, err)
			}
			item := doc.Paths.Find("/form")
			got := "refused"
			if plans, planErr := planRequestBodiesFor(doc, item.Post, BindingSpec); planErr == nil {
				if plansRequirePropertyMedia(plans) {
					got = "missing-required-choice"
				} else {
					media := item.Post.RequestBody.Value.Content[c.Media]
					fields := map[string]any{c.PropertyName: c.NonArrayValue}
					got = "admitted;emit=" + arrayItemsPartDefaultRendering(t, doc, media, c, fields)
				}
			}
			if got != c.NonArrayValueExpect {
				t.Fatalf("%s: decision with a non-array value = %q, want %q\nbasis: %s", c.Name, got, c.NonArrayValueExpect, c.Basis)
			}
		})
	}
	if executed != 24 {
		t.Fatalf("%d cells carry a nonArrayValueExpect decision, want 24 (every multipart cell)", executed)
	}
}

// TestArrayItemsPartDefaultStillExpandsAGoStringSlice pins the breadth of the
// value side of that rule in this language. Go's asArray recognizes []any and
// []string, which is what the routed JSON boundary produces; the refusal above
// must not turn a []string into a refusal.
func TestArrayItemsPartDefaultStillExpandsAGoStringSlice(t *testing.T) {
	c := arrayItemsPartDefaultCase{
		Name: "go []string", OpenAPI: "3.1.1", Media: "multipart/form-data",
		ItemsSchema: map[string]any{"type": "string"}, PropertyName: "p",
	}
	doc, err := loadDocument("", json.RawMessage(arrayItemsPartDefaultDocument(t, c)))
	if err != nil {
		t.Fatalf("load document: %v", err)
	}
	media := doc.Paths.Find("/form").Post.RequestBody.Value.Content[c.Media]
	fields := map[string]any{"p": []string{"a", "b"}}
	if got := arrayItemsPartDefaultRendering(t, doc, media, c, fields); got != "text/plain:a&text/plain:b" {
		t.Fatalf("[]string rendering = %q, want two repeated parts", got)
	}
}

// TestArrayItemsPartDefaultWriteLaneAgreesWithAdmission executes the
// body-encoding lane DIRECTLY, bypassing media admission, for every cell whose
// table row carries a `writeLane` decision.
//
// Admission and encoding are two lanes reading one declaration. Where admission
// refuses, nothing selects the plan, so the encoder's own answer is invisible on
// the wire — which is precisely why it went unasserted and why the two lanes
// were free to disagree. This test measures the encoder without admission in
// front of it, so "unreachable" is a property of the code rather than of what
// happens to run first.
func TestArrayItemsPartDefaultWriteLaneAgreesWithAdmission(t *testing.T) {
	executed := 0
	for _, c := range loadArrayItemsPartDefaultTable(t) {
		if c.WriteLane == "" {
			continue
		}
		executed++
		t.Run(c.Name, func(t *testing.T) {
			if c.Media != "multipart/form-data" {
				t.Fatalf("%s: writeLane is defined for the multipart body-encoding lane only", c.Name)
			}
			doc, err := loadDocument("", json.RawMessage(arrayItemsPartDefaultDocument(t, c)))
			if err != nil {
				t.Fatalf("%s: load document: %v", c.Name, err)
			}
			operation := doc.Paths.Find("/form").Post
			plans, planErr := planRequestBodiesFor(doc, operation, BindingSpec)
			if planErr == nil && plansRequirePropertyMedia(plans) {
				if c.WriteLane != "missing-required-choice" {
					t.Fatalf("%s: direct write requires propertyMedia, want %q", c.Name, c.WriteLane)
				}
				return
			}
			media := operation.RequestBody.Value.Content[c.Media]
			fields := map[string]any{c.PropertyName: c.Value}
			_, _, buildErr := buildMultipartBodyForRevision(doc, media, fields, BindingSpec)
			got := "admitted"
			if buildErr != nil {
				got = "refused"
			}
			if got != c.WriteLane {
				t.Fatalf("%s: write-lane decision = %q, want %q (admission decides %q)\nbasis: %s", c.Name, got, c.WriteLane, c.Expect, c.Basis)
			}
		})
	}
	if executed == 0 {
		t.Fatal("no case carries a writeLane decision; the shared table lost its write-lane cells")
	}
}

// TestArrayItemsPartDefaultNestedArrayCellsPinBothLanes fails if a multipart
// nested-array cell stops carrying its write-lane decision, so the guard cannot
// be removed by deleting a table field instead of a test.
func TestArrayItemsPartDefaultNestedArrayCellsPinBothLanes(t *testing.T) {
	seen := 0
	for _, c := range loadArrayItemsPartDefaultTable(t) {
		if c.Media != "multipart/form-data" || c.Items != "nested-array" {
			continue
		}
		seen++
		if c.Expect != "refused" || c.WriteLane != "refused" {
			t.Fatalf("%s: expect = %q, writeLane = %q; both lanes must refuse a nested array declaration", c.Name, c.Expect, c.WriteLane)
		}
	}
	if seen != 3 {
		t.Fatalf("table carries %d multipart nested-array cells, want 3 (one per edition branch)", seen)
	}
}

// TestArrayItemsTypelessItemsCellsPinBothLanes pins the 3.0 half of the
// corrected family split: the plan is represented, while a table harness with
// no configuration reaches the required propertyMedia choice in both probes.
func TestArrayItemsTypelessItemsCellsPinBothLanes(t *testing.T) {
	seen := 0
	for _, c := range loadArrayItemsPartDefaultTable(t) {
		if c.Media != "multipart/form-data" || c.Items != "unconstrained" || !strings.HasPrefix(c.OpenAPI, "3.0") {
			continue
		}
		seen++
		if c.Expect != "missing-required-choice" || c.WriteLane != "missing-required-choice" {
			t.Fatalf("%s: expect = %q, writeLane = %q; both probes must expose the missing propertyMedia choice", c.Name, c.Expect, c.WriteLane)
		}
	}
	if seen != 2 {
		t.Fatalf("table carries %d 3.0-line multipart typeless-items cells, want 2", seen)
	}
}

// TestArrayItemsPartDefaultIsKeyedOnItemsNotTheArray states the invariant the
// table exists for as a claim in its own right: on the multipart lane, an array
// property whose items schema is a string carries text/plain parts, which is
// the ITEMS default — and is exactly what an array-keyed default
// (application/json) would not produce.
func TestArrayItemsPartDefaultIsKeyedOnItemsNotTheArray(t *testing.T) {
	seen := 0
	for _, c := range loadArrayItemsPartDefaultTable(t) {
		if c.Media != "multipart/form-data" || c.DerivedFrom != "items" {
			continue
		}
		seen++
		if strings.Contains(c.Expect, "application/json") && c.Items != "object" {
			t.Fatalf("%s expects an application/json part, but its items schema is %q; only an object items schema reaches that OAS row", c.Name, c.Items)
		}
	}
	if seen != 24 {
		t.Fatalf("table carries %d items-derived multipart cells, want 24 (3 editions x 8 items schemas)", seen)
	}
}

// TestArrayItemsPartDefaultTableCoversEveryEditionAndItemsKind fails if the
// table stops exercising a branch the engines distinguish. Before this table the
// invariant was guarded only incidentally, by cells of tables about other
// questions, so a cell silently dropping out of this one is the failure mode
// this asserts against.
func TestArrayItemsPartDefaultTableCoversEveryEditionAndItemsKind(t *testing.T) {
	want := map[string]bool{}
	for _, edition := range []string{"3.0.3", "3.0.4", "3.1.1"} {
		for _, media := range []string{"multipart", "urlencoded"} {
			for _, items := range []string{
				"string", "integer", "object", "nested-array",
				"unconstrained", "string-base64", "union-type", "union-choice",
			} {
				want[fmt.Sprintf("%s|%s|%s", edition, media, items)] = true
			}
		}
	}
	for _, c := range loadArrayItemsPartDefaultTable(t) {
		if !want[c.Name] {
			t.Fatalf("case %q is not one of the covered cells", c.Name)
		}
		delete(want, c.Name)
	}
	if len(want) != 0 {
		t.Fatalf("case table is missing %d cells, including %v", len(want), firstKey(want))
	}
}

func firstKey(m map[string]bool) string {
	for k := range m {
		return k
	}
	return ""
}
