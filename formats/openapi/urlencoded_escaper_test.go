package openapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

// urlencodedEscaperCasesDigest pins the frozen twin case table. The identical
// file is executed by openapi-client/go and by
// openapi-client/typescript/src; changing it in one engine without the others
// fails here.
const urlencodedEscaperCasesDigest = "1af21db9a7c158d671bd4e388f7729dc5372d571c4c00fa257bfb707d162d818"

type urlencodedEscaperTable struct {
	Comment string                  `json:"$comment"`
	Cases   []urlencodedEscaperCase `json:"cases"`
}

type urlencodedEscaperCase struct {
	Name         string         `json:"name"`
	OpenAPI      string         `json:"openapi"`
	Declaration  string         `json:"declaration"`
	Encoding     map[string]any `json:"encoding"`
	Position     string         `json:"position"`
	Cell         string         `json:"cell"`
	Lane         string         `json:"lane"`
	PropertyName string         `json:"propertyName"`
	Value        string         `json:"value"`
	Expect       string         `json:"expect"`
}

func loadURLEncodedEscaperTable(t *testing.T) []urlencodedEscaperCase {
	t.Helper()
	raw, err := os.ReadFile("testdata/urlencoded-escaper-cases.json")
	if err != nil {
		t.Fatalf("read case table: %v", err)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != urlencodedEscaperCasesDigest {
		t.Fatalf("case table digest = %s, want %s (the table is shared byte-for-byte with the two twin engines)", got, urlencodedEscaperCasesDigest)
	}
	var table urlencodedEscaperTable
	if err := json.Unmarshal(raw, &table); err != nil {
		t.Fatalf("parse case table: %v", err)
	}
	if len(table.Cases) == 0 {
		t.Fatal("case table is empty")
	}
	return table.Cases
}

// urlencodedEscaperDocument renders one case as a whole OpenAPI document.
//
// The document, and not a hand-built openapi3.MediaType, is what the engine has
// to be given: an Encoding Object writing `allowReserved: false` is erased by
// kin-openapi's typed model, which stores the field as a plain bool and so
// cannot tell an explicit default from an absent field. The shipped loader
// preserves that presence before the typed model exists (ref_siblings.go's
// normalizeContent materializes the equivalent explicit `style`), and a harness
// that unmarshals a MediaType directly skips that step and measures an engine
// the project does not ship.
func urlencodedEscaperDocument(t *testing.T, c urlencodedEscaperCase) []byte {
	t.Helper()
	media := map[string]any{
		"schema": map[string]any{
			"type":       "object",
			"properties": map[string]any{c.PropertyName: map[string]any{"type": "string"}},
		},
	}
	if c.Encoding != nil {
		media["encoding"] = map[string]any{c.PropertyName: c.Encoding}
	}
	document := map[string]any{
		"openapi": c.OpenAPI,
		"info":    map[string]any{"title": "urlencoded escaper case table", "version": "1.0.0"},
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

func urlencodedEscaperMedia(t *testing.T, c urlencodedEscaperCase) (*openapi3.T, *openapi3.MediaType) {
	t.Helper()
	doc, err := loadDocument("file:///urlencoded-escaper-cases.json", urlencodedEscaperDocument(t, c))
	if err != nil {
		t.Fatalf("%s: load document: %v", c.Name, err)
	}
	item := doc.Paths.Find("/form")
	if item == nil || item.Post == nil || item.Post.RequestBody == nil || item.Post.RequestBody.Value == nil {
		t.Fatalf("%s: loaded document has no urlencoded request body", c.Name)
	}
	media := item.Post.RequestBody.Value.Content["application/x-www-form-urlencoded"]
	if media == nil {
		t.Fatalf("%s: loaded document has no urlencoded media type", c.Name)
	}
	return doc, media
}

// TestURLEncodedEscaperCaseTable executes the shared table. Its expectations
// come from upstream authority text, never from an engine:
//
//   - The STYLE lane is RFC 6570 form expansion with allowReserved not in
//     effect, so its literal set is RFC 3986's unreserved set and a space is
//     %20. OAS 3.0.4 / 3.1.1 Appendix E.3 (E.4 in 3.1.2) assigns that lane to
//     RFC 6570 and notes it "does not use + for form-urlencoded".
//   - The CONTENT lane is RFC 1866 Section 8.2.1 ("space characters are
//     replaced by `+`"), whose operative clause escapes the rest "as per
//     [URL]" = RFC 1738. RFC 1738 Section 2.2 permits "only alphanumerics, the
//     special characters `$-_.+!*'(),`, and reserved characters used for their
//     reserved purposes" unencoded, names `~` UNSAFE ("must always be
//     encoded"), and permits encoding anything not required to be encoded. The
//     WHATWG form-urlencoded serializer set the engines emit lies inside that
//     permission, and OAS 3.0.4 / 3.1.1 Appendix E.3.2 (E.4.2 in 3.1.2) gives
//     it a SHOULD.
//
// The two lanes are therefore SUPPOSED to disagree about the space character;
// a change that made them agree would break conformance in the style lane.
func TestURLEncodedEscaperCaseTable(t *testing.T) {
	for _, c := range loadURLEncodedEscaperTable(t) {
		t.Run(c.Name, func(t *testing.T) {
			doc, media := urlencodedEscaperMedia(t, c)
			got, err := buildURLEncodedBodyForRevision(doc, media, map[string]any{c.PropertyName: c.Value}, BindingSpec)
			if err != nil {
				t.Fatalf("%s (%s lane): %v", c.Name, c.Lane, err)
			}
			if got != c.Expect {
				t.Fatalf("%s (%s lane): body = %q, want %q", c.Name, c.Lane, got, c.Expect)
			}
		})
	}
}

// TestURLEncodedEscaperLanesDivergeOnlyOnSpace states the audit's finding as an
// executable claim: across the shared table's cells, the only character the two
// lanes render differently is the space, and they render it the two ways the OAS
// assigns to them.
func TestURLEncodedEscaperLanesDivergeOnlyOnSpace(t *testing.T) {
	byCell := map[string]map[string]string{}
	for _, c := range loadURLEncodedEscaperTable(t) {
		if c.Position != "value" || c.OpenAPI != "3.1.1" {
			continue // one edition line, one position: the lanes' own comparison
		}
		if byCell[c.Cell] == nil {
			byCell[c.Cell] = map[string]string{}
		}
		byCell[c.Cell][c.Lane] = c.Expect
	}
	for cell, lanes := range byCell {
		style, content := lanes["style"], lanes["content"]
		if style == "" || content == "" {
			t.Fatalf("cell %q lacks both lanes: %#v", cell, lanes)
		}
		switch cell {
		case "space":
			if style != "p=%20" || content != "p=+" {
				t.Fatalf("space: style = %q, content = %q; want p=%%20 and p=+", style, content)
			}
		case "asterisk", "tilde":
			// Both lanes reach the OAS-named literal set for their own
			// specification; the two sets differ here on purpose.
			if style == content {
				t.Fatalf("cell %q: lanes agree at %q, but RFC 3986's unreserved set and the WHATWG form set differ here", cell, style)
			}
		default:
			if style != content {
				t.Fatalf("cell %q: style = %q, content = %q; only space, asterisk and tilde may differ", cell, style, content)
			}
		}
	}
}

// TestURLEncodedEscaperTableCoversEveryEditionBranchAndThePresenceClass fails if
// the table stops exercising a branch the engines distinguish. The table is the
// only guard on those branches, so an edition or declaration shape silently
// dropping out of it is the failure mode this asserts against.
func TestURLEncodedEscaperTableCoversEveryEditionBranchAndThePresenceClass(t *testing.T) {
	seen := map[string]map[string]string{}
	for _, c := range loadURLEncodedEscaperTable(t) {
		if seen[c.OpenAPI] == nil {
			seen[c.OpenAPI] = map[string]string{}
		}
		if prior, ok := seen[c.OpenAPI][c.Declaration]; ok && prior != c.Lane {
			t.Fatalf("edition %s declaration %s appears in both the %s and %s lanes", c.OpenAPI, c.Declaration, prior, c.Lane)
		}
		seen[c.OpenAPI][c.Declaration] = c.Lane
	}
	// One edition per branch the engines distinguish: the 3.0 line that applies
	// the style defaults unconditionally, the 3.0 edition that reaches the
	// content lane, and the 3.1 line.
	want := map[string]map[string]string{
		"3.0.3": {"content": "style", "style": "style", "allow-reserved-false": "style"},
		"3.0.4": {"content": "content", "style": "style", "allow-reserved-false": "style"},
		"3.1.1": {"content": "content", "style": "style", "allow-reserved-false": "style"},
	}
	for edition, declarations := range want {
		for declaration, lane := range declarations {
			got, ok := seen[edition][declaration]
			if !ok {
				t.Fatalf("case table has no %s %s cells", edition, declaration)
			}
			if got != lane {
				t.Fatalf("%s %s: table puts it in the %s lane, want %s", edition, declaration, got, lane)
			}
		}
	}
	if len(seen) != len(want) {
		t.Fatalf("case table covers editions %v, want exactly %v", keysOf(seen), keysOf(want))
	}
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestURLEncodedEncodingPresenceSurvivesTheTypedModel pins the mechanism the
// case table depends on. kin-openapi's Encoding stores allowReserved as a plain
// bool, so an explicit `false` is indistinguishable from an absent field once
// the document is typed. The loader preserves the distinction by materializing
// the equivalent explicit style, which is what puts the property in the style
// lane; without it every allow-reserved-false cell silently moves to the
// content lane.
func TestURLEncodedEncodingPresenceSurvivesTheTypedModel(t *testing.T) {
	for _, tc := range []struct {
		name         string
		encoding     string
		wantSelected bool
	}{
		{"explicit false", `{"allowReserved": false}`, true},
		{"explicit true", `{"allowReserved": true}`, true},
		{"absent", ``, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			encodingField := ""
			if tc.encoding != "" {
				encodingField = `, "encoding": {"note": ` + tc.encoding + `}`
			}
			document := `{"openapi": "3.1.1", "info": {"title": "t", "version": "1.0.0"},
"paths": {"/form": {"post": {"operationId": "postForm", "requestBody": {"content": {"application/x-www-form-urlencoded": {
"schema": {"type": "object", "properties": {"note": {"type": "string"}}}` + encodingField + `}}},
"responses": {"200": {"description": "ok"}}}}}}`
			doc, err := loadDocument("file:///presence.json", json.RawMessage(document))
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			media := doc.Paths.Find("/form").Post.RequestBody.Value.Content["application/x-www-form-urlencoded"]
			if got := encodingUsesSerialization(media.Encoding["note"]); got != tc.wantSelected {
				t.Fatalf("encodingUsesSerialization = %v, want %v", got, tc.wantSelected)
			}
		})
	}
}
