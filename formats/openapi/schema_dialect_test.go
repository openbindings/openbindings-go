package openapi

// The two guards the split in schema_dialect.go owes.
//
// 1. The 3.1 line DELEGATES, so what must be guarded is the ARTIFACT: the
//    vendored dialect bytes are the published ones, verbatim.
// 2. The 3.0 line TRANSCRIBES, so what must be guarded is the CELL SET: the
//    transcription answers for exactly the four keywords whose sentences the
//    accepted 3.0 editions state, and for nothing else. The sentences
//    themselves are checked against the pinned edition bytes by
//    `spec/scripts/verify-openapi-30-schema-object-transcription.mjs`, which is
//    where those bytes live.

import (
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"testing"
)

// The published digests of the artifacts the 3.1 verdict is delegated to.
// Re-fetch and re-digest to check: these are byte copies, never paraphrases.
//
//	https://spec.openapis.org/oas/3.1/dialect/base
//	https://spec.openapis.org/oas/3.1/meta/base
const (
	oas31DialectBaseSHA256 = "8a0e89e365dadbebce2921ce6244340c1090e9d544c60d977e9ad6b97a61227b"
	oas31MetaBaseSHA256    = "267a88226e64e96dfc8c89dbd7e863160c84715e0fb893ca1d9fbf9f830f1f54"
)

func TestVendoredAuthorityArtifactDigests(t *testing.T) {
	for _, artifact := range []struct {
		name  string
		bytes []byte
		want  string
	}{
		{oas31DialectURI, oas31DialectBaseJSON, oas31DialectBaseSHA256},
		{oas31MetaBaseURI, oas31MetaBaseJSON, oas31MetaBaseSHA256},
	} {
		sum := sha256.Sum256(artifact.bytes)
		if got := hex.EncodeToString(sum[:]); got != artifact.want {
			t.Errorf("vendored %s digest %s, published %s: the delegated artifact is not the authority's bytes", artifact.name, got, artifact.want)
		}
	}
}

// The transcription's whole surface, exercised at once: a node carrying every
// cell's trigger plus a spread of keyword values the 3.0 line does NOT judge.
// A cell added to `oas30SchemaObjectDefects` without a grounded sentence shows
// up here as an extra position.
func TestOAS30TranscriptionAnswersExactlyItsGroundedCells(t *testing.T) {
	node := map[string]any{
		// The four grounded cells, each triggered.
		"required":   []any{"a", "a"},
		"enum":       "not-a-list",
		"items":      []any{map[string]any{"type": "string"}},
		"properties": map[string]any{"f": true},
		// Positions this line's own dialect states nothing about, or states
		// the opposite of the 3.1 line about. None may be judged here.
		"exclusiveMinimum": true,
		"exclusiveMaximum": true,
		"minItems":         -1,
		"pattern":          42,
		"multipleOf":       0,
		"$recursiveAnchor": true,
		"contains":         3,
		"nullable":         true,
		"min_items":        3,
		"unknownKeyword":   map[string]any{},
	}
	want := []string{"/enum", "/items", "/properties/f", "/required"}
	if got := oas30SchemaObjectDefects(node); !reflect.DeepEqual(got, want) {
		t.Fatalf("the 3.0 transcription answers %v, want exactly %v (its four grounded cells)", got, want)
	}

	// And the same node under the 3.1 line, to show the difference is the
	// dialect's rather than ours: every position above is judged there, plus
	// the ones the transcription cannot reach.
	got31 := oas31DialectDefects(node)
	for _, position := range []string{"/$recursiveAnchor", "/exclusiveMinimum", "/minItems", "/pattern", "/multipleOf", "/contains"} {
		found := false
		for _, candidate := range got31 {
			if candidate == position {
				found = true
			}
		}
		if !found {
			t.Errorf("the delegated 3.1 verdict does not reach %s: %v", position, got31)
		}
	}
	for _, legal := range []string{"/nullable", "/min_items", "/unknownKeyword"} {
		for _, candidate := range got31 {
			if candidate == legal {
				t.Errorf("the delegated 3.1 verdict refuses %s, which the dialect admits as an annotation", legal)
			}
		}
	}
}
