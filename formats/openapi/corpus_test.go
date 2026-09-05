package openapi

// Binding-specification conformance corpus adapter: runs the spec
// repository's binding-specs/openapi fixtures (OAPI-D-01..03) through this
// module's own offline lanes — content load, location grammar, and selector
// grammar/resolution — under the subcorpus README's verdict semantics:
// valid:false means a conformant latest-revision OpenAPI processor refuses
// the document's family-scoped material at or before bind time, decidable
// offline with no network and no live source. Positive location-only
// fixtures are judged by grammar alone (never dereferenced), so the run
// performs no I/O beyond reading the fixtures.
//
// The corpus root is located via OB_SPEC_CORPUS (the spec repo's
// conformance/ directory) or the local-dev sibling path; the test skips
// when absent.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	openapiprovider "github.com/openbindings/openapi-client/go/provider"
	openbindings "github.com/openbindings/openbindings-go"
)

func bindingSpecCorpusDir(t *testing.T, family string) string {
	t.Helper()
	root := os.Getenv("OB_SPEC_CORPUS")
	if root == "" {
		root = filepath.Join("..", "..", "..", "spec", "conformance")
	}
	dir := filepath.Join(root, "binding-specs", family)
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
// and an omitted binding selector is distinct from a present empty string.
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
	Source   string  `json:"source"`
	Selector *string `json:"selector"`
}

func TestBindingSpecCorpus(t *testing.T) {
	for _, family := range []struct {
		name        string
		bindingSpec string
	}{
		{"openapi-2.0", BindingSpecOpenAPI20},
		{"openapi-3.0", BindingSpecOpenAPI30},
		{"openapi-3.1", BindingSpecOpenAPI31},
		{"openapi-3.2", BindingSpecOpenAPI32},
	} {
		family := family
		t.Run(family.name, func(t *testing.T) {
			dir := bindingSpecCorpusDir(t, family.name)
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
				if fix.BindingSpec != family.bindingSpec {
					t.Fatalf("%s: fixture bindingSpec %q, want %q", e.Name(), fix.BindingSpec, family.bindingSpec)
				}
				ran = true
				for _, tt := range fix.Tests {
					tt := tt
					t.Run(fix.Rule+"/"+tt.Description, func(t *testing.T) {
						refusal := judgeCorpusDocument(t, tt.Document, family.bindingSpec)
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
		})
	}
}

// judgeCorpusDocument routes one embedded OBI document's family-scoped
// material through this module's offline lanes, returning the first refusal
// or nil when there is nothing to refuse.
func judgeCorpusDocument(t *testing.T, raw json.RawMessage, bindingSpec string) error {
	t.Helper()
	var obiDoc corpusDocument
	if err := json.Unmarshal(raw, &obiDoc); err != nil {
		t.Fatalf("fixture document does not parse: %v", err)
	}
	for name, src := range obiDoc.Sources {
		if src.BindingSpec != bindingSpec {
			for _, binding := range obiDoc.Bindings {
				if binding.Source == name {
					return fmt.Errorf("source %q selects sibling binding specification %q, want %q", name, src.BindingSpec, bindingSpec)
				}
			}
			continue
		}

		// Content lane (OAPI-D-01): a present member — null included —
		// must be the parsed document object or its source text. Fixture
		// artifacts are self-contained, so the load performs no I/O.
		var artifact *openapiprovider.Artifact
		var swagger20 *openapiprovider.Swagger20Document
		if src.Content != nil {
			switch bindingSpec {
			case BindingSpecOpenAPI20:
				content, err := openbindings.ContentToBytes(src.Content)
				if err != nil {
					return err
				}
				loaded, err := openapiprovider.LoadSwagger20(context.Background(), openapiprovider.Swagger20Source{Content: content}, openapiprovider.ClientOptions{})
				if err != nil {
					return err
				}
				swagger20 = loaded.Document()
			default:
				data, err := openbindings.ContentToBytes(src.Content)
				if err != nil {
					return err
				}
				artifact, err = openapiprovider.LoadArtifact(t.Context(), openapiprovider.Source{Content: data}, openapiprovider.ArtifactLoadOptions{})
				if err != nil {
					return err
				}
				if !openAPIBindingSpecRegistry[bindingSpec].editions[string(artifact.Edition())] {
					return fmt.Errorf("document edition %q is not admitted by binding specification %q", artifact.Edition(), bindingSpec)
				}
			}
		}

		// Location lane (OAPI-D-02): grammar only, never dereferenced.
		if src.Location != nil {
			if err := validateDocumentAddress(*src.Location); err != nil {
				return err
			}
		}

		// Selector lane (OAPI-D-03): selector is REQUIRED (an omitted selector reaches the
		// invoker as the empty string and is refused by the same grammar);
		// pointer evaluation follows OAS reference resolution — the loader
		// resolves path-item $refs (3.1 components.pathItems included)
		// before the lookup, exactly as runBinding does.
		for _, b := range obiDoc.Bindings {
			if b.Source != name {
				continue
			}
			selector := ""
			if b.Selector != nil {
				selector = *b.Selector
			}
			switch bindingSpec {
			case BindingSpecOpenAPI20:
				if err := openapiprovider.ValidateSwagger20Selector(selector); err != nil {
					return err
				}
				if swagger20 == nil {
					continue // location-only source: grammar-checked alone
				}
				if err := openapiprovider.ValidateSwagger20Operation(context.Background(), openapiprovider.Swagger20PrepareOptions{
					Source: openapiprovider.Swagger20Source{Document: swagger20}, Ref: selector,
				}); err != nil {
					return err
				}
				continue
			default:
				edition := openapiprovider.EditionOpenAPI304
				if bindingSpec == BindingSpecOpenAPI31 {
					edition = openapiprovider.EditionOpenAPI312
				} else if bindingSpec == BindingSpecOpenAPI32 {
					edition = openapiprovider.EditionOpenAPI320
				}
				if artifact != nil {
					edition = artifact.Edition()
				}
				if _, err := openapiprovider.ParseOperationReference(selector, edition); err != nil {
					return err
				}
				if artifact == nil {
					continue
				}
				if _, err := artifact.ResolveOperation(selector); err != nil {
					// The D-rule corpus judges selector grammar and structural
					// resolution in isolation. A structurally resolved target may
					// still be excluded later by a request-surface P-rule (for
					// example, path-parameter correspondence), exactly as the 3.0
					// and 3.1 lanes below do not apply their parameter gates here.
					var resolution *openapiprovider.OperationResolutionError
					if !errors.As(err, &resolution) || (resolution.Kind != openapiprovider.OperationTargetExcluded && resolution.Kind != openapiprovider.OperationTargetInvalid) {
						return err
					}
				}
				continue
			}
		}
	}
	return nil
}
