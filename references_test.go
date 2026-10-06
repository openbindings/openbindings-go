package openbindings

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// referencesDocument holds an anchor, percent-encoded pointers, a cycle, a
// $dynamicRef, an $id resource referenced by its $id and holding references
// of its own, an operation-schema target, an external schema, a meta-schema,
// a legacy definitions target, and $ref-shaped data that is not a reference.
const referencesDocument = `{
  "openbindings": "0.2.0",
  "schemas": {
    "Task": { "$anchor": "task", "type": "object", "properties": { "id": { "type": "string" }, "next": { "$ref": "#/schemas/Task" } } },
    "List": { "type": "array", "items": { "$ref": "#/schemas/T%61sk" } },
    "Tree": { "$dynamicAnchor": "node", "type": "object", "properties": { "kids": { "type": "array", "items": { "$dynamicRef": "#node" } } } },
    "Wrapped": { "$id": "https://example.com/wrapped", "$defs": { "x": { "$ref": "#/schemas/Task" } }, "properties": { "t": { "$ref": "https://example.com/wrapped#/$defs/x" }, "self": { "$ref": "#" } } },
    "Legacy": { "definitions": { "d": { "type": "string" } }, "$ref": "#/schemas/Legacy/definitions/d" },
    "Meta": { "$ref": "https://json-schema.org/draft/2020-12/schema" },
    "NotAReference": { "const": { "$ref": "#/schemas/Task" }, "$ref": 5 }
  },
  "operations": {
    "a": { "input": { "$ref": "#task" }, "output": { "$ref": "#/schemas/List" }, "examples": { "e": { "input": { "$ref": "#/schemas/Task" } } } },
    "b": { "input": { "$ref": "#/operations/a/output" }, "output": { "$ref": "https://example.com/wrapped" } },
    "c": { "input": { "$ref": "https://schemas.example.com/address.json" } },
    "d": { "output": { "$ref": "#%2Fschemas%2FTask" } },
    "e": { "input": { "$ref": "#/schemas/Missing" } }
  },
  "sources": { "s": { "kind": "example.openapi@1", "content": { "$ref": "#/schemas/Task" } } },
  "x-note": { "$ref": "#/schemas/Task" }
}`

func TestReferences(t *testing.T) {
	refs, err := mustDecodeDocument(t, referencesDocument).References()
	if err != nil {
		t.Fatal(err)
	}
	type row struct{ location, keyword, value, base, target string }
	want := []row{
		{"/operations/a/input/$ref", "$ref", "#task", "", "/schemas/Task"},
		{"/operations/a/output/$ref", "$ref", "#/schemas/List", "", "/schemas/List"},
		{"/operations/b/input/$ref", "$ref", "#/operations/a/output", "", "/operations/a/output"},
		{"/operations/b/output/$ref", "$ref", "https://example.com/wrapped", "", "/schemas/Wrapped"},
		{"/operations/c/input/$ref", "$ref", "https://schemas.example.com/address.json", "", ""},
		{"/operations/d/output/$ref", "$ref", "#%2Fschemas%2FTask", "", "/schemas/Task"},
		{"/operations/e/input/$ref", "$ref", "#/schemas/Missing", "", ""},
		{"/schemas/Legacy/$ref", "$ref", "#/schemas/Legacy/definitions/d", "", "/schemas/Legacy/definitions/d"},
		{"/schemas/List/items/$ref", "$ref", "#/schemas/T%61sk", "", "/schemas/Task"},
		{"/schemas/Meta/$ref", "$ref", "https://json-schema.org/draft/2020-12/schema", "", ""},
		{"/schemas/Task/properties/next/$ref", "$ref", "#/schemas/Task", "", "/schemas/Task"},
		{"/schemas/Tree/properties/kids/items/$dynamicRef", "$dynamicRef", "#node", "", "/schemas/Tree"},
		// Inside the resource, "#/schemas/Task" is a pointer into the
		// resource, which holds nothing there.
		{"/schemas/Wrapped/$defs/x/$ref", "$ref", "#/schemas/Task", "https://example.com/wrapped", ""},
		{"/schemas/Wrapped/properties/self/$ref", "$ref", "#", "https://example.com/wrapped", "/schemas/Wrapped"},
		{"/schemas/Wrapped/properties/t/$ref", "$ref", "https://example.com/wrapped#/$defs/x", "https://example.com/wrapped", "/schemas/Wrapped/$defs/x"},
	}
	var got []row
	for _, r := range refs {
		got = append(got, row{r.Location, r.Keyword, r.Value, r.Base, r.Target})
		if (r.Target == "") == (r.Unresolved == "") {
			t.Errorf("%s: Target %q and Unresolved %q", r.Location, r.Target, r.Unresolved)
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got\n%v\nwant\n%v", got, want)
	}
	for location, reason := range map[string]string{
		"/operations/c/input/$ref":      "a resource the document does not embed (§7.4)",
		"/operations/e/input/$ref":      "does not resolve within the document",
		"/schemas/Meta/$ref":            "meta-schema https://json-schema.org/draft/2020-12/schema, outside the document",
		"/schemas/Wrapped/$defs/x/$ref": "reaches no schema in https://example.com/wrapped",
	} {
		i := slices.IndexFunc(refs, func(r Reference) bool { return r.Location == location })
		if !strings.HasSuffix(refs[i].Unresolved, reason) && !strings.Contains(refs[i].Unresolved, reason) {
			t.Errorf("%s: Unresolved %q, want it to say %q", location, refs[i].Unresolved, reason)
		}
	}
}

// A same-document reference in the document resource identifies a schema
// exactly when OBI-D-12, which uses the same lookup, holds for it: it lacks
// a target exactly where OBI-D-05 or OBI-D-12 reports a violation, or where
// it names a plain name declared more than once, which OBI-D-13 reports. A
// string OBI-D-05 reports as no well-formed URI-reference is not listed at
// all (§7.1). The documents are the corpus's, when it is found, and a few
// of the test's own.
func TestReferences_AgreeWithOBID12(t *testing.T) {
	documents := map[string][]byte{
		"references": []byte(referencesDocument),
		"pointers": []byte(`{"openbindings":"0.2.0","schemas":{"T":{"properties":{"my type":{"type":"string"}}},"R":{"$id":"https://ex.test/r","properties":{"x":{}}}},"operations":{"op":{"input":{"allOf":[
		  {"$ref":"#"},{"$ref":""},{"$ref":"#/schemas/T/properties/my%20type"},{"$ref":"#/schemas/T/properties/my type"},{"$ref":"#/schemas/T/properties/my%2520type"},
		  {"$ref":"#/schemas/T/properties"},{"$ref":"#/schemas/R"},{"$ref":"#/schemas/R/properties/x"},{"$ref":"#%FF"},{"$ref":"#/operations"},{"$ref":"#/schemas/~2"}]}}}}`),
		"plain names": []byte(`{"openbindings":"0.2.0","schemas":{"P":{"$anchor":"n"},"Q":{"$anchor":"n"},"A":{"$anchor":"a","$dynamicAnchor":"a"},"I":{"$id":"https://ex.test/i","$anchor":"inner"}},
		  "operations":{"op":{"input":{"anyOf":[{"$ref":"#n"},{"$ref":"#a"},{"$ref":"#inner"},{"$ref":"#t%61sk"},{"$dynamicRef":"#n"}]}}}}`),
	}
	if dir := findConformanceCorpus(); dir != "" {
		for _, rule := range []string{"OBI-D-05", "OBI-D-12", "OBI-D-13"} {
			data, err := os.ReadFile(filepath.Join(dir, "document", rule+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var fixture conformanceFixture
			if err := json.Unmarshal(data, &fixture); err != nil {
				t.Fatal(err)
			}
			for _, test := range fixture.Tests {
				if test.Document != nil {
					documents[rule+"/"+test.Description] = test.Document
				}
			}
		}
	}
	checked := 0
	for name, data := range documents {
		var doc Document
		if err := json.Unmarshal(data, &doc); err != nil {
			continue // the model does not carry it
		}
		refs, err := doc.References()
		if err != nil {
			continue // refused, or no valid version
		}
		report, _ := doc.Validate()
		violated := map[string]bool{}
		for _, finding := range report.Violations() {
			if finding.Rule == "OBI-D-05" || finding.Rule == "OBI-D-12" {
				violated[finding.Path] = true
			}
		}
		listed := map[string]bool{}
		for _, r := range refs {
			listed[r.Location] = true
		}
		for _, finding := range report.Violations() {
			if finding.Rule == "OBI-D-05" && strings.Contains(finding.Message, "not a well-formed URI-reference") && !strings.HasSuffix(finding.Path, "/$id") && listed[finding.Path] {
				t.Errorf("%s: %s is no reference of any form, but it is listed", name, finding.Path)
			}
		}
		for _, r := range refs {
			if r.Base != "" || r.Value != "" && !strings.HasPrefix(r.Value, "#") || inResource(&doc, r.Location) {
				continue
			}
			checked++
			declaredTwice := strings.Contains(r.Unresolved, "declares more than once")
			if (r.Target == "") != (violated[r.Location] || declaredTwice) {
				t.Errorf("%s: %s %q: Target %q (%s), but OBI-D-05 or OBI-D-12 violated there: %v", name, r.Location, r.Value, r.Target, r.Unresolved, violated[r.Location])
			}
		}
	}
	if checked < 25 {
		t.Fatalf("only %d references checked", checked)
	}
	t.Logf("%d references in %d documents agree with OBI-D-12", checked, len(documents))
}

// A $ref or $dynamicRef string that is not a well-formed URI-reference is not
// a reference of any form (§7.1), so References omits it: in the document
// resource, where OBI-D-05 reports it and OBI-D-12 does not govern it, and
// in an $id resource alike. A value whose evaluation depends on one gets no
// verdict, the result being undefined.
func TestReferences_OmitMalformedStrings(t *testing.T) {
	document := `{"openbindings":"0.2.0","schemas":{"T":{"type":"string"},
	  "R":{"$id":"https://ex.test/r","properties":{"bad":{"$ref":"#%zz"},"good":{"$ref":"#/properties/bad"}}}},
	  "operations":{"op":{"input":{"anyOf":[{"$ref":"#%zz"},{"$dynamicRef":"#/schemas/T x"},{"$ref":"http://[bad"},{"$ref":"#/schemas/T"}]}},
	    "inR":{"input":{"$ref":"https://ex.test/r#/properties/bad"}}}}`
	refs, err := mustDecodeDocument(t, document).References()
	if err != nil {
		t.Fatal(err)
	}
	type row struct{ location, target string }
	var got []row
	for _, r := range refs {
		got = append(got, row{r.Location, r.Target})
	}
	want := []row{
		{"/operations/inR/input/$ref", "/schemas/R/properties/bad"},
		{"/operations/op/input/anyOf/3/$ref", "/schemas/T"},
		{"/schemas/R/properties/good/$ref", "/schemas/R/properties/bad"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("listed %v, want %v", got, want)
	}
	report := mustValidateDocument(t, document)
	var d05 []string
	for _, finding := range report.Violations() {
		switch finding.Rule {
		case "OBI-D-05":
			d05 = append(d05, finding.Path)
		case "OBI-D-12":
			t.Errorf("OBI-D-12 judges %s: %s", finding.Path, finding.Message)
		}
	}
	if want := []string{"/operations/op/input/anyOf/0/$ref", "/operations/op/input/anyOf/1/$dynamicRef", "/operations/op/input/anyOf/2/$ref"}; !slices.Equal(d05, want) {
		t.Errorf("OBI-D-05 violated at %v, want %v", d05, want)
	}
	for _, operation := range []string{"op", "inR"} {
		if refusal := refusalOf(t, document, operation); !errors.Is(refusal, ErrUndefined) {
			t.Errorf("%s: %v", operation, refusal)
		}
	}
}

// inResource reports whether a location lies within a schema that declares
// $id, below the document resource: what OBI-D-12 does not govern.
func inResource(doc *Document, location string) bool {
	view, err := documentView(*doc)
	if err != nil {
		return false
	}
	space := newSchemaSpace(view, nil)
	parent := location[:strings.LastIndexByte(location, '/')]
	resource, ok := space.obi.schemas[parent]
	return ok && !resource.document
}

func TestReferences_WholeCall(t *testing.T) {
	var none *Document
	if refs, err := none.References(); refs != nil || err != nil {
		t.Errorf("a nil Document: %v, %v", refs, err)
	}
	refs, err := (&Document{OpenBindings: "0.3.0", Operations: map[string]Operation{"op": {Input: map[string]any{"$ref": "#/x"}}}}).References()
	if !errors.As(err, new(*VersionRefusalError)) || errors.Is(err, ErrInconclusive) || refs != nil {
		t.Errorf("an unsupported version: %v, %v", refs, err)
	}
	for _, version := range []string{"0.2", ""} {
		doc := &Document{OpenBindings: version, Operations: map[string]Operation{"op": {Input: map[string]any{"$ref": "#/x"}}}}
		refs, err := doc.References()
		if !isVersionViolation(err, doc) || refs != nil {
			t.Errorf("no valid version %q: %v, %v", version, refs, err)
		}
	}
	refs, err = (&Document{OpenBindings: "0.2.0", Operations: map[string]Operation{"op": {Input: map[string]any{"maximum": math.NaN()}}}}).References()
	if err == nil || errors.Is(err, ErrInconclusive) || refs != nil {
		t.Errorf("an unencodable value: %v, %v", refs, err)
	}
	refs, err = (&Document{OpenBindings: "0.2.0", Operations: map[string]Operation{"op": {Input: true}}}).References()
	if refs != nil || err != nil {
		t.Errorf("no references: %v, %v", refs, err)
	}
	// Past the index's limit, the references found come back with an error:
	// the one at 300 levels is not among them.
	deep := strings.Repeat(`{"not":`, 300) + `{"$ref":"#/schemas/S"}` + strings.Repeat(`}`, 300)
	shallow := strings.Repeat(`{"not":`, 256) + `{"$ref":"#/schemas/S"}` + strings.Repeat(`}`, 256)
	doc := mustDecodeDocument(t, `{"openbindings":"0.2.0","schemas":{"S":{"type":"string"}},"operations":{
	  "deep":{"input":`+deep+`,"output":{"$ref":"#/schemas/S"}},"shallow":{"input":`+shallow+`}}}`)
	refs, err = doc.References()
	if !errors.Is(err, ErrInconclusive) || !strings.Contains(err.Error(), "/operations/deep/input") || strings.Contains(err.Error(), "shallow") {
		t.Fatalf("an incomplete index: %v", err)
	}
	var found []string
	for _, r := range refs {
		found = append(found, r.Location[:min(len(r.Location), 30)])
	}
	if want := []string{"/operations/deep/output/$ref", "/operations/shallow/input/not/"}; !slices.Equal(found, want) {
		t.Fatalf("found %v, want %v", found, want)
	}
}

// References reads the Document, so concurrent calls are safe.
func TestReferences_Concurrent(t *testing.T) {
	doc := mustDecodeDocument(t, referencesDocument)
	want, err := doc.References()
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if got, err := doc.References(); err != nil || !slices.Equal(got, want) {
				t.Errorf("a concurrent call: %v", err)
			}
		})
	}
	wg.Wait()
}
