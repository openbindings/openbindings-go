package openapi

// The §9.2 normalized-collision CONFINEMENT case table, shared
// byte-identically with the two client engines and the TypeScript adapter
// (testdata/media-collision-confinement-cases.json).
//
// Two keys in ONE content map that denote the same parsed media type are a
// normalized collision, and the defect confines to that colliding parsed
// identity -- the smallest unit that owns it. Here the REQUEST cells run
// through this engine's shipped path, synthesis and its coverage ledger: a
// colliding key is an accounted `excluded` alternative naming the identity it
// collides on, while its non-colliding siblings stay `represented` and the
// target survives. The RESPONSE cells run through this package's response
// media twin, which has no non-test caller today; they are carried so the
// twin cannot silently re-diverge from the client engine that ships it.

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/openbindings/openbindings-go/synthesize"
)

type mediaCollisionCase struct {
	Name              string         `json:"name"`
	OpenAPI           string         `json:"openapi"`
	Side              string         `json:"side"`
	Description       string         `json:"description"`
	Content           map[string]any `json:"content"`
	Select            string         `json:"select"`
	ResponseBody      string         `json:"responseBody"`
	Outcome           string         `json:"outcome"`
	Output            any            `json:"output"`
	Advertised        []string       `json:"advertised"`
	Target            string         `json:"target"`
	TargetReasonCode  string         `json:"targetReasonCode"`
	TargetRule        string         `json:"targetRule"`
	Represented       []string       `json:"represented"`
	Excluded          []string       `json:"excluded"`
	CollidingIdentity string         `json:"collidingIdentity"`
}

func loadMediaCollisionCases(t *testing.T) []mediaCollisionCase {
	t.Helper()
	data, err := os.ReadFile("testdata/media-collision-confinement-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var table struct {
		Cases []mediaCollisionCase `json:"cases"`
	}
	if err := json.Unmarshal(data, &table); err != nil {
		t.Fatal(err)
	}
	if len(table.Cases) == 0 {
		t.Fatal("case table is empty")
	}
	return table.Cases
}

func mediaCollisionRequestDocument(t *testing.T, fixture mediaCollisionCase) []byte {
	t.Helper()
	document := map[string]any{
		"openapi": fixture.OpenAPI,
		"info":    map[string]any{"title": "media collision confinement", "version": "1"},
		"servers": []any{map[string]any{"url": "https://api.example.test"}},
		"paths": map[string]any{
			"/items": map[string]any{
				"post": map[string]any{
					"operationId": "createItem",
					"requestBody": map[string]any{"required": true, "content": fixture.Content},
					"responses":   map[string]any{"204": map[string]any{"description": "stored"}},
				},
			},
		},
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestSharedMediaCollisionConfinementConformance(t *testing.T) {
	requestCells, responseCells, confinedCells, allCollidingCells, controlCells := 0, 0, 0, 0, 0
	for _, fixture := range loadMediaCollisionCases(t) {
		fixture := fixture
		switch fixture.Side {
		case "request":
			requestCells++
		case "response":
			responseCells++
		default:
			t.Fatalf("unknown side %q", fixture.Side)
		}
		switch {
		case fixture.CollidingIdentity == "" && len(fixture.Advertised) == len(fixture.Content):
			controlCells++
		case len(fixture.Represented) == 0 && len(fixture.Advertised) == 0:
			allCollidingCells++
		default:
			confinedCells++
		}
		t.Run(fixture.Name, func(t *testing.T) {
			if fixture.Side == "request" {
				assertMediaCollisionRequestCell(t, fixture)
				return
			}
			assertMediaCollisionResponseCell(t, fixture)
		})
	}
	// The table's own shape, asserted rather than described: a later editor
	// who deletes one of the four shapes has to notice.
	if requestCells == 0 || responseCells == 0 || confinedCells == 0 || allCollidingCells == 0 || controlCells == 0 {
		t.Fatalf("case table shape = %d request / %d response / %d confined / %d all-colliding / %d control; every one must be non-zero",
			requestCells, responseCells, confinedCells, allCollidingCells, controlCells)
	}
}

func assertMediaCollisionRequestCell(t *testing.T, fixture mediaCollisionCase) {
	t.Helper()
	document := mediaCollisionRequestDocument(t, fixture)
	result, err := NewSynthesizer().SynthesizeInterfaceWithCoverage(context.Background(), &synthesize.SynthesizeInput{
		Sources: []synthesize.SynthesizeSource{{
			BindingSpec: bindingSpecForTestDocument(document),
			Content:     json.RawMessage(document),
		}},
	})
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}

	const targetRef = "#/paths/~1items/post"
	var target *synthesize.SynthesisCoverageEntry
	alternatives := map[string]synthesize.SynthesisCoverageEntry{}
	for index := range result.Coverage.Entries {
		entry := result.Coverage.Entries[index]
		switch entry.Scope {
		case synthesize.SynthesisCoverageTarget:
			target = &result.Coverage.Entries[index]
		case synthesize.SynthesisCoverageAlternative:
			alternatives[strings.TrimPrefix(entry.SourceRef, targetRef+"/requestBody/content/")] = entry
		}
	}
	if target == nil {
		t.Fatal("no target coverage entry emitted")
	}

	_, operationPresent := result.Interface.Operations["createItem"]
	switch fixture.Target {
	case "represented":
		if !operationPresent {
			t.Fatalf("operation absent; coverage says %s / %s", target.Status, target.ReasonCode)
		}
		if target.Status != synthesize.SynthesisRepresented {
			t.Fatalf("target status = %s / %s, want represented", target.Status, target.ReasonCode)
		}
	case "excluded":
		if operationPresent {
			t.Fatal("operation present, want the target excluded")
		}
		if target.Status != synthesize.SynthesisExcluded {
			t.Fatalf("target status = %s, want excluded", target.Status)
		}
		if target.ReasonCode != fixture.TargetReasonCode {
			t.Fatalf("target reason code = %q, want %q", target.ReasonCode, fixture.TargetReasonCode)
		}
		legacyRule := strings.TrimPrefix(fixture.TargetRule, "OAPI-")
		if legacyRule == "P-04" {
			legacyRule = "P-03"
		} else if legacyRule == "P-03" {
			legacyRule = "P-02"
		}
		wantRule := openAPIRule(bindingSpecForOpenAPIEdition(fixture.OpenAPI), legacyRule)
		if target.Rule != wantRule {
			t.Fatalf("target rule = %q, want %q", target.Rule, wantRule)
		}
	default:
		t.Fatalf("unknown target expectation %q", fixture.Target)
	}

	if got, want := len(alternatives), len(fixture.Represented)+len(fixture.Excluded); got != want {
		t.Fatalf("alternative entry count = %d, want %d (%v)", got, want, alternatives)
	}
	for _, mediaKey := range fixture.Represented {
		entry, present := alternatives[escapeJSONPointerToken(mediaKey)]
		if !present {
			t.Fatalf("no alternative entry for %q; entries = %v", mediaKey, alternatives)
		}
		if entry.Status != synthesize.SynthesisRepresented {
			t.Fatalf("alternative %q status = %s / %s, want represented", mediaKey, entry.Status, entry.ReasonCode)
		}
	}
	for _, mediaKey := range fixture.Excluded {
		entry, present := alternatives[escapeJSONPointerToken(mediaKey)]
		if !present {
			t.Fatalf("no alternative entry for %q; entries = %v", mediaKey, alternatives)
		}
		if entry.Status != synthesize.SynthesisExcluded {
			t.Fatalf("alternative %q status = %s, want excluded", mediaKey, entry.Status)
		}
		// The reason vocabulary is unchanged by confinement: a colliding
		// alternative is the OAPI-P-04 media exclusion, never OAPI-P-03's
		// parameter-boundary flattening collision.
		wantRule := openAPIRule(bindingSpecForOpenAPIEdition(fixture.OpenAPI), "P-03")
		if entry.ReasonCode != "openapi.request_media_excluded" || entry.Rule != wantRule {
			t.Fatalf("alternative %q accounted %s / %s, want openapi.request_media_excluded / %s", mediaKey, entry.ReasonCode, entry.Rule, wantRule)
		}
		if !strings.Contains(entry.Message, fixture.CollidingIdentity) {
			t.Errorf("alternative %q message = %q, want it to name the colliding identity %q", mediaKey, entry.Message, fixture.CollidingIdentity)
		}
	}
}

func assertMediaCollisionResponseCell(t *testing.T, fixture mediaCollisionCase) {
	t.Helper()
	content := openapi3.Content{}
	for key := range fixture.Content {
		content[key] = emptyMedia()
	}
	response := &openapi3.Response{Content: content}

	matched, err := governingResponseMediaFor(response, fixture.Select, BindingSpec)
	if fixture.Outcome == "usable" {
		if err != nil {
			t.Fatalf("governing response media = %v, want a confined match", err)
		}
		if matched.canonical == "" {
			t.Fatalf("governing response media = %#v", matched)
		}
	} else if err == nil {
		t.Fatalf("governing response media = %#v, want a loud refusal", matched)
	}

	op := &openapi3.Operation{Responses: openapi3.NewResponses()}
	op.Responses.Set("200", &openapi3.ResponseRef{Value: response})
	advertised := successMediaTypesFor(op, BindingSpec)
	if len(advertised) != len(fixture.Advertised) {
		t.Fatalf("advertised success media = %v, want %v", advertised, fixture.Advertised)
	}
	for index, want := range fixture.Advertised {
		if advertised[index] != want {
			t.Fatalf("advertised success media = %v, want %v", advertised, fixture.Advertised)
		}
	}
}
