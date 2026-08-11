package mcp

// Binding-specification conformance corpus adapter: runs the spec
// repository's binding-specs/mcp fixtures (MCP-D-01..03) through this
// module's own offline lanes — pinned-listing validation, endpoint grammar,
// and ref grammar/resolution — under the subcorpus README's verdict
// semantics: valid:false means a conformant openbindings.mcp@1 processor
// refuses the document's family-scoped material at or before bind time,
// decidable offline with no network and no live source. Positive
// location-only fixtures are judged by grammar alone (never connected), so
// the run performs no I/O beyond reading the fixtures.
//
// The corpus root is located via OB_SPEC_CORPUS (the spec repo's
// conformance/ directory) or the local-dev sibling path; the test skips
// when absent.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func bindingSpecCorpusDir(t *testing.T) string {
	t.Helper()
	root := os.Getenv("OB_SPEC_CORPUS")
	if root == "" {
		root = filepath.Join("..", "..", "..", "spec", "conformance")
	}
	dir := filepath.Join(root, "binding-specs", "mcp")
	if _, err := os.Stat(dir); err != nil {
		// OB_CORPUS_REQUIRED (set in CI) turns a missing corpus into a hard
		// failure so a mis-wired path turns CI red instead of silently green;
		// unset (local dev) it still skips.
		if os.Getenv("OB_CORPUS_REQUIRED") != "" {
			t.Fatalf("binding-specs conformance corpus not found at %s (OB_CORPUS_REQUIRED is set; set OB_SPEC_CORPUS)", dir)
		}
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

		// Content lane (MCP-D-01): a present member — null included —
		// must be a pinned listing.
		var pin *listing
		if src.Content != nil {
			l, err := parsePinnedListing(src.Content)
			if err != nil {
				return err
			}
			pin = l
		}

		// Location lane (MCP-D-02): REQUIRED — a content-only source
		// addresses nothing — and an absolute http/https URI. Grammar
		// only, never connected.
		loc := ""
		if src.Location != nil {
			loc = strings.TrimSpace(*src.Location)
		}
		if err := validateEndpoint(loc); err != nil {
			return err
		}

		// Ref lane (MCP-D-03): ref is REQUIRED (an omitted ref reaches the
		// invoker as the empty string and is refused by the same grammar);
		// with a pin, resolution is offline — byte-exact and
		// multiplicity-aware, exactly as run does before any connection.
		for _, b := range doc.Bindings {
			if b.Source != name {
				continue
			}
			ref := ""
			if b.Ref != nil {
				ref = *b.Ref
			}
			entityType, remainder, err := parseRef(ref)
			if err != nil {
				return err
			}
			if entityType != "tools" {
				return fmt.Errorf("openbindings.mcp@1 ref must use tools/<name>, got %q", ref)
			}
			if pin != nil {
				if _, rerr := resolveRef(pin, entityType, remainder, BindingSpec); rerr != nil {
					return rerr
				}
			}
		}
	}
	return nil
}
