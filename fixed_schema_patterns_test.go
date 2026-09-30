package openbindings

import (
	"encoding/json"
	"io/fs"
	"slices"
	"testing"
)

// The document rules evaluate fixed schemas with the schema library's Go
// regexp engine (internal/schemacompiler). That reads a pattern as ECMA-262
// with the u flag, as OBI-D-02 and OBI-D-10 require, only for patterns like
// these: anchored, over ASCII classes and literals. A schema update adding
// any other pattern fails here, for review.
func TestFixedSchemaPatterns(t *testing.T) {
	var patterns []string
	var collect func(schema any)
	collect = func(schema any) {
		object, ok := schema.(map[string]any)
		if !ok {
			return
		}
		if pattern, ok := object["pattern"].(string); ok {
			patterns = append(patterns, pattern)
		}
		patterns = append(patterns, sortedKeys(asObject(object["patternProperties"]))...)
		forEachDescribedSubschema(object, func(child any, _ ...string) { collect(child) })
	}
	var document any
	if err := json.Unmarshal(openbindingsSchemaJSON, &document); err != nil {
		t.Fatal(err)
	}
	collect(document)
	err := fs.WalkDir(metaSchemaFiles, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := metaSchemaFiles.ReadFile(path)
		if err != nil {
			return err
		}
		var metaSchema any
		if err := json.Unmarshal(data, &metaSchema); err != nil {
			return err
		}
		collect(metaSchema)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(patterns)
	patterns = slices.Compact(patterns)
	want := []string{
		`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-((?:0|[1-9][0-9]*|[0-9]*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9][0-9]*|[0-9]*[a-zA-Z-][0-9a-zA-Z-]*))*))?(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$`,
		`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`,
		`^[A-Za-z_][-A-Za-z0-9._]*$`,
		`^[^#]*#?$`,
		`^x-`,
	}
	if !slices.Equal(patterns, want) {
		t.Fatalf("the fixed schemas' patterns are\n%q\nreview each for Go regexp reading it as ECMA-262 does, then update this list", patterns)
	}
}
