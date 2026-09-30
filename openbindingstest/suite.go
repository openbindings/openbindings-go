package openbindingstest

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"

	"github.com/openbindings/openbindings-go"
)

// suiteFiles is the JSON Schema Test Suite's draft2020-12 tests and its
// remote fixtures, at the commit testdata/json-schema-test-suite/README.md
// names.
//
//go:embed testdata/json-schema-test-suite/draft2020-12 testdata/json-schema-test-suite/remotes
var suiteFiles embed.FS

const suiteRoot = "testdata/json-schema-test-suite/"

// suiteExcluded names the suite files and folders that test what OBI-T-08
// rules out, and why.
var suiteExcluded = map[string]string{
	"draft2020-12/optional/format":                          "these assert format, which OBI-T-08 makes an annotation; format.json tests that it is one",
	"draft2020-12/optional/dependencies-compatibility.json": "these evaluate dependencies, which strict 2020-12 does not define, so it constrains nothing",
}

// suitePinned names the suite groups core refuses, by group ID, and why. A
// group listed here that core compiles fails, as does a group core refuses
// that is not listed, and an entry naming no group.
var suitePinned = map[string]string{
	"suite/draft2020-12/optional/cross-draft.json#0 refs to historic drafts are processed as historic drafts":                   "it reaches a remote schema in draft 2019-09, a dialect core does not read as 2020-12",
	"suite/draft2020-12/optional/format-assertion.json#0 schema that uses custom metaschema with format-assertion: false":       "its $schema names a custom meta-schema, a dialect core does not read as 2020-12",
	"suite/draft2020-12/optional/format-assertion.json#1 schema that uses custom metaschema with format-assertion: true":        "its $schema names a custom meta-schema, a dialect core does not read as 2020-12",
	"suite/draft2020-12/vocabulary.json#0 schema that uses custom metaschema with with no validation vocabulary":                "its $schema names a custom meta-schema, a dialect core does not read as 2020-12",
	"suite/draft2020-12/vocabulary.json#1 ignore unrecognized optional vocabulary":                                              "its $schema names a custom meta-schema, a dialect core does not read as 2020-12",
	"suite/draft2020-12/optional/refOfUnknownKeyword.json#0 reference of a root arbitrary keyword ":                             unknownKeyword,
	"suite/draft2020-12/optional/refOfUnknownKeyword.json#1 reference of a root arbitrary keyword with encoded ref":             unknownKeyword,
	"suite/draft2020-12/optional/refOfUnknownKeyword.json#2 reference of an arbitrary keyword of a sub-schema":                  unknownKeyword,
	"suite/draft2020-12/optional/refOfUnknownKeyword.json#3 reference internals of known non-applicator":                        unknownKeyword,
	"suite/draft2020-12/optional/refOfUnknownKeyword.json#4 reference of an arbitrary keyword of a sub-schema with encoded ref": unknownKeyword,
}

const unknownKeyword = "it references a value under a keyword that holds no schema, a pointer that reaches no schema, which JSON Schema and so OBI leave undefined (§7.4)"

// suiteURL is the $id a suite schema without one is embedded under, so its
// fragments mean its own locations: in the document resource a fragment is a
// JSON Pointer from the OBI document's root (§7.2).
const suiteURL = "https://suite.openbindings.invalid/schema.json"

// suiteGroups returns a group per suite schema: an OBI document holding the
// schema as operation "op"'s input, the suite's remotes supplied as
// resources, and the schema's tests as cases. A group's ID is the file's
// path, "#", the group's index, a space, and its description; a case's is
// the file's path, "#", the group and test indices joined by "/", a space,
// and the group's and test's descriptions joined by "/". Indices and
// descriptions are both part of an ID, so a suite update that moves a group
// or case fails every entry naming it.
func suiteGroups() ([]group, error) {
	resources, err := suiteRemotes()
	if err != nil {
		return nil, err
	}
	var groups []group
	err = fs.WalkDir(suiteFiles, suiteRoot+"draft2020-12", func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative := strings.TrimPrefix(file, suiteRoot)
		if _, excluded := suiteExcluded[relative]; excluded {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() || path.Ext(file) != ".json" {
			return nil
		}
		data, err := suiteFiles.ReadFile(file)
		if err != nil {
			return err
		}
		var schemas []struct {
			Description string
			Schema      json.RawMessage
			Tests       []struct {
				Description string
				Data        json.RawMessage
				Valid       bool
			}
		}
		if err := json.Unmarshal(data, &schemas); err != nil {
			return fmt.Errorf("%s: %w", relative, err)
		}
		for i, s := range schemas {
			schema, err := asResource(s.Schema)
			if err != nil {
				return fmt.Errorf("%s: %w", relative, err)
			}
			g := group{
				id:        fmt.Sprintf("suite/%s#%d %s", relative, i, s.Description),
				tally:     "suite/" + relative,
				document:  `{"openbindings":"0.2.0","operations":{"op":{"input":` + string(schema) + `}}}`,
				resources: resources,
			}
			g.pinned = suitePinned[g.id]
			for j, test := range s.Tests {
				want := mismatch
				if test.Valid {
					want = valid
				}
				g.cases = append(g.cases, testCase{
					id:    fmt.Sprintf("suite/%s#%d/%d %s/%s", relative, i, j, s.Description, test.Description),
					value: string(test.Data),
					want:  want,
				})
			}
			groups = append(groups, g)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for id := range suitePinned {
		if !slices.ContainsFunc(groups, func(g group) bool { return g.id == id }) {
			return nil, fmt.Errorf("suitePinned names %q, which is no group", id)
		}
	}
	return groups, nil
}

// asResource returns a suite schema with an $id: its own, or suiteURL.
func asResource(schema json.RawMessage) (json.RawMessage, error) {
	var object map[string]json.RawMessage
	if json.Unmarshal(schema, &object) != nil || object == nil {
		return schema, nil
	}
	var id string
	if raw, present := object["$id"]; present && json.Unmarshal(raw, &id) == nil && id != "" && !strings.HasPrefix(id, "#") {
		return schema, nil
	}
	object["$id"] = json.RawMessage(`"` + suiteURL + `"`)
	return json.Marshal(object)
}

// suiteRemotes supplies the suite's remote fixtures under the URIs its
// schemas name them by.
func suiteRemotes() ([]openbindings.Resource, error) {
	var out []openbindings.Resource
	err := fs.WalkDir(suiteFiles, suiteRoot+"remotes", func(file string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := suiteFiles.ReadFile(file)
		if err != nil {
			return err
		}
		out = append(out, openbindings.Resource{URI: "http://localhost:1234/" + strings.TrimPrefix(file, suiteRoot+"remotes/"), Document: data})
		return nil
	})
	return out, err
}
