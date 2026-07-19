package openapi

// Binding-specification conformance corpus adapter: runs the spec
// repository's binding-specs/openapi fixtures (OAPI-D-01..03) through this
// module's own offline lanes — content load, location grammar, and ref
// grammar/resolution — under the subcorpus README's verdict semantics:
// valid:false means a conformant openbindings.openapi@1 processor refuses
// the document's family-scoped material at or before bind time, decidable
// offline with no network and no live source. Positive location-only
// fixtures are judged by grammar alone (never dereferenced), so the run
// performs no I/O beyond reading the fixtures.
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

	"github.com/getkin/kin-openapi/openapi3"
)

func bindingSpecCorpusDir(t *testing.T) string {
	t.Helper()
	root := os.Getenv("OB_SPEC_CORPUS")
	if root == "" {
		root = filepath.Join("..", "..", "..", "spec", "conformance")
	}
	dir := filepath.Join(root, "binding-specs", "openapi")
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
	var obiDoc corpusDocument
	if err := json.Unmarshal(raw, &obiDoc); err != nil {
		t.Fatalf("fixture document does not parse: %v", err)
	}
	for name, src := range obiDoc.Sources {
		if src.BindingSpec != BindingSpec {
			continue
		}

		// Content lane (OAPI-D-01): a present member — null included —
		// must be the parsed document object or its source text. Fixture
		// artifacts are self-contained, so the load performs no I/O.
		var doc *openapi3.T
		if src.Content != nil {
			d, err := loadDocument("", src.Content)
			if err != nil {
				return err
			}
			doc = d
		}

		// Location lane (OAPI-D-02): grammar only, never dereferenced.
		if src.Location != nil {
			if err := validateDocumentAddress(*src.Location); err != nil {
				return err
			}
		}

		// Ref lane (OAPI-D-03): ref is REQUIRED (an omitted ref reaches the
		// invoker as the empty string and is refused by the same grammar);
		// pointer evaluation follows OAS reference resolution — the loader
		// resolves path-item $refs (3.1 components.pathItems included)
		// before the lookup, exactly as runBinding does.
		for _, b := range obiDoc.Bindings {
			if b.Source != name {
				continue
			}
			ref := ""
			if b.Ref != nil {
				ref = *b.Ref
			}
			pathTemplate, method, err := parseRef(ref)
			if err != nil {
				return err
			}
			if doc == nil {
				continue // location-only source: grammar-checked alone
			}
			if doc.Paths == nil {
				return fmt.Errorf("OpenAPI document has no paths defined")
			}
			pathItem := doc.Paths.Find(pathTemplate)
			if pathItem == nil {
				return fmt.Errorf("path %q not in OpenAPI doc", pathTemplate)
			}
			if pathItem.GetOperation(strings.ToUpper(method)) == nil {
				return fmt.Errorf("method %q not in path %q", method, pathTemplate)
			}
		}
	}
	return nil
}
