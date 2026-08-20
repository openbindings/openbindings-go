package openapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// urlencodedContentPathCasesDigest pins the frozen twin case table. The
// identical file is executed by openbindings-go/formats/openapi and by
// openapi-client/typescript/src (and carried by openbindings-ts's openapi
// package); changing it in one engine without the others fails here.
const urlencodedContentPathCasesDigest = "ca17623d67205f5c85424e58295400571394c8e095850c13d7ada68eb72a0fa8"

type urlencodedContentPathCase struct {
	Name           string         `json:"name"`
	OpenAPI        string         `json:"openapi"`
	Shape          string         `json:"shape"`
	Path           string         `json:"path"`
	PropertySchema map[string]any `json:"propertySchema"`
	Encoding       map[string]any `json:"encoding"`
	Value          any            `json:"value"`
	Expect         string         `json:"expect"`
	Basis          string         `json:"basis"`
}

func loadURLEncodedContentPathTable(t *testing.T) []urlencodedContentPathCase {
	t.Helper()
	raw, err := os.ReadFile("testdata/urlencoded-content-path-cases.json")
	if err != nil {
		t.Fatalf("read case table: %v", err)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != urlencodedContentPathCasesDigest {
		t.Fatalf("case table digest = %s, want %s (the table is shared byte-for-byte with the twin engines)", got, urlencodedContentPathCasesDigest)
	}
	var table struct {
		Cases []urlencodedContentPathCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &table); err != nil {
		t.Fatalf("parse case table: %v", err)
	}
	if len(table.Cases) != 80 {
		t.Fatalf("case table has %d cases, want 80", len(table.Cases))
	}
	return table.Cases
}

// urlencodedContentPathDocument renders one case as a WHOLE OpenAPI document.
// The document, and not a hand-built openapi3.MediaType, is what the engine has
// to be given: the shipped loader normalizes the raw tree before kin-openapi's
// typed model exists, and a harness that unmarshals a MediaType directly
// measures an engine the project does not ship.
func urlencodedContentPathDocument(t *testing.T, c urlencodedContentPathCase) []byte {
	t.Helper()
	media := map[string]any{
		"schema": map[string]any{
			"type":       "object",
			"properties": map[string]any{"p": c.PropertySchema},
		},
	}
	if c.Encoding != nil {
		media["encoding"] = map[string]any{"p": c.Encoding}
	}
	document := map[string]any{
		"openapi": c.OpenAPI,
		"info":    map[string]any{"title": "urlencoded content path case table", "version": "1.0.0"},
		"paths": map[string]any{
			"/form": map[string]any{
				"post": map[string]any{
					"operationId": "postForm",
					"requestBody": map[string]any{
						"required": true,
						"content":  map[string]any{"application/x-www-form-urlencoded": media},
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

// urlencodedContentPathDecision renders one cell exactly as the twin engines
// render it. Refusal messages are each implementation's own surface, so only
// the decision itself crosses the twin boundary.
func urlencodedContentPathDecision(t *testing.T, c urlencodedContentPathCase) string {
	t.Helper()
	doc, err := loadDocument("", json.RawMessage(urlencodedContentPathDocument(t, c)))
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
	media := item.Post.RequestBody.Value.Content["application/x-www-form-urlencoded"]
	if media == nil {
		t.Fatalf("%s: loaded document has no urlencoded media type", c.Name)
	}
	encoded, err := buildURLEncodedBodyForRevision(doc, media, map[string]any{"p": c.Value}, BindingSpec)
	if err != nil {
		return "error"
	}
	if encoded == "" {
		return "admitted;emit=elided"
	}
	return "admitted;emit=" + encoded
}

// TestURLEncodedContentPathCaseTable executes the shared 80-cell table: ten
// declaration shapes on each of the eight accepted OAS editions, each pinned to
// the whole emitted request body. It is the layer neither sibling table covers
// -- the lane table pins WHICH path a shape takes, the escaper table pins WHICH
// CHARACTERS a path leaves literal -- and it is the layer no corpus aggregate
// can see, because no evaluation report records request bytes.
func TestURLEncodedContentPathCaseTable(t *testing.T) {
	for _, c := range loadURLEncodedContentPathTable(t) {
		t.Run(c.Name, func(t *testing.T) {
			if got := urlencodedContentPathDecision(t, c); got != c.Expect {
				t.Fatalf("%s: decision = %q, want %q\nbasis: %s", c.Name, got, c.Expect, c.Basis)
			}
		})
	}
}

// TestURLEncodedContentPathIgnoresThePatchComponent is the deleted
// legacyOpenAPIFormEncoding predicate stated as an executable claim rather than
// as an absence in prose: two documents differing ONLY in the patch component
// of their openapi field emit the same bytes, for every declaration shape on
// both accepted lines. The one edition difference this family does carry --
// the type-absent part default -- is a LINE split, so it survives this check.
func TestURLEncodedContentPathIgnoresThePatchComponent(t *testing.T) {
	byLineAndShape := map[string]map[string][]string{}
	for _, c := range loadURLEncodedContentPathTable(t) {
		line := c.OpenAPI[:strings.LastIndex(c.OpenAPI, ".")]
		key := line + "|" + c.Shape
		if byLineAndShape[key] == nil {
			byLineAndShape[key] = map[string][]string{}
		}
		decision := urlencodedContentPathDecision(t, c)
		byLineAndShape[key][decision] = append(byLineAndShape[key][decision], c.OpenAPI)
	}
	if len(byLineAndShape) != 20 {
		t.Fatalf("covered %d line/shape cells, want 20", len(byLineAndShape))
	}
	for key, decisions := range byLineAndShape {
		if len(decisions) != 1 {
			t.Fatalf("%s emits different bytes across the patch component of one line: %v", key, decisions)
		}
	}
}

// TestURLEncodedContentPathDecidesIdenticallyOnBothLines states, positively,
// that NO declaration shape on this lane decides differently between the two
// accepted lines. It replaces TestURLEncodedContentPathEditionDifferencesAreLineScoped,
// whose whole content was the one exception: the type-absent part default,
// which the 3.0 line's deleted value-keyed convention admitted while the 3.1
// line refused. Escalation M2 (ruled 2026-08-20) removed that convention, so
// the shapes agree — on grounds that still differ per line, which is why the
// exception was legitimate while it stood and why its removal is a
// convergence rather than an erasure.
func TestURLEncodedContentPathDecidesIdenticallyOnBothLines(t *testing.T) {
	typeAbsent := map[string]bool{"typeless-with-members": true, "unconstrained": true}
	byShape := map[string]map[string]string{}
	for _, c := range loadURLEncodedContentPathTable(t) {
		line := c.OpenAPI[:strings.LastIndex(c.OpenAPI, ".")]
		if byShape[c.Shape] == nil {
			byShape[c.Shape] = map[string]string{}
		}
		byShape[c.Shape][line] = c.Expect
	}
	if len(byShape) != 10 {
		t.Fatalf("covered %d shapes, want 10", len(byShape))
	}
	for shape, byLine := range byShape {
		if byLine["3.0"] != byLine["3.1"] {
			t.Fatalf("shape %q: 3.0 line emits %q and 3.1 line emits %q; no shape on this lane may differ between the lines",
				shape, byLine["3.0"], byLine["3.1"])
		}
	}
	// And the converged answer for the type-absent shapes is the refusal, not
	// some other agreement reached by widening one line.
	for shape := range typeAbsent {
		for _, line := range []string{"3.0", "3.1"} {
			if byShape[shape][line] != "refused" {
				t.Fatalf("shape %q on the %s line = %q, want refused (a resolved part schema declaring no `type` refuses on every accepted edition)", shape, line, byShape[shape][line])
			}
		}
	}
}

// TestURLEncodedContentPathTableCoversEveryEditionAndShape fails if a cell
// silently drops out of the table.
func TestURLEncodedContentPathTableCoversEveryEditionAndShape(t *testing.T) {
	want := map[string]bool{}
	for _, edition := range []string{"3.0.0", "3.0.1", "3.0.2", "3.0.3", "3.0.4", "3.1.0", "3.1.1", "3.1.2"} {
		for _, shape := range []string{
			"object", "array-of-objects", "array-of-strings", "primitive-with-space",
			"explicit-content-type", "explicit-style-only", "explicit-allow-reserved-false",
			"typeless-with-members", "unconstrained", "non-collapsing-anyOf",
		} {
			want[fmt.Sprintf("%s|%s", edition, shape)] = true
		}
	}
	for _, c := range loadURLEncodedContentPathTable(t) {
		if !want[c.Name] {
			t.Fatalf("case %q is not one of the covered cells", c.Name)
		}
		delete(want, c.Name)
	}
	if len(want) != 0 {
		t.Fatalf("case table is missing %d cells, including %v", len(want), firstKey(want))
	}
}
