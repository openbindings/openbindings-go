package openbindings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openbindings/openbindings-go/internal/schemacompiler"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Cases of the JSON Schema Test Suite (testdata/json-schema-test-suite), each
// schema embedded as an operation's input and each value validated against
// it. A suite schema's fragments mean its own locations, so one without an
// $id is embedded with the URI the library is given it under alone: in an OBI
// document, a fragment outside every resource would mean a location in the
// document instead (§7).
//
// The bundle the SDK gives the schema library must not change an answer: each
// verdict is the suite's, or, where the library itself answers otherwise when
// given the schema alone, the library's. A case gets no verdict only when its
// schema references one of the suite's remote schemas, which no document
// here embeds.
func TestJSONSchemaTestSuite(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "json-schema-test-suite", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no suite files: %v", err)
	}
	var cases, agreed, libraryDiffers, refused int
	for _, file := range files {
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
			iface := mustDecodeInterface(t, `{"openbindings":"0.2.0","operations":{"op":{"input":`+string(asResource(t, group.Schema))+`}}}`)
			standalone := compileStandalone(t, group.Schema)
			for _, test := range group.Tests {
				cases++
				name := filepath.Base(file) + ": " + group.Description + ": " + test.Description
				value := decodeValue(t, test.Data)
				ours := outcome(ValidateOperationInput(value, iface, "op"))
				want := map[bool]string{true: "valid", false: "mismatch"}[test.Valid]
				library := "unavailable"
				if standalone != nil {
					library = outcome(schemaValidationError(standalone.Validate(decodeLibraryValue(t, test.Data))))
				}
				switch {
				case ours == "unavailable":
					refused++
					if !strings.Contains(string(group.Schema), "localhost:1234") && !strings.Contains(string(group.Schema), `"$id":"http://localhost:1234`) {
						t.Errorf("%s: no verdict, though the schema references no remote schema", name)
					}
				case ours == want:
					agreed++
				case ours == library:
					libraryDiffers++
					t.Logf("the library answers %s given the schema alone, as here: %s", library, name)
				default:
					t.Errorf("%s: got %s; the suite says %s, the library alone %s", name, ours, want, library)
				}
			}
		}
	}
	t.Logf("%d cases: %d as the suite says, %d as the library alone answers, %d without a verdict", cases, agreed, libraryDiffers, refused)
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
	if _, declares := declaredID(object); declares {
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
