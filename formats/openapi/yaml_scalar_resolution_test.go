package openapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/oasdiff/yaml"
)

// yamlScalarResolutionCasesDigest pins the frozen twin case table. The
// identical file is executed by openbindings-go/formats/openapi, by
// openapi-client/typescript, and by openbindings-ts/packages/openapi against
// that package's BUILT dist; changing it in one engine without the others
// fails here. Regenerate with
// corpus-lab/scripts/build-yaml-scalar-resolution-table.mjs, which re-proves
// every authority quote against the pinned YAML 1.2.2 bytes first.
const yamlScalarResolutionCasesDigest = "f34df2ad41c9fb3f99f49406c4e3479e862d5f66cd398a508fedbece97be43b0"

type yamlScalarResolutionTable struct {
	Comment string                     `json:"$comment"`
	Cases   []yamlScalarResolutionCase `json:"cases"`
	Auth    map[string]any             `json:"authority"`
}

type yamlScalarResolutionCase struct {
	Name     string `json:"name"`
	Position string `json:"position"`
	Spelling string `json:"spelling"`
	Outcome  string `json:"outcome"`
	Image    string `json:"image"`
	Basis    string `json:"basis"`
}

func loadYAMLScalarResolutionTable(t *testing.T) []yamlScalarResolutionCase {
	t.Helper()
	raw, err := os.ReadFile("testdata/yaml-scalar-resolution-cases.json")
	if err != nil {
		t.Fatalf("read case table: %v", err)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != yamlScalarResolutionCasesDigest {
		t.Fatalf("case table digest = %s, want %s (the table is shared byte-for-byte with the twin engines)", got, yamlScalarResolutionCasesDigest)
	}
	var table yamlScalarResolutionTable
	if err := json.Unmarshal(raw, &table); err != nil {
		t.Fatalf("parse case table: %v", err)
	}
	if len(table.Cases) == 0 {
		t.Fatal("case table is empty")
	}
	return table.Cases
}

// yamlScalarResolutionDocument renders one case as a WHOLE OpenAPI document,
// in YAML, because YAML is the grammar under test: a harness that hands the
// engine a pre-parsed tree measures nothing at all here.
//
// The scalar sits at `info/x-case`, a specification extension, so it survives
// into the loaded document untouched by any schema or parameter machinery.
func yamlScalarResolutionDocument(c yamlScalarResolutionCase) []byte {
	var body string
	switch c.Position {
	case "key":
		body = "  x-case:\n    " + c.Spelling + ": marker\n"
	case "merge":
		body = "  x-anchor: &anchor\n    x: 1\n  x-case:\n    " + c.Spelling + ": *anchor\n    y: 2\n"
	default:
		body = "  x-case: " + c.Spelling + "\n"
	}
	return []byte("openapi: 3.1.0\n" +
		"info:\n" +
		"  title: yaml scalar resolution case table\n" +
		"  version: 1.0.0\n" +
		body +
		"paths:\n" +
		"  /p:\n" +
		"    get:\n" +
		"      operationId: getP\n" +
		"      responses:\n" +
		"        \"200\":\n" +
		"          description: ok\n")
}

// yamlScalarResolutionImage loads the case through this engine's SHIPPED
// loader and renders the value the document carries at info/x-case as
// canonical JSON. `source-refused` is the load failing.
func yamlScalarResolutionImage(t *testing.T, c yamlScalarResolutionCase) string {
	t.Helper()
	document, err := loadDocument("", json.RawMessage(yamlScalarResolutionDocument(c)))
	if err != nil {
		return "source-refused"
	}
	if document.Info == nil {
		t.Fatalf("%s: loaded document has no Info Object", c.Name)
	}
	value, present := document.Info.Extensions["x-case"]
	if !present {
		t.Fatalf("%s: loaded document does not carry info/x-case; the harness, not the engine, is wrong", c.Name)
	}
	// SetEscapeHTML(false): `<` and `>` are ordinary characters in a JSON
	// string, and the merge-key case turns on seeing `<<` rather than
	// `\u003c\u003c`. The twin renderings must agree character for character.
	var rendered strings.Builder
	encoder := json.NewEncoder(&rendered)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		t.Fatalf("%s: render loaded value: %v", c.Name, err)
	}
	return strings.TrimRight(rendered.String(), "\n")
}

func TestYAMLScalarResolutionTable(t *testing.T) {
	cases := loadYAMLScalarResolutionTable(t)
	for _, c := range cases {
		t.Run(strings.NewReplacer("/", "_", " ", "_").Replace(c.Name), func(t *testing.T) {
			want := c.Image
			if c.Outcome == "source-refused" {
				want = "source-refused"
			}
			if got := yamlScalarResolutionImage(t, c); got != want {
				t.Fatalf("%s: image = %s, want %s\n  basis: %s", c.Name, got, want, c.Basis)
			}
		})
	}
}

// TestYAMLScalarResolutionTableIsTotal keeps the table from silently shrinking
// below the classes F-O1-7 was opened on.
func TestYAMLScalarResolutionTableIsTotal(t *testing.T) {
	cases := loadYAMLScalarResolutionTable(t)
	required := []string{
		"value|017", "value|0o17", "value|17", "value|1_000", "value|0b101", "value|0x1F",
		"merge|<<", "value|true", "value|True", "value|TRUE",
		"value|null", "value|Null", "value|~",
		"value|.inf", "value|.nan",
		"value|2020-01-01T12:00:00Z", "value|12:30:45",
		"tag|!!timestamp", "tag|!!binary", "tag|!!merge", "tag|!!set", "tag|!ruby/object",
	}
	present := map[string]bool{}
	for _, c := range cases {
		present[c.Name] = true
	}
	for _, name := range required {
		if !present[name] {
			t.Errorf("case table lost the required case %q", name)
		}
	}
	if len(cases) < len(required) {
		t.Fatalf("case table has %d cases, fewer than the %d required", len(cases), len(required))
	}
}

// TestYAMLScalarResolutionPinsEveryOpenMagnitudeRow is R3's guard.
//
// F-O1-15 — what a numeric scalar outside the double domain becomes — is
// OPEN, and it spans EVERY §10.3.2 numeric row: base 10, base 8, base 16 and
// the float row. A class filed as open needs a pin on every row it spans in
// BOTH directions of its boundary, or "the behavior is pinned by name so it
// cannot drift silently" is not true. Pinning the class on the float row
// alone is exactly what let one engine decide it by side effect, toward
// refusal, with the corpus byte-identical.
//
// The spellings are constructed here rather than copied out of the table, so
// this test is an independent expectation and not a restatement of the file.
func TestYAMLScalarResolutionPinsEveryOpenMagnitudeRow(t *testing.T) {
	byName := map[string]yamlScalarResolutionCase{}
	for _, c := range loadYAMLScalarResolutionTable(t) {
		byName[c.Name] = c
	}
	rows := []struct {
		row      string
		inDomain string
		outside  string
	}{
		{"base 10", "1" + strings.Repeat("0", 308), "1" + strings.Repeat("0", 309)},
		{"base 16", "0x" + strings.Repeat("F", 255), "0x" + strings.Repeat("F", 256)},
		{"base 8", "0o" + strings.Repeat("7", 341), "0o" + strings.Repeat("7", 342)},
		{"float", "1e308", "1e309"},
	}
	for _, row := range rows {
		inside, ok := byName["value|"+row.inDomain]
		if !ok {
			t.Errorf("%s row: the case table has no representable-side pin", row.row)
		} else if inside.Outcome != "resolved" || strings.HasPrefix(inside.Image, `"`) {
			t.Errorf("%s row: the representable side is pinned as %s/%s, want a number", row.row, inside.Outcome, inside.Image)
		}
		outside, ok := byName["value|"+row.outside]
		if !ok {
			t.Errorf("%s row: the case table has no out-of-domain pin, so F-O1-15 can be decided here by side effect", row.row)
			continue
		}
		want := `"` + row.outside + `"`
		if outside.Outcome != "resolved" || outside.Image != want {
			t.Errorf("%s row: the out-of-domain side is pinned as %s/%s, want the source text %s; changing it decides F-O1-15, which is a ruling", row.row, outside.Outcome, outside.Image, want)
		}
	}
}

// TestYAMLScalarResolutionRefusalsAreLoud proves the refused half fails at
// LOAD rather than producing a silently different value.
func TestYAMLScalarResolutionRefusalsAreLoud(t *testing.T) {
	for _, c := range loadYAMLScalarResolutionTable(t) {
		if c.Outcome != "source-refused" {
			continue
		}
		_, err := loadDocument("", json.RawMessage(yamlScalarResolutionDocument(c)))
		if err == nil {
			t.Errorf("%s: load succeeded; %s", c.Name, c.Basis)
			continue
		}
		if message := err.Error(); strings.TrimSpace(message) == "" {
			t.Errorf("%s: refusal carries no message", c.Name)
		}
	}
}

// TestYAMLScalarResolutionLeavesConformantResourcesAlone is the blast-radius
// guard: a document whose scalars the incumbent decoder already resolves
// conformantly must not be re-encoded, because re-encoding is what would move
// the corpus.
func TestYAMLScalarResolutionLeavesConformantResourcesAlone(t *testing.T) {
	ordinary := []byte("openapi: 3.1.0\n" +
		"info:\n  title: ordinary\n  version: 1.0.0\n" +
		"paths:\n  /p:\n    get:\n      operationId: getP\n      responses:\n        \"200\":\n          description: ok\n")
	normalizer := newRawRefSiblingNormalizer(nil)
	out, err := normalizer.normalizeResource(ordinary, nil)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if string(out) != string(ordinary) {
		t.Fatalf("a conformant resource was rewritten:\n%s", out)
	}

	divergent := []byte("openapi: 3.1.0\n" +
		"info:\n  title: divergent\n  version: 1.0.0\n  x-case: 017\n" +
		"paths:\n  /p:\n    get:\n      operationId: getP\n      responses:\n        \"200\":\n          description: ok\n")
	rewritten, err := normalizer.normalizeResource(divergent, nil)
	if err != nil {
		t.Fatalf("normalize divergent: %v", err)
	}
	if string(rewritten) == string(divergent) {
		t.Fatal("a divergent resource was handed on unchanged")
	}
	if !strings.Contains(string(rewritten), `"x-case":17`) {
		t.Fatalf("rewritten resource does not carry the conformant value: %s", rewritten)
	}
}

// TestYAMLScalarResolutionUnitTable exercises the resolution layer directly,
// which is the only way to see the values a whole-document harness flattens
// into JSON (a float64 versus an int, say) and the only way to name the
// fall-back seam.
func TestYAMLScalarResolutionUnitTable(t *testing.T) {
	for _, tc := range []struct {
		yaml string
		want string
	}{
		{"v: 017\n", `map[string]interface {}{"v":17}`},
		{"v: 0o17\n", `map[string]interface {}{"v":15}`},
		{"v: 1_000\n", `map[string]interface {}{"v":"1_000"}`},
		{"v: 0b101\n", `map[string]interface {}{"v":"0b101"}`},
		{"v: 0x1F\n", `map[string]interface {}{"v":31}`},
		{"v: <<\n", `map[string]interface {}{"v":"<<"}`},
		{"v: 12:30:45\n", `map[string]interface {}{"v":"12:30:45"}`},
		{"v: 600e27371700\n", `map[string]interface {}{"v":"600e27371700"}`},
		{"v: 1e-400\n", `map[string]interface {}{"v":0}`},
	} {
		got, err := resolveScalarsConformantly([]byte(tc.yaml))
		if err != nil {
			t.Errorf("%q: %v", tc.yaml, err)
			continue
		}
		if rendered := fmt.Sprintf("%#v", got); rendered != tc.want {
			t.Errorf("%q: %s, want %s", tc.yaml, rendered, tc.want)
		}
	}

	if _, err := resolveScalarsConformantly([]byte("a: [1,\n")); err == nil {
		t.Error("a malformed stream should hand the resource back to the incumbent decoder")
	}
}

// TestYAMLScalarResolutionCorpusIncidence reports how many artifacts under a
// local corpus tree this layer actually corrects. It is the reproducer for the
// incidence figures in corpus-lab/OPENAPI-RUNTIME.md "F-O1-7", and it is
// skipped unless OB_YAML_SCALAR_CORPUS names that tree — no corpus bytes are
// committed anywhere (corpus-lab/EVIDENCE-POLICY.md).
//
//	OB_YAML_SCALAR_CORPUS=../../corpus-lab/data/openapi go test -run CorpusIncidence -v ./...
func TestYAMLScalarResolutionCorpusIncidence(t *testing.T) {
	tree := os.Getenv("OB_YAML_SCALAR_CORPUS")
	if tree == "" {
		t.Skip("OB_YAML_SCALAR_CORPUS is unset")
	}
	var read, corrected, refused, declined int
	err := filepath.WalkDir(tree, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".yaml", ".yml", ".json":
		default:
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		var incumbent any
		if _, decodeErr := yaml.Unmarshal(data, &incumbent, yaml.DecodeOpts{DisableTimestamps: true}); decodeErr != nil {
			return nil
		}
		read++
		conformant, scalarErr := resolveScalarsConformantly(data)
		switch {
		case errors.Is(scalarErr, errScalarResolutionUnavailable):
			declined++
		case scalarErr != nil:
			refused++
			t.Logf("REFUSED %s: %v", path, scalarErr)
		case !reflect.DeepEqual(incumbent, conformant):
			corrected++
			t.Logf("CORRECTED %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk corpus: %v", err)
	}
	t.Logf("artifacts read %d, corrected %d, refused %d, declined %d", read, corrected, refused, declined)
}
