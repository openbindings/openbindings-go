package openapi

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

// The union-type carriage table. Every `type` spelling a Schema Object can
// present, crossed with the two form media types and both accepted OAS
// lines, decided end to end: is the candidate admitted, what does one part
// carry, and what does a JSON null do.
//
// The table is asserted against the same literal expectations in the
// TypeScript twin (openapi-client/typescript/src/union-type-carriage.test.ts).
// Two engines that both assert this table cannot drift on the question
// silently, which is the point: the defect this table exists to pin was two
// engines reading `{"type": ["string", "null"]}` differently — Go as a
// typeless schema with no part carriage, TypeScript as every one of its
// members at once.
//
// Authority for the expectations:
//   - JSON Schema 2020-12 §6.1.1 — an array-valued `type` is a union: "an
//     instance validates successfully if its type matches any of the types
//     indicated by the strings in the array". A union with one non-"null"
//     member asserts exactly what the collapsing anyOf/oneOf spelling
//     asserts, so it takes the same §9.2 collapse; two or more non-null
//     members declare value-dependent alternatives and refuse.
//   - OAS 3.0.0–3.0.4, Schema Object — "type - Value MUST be a string.
//     Multiple types via an array are not supported." Under that line an
//     array-valued `type` is not a union declaration at all, and every
//     array spelling refuses.

var unionTypeSpellings = []struct {
	name   string
	schema string
}{
	{"string", `{"type":"string"}`},
	{"string-array-1", `{"type":["string"]}`},
	{"string-null", `{"type":["string","null"]}`},
	{"null-string", `{"type":["null","string"]}`},
	{"array-null", `{"type":["array","null"],"items":{"type":"string"}}`},
	{"object-null", `{"type":["object","null"]}`},
	{"integer-null", `{"type":["integer","null"]}`},
	{"string-object", `{"type":["string","object"]}`},
	{"string-integer", `{"type":["string","integer"]}`},
	{"null-only", `{"type":["null"]}`},
	{"empty-array", `{"type":[]}`},
	{"absent-type", `{"description":"probe"}`},
	{"memberless", `{}`},
	{"boolean-true", `{"anyOf":[{},{"not":{}}]}`},
}

var unionTypeEditions = []string{"3.0.4", "3.1.2"}

var unionTypeMedias = []string{"multipart/form-data", "application/x-www-form-urlencoded"}

// unionTypeProbeValue is the canonical value for a spelling: whatever JSON
// type the declaration's single non-null member admits. Deterministic so the
// twins probe identically.
func unionTypeProbeValue(schema string) any {
	switch {
	case strings.Contains(schema, `"object"`):
		return map[string]any{"k": "v"}
	case strings.Contains(schema, `"array"`):
		return []any{"a"}
	case strings.Contains(schema, `"integer"`):
		return float64(7)
	case strings.Contains(schema, `"boolean"`):
		return true
	default:
		return "x"
	}
}

func unionTypeCellKey(edition, media, spelling string, encoded bool) string {
	suffix := "plain"
	if encoded {
		suffix = "contentEncoding"
	}
	return fmt.Sprintf("%s|%s|%s|%s", edition, media, spelling, suffix)
}

func unionTypePartSchema(t *testing.T, raw string, encoded bool) *openapi3.Schema {
	t.Helper()
	var members map[string]any
	if err := json.Unmarshal([]byte(raw), &members); err != nil {
		t.Fatalf("part schema %s: %v", raw, err)
	}
	if encoded {
		members["contentEncoding"] = "base64"
	}
	reencoded, err := json.Marshal(members)
	if err != nil {
		t.Fatalf("part schema %s: %v", raw, err)
	}
	schema := &openapi3.Schema{}
	if err := schema.UnmarshalJSON(reencoded); err != nil {
		t.Fatalf("part schema %s: %v", reencoded, err)
	}
	return schema
}

// unionTypeCarriageDecision runs one cell and renders its decision. The
// rendering carries no engine-specific text: refusal messages are the
// implementations' own surface, so only the decision itself is compared.
func unionTypeCarriageDecision(t *testing.T, edition, media, rawSchema string, encoded bool) string {
	t.Helper()
	doc := &openapi3.T{OpenAPI: edition}
	part := unionTypePartSchema(t, rawSchema, encoded)
	body := &openapi3.MediaType{Schema: &openapi3.SchemaRef{Value: &openapi3.Schema{
		Type:       &openapi3.Types{"object"},
		Properties: openapi3.Schemas{"p": {Value: part}},
	}}}
	op := opWithRequestBody(openapi3.Content{media: body}, true)
	plans, err := planRequestBodiesFor(doc, op, BindingSpec)
	if err != nil {
		return "refused"
	}
	for _, plan := range plans {
		if len(plan.propertyMedia) > 0 {
			return "missing-required-choice"
		}
	}
	return "admitted;value=" + unionTypeEmission(t, doc, media, body, unionTypeProbeValue(rawSchema)) +
		";null=" + unionTypeEmission(t, doc, media, body, nil)
}

func unionTypeEmission(t *testing.T, doc *openapi3.T, media string, body *openapi3.MediaType, value any) string {
	t.Helper()
	if media == "application/x-www-form-urlencoded" {
		encoded, err := buildURLEncodedBodyForRevision(doc, body, map[string]any{"p": value}, BindingSpec)
		if err != nil {
			return "error"
		}
		if encoded == "" {
			return "elided"
		}
		return encoded
	}
	reader, contentType, err := buildMultipartBodyForRevision(doc, body, map[string]any{"p": value}, BindingSpec)
	if err != nil {
		return "error"
	}
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "error"
	}
	parts := multipart.NewReader(reader, params["boundary"])
	rendered := ""
	for {
		part, err := parts.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "error"
		}
		content, err := io.ReadAll(part)
		if err != nil {
			return "error"
		}
		if rendered != "" {
			rendered += "&"
		}
		rendered += part.Header.Get("Content-Type") + ":" + string(content)
		part.Close()
	}
	if rendered == "" {
		return "elided"
	}
	return rendered
}

func unionTypeCarriageTable(t *testing.T) map[string]string {
	t.Helper()
	table := map[string]string{}
	for _, edition := range unionTypeEditions {
		for _, media := range unionTypeMedias {
			for _, spelling := range unionTypeSpellings {
				for _, encoded := range []bool{false, true} {
					key := unionTypeCellKey(edition, media, spelling.name, encoded)
					table[key] = unionTypeCarriageDecision(t, edition, media, spelling.schema, encoded)
				}
			}
		}
	}
	return table
}

func TestUnionTypeCarriageTwinTable(t *testing.T) {
	got := unionTypeCarriageTable(t)
	if os.Getenv("OB_PRINT_UNION_TABLE") != "" {
		keys := make([]string, 0, len(got))
		for key := range got {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Printf("\t%q: %q,\n", key, got[key])
		}
	}
	if len(got) != len(unionTypeCarriageExpectations) {
		t.Fatalf("table has %d cells, expectations have %d", len(got), len(unionTypeCarriageExpectations))
	}
	for key, want := range unionTypeCarriageExpectations {
		if got[key] != want {
			t.Errorf("cell %s = %q, want %q", key, got[key], want)
		}
	}
}
