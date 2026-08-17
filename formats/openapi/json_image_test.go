package openapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"os"
	"sort"
	"strings"
	"testing"
)

// jsonImageCasesDigest pins the frozen twin case table. The identical file is
// executed by openapi-client/go, by openapi-client/typescript,
// and by openbindings-ts/packages/openapi against that package's BUILT dist;
// changing it in one engine without the others fails here.
const jsonImageCasesDigest = "4ada3f3817f9a3f6114ab4aef073bffd405027b568149f3c96e3374f3b8ef27c"

type jsonImageTable struct {
	Comment string          `json:"$comment"`
	Cases   []jsonImageCase `json:"cases"`
}

type jsonImageCase struct {
	Name           string         `json:"name"`
	OpenAPI        string         `json:"openapi"`
	Lane           string         `json:"lane"`
	Media          string         `json:"media"`
	Cell           string         `json:"cell"`
	FormsJSONImage bool           `json:"formsJSONImage"`
	Document       map[string]any `json:"document"`
	Input          map[string]any `json:"input"`
	Expect         string         `json:"expect"`
	Basis          string         `json:"basis"`
}

func loadJSONImageTable(t *testing.T) []jsonImageCase {
	t.Helper()
	raw, err := os.ReadFile("testdata/json-image-cases.json")
	if err != nil {
		t.Fatalf("read case table: %v", err)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != jsonImageCasesDigest {
		t.Fatalf("case table digest = %s, want %s (the table is shared byte-for-byte with the twin engines)", got, jsonImageCasesDigest)
	}
	var table jsonImageTable
	if err := json.Unmarshal(raw, &table); err != nil {
		t.Fatalf("parse case table: %v", err)
	}
	if len(table.Cases) == 0 {
		t.Fatal("case table is empty")
	}
	return table.Cases
}

// jsonImageEmission runs one cell through this engine's own shipped path: the
// case's WHOLE document is loaded by the shipped loader, the request bodies
// are planned, the flat input is routed, and the wire body (or, for the
// parameter lane, the wire header) is rendered.
func jsonImageEmission(t *testing.T, c jsonImageCase) string {
	t.Helper()
	encoded, err := json.Marshal(c.Document)
	if err != nil {
		t.Fatalf("%s: marshal document: %v", c.Name, err)
	}
	doc, err := loadDocument("", json.RawMessage(encoded))
	if err != nil {
		t.Fatalf("%s: load document: %v", c.Name, err)
	}
	item := doc.Paths.Find("/probe")
	if item == nil || item.Post == nil {
		t.Fatalf("%s: loaded document has no probe operation", c.Name)
	}
	op := item.Post

	if c.Lane == "parameter-content" {
		routed, err := routeInputFor(op.Parameters, c.Input, "/probe", nil, BindingSpec)
		if err != nil {
			t.Fatalf("%s: route input: %v", c.Name, err)
		}
		rendered := make([]string, 0, len(routed.headers))
		for _, header := range routed.headers {
			rendered = append(rendered, header[0]+": "+header[1])
		}
		sort.Strings(rendered)
		return strings.Join(rendered, "\n")
	}

	plans, err := planRequestBodiesFor(doc, op, BindingSpec)
	if err != nil {
		t.Fatalf("%s: plan request bodies: %v", c.Name, err)
	}
	var plan *bodyPlan
	for _, candidate := range plans {
		if candidate != nil && candidate.mediaType == c.Media {
			plan = candidate
			break
		}
	}
	if plan == nil {
		t.Fatalf("%s: no request plan for %s", c.Name, c.Media)
	}
	routed, err := routeInputFor(op.Parameters, c.Input, "/probe", plan, BindingSpec)
	if err != nil {
		t.Fatalf("%s: route input: %v", c.Name, err)
	}
	reader, contentType, err := buildRequestBody(doc, plan, routed)
	if err != nil {
		t.Fatalf("%s: build request body: %v", c.Name, err)
	}
	if reader == nil {
		t.Fatalf("%s: no body emitted", c.Name)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("%s: read body: %v", c.Name, err)
	}
	if c.Lane != "multipart-part" {
		return string(body)
	}
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		t.Fatalf("%s: parse multipart content type: %v", c.Name, err)
	}
	parts := multipart.NewReader(strings.NewReader(string(body)), params["boundary"])
	rendered := []string{}
	for {
		part, err := parts.NextRawPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("%s: read part: %v", c.Name, err)
		}
		partBody, err := io.ReadAll(part)
		if err != nil {
			t.Fatalf("%s: read part body: %v", c.Name, err)
		}
		rendered = append(rendered, part.Header.Get("Content-Type")+":"+string(partBody))
		part.Close()
	}
	return strings.Join(rendered, "&")
}

func TestJSONImageCaseTable(t *testing.T) {
	cases := loadJSONImageTable(t)
	if len(cases) != 42 {
		t.Fatalf("case table has %d cells, want 42", len(cases))
	}
	for _, c := range cases {
		if got := jsonImageEmission(t, c); got != c.Expect {
			t.Errorf("%s: emission = %q, want %q\nbasis: %s", c.Name, got, c.Expect, c.Basis)
		}
	}
}

// TestJSONImageStyleLaneIsUntouched states the control as a claim in its own
// right rather than leaving it implicit in six cells. The urlencoded STYLE
// path expands an object's members as separate name=value pairs and never
// forms a JSON image, and the two urlencoded lanes are SUPPOSED to disagree
// about a space, so no style-lane expectation may carry the JSON image's
// punctuation or the content path's `+`.
func TestJSONImageStyleLaneIsUntouched(t *testing.T) {
	checked := 0
	for _, c := range loadJSONImageTable(t) {
		if c.FormsJSONImage {
			continue
		}
		for _, forbidden := range []string{"%7B", "%22", "+"} {
			if strings.Contains(c.Expect, forbidden) {
				t.Errorf("%s: style-lane expectation contains %q, which belongs to a lane that forms a JSON image: %q", c.Name, forbidden, c.Expect)
			}
		}
		// EXECUTED, not read off the table: the claim is about the engine.
		if got := jsonImageEmission(t, c); got != c.Expect {
			t.Errorf("%s: style-lane emission moved: %q, want %q", c.Name, got, c.Expect)
		}
		checked++
	}
	if checked != 6 {
		t.Fatalf("checked %d style-lane cells, want 6", checked)
	}
}

// TestJSONImageEncoderEmitsLiteralCharacters pins the convention at the
// encoder itself, below any lane. RFC 8259 §7 permits both spellings; which
// one this engine emits is its own documented behavior, and it is this.
func TestJSONImageEncoderEmitsLiteralCharacters(t *testing.T) {
	got, err := jsonImage(map[string]any{"k": "a&b<c>d"})
	if err != nil {
		t.Fatalf("jsonImage: %v", err)
	}
	if want := `{"k":"a&b<c>d"}`; string(got) != want {
		t.Errorf("jsonImage = %q, want %q", got, want)
	}
	// json.Encoder.Encode terminates each value with a newline; a wire body is
	// one value, so the trailing byte must be gone.
	if strings.HasSuffix(string(got), "\n") {
		t.Errorf("jsonImage kept the encoder's trailing newline: %q", got)
	}
}

// TestJSONImageRoutingTransformEmitsLiteralCharacters covers the fifth
// emission site, which is not a wire lane: the revision-2 routing transform
// expression this engine builds from parameter and body route names.
func TestJSONImageRoutingTransformEmitsLiteralCharacters(t *testing.T) {
	routes := abstractInputRoutes{
		parameters: []abstractParameterRoute{{Field: "a&b<c>d", Name: "a&b<c>d", In: "query"}},
	}
	expression := routes.transformExpressionFor(BindingSpec)
	for _, escape := range []string{`\u0026`, `\u003c`, `\u003e`} {
		if strings.Contains(expression, escape) {
			t.Errorf("routing transform carries the six-character escape %s: %s", escape, expression)
		}
	}
	if !strings.Contains(expression, "a&b<c>d") {
		t.Errorf("routing transform lost the literal characters: %s", expression)
	}
}
