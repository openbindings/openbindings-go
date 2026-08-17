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
	"sort"
	"strings"
	"testing"
)

// jsonImageCasesDigest pins the frozen twin case table. The identical file is
// executed by openapi-client/go and by
// openapi-client/typescript/src; changing it in one engine without the others
// fails here.
const jsonImageCasesDigest = "620a14d1f8f3572ff087e5dbc2f84603a2b79513cc9661f83f357db463792e80"

type jsonImageTable struct {
	Comment string          `json:"$comment"`
	Cells   []string        `json:"cells"`
	Lanes   []string        `json:"lanes"`
	Cases   []jsonImageCase `json:"cases"`
}

type jsonImageCase struct {
	Name         string         `json:"name"`
	OpenAPI      string         `json:"openapi"`
	Lane         string         `json:"lane"`
	Cell         string         `json:"cell"`
	PropertyName string         `json:"propertyName"`
	Value        map[string]any `json:"value"`
	Expect       string         `json:"expect"`
	Basis        string         `json:"basis"`
}

func loadJSONImageTable(t *testing.T) jsonImageTable {
	t.Helper()
	raw, err := os.ReadFile("testdata/json-image-cases.json")
	if err != nil {
		t.Fatalf("read case table: %v", err)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != jsonImageCasesDigest {
		t.Fatalf("case table digest = %s, want %s (the table is shared byte-for-byte with the two twin engines)", got, jsonImageCasesDigest)
	}
	var table jsonImageTable
	if err := json.Unmarshal(raw, &table); err != nil {
		t.Fatalf("parse case table: %v", err)
	}
	if len(table.Cases) == 0 {
		t.Fatal("case table is empty")
	}
	return table
}

// jsonImageDocument renders one case as a WHOLE OpenAPI document. The document,
// and not a hand-built openapi3.MediaType, is what the engine has to be given:
// the shipped loader normalizes the raw tree before kin-openapi's typed model
// exists, and a harness that unmarshals a MediaType directly measures an engine
// the project does not ship.
func jsonImageDocument(t *testing.T, c jsonImageCase) []byte {
	t.Helper()
	object := map[string]any{"type": "object"}
	operation := map[string]any{
		"operationId": "probe",
		"responses":   map[string]any{"200": map[string]any{"description": "ok"}},
	}
	switch c.Lane {
	case "json-body":
		operation["requestBody"] = map[string]any{"required": true, "content": map[string]any{
			"application/json": map[string]any{"schema": object},
		}}
	case "multipart-part":
		operation["requestBody"] = map[string]any{"required": true, "content": map[string]any{
			"multipart/form-data": map[string]any{"schema": map[string]any{
				"type":       "object",
				"properties": map[string]any{c.PropertyName: object},
			}},
		}}
	case "urlencoded-content", "urlencoded-style":
		operation["requestBody"] = map[string]any{"required": true, "content": map[string]any{
			"application/x-www-form-urlencoded": map[string]any{"schema": map[string]any{
				"type":       "object",
				"properties": map[string]any{c.PropertyName: object},
			}},
		}}
	case "parameter-content":
		operation["parameters"] = []any{map[string]any{
			"name": c.PropertyName, "in": "query",
			"content": map[string]any{"application/json": map[string]any{"schema": object}},
		}}
	default:
		t.Fatalf("%s: unknown lane %q", c.Name, c.Lane)
	}
	document := map[string]any{
		"openapi": c.OpenAPI,
		"info":    map[string]any{"title": "json image case table", "version": "1.0.0"},
		"paths":   map[string]any{"/probe": map[string]any{"post": operation}},
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("%s: marshal document: %v", c.Name, err)
	}
	return encoded
}

// jsonImageEmission renders one cell through the shipped path. Refusal messages
// are each implementation's own surface, so only the emitted bytes cross the
// twin boundary.
func jsonImageEmission(t *testing.T, c jsonImageCase) string {
	t.Helper()
	doc, err := loadDocument("", json.RawMessage(jsonImageDocument(t, c)))
	if err != nil {
		t.Fatalf("%s: load document: %v", c.Name, err)
	}
	item := doc.Paths.Find("/probe")
	if item == nil || item.Post == nil {
		t.Fatalf("%s: loaded document has no probe operation", c.Name)
	}
	op := item.Post

	if c.Lane == "parameter-content" {
		if len(op.Parameters) != 1 || op.Parameters[0].Value == nil {
			t.Fatalf("%s: loaded document has no content-form parameter", c.Name)
		}
		text, err := serializeParamContentFor(op.Parameters[0].Value, c.Value, BindingSpec)
		if err != nil {
			return "error"
		}
		return text
	}

	plans, err := planRequestBodiesFor(doc, op, BindingSpec)
	if err != nil || len(plans) != 1 {
		return "refused"
	}
	plan := plans[0]

	if c.Lane == "json-body" {
		// Both JSON-body branches are covered without the table having to
		// know which one this plan takes: the synthetic/whole-object branch
		// reads bodyValue and the field branch reads bodyFields, and both are
		// given the same value, so either branch owes the same bytes.
		routed := &routedInput{bodySet: true, bodyValue: c.Value, bodyFields: c.Value}
		reader, _, err := buildRequestBody(doc, plan, routed)
		if err != nil {
			return "error"
		}
		raw, err := io.ReadAll(reader)
		if err != nil {
			return "error"
		}
		return string(raw)
	}

	media := op.RequestBody.Value.Content[plan.mediaType]
	if media == nil {
		t.Fatalf("%s: loaded document has no %s media type", c.Name, plan.mediaType)
	}
	fields := map[string]any{c.PropertyName: c.Value}

	if c.Lane == "urlencoded-content" || c.Lane == "urlencoded-style" {
		encoded, err := buildURLEncodedBodyForRevision(doc, media, fields, BindingSpec)
		if err != nil {
			return "error"
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
	var rendered []string
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
	return strings.Join(rendered, "&")
}

// TestJSONImageCaseTable executes the shared table.
//
// The expectations are authored from three rules and never from an engine:
// RFC 8259 Section 7 plus the implementations' stated literal-character
// convention for the JSON image itself; RFC 1866 Section 8.2.1 / RFC 1738
// Section 2.2 (the WHATWG form-urlencoded serializer set) for the content
// lane's escaper; and RFC 6570 with allowReserved not in effect — RFC 3986's
// unreserved set — for the style lane's. See the table's own $comment.
func TestJSONImageCaseTable(t *testing.T) {
	for _, c := range loadJSONImageTable(t).Cases {
		t.Run(c.Name, func(t *testing.T) {
			if got := jsonImageEmission(t, c); got != c.Expect {
				t.Fatalf("%s: emission = %q, want %q\nbasis: %s", c.Name, got, c.Expect, c.Basis)
			}
		})
	}
}

// TestJSONImageTableCoversEveryLaneAndCell fails if the table stops exercising
// a lane or a character class. A cell silently dropping out of a table that is
// the only guard on a convention is the failure mode this asserts against.
func TestJSONImageTableCoversEveryLaneAndCell(t *testing.T) {
	table := loadJSONImageTable(t)
	wantLanes := []string{
		"3.0.3|json-body", "3.0.3|urlencoded-style", "3.0.4|urlencoded-content",
		"3.1.1|json-body", "3.1.1|multipart-part", "3.1.1|parameter-content",
		"3.1.1|urlencoded-content",
	}
	wantCells := []string{"all-three", "ampersand", "greater-than", "key-and-value", "less-than", "none"}

	lanes := append([]string(nil), table.Lanes...)
	cells := append([]string(nil), table.Cells...)
	sort.Strings(lanes)
	sort.Strings(cells)
	if strings.Join(lanes, ",") != strings.Join(wantLanes, ",") {
		t.Fatalf("table lanes = %v, want %v", lanes, wantLanes)
	}
	if strings.Join(cells, ",") != strings.Join(wantCells, ",") {
		t.Fatalf("table cells = %v, want %v", cells, wantCells)
	}

	seen := map[string]bool{}
	for _, c := range table.Cases {
		seen[fmt.Sprintf("%s|%s|%s", c.OpenAPI, c.Lane, c.Cell)] = true
	}
	if len(seen) != len(wantLanes)*len(wantCells) {
		t.Fatalf("table carries %d distinct lane x cell cells, want %d", len(seen), len(wantLanes)*len(wantCells))
	}
	for _, lane := range wantLanes {
		for _, cell := range wantCells {
			if !seen[lane+"|"+cell] {
				t.Fatalf("table has no %s|%s cell", lane, cell)
			}
		}
	}
}

// TestJSONImageStyleLaneIsUnaffected states the control as a claim in its own
// right. The two urlencoded lanes are SUPPOSED to disagree — a space is `+` on
// the content lane and %20 on the style lane, which is audited and closed — and
// the style lane never forms a JSON image at all. Every style-lane expectation
// therefore has to be free of the JSON image's own punctuation, which is what a
// convergence leaking across the lanes would introduce.
func TestJSONImageStyleLaneIsUnaffected(t *testing.T) {
	seen := 0
	for _, c := range loadJSONImageTable(t).Cases {
		if c.Lane != "urlencoded-style" {
			continue
		}
		seen++
		if strings.Contains(c.Expect, "%7B") || strings.Contains(c.Expect, "%22") {
			t.Fatalf("%s expects %q, which carries a JSON image; the style lane must not form one", c.Name, c.Expect)
		}
		if strings.Contains(c.Expect, "+") {
			t.Fatalf("%s expects %q; the style lane spells a space %%20, never `+`", c.Name, c.Expect)
		}
	}
	if seen != 6 {
		t.Fatalf("table carries %d style-lane cells, want 6", seen)
	}
}

// TestJSONImageEncoderIsExplicit pins the unit directly, so a caller reverting
// to the host default fails here as well as in the lane cells. RFC 8259
// Section 7's MUST-escape set is quotation mark, reverse solidus and U+0000
// through U+001F; those are still escaped, and only those.
func TestJSONImageEncoderIsExplicit(t *testing.T) {
	got, err := jsonImage(map[string]any{"a&b<c>": "x&y<z>\"\\\n"})
	if err != nil {
		t.Fatalf("jsonImage: %v", err)
	}
	const want = `{"a&b<c>":"x&y<z>\"\\\n"}`
	if string(got) != want {
		t.Fatalf("jsonImage = %s, want %s", got, want)
	}
	// The encoder appends a newline that json.Marshal does not; a wire body is
	// one value, so the trailing byte must be gone.
	if strings.HasSuffix(string(got), "\n") {
		t.Fatalf("jsonImage returned a trailing newline: %q", string(got))
	}
}

// TestJSONImageRoutingTransformUsesTheConvention pins the one emission this
// engine makes that is not a wire byte: the revision-2 routing transform
// expression, which carries artifact-derived parameter and property names into
// a synthesized OBI. Its TypeScript twin is
// openapi-client/typescript/src/input-routes-v2.ts.
func TestJSONImageRoutingTransformUsesTheConvention(t *testing.T) {
	routes := abstractInputRoutes{
		parameters: []abstractParameterRoute{{Name: "a&b<c>", In: "query", Field: "a&b<c>"}},
		bodyFields: map[string]string{"x&y": "x&y"},
	}
	got := routes.transformExpressionFor(BindingSpec)
	for _, literal := range []string{`"a&b<c>"`, `"x&y"`} {
		if !strings.Contains(got, literal) {
			t.Fatalf("transform expression = %s, want it to carry %s literally", got, literal)
		}
	}
	for _, r := range []rune{'&', '<', '>'} {
		// Built rather than written, so the six-character sequence cannot be
		// silently un-escaped by an editor on its way into this file.
		escaped := fmt.Sprintf(`\u%04x`, r)
		if strings.Contains(got, escaped) {
			t.Fatalf("transform expression = %s, want no %s escape", got, escaped)
		}
	}
}

// TestJSONImageDocumentsTheAuthority keeps the citation the convention rests on
// in front of anyone who changes it: the pinned RFC 8259 digest appears in the
// engine's own doc comment, so a reader can re-run the audit.
func TestJSONImageDocumentsTheAuthority(t *testing.T) {
	source, err := os.ReadFile("json_image.go")
	if err != nil {
		t.Fatalf("read json_image.go: %v", err)
	}
	for _, needle := range []string{
		"RFC 8259 Section 7",
		"61a5378f4255c720beb2a4b4a63b29540147c140f36988bf086291989b4cd2d7",
		"SetEscapeHTML",
	} {
		if !strings.Contains(string(source), needle) {
			t.Fatalf("json_image.go no longer states %q", needle)
		}
	}
}

// TestJSONImageParameterLaneOnTheShippedPath establishes the path the
// parameter-content cells measure. The table calls the serializer that
// serializeParamContent wraps; the invoker reaches the SAME function through
// routeParameterFor at the full profile, and the two profiles differ only in
// media-type parsing strictness, never in the JSON branch. Asserted by
// execution rather than by reading, because a table that measures a function
// the product does not call measures nothing.
func TestJSONImageParameterLaneOnTheShippedPath(t *testing.T) {
	c := jsonImageCase{
		Name: "shipped-path", OpenAPI: "3.1.1", Lane: "parameter-content",
		PropertyName: "address", Value: map[string]any{"street": "1 A&B <c> d"},
	}
	doc, err := loadDocument("", json.RawMessage(jsonImageDocument(t, c)))
	if err != nil {
		t.Fatalf("load document: %v", err)
	}
	op := doc.Paths.Find("/probe").Post
	routed, err := routeInputFor(op.Parameters, map[string]any{c.PropertyName: c.Value}, "/probe", nil, BindingSpec)
	if err != nil {
		t.Fatalf("routeInputFor: %v", err)
	}
	if len(routed.queryUnits) != 1 {
		t.Fatalf("query units = %v, want exactly one", routed.queryUnits)
	}
	unit := routed.queryUnits[0]
	for _, want := range []string{"%26", "%3C", "%3E"} {
		if !strings.Contains(unit, want) {
			t.Fatalf("query unit = %q, want it to carry %s (the percent-encoding of the literal character)", unit, want)
		}
	}
	for _, r := range []rune{'&', '<', '>'} {
		escaped := fmt.Sprintf("%%5Cu%04x", r)
		if strings.Contains(strings.ToUpper(unit), strings.ToUpper(escaped)) {
			t.Fatalf("query unit = %q, want no %s (a six-character escape survived into the wire)", unit, escaped)
		}
	}
}
