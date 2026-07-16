package connect

// Binding-specification conformance corpus adapter: runs the spec
// repository's binding-specs/connect fixtures (CONN-D-01..03) through this
// module's own offline lanes — schema-carriage discrimination (incorporated
// from openbindings.grpc@1), base-URL grammar, and ref grammar/resolution —
// under the subcorpus README's verdict semantics: valid:false means a
// conformant openbindings.connect@1 processor refuses the document's
// family-scoped material at or before bind time, decidable offline with no
// network and no live source. Positive location-only fixtures (descriptorless
// mode) are judged by grammar alone (never dialed), so the run performs no
// I/O beyond reading the fixtures.
//
// The corpus root is located via OB_SPEC_CORPUS (the spec repo's
// conformance/ directory) or the local-dev sibling path; the test skips
// when absent.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func bindingSpecCorpusDir(t *testing.T) string {
	t.Helper()
	root := os.Getenv("OB_SPEC_CORPUS")
	if root == "" {
		root = filepath.Join("..", "..", "..", "spec", "conformance")
	}
	dir := filepath.Join(root, "binding-specs", "connect")
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("binding-specs conformance corpus not found at %s (set OB_SPEC_CORPUS)", dir)
	}
	return dir
}

// Fixture shapes per conformance/binding-specs/fixture.schema.json.
type corpusFixture struct {
	Rule        string       `json:"rule"`
	BindingSpec string       `json:"bindingSpec"`
	Tests       []corpusTest `json:"tests"`
}

type corpusTest struct {
	Description string          `json:"description"`
	Document    json.RawMessage `json:"document"`
	Valid       bool            `json:"valid"`
}

// corpusDocument is the raw view of a fixture's embedded OBI document,
// preserving member PRESENCE where a typed view cannot: `content: null` is
// a present member (the core §7 presence rule; json.RawMessage keeps it),
// and an omitted binding ref is distinct from a present empty string.
type corpusDocument struct {
	Sources  map[string]corpusSource  `json:"sources"`
	Bindings map[string]corpusBinding `json:"bindings"`
}

type corpusSource struct {
	BindingSpec string          `json:"bindingSpec"`
	Location    *string         `json:"location"`
	Content     json.RawMessage `json:"content"`
}

type corpusBinding struct {
	Source string  `json:"source"`
	Ref    *string `json:"ref"`
}

func TestBindingSpecCorpus(t *testing.T) {
	dir := bindingSpecCorpusDir(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read corpus dir: %v", err)
	}
	ran := false
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		var fix corpusFixture
		if err := json.Unmarshal(raw, &fix); err != nil {
			t.Fatalf("parse %s: %v", e.Name(), err)
		}
		if fix.BindingSpec != BindingSpec {
			t.Fatalf("%s: fixture bindingSpec %q is not %q", e.Name(), fix.BindingSpec, BindingSpec)
		}
		ran = true
		for _, tt := range fix.Tests {
			tt := tt
			t.Run(fix.Rule+"/"+tt.Description, func(t *testing.T) {
				refusal := judgeCorpusDocument(t, tt.Document)
				if tt.Valid && refusal != nil {
					t.Errorf("expected nothing to refuse, got: %v", refusal)
				}
				if !tt.Valid && refusal == nil {
					t.Errorf("expected a bind-time refusal, but the family-scoped material was accepted")
				}
			})
		}
	}
	if !ran {
		t.Fatal("corpus directory contains no fixture files")
	}
}

// judgeCorpusDocument routes one embedded OBI document's family-scoped
// material through this module's offline lanes, returning the first refusal
// or nil when there is nothing to refuse.
func judgeCorpusDocument(t *testing.T, raw json.RawMessage) error {
	t.Helper()
	var doc corpusDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("fixture document does not parse: %v", err)
	}
	for name, src := range doc.Sources {
		if src.BindingSpec != BindingSpec {
			continue
		}

		// Content lane (CONN-D-01): a present member must be one of the two
		// embedded schema carriages incorporated from openbindings.grpc@1;
		// an absent member selects descriptorless mode — nothing to refuse.
		var disc *discovery
		if src.Content != nil {
			var content any
			if err := json.Unmarshal(src.Content, &content); err != nil {
				t.Fatalf("fixture content does not parse: %v", err)
			}
			d, err := discoverFromContent(context.Background(), content)
			if err != nil {
				return err
			}
			disc = d
		}

		// Location lane (CONN-D-02): REQUIRED — a content-only source is
		// not conformant in this service-addressed family (the absent
		// member reaches the same grammar as the empty string) — and an
		// absolute http/https base URL. Grammar only, never dialed.
		loc := ""
		if src.Location != nil {
			loc = *src.Location
		}
		if err := validateBaseURL(loc); err != nil {
			return err
		}

		// Ref lane (CONN-D-03): ref is REQUIRED (an omitted ref reaches the
		// invoker as the empty string and is refused by the same grammar);
		// in schema mode resolution is offline and byte-exact, exactly as
		// run does before any network I/O; in descriptorless mode the
		// grammar-valid segments ride verbatim — nothing to resolve.
		for _, b := range doc.Bindings {
			if b.Source != name {
				continue
			}
			ref := ""
			if b.Ref != nil {
				ref = *b.Ref
			}
			svcName, methodName, err := parseRef(ref)
			if err != nil {
				return err
			}
			if disc != nil {
				if _, ie := resolveMethod(disc, svcName, methodName); ie != nil {
					return ie
				}
			}
		}
	}
	return nil
}
