package openbindings

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openbindings/openbindings-go/internal/schemacompiler"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Cases of the JSON Schema Test Suite's draft2020-12 tests
// (testdata/json-schema-test-suite), each schema embedded as an operation's
// input and each value validated against it. A suite schema's fragments mean
// its own locations, so one without an $id is embedded with the URI the
// library is given it under alone: in an OBI document, a fragment outside
// every resource would mean a location in the document instead (§7).
//
// Each verdict is the suite's: neither the bundle the SDK gives the schema
// library nor the library itself may change an answer. A failure names what
// the library answers given the schema alone, to tell the two apart. A case
// gets no verdict only for a reason suiteNoVerdict names, and every file
// runs but those suiteExcluded names.
func TestJSONSchemaTestSuite(t *testing.T) {
	root := filepath.Join("testdata", "json-schema-test-suite", "draft2020-12")
	var files []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(root, path)
		relative = filepath.ToSlash(relative)
		if _, excluded := suiteExcluded[relative]; excluded {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.IsDir() && strings.HasSuffix(path, ".json") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil || len(files) == 0 {
		t.Fatalf("no suite files: %v", err)
	}
	var cases, agreed int
	refused := map[string]int{}
	for _, file := range files {
		relative, _ := filepath.Rel(root, file)
		relative = filepath.ToSlash(relative)
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var groups []struct {
			Description string
			Schema      json.RawMessage
			Tests       []struct {
				Description string
				Data        json.RawMessage
				Valid       bool
			}
		}
		if err := json.Unmarshal(data, &groups); err != nil {
			t.Fatal(err)
		}
		for _, group := range groups {
			// Each schema runs as a resource of its own. One holding no
			// same-document reference runs as the document resource's too,
			// where no $id is added and the bundle rewrites nothing.
			embeddings := map[string]json.RawMessage{"as a resource": asResource(t, group.Schema)}
			if !strings.Contains(string(group.Schema), `"#`) && !strings.Contains(string(group.Schema), `"$id"`) {
				embeddings["in the document resource"] = group.Schema
			}
			standalone := compileStandalone(t, group.Schema)
			for embedding, schema := range embeddings {
				iface := mustDecodeInterface(t, `{"openbindings":"0.2.0","operations":{"op":{"input":`+string(schema)+`}}}`)
				for _, test := range group.Tests {
					cases++
					name := relative + ": " + group.Description + ": " + test.Description + " (" + embedding + ")"
					value := decodeValue(t, test.Data)
					err := ValidateOperationInput(value, iface, "op")
					ours := outcome(err)
					want := map[bool]string{true: "valid", false: "mismatch"}[test.Valid]
					library := "unavailable"
					if standalone != nil {
						library = outcome(schemaValidationError(standalone.Validate(decodeLibraryValue(t, test.Data)), schemacompiler.Substitution{}))
					}
					switch {
					case ours == "unavailable":
						reason := suiteNoVerdict(relative, err.Error())
						if reason == "" {
							t.Errorf("%s: no verdict, for no reason the suite test allows: %v", name, err)
						}
						refused[reason]++
					case ours == want:
						agreed++
					default:
						t.Errorf("%s: got %s; the suite says %s, the library alone %s", name, ours, want, library)
					}
				}
			}
		}
	}
	t.Logf("%d cases in %d files: %d as the suite says, and without a verdict %v", cases, len(files), agreed, refused)
}

// suiteExcluded names the suite files and folders that test what OBI-T-08
// rules out, and why.
var suiteExcluded = map[string]string{
	"optional/format":                          "these assert format, which OBI-T-08 makes an annotation; format.json tests that it is one",
	"optional/dependencies-compatibility.json": "these evaluate dependencies, which strict 2020-12 does not define, so it constrains nothing",
}

// suiteNoVerdict returns the reason a suite case may reach no verdict, read
// from the cause the SDK gives, or "" when none allows it: the graph reaches
// one of the suite's remote schemas, or names one as its dialect, which no
// document here embeds; a pattern holds a Unicode property escape, which this
// SDK does not evaluate; or, in the file that tests it, a reference names a
// value under a keyword that holds no schema, which evaluation does not reach
// (the maintainer's open question on identity keywords in unknown members).
func suiteNoVerdict(file, cause string) string {
	switch {
	case strings.Contains(cause, "http://localhost:1234/") && (strings.Contains(cause, "which the document does not embed") || strings.Contains(cause, "declares $schema")):
		return "remote schema"
	case strings.Contains(cause, "Unicode property escape"):
		return "Unicode property escape"
	case file == "optional/refOfUnknownKeyword.json" && strings.Contains(cause, "is not a schema position"):
		return "reference to no schema position"
	}
	return ""
}

// compileStandalone compiles a suite schema as the only resource of an SDK
// compiler, or returns nil when it does not compile so.
func compileStandalone(t *testing.T, schema json.RawMessage) *jsonschema.Schema {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(string(schema)))
	if err != nil {
		t.Fatal(err)
	}
	c := schemacompiler.New()
	if err := c.AddResource(suiteURL, doc); err != nil {
		return nil
	}
	compiled, err := c.Compile(suiteURL)
	if err != nil {
		return nil
	}
	return compiled
}

// suiteURL is the URI a suite schema is given under.
const suiteURL = "https://suite.openbindings.invalid/schema.json"

// asResource returns a suite schema with an $id: its own, or suiteURL.
func asResource(t *testing.T, schema json.RawMessage) json.RawMessage {
	t.Helper()
	var object map[string]any
	if json.Unmarshal(schema, &object) != nil || object == nil {
		return schema
	}
	if id, _ := object["$id"].(string); id != "" && !strings.HasPrefix(id, "#") {
		return schema
	}
	object["$id"] = suiteURL
	out, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func decodeValue(t *testing.T, data json.RawMessage) any {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func decodeLibraryValue(t *testing.T, data json.RawMessage) any {
	t.Helper()
	value, err := jsonschema.UnmarshalJSON(strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	return value
}
