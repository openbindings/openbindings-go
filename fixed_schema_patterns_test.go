package openbindings

import (
	"encoding/json"
	"io/fs"
	"maps"
	"slices"
	"testing"

	"github.com/openbindings/openbindings-go/internal/jsonpointer"
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

// namedMaps holds every map whose member names the document schema
// constrains, so a refused name is located where the document holds it. A
// schema update adding a propertyNames fails here, for review.
func TestDocumentSchema_NamedMapsAreEveryConstrainedMap(t *testing.T) {
	var document any
	if err := json.Unmarshal(openbindingsSchemaJSON, &document); err != nil {
		t.Fatal(err)
	}
	constrained := 0
	var walk func(value any)
	walk = func(value any) {
		switch value := value.(type) {
		case map[string]any:
			if _, ok := value["propertyNames"]; ok {
				constrained++
			}
			for _, child := range value {
				walk(child)
			}
		case []any:
			for _, child := range value {
				walk(child)
			}
		}
	}
	walk(document)
	if constrained != len(namedMaps) {
		t.Fatalf("the document schema constrains the names of %d maps; namedMaps lists %d", constrained, len(namedMaps))
	}
	base := `{"openbindings":"0.2.0","operations":{"op":{}},"sources":{"s":{"kind":"k"}}`
	for path, document := range map[string]string{
		"/schemas/<":                base + `,"schemas":{"<":{}}}`,
		"/operations/<":             `{"openbindings":"0.2.0","operations":{"<":{}}}`,
		"/dependencies/<":           base + `,"dependencies":{"<":{"operation":"op"}}}`,
		"/sources/<":                `{"openbindings":"0.2.0","operations":{},"sources":{"<":{"kind":"k"}}}`,
		"/bindings/<":               base + `,"bindings":{"<":{"operation":"op","source":"s"}}}`,
		"/operations/op/examples/<": `{"openbindings":"0.2.0","operations":{"op":{"examples":{"<":{}}}}}`,
	} {
		_, report, _ := ValidateDocument([]byte(document))
		var at []string
		for _, finding := range report.Findings {
			if finding.Rule == "OBI-D-02" {
				at = append(at, finding.Path)
			}
		}
		if !slices.Equal(at, []string{path}) {
			t.Errorf("OBI-D-02 findings at %q, want one at %s", at, path)
		}
	}
}

// numberComparisons lists where a schema, or a subschema it describes, tells
// numbers apart other than by type, by equality, and by comparison with
// zero: a nonzero bound, a multipleOf, or a number in const or enum, each as
// a JSON Pointer to the keyword. A stand-in for a number beyond the numeric
// limits of schema evaluation keeps only those three
// (schemacompiler.Substitute).
func numberComparisons(schema any) []string {
	var holdsNumber func(value any) bool
	holdsNumber = func(value any) bool {
		switch value := value.(type) {
		case float64:
			return true
		case []any:
			return slices.ContainsFunc(value, holdsNumber)
		case map[string]any:
			return slices.ContainsFunc(slices.Collect(maps.Values(value)), holdsNumber)
		}
		return false
	}
	var found []string
	var walk func(schema any, tokens []string)
	walk = func(schema any, tokens []string) {
		object, isObject := schema.(map[string]any)
		if !isObject {
			return
		}
		for _, keyword := range sortedKeys(object) {
			value := object[keyword]
			switch keyword {
			case "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum":
				if value != float64(0) {
					found = append(found, jsonpointer.Format(append(slices.Clip(tokens), keyword)...))
				}
			case "multipleOf":
				found = append(found, jsonpointer.Format(append(slices.Clip(tokens), keyword)...))
			case "const", "enum":
				if holdsNumber(value) {
					found = append(found, jsonpointer.Format(append(slices.Clip(tokens), keyword)...))
				}
			}
		}
		forEachDescribedSubschema(object, func(child any, below ...string) {
			walk(child, append(slices.Clip(tokens), below...))
		})
	}
	walk(schema, nil)
	slices.Sort(found)
	return found
}

// The 2020-12 meta-schemas tell numbers apart only by type, by equality, and
// by comparison with zero, which a stand-in for a number beyond the numeric
// limits keeps, so OBI-D-10 checks a schema holding one as a stand-in
// (checkAgainstMetaSchema). A meta-schema update comparing numbers otherwise
// fails here, for review.
func TestMetaSchema_ComparesNumbersOnlyWithZero(t *testing.T) {
	probe := map[string]any{"properties": map[string]any{"minimum": map[string]any{"minimum": float64(1)}, "b": map[string]any{"exclusiveMinimum": float64(0), "enum": []any{"s", float64(2)}}}}
	if got, want := numberComparisons(probe), []string{"/properties/b/enum", "/properties/minimum/minimum"}; !slices.Equal(got, want) {
		t.Fatalf("the walk found %v, want %v", got, want)
	}
	walked := 0
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
		walked++
		if found := numberComparisons(metaSchema); len(found) > 0 {
			t.Errorf("%s compares numbers at %v", path, found)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if walked != 8 {
		t.Fatalf("walked %d meta-schemas, want the dialect's and its seven vocabularies'", walked)
	}
}

// The document schema tells numbers apart only by type and equality, which a
// stand-in keeps (validateAgainstOBISchema), except at a binding's
// preference, which validation decides exactly. A schema update comparing
// numbers anywhere else fails here, and the preference range is §5.3's.
func TestDocumentSchema_ComparesNumbersOnlyAtAPreference(t *testing.T) {
	var document any
	if err := json.Unmarshal(openbindingsSchemaJSON, &document); err != nil {
		t.Fatal(err)
	}
	preference := "/$defs/BindingEntry/properties/preference"
	if got, want := numberComparisons(document), []string{preference + "/maximum", preference + "/minimum"}; !slices.Equal(got, want) {
		t.Fatalf("the document schema compares numbers at %v, want %v", got, want)
	}
	bounds, _ := jsonpointer.Resolve(document, preference)
	if object := bounds.(map[string]any); object["minimum"] != float64(-maxPreference) || object["maximum"] != float64(maxPreference) {
		t.Fatalf("the document schema bounds a preference by %v and %v", object["minimum"], object["maximum"])
	}
}
