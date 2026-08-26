package openapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"github.com/openbindings/openbindings-go/synthesize"
)

// The style-lane composite-member case table is SHARED, byte-for-byte, with
// three other engines: openapi-client/go, openapi-client/typescript and
// openbindings-ts/packages/openapi. Each cell pins the ADMISSION decision all
// four must reach for one style-lane declaration, so a divergence in any one
// of them fails the others' suites.
//
// This engine executes the cells through the SHIPPED synthesizer: a refused
// parameter cell must leave no operation behind and must carry a target
// coverage entry excluded under openapi.parameter_style_expansion_excluded, and
// a refused body cell must leave the operation standing with its request-media
// alternative excluded. The two openapi-client engines execute the same cells
// through the shipped admission predicates and additionally assert the member
// each names.
//
// Authority: styleLaneUndefinedExpansionMember in media.go reads the style
// table per edition. Package:
// design/openapi-style-lane-composite-member-ruling.md, RULED 2026-08-18.
const styleLaneCompositeMemberCasesDigest = "1ea1045c75039b00c1035a2e2c3d09e440644e32a5fa1c3689be6add1eac7673"

type styleLaneCompositeMemberCase struct {
	Name     string          `json:"name"`
	OpenAPI  string          `json:"openapi"`
	Position string          `json:"position"`
	In       string          `json:"in"`
	Style    string          `json:"style"`
	Explode  *bool           `json:"explode"`
	Media    string          `json:"media"`
	Encoding map[string]any  `json:"encoding"`
	Schema   json.RawMessage `json:"schema"`
	Expect   string          `json:"expect"`
	Member   string          `json:"member"`
	Basis    string          `json:"basis"`
}

func loadStyleLaneCompositeMemberCases(t *testing.T) []styleLaneCompositeMemberCase {
	t.Helper()
	raw, err := os.ReadFile("testdata/style-lane-composite-member-cases.json")
	if err != nil {
		t.Fatalf("read case table: %v", err)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != styleLaneCompositeMemberCasesDigest {
		t.Fatalf("case table digest = %s, want %s (the table is shared byte-for-byte with three twin engines)", got, styleLaneCompositeMemberCasesDigest)
	}
	var table struct {
		Cases []styleLaneCompositeMemberCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &table); err != nil {
		t.Fatalf("parse case table: %v", err)
	}
	if len(table.Cases) == 0 {
		t.Fatal("case table is empty")
	}
	return table.Cases
}

// styleLaneCompositeMemberDocument renders one cell as a WHOLE OpenAPI
// document. The document, and not a hand-built model object, is what the
// engine has to be given: the shipped loader normalizes the raw tree before
// the typed model exists, and a harness that skips it measures an engine the
// project does not ship.
func styleLaneCompositeMemberDocument(t *testing.T, c styleLaneCompositeMemberCase) []byte {
	t.Helper()
	var schema any
	if len(c.Schema) > 0 && string(c.Schema) != "null" {
		if err := json.Unmarshal(c.Schema, &schema); err != nil {
			t.Fatalf("%s: parse cell schema: %v", c.Name, err)
		}
	}

	var paths map[string]any
	if c.Position == "parameter" {
		parameter := map[string]any{"name": "filter", "in": c.In}
		if c.Style != "" {
			parameter["style"] = c.Style
		}
		if c.Explode != nil {
			parameter["explode"] = *c.Explode
		}
		if schema != nil {
			parameter["schema"] = schema
		} else {
			// The content lane, declared as the table's only schema-less cell.
			parameter["content"] = map[string]any{"application/json": map[string]any{
				"schema": map[string]any{"type": "object", "properties": map[string]any{"where": map[string]any{"type": "object"}}},
			}}
		}
		template := "/q"
		if c.In == "path" {
			template = "/q/{filter}"
			parameter["required"] = true
		}
		paths = map[string]any{template: map[string]any{"get": map[string]any{
			"operationId": "query",
			"parameters":  []any{parameter},
			"responses":   map[string]any{"200": map[string]any{"description": "ok"}},
		}}}
	} else {
		media := map[string]any{"schema": map[string]any{
			"type":       "object",
			"properties": map[string]any{"field": schema},
		}}
		if c.Encoding != nil {
			media["encoding"] = map[string]any{"field": c.Encoding}
		}
		paths = map[string]any{"/form": map[string]any{"post": map[string]any{
			"operationId": "postForm",
			// The body stays OPTIONAL so a refused candidate excludes the
			// alternative rather than the whole target: the alternative is the
			// unit this cell is about.
			"requestBody": map[string]any{"content": map[string]any{c.Media: media}},
			"responses":   map[string]any{"200": map[string]any{"description": "ok"}},
		}}}
	}

	encoded, err := json.Marshal(map[string]any{
		"openapi": c.OpenAPI,
		"info":    map[string]any{"title": "style lane composite member case table", "version": "1.0.0"},
		"servers": []any{map[string]any{"url": "https://api.example.test"}},
		"paths":   paths,
	})
	if err != nil {
		t.Fatalf("%s: marshal document: %v", c.Name, err)
	}
	return encoded
}

func TestStyleLaneCompositeMemberCaseTable(t *testing.T) {
	parameterCells, bodyCells, refusedCells := 0, 0, 0
	for _, testCase := range loadStyleLaneCompositeMemberCases(t) {
		if testCase.Position == "parameter" {
			parameterCells++
		} else {
			bodyCells++
		}
		if testCase.Expect == "refused" {
			refusedCells++
		}
		t.Run(testCase.Name, func(t *testing.T) {
			result, err := NewSynthesizer().SynthesizeInterfaceWithCoverage(context.Background(), &synthesize.SynthesizeInput{
				Sources: []synthesize.SynthesizeSource{{
					BindingSpec: bindingSpecForTestDocument(styleLaneCompositeMemberDocument(t, testCase)),
					Content:     json.RawMessage(styleLaneCompositeMemberDocument(t, testCase)),
				}},
			})
			if err != nil {
				t.Fatalf("synthesize: %v", err)
			}
			if testCase.Position == "parameter" {
				assertStyleLaneParameterCell(t, testCase, result)
				return
			}
			assertStyleLaneBodyCell(t, testCase, result)
		})
	}
	// The table's own shape, asserted rather than described: a later editor who
	// deletes a position's cells has to notice.
	if parameterCells == 0 || bodyCells == 0 || refusedCells == 0 {
		t.Fatalf("case table shape = %d parameter / %d body / %d refused; every one must be non-zero",
			parameterCells, bodyCells, refusedCells)
	}
}

func assertStyleLaneParameterCell(t *testing.T, c styleLaneCompositeMemberCase, result *synthesize.SynthesizeResult) {
	t.Helper()
	_, present := result.Interface.Operations["query"]
	var entry *synthesize.SynthesisCoverageEntry
	for index := range result.Coverage.Entries {
		if result.Coverage.Entries[index].Scope == synthesize.SynthesisCoverageTarget {
			entry = &result.Coverage.Entries[index]
			break
		}
	}
	if entry == nil {
		t.Fatalf("no target coverage entry emitted")
	}
	switch c.Expect {
	case "admitted":
		if !present {
			t.Fatalf("operation absent; coverage says %s / %s", entry.Status, entry.ReasonCode)
		}
		if entry.Status != synthesize.SynthesisRepresented {
			t.Fatalf("target status = %s, want represented", entry.Status)
		}
	case "refused":
		if present {
			t.Fatalf("operation present, want it excluded")
		}
		if entry.Status != synthesize.SynthesisExcluded {
			t.Fatalf("target status = %s, want excluded", entry.Status)
		}
		if entry.ReasonCode != "openapi.parameter_style_expansion_excluded" {
			t.Fatalf("target reason code = %q, want openapi.parameter_style_expansion_excluded", entry.ReasonCode)
		}
		wantRule := openAPIRule(bindingSpecForOpenAPIEdition(c.OpenAPI), "P-02")
		if entry.Rule != wantRule {
			t.Fatalf("target rule = %q, want %s", entry.Rule, wantRule)
		}
	default:
		t.Fatalf("unknown expectation %q", c.Expect)
	}
}

func assertStyleLaneBodyCell(t *testing.T, c styleLaneCompositeMemberCase, result *synthesize.SynthesizeResult) {
	t.Helper()
	var entry *synthesize.SynthesisCoverageEntry
	for index := range result.Coverage.Entries {
		if result.Coverage.Entries[index].Scope == synthesize.SynthesisCoverageAlternative {
			entry = &result.Coverage.Entries[index]
			break
		}
	}
	if entry == nil {
		t.Fatalf("no request-media alternative coverage entry emitted")
	}
	switch c.Expect {
	case "admitted":
		if entry.Status != synthesize.SynthesisRepresented {
			t.Fatalf("alternative status = %s / %s, want represented", entry.Status, entry.ReasonCode)
		}
	case "refused":
		if entry.Status != synthesize.SynthesisExcluded {
			t.Fatalf("alternative status = %s, want excluded", entry.Status)
		}
		if entry.ReasonCode != "openapi.request_media_excluded" {
			t.Fatalf("alternative reason code = %q, want openapi.request_media_excluded", entry.ReasonCode)
		}
	default:
		t.Fatalf("unknown expectation %q", c.Expect)
	}
}
