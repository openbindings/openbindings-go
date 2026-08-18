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

// urlencodedLanePartitionCasesDigest pins the frozen twin case table. The
// identical file is executed by openapi-client/go and by
// openapi-client/typescript/src; changing it in one engine without the others
// fails here.
const urlencodedLanePartitionCasesDigest = "254966b36a9ee291416330518bbc2af341a2f54c2fe547f58e946ceb4e0d1e09"

type urlencodedLanePartitionTable struct {
	Comment   string                        `json:"$comment"`
	Partition map[string]string             `json:"partition"`
	Cases     []urlencodedLanePartitionCase `json:"cases"`
}

type urlencodedLanePartitionCase struct {
	Name            string         `json:"name"`
	OpenAPI         string         `json:"openapi"`
	Variant         string         `json:"variant"`
	Encoding        map[string]any `json:"encoding"`
	ExplicitControl bool           `json:"explicitControl"`
	Lane            string         `json:"lane"`
	Expect          string         `json:"expect"`
	Basis           string         `json:"basis"`
}

func loadURLEncodedLanePartitionTable(t *testing.T) urlencodedLanePartitionTable {
	t.Helper()
	raw, err := os.ReadFile("testdata/urlencoded-lane-partition-cases.json")
	if err != nil {
		t.Fatalf("read case table: %v", err)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != urlencodedLanePartitionCasesDigest {
		t.Fatalf("case table digest = %s, want %s (the table is shared byte-for-byte with the two twin engines)", got, urlencodedLanePartitionCasesDigest)
	}
	var table urlencodedLanePartitionTable
	if err := json.Unmarshal(raw, &table); err != nil {
		t.Fatalf("parse case table: %v", err)
	}
	if len(table.Cases) == 0 {
		t.Fatal("case table is empty")
	}
	return table
}

// urlencodedLanePartitionDocument renders one case as a WHOLE OpenAPI document.
// The document, and not a hand-built openapi3.MediaType, is what the engine has
// to be given: the shipped loader normalizes the raw tree before kin-openapi's
// typed model exists, and a harness that unmarshals a MediaType directly
// measures an engine the project does not ship.
func urlencodedLanePartitionDocument(t *testing.T, c urlencodedLanePartitionCase) []byte {
	t.Helper()
	media := map[string]any{
		"schema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"address": map[string]any{
					"type":       "object",
					"properties": map[string]any{"street": map[string]any{"type": "string"}},
				},
			},
		},
	}
	if c.Encoding != nil {
		media["encoding"] = map[string]any{"address": c.Encoding}
	}
	document := map[string]any{
		"openapi": c.OpenAPI,
		"info":    map[string]any{"title": "urlencoded lane partition case table", "version": "1.0.0"},
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

// urlencodedLanePartitionDecision renders one cell exactly as the twin engines
// render it. Refusal messages are each implementation's own surface, so only
// the decision itself crosses the twin boundary.
func urlencodedLanePartitionDecision(t *testing.T, c urlencodedLanePartitionCase) string {
	t.Helper()
	doc, err := loadDocument("", json.RawMessage(urlencodedLanePartitionDocument(t, c)))
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
	fields := map[string]any{"address": map[string]any{"street": "main st"}}
	encoded, err := buildURLEncodedBodyForRevision(doc, media, fields, BindingSpec)
	if err != nil {
		return "error"
	}
	if encoded == "" {
		return "admitted;emit=elided"
	}
	return "admitted;emit=" + encoded
}

// TestURLEncodedLanePartitionCaseTable executes the shared table.
//
// What it pins is that lane selection is PRESENCE-keyed and the same on every
// accepted edition. 3.0.4 and the 3.1 line each state that an explicitly
// defined style, explode or allowReserved makes the Encoding Object's
// contentType ignored, which is meaningful only because contentType otherwise
// governs; 3.0.4 adds that with all three absent "Encoding is to be based on
// contentType alone". 3.0.0-3.0.3 state no lane-selection rule of their own and
// reach the same behavior through their own section 4.1 patch-uniformity
// instruction. See each case's "basis".
func TestURLEncodedLanePartitionCaseTable(t *testing.T) {
	for _, c := range loadURLEncodedLanePartitionTable(t).Cases {
		t.Run(c.Name, func(t *testing.T) {
			if got := urlencodedLanePartitionDecision(t, c); got != c.Expect {
				t.Fatalf("%s: decision = %q, want %q\nbasis: %s", c.Name, got, c.Expect, c.Basis)
			}
		})
	}
}

// TestURLEncodedLanePartitionMatchesTheLiteralEditionTable states the rule as a
// claim in its own right, against the table's own literal edition->lane map, so
// a drift in the engine cannot pass by moving the expectations with it. The map
// is UNIFORM, and its uniformity is the assertion: no urlencoded behavior here
// keys on the artifact's openapi field.
func TestURLEncodedLanePartitionMatchesTheLiteralEditionTable(t *testing.T) {
	table := loadURLEncodedLanePartitionTable(t)
	want := map[string]string{
		"3.0.0": "content",
		"3.0.1": "content",
		"3.0.2": "content",
		"3.0.3": "content",
		"3.0.4": "content",
		"3.1.0": "content",
		"3.1.1": "content",
		"3.1.2": "content",
	}
	if len(table.Partition) != len(want) {
		t.Fatalf("table partition covers %d editions, want %d", len(table.Partition), len(want))
	}
	lanes := map[string]bool{}
	for edition, lane := range want {
		if got := table.Partition[edition]; got != lane {
			t.Fatalf("table partition[%s] = %q, want %q", edition, got, lane)
		}
		lanes[table.Partition[edition]] = true
	}
	if len(lanes) != 1 {
		t.Fatalf("the edition->lane map is not uniform: %v", table.Partition)
	}
	for _, c := range table.Cases {
		if c.ExplicitControl {
			if c.Lane != "style" {
				t.Fatalf("%s writes an explicit RFC6570-style control, so its lane must be style, not %q", c.Name, c.Lane)
			}
			continue
		}
		if c.Lane != want[c.OpenAPI] {
			t.Fatalf("%s writes no RFC6570-style control, so its lane must be %q on every accepted edition, not %q", c.Name, want[c.OpenAPI], c.Lane)
		}
	}
}

// TestURLEncodedLanePartitionIgnoresThePatchComponent states the deleted
// predicate as an executable claim rather than as an absence in prose: two
// documents differing ONLY in the patch component of their openapi field emit
// the same bytes, for every Encoding Object shape on both accepted lines.
func TestURLEncodedLanePartitionIgnoresThePatchComponent(t *testing.T) {
	table := loadURLEncodedLanePartitionTable(t)
	byVariant := map[string]map[string][]string{}
	for _, c := range table.Cases {
		line := c.OpenAPI[:strings.LastIndex(c.OpenAPI, ".")]
		key := line + "|" + c.Variant
		if byVariant[key] == nil {
			byVariant[key] = map[string][]string{}
		}
		decision := urlencodedLanePartitionDecision(t, c)
		byVariant[key][decision] = append(byVariant[key][decision], c.OpenAPI)
	}
	if len(byVariant) != 16 {
		t.Fatalf("covered %d line/variant cells, want 16", len(byVariant))
	}
	for key, decisions := range byVariant {
		if len(decisions) != 1 {
			t.Fatalf("%s emits different bytes across the patch component of one line: %v", key, decisions)
		}
	}
}

// TestURLEncodedLanePartitionTableCoversEveryEditionAndEncodingShape fails if
// the table stops exercising a branch the engines distinguish. A cell silently
// dropping out is the failure mode this asserts against.
func TestURLEncodedLanePartitionTableCoversEveryEditionAndEncodingShape(t *testing.T) {
	want := map[string]bool{}
	for _, edition := range []string{"3.0.0", "3.0.1", "3.0.2", "3.0.3", "3.0.4", "3.1.0", "3.1.1", "3.1.2"} {
		for _, variant := range []string{
			"no-encoding", "empty-encoding", "content-type",
			"style", "explode-true", "explode-false",
			"allow-reserved-true", "allow-reserved-false",
		} {
			want[fmt.Sprintf("%s|%s", edition, variant)] = true
		}
	}
	for _, c := range loadURLEncodedLanePartitionTable(t).Cases {
		if !want[c.Name] {
			t.Fatalf("case %q is not one of the covered cells", c.Name)
		}
		delete(want, c.Name)
	}
	if len(want) != 0 {
		t.Fatalf("case table is missing %d cells, including %v", len(want), firstKey(want))
	}
}
