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
const urlencodedEscaperCasesDigest = "7fa47d6a207b33e8530e94b49442e3fbc39610dc16e6fb0ea573d3ac2e94821f"

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

func urlencodedEscaperMedia(t *testing.T, c urlencodedEscaperCase) *openapi3.MediaType {
	t.Helper()
	body := map[string]any{
		"schema": map[string]any{
			"type":       "object",
			"properties": map[string]any{c.PropertyName: map[string]any{"type": "string"}},
		},
	}
	if c.Encoding != nil {
		body["encoding"] = map[string]any{c.PropertyName: c.Encoding}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("%s: marshal media: %v", c.Name, err)
	}
	media := &openapi3.MediaType{}
	if err := media.UnmarshalJSON(encoded); err != nil {
		t.Fatalf("%s: unmarshal media: %v", c.Name, err)
	}
	return media
}

// TestURLEncodedEscaperCaseTable executes the shared table. Its expectations
// come from upstream authority text, never from an engine:
//
//   - The STYLE lane is RFC 6570 form expansion with allowReserved absent, so
//     its literal set is RFC 3986's unreserved set and a space is %20. OAS
//     3.0.4 / 3.1.1 Appendix E.3 (E.4 in 3.1.2) assigns that lane to RFC 6570
//     and notes it "does not use + for form-urlencoded".
//   - The CONTENT lane is RFC 1866 Section 8.2.1 ("space characters are
//     replaced by `+`"), whose literal set is fixed to the WHATWG
//     form-urlencoded serializer set on the authority of the same appendix's
//     SHOULD for browser compatibility.
//
// The two lanes are therefore SUPPOSED to disagree about the space character;
// a change that made them agree would break conformance in the style lane.
func TestURLEncodedEscaperCaseTable(t *testing.T) {
	for _, c := range loadURLEncodedEscaperTable(t) {
		t.Run(c.Name, func(t *testing.T) {
			doc := &openapi3.T{OpenAPI: c.OpenAPI}
			media := urlencodedEscaperMedia(t, c)
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
