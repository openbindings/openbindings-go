package openbindings

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// bundleOf returns the bundle core writes for a value contract, decoded.
func bundleOf(t *testing.T, document, entry string, resources ...Resource) map[string]any {
	t.Helper()
	contracts := contractsFor(t, mustDecodeInterface(t, document), resources...)
	raw, refusal := contracts.space.bundle(entry, bundleSpelling{})
	if refusal != nil {
		t.Fatalf("no bundle: %v", refusal)
	}
	var bundle map[string]any
	if err := json.Unmarshal(raw, &bundle); err != nil {
		t.Fatal(err)
	}
	return bundle
}

// refusalOf returns the standing refusal of a value contract.
func refusalOf(t *testing.T, document, operation string, resources ...Resource) *NoVerdictError {
	t.Helper()
	contract, err := contractsFor(t, mustDecodeInterface(t, document), resources...).CompileInput(context.Background(), operation)
	if err != nil {
		t.Fatal(err)
	}
	var refusal *NoVerdictError
	if !errors.As(contract.Err(), &refusal) {
		t.Fatalf("want a standing refusal, got %v", contract.Err())
	}
	return refusal
}

func verdicts(t *testing.T, document, operation string, values ...any) []string {
	t.Helper()
	contract, err := contractsFor(t, mustDecodeInterface(t, document)).CompileInput(context.Background(), operation)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, value := range values {
		out = append(out, verdictOf(t, contract.Validate(context.Background(), value)))
	}
	return out
}

func equalStrings(a, b []string) bool {
	return strings.Join(a, "\x00") == strings.Join(b, "\x00")
}

// The bundle is a closed 2020-12 compound document: its root declares an $id
// under .invalid and $refs the entry, its $defs hold the copied OBI
// positions, each object position of the document resource with an $id core
// generates, and every reference names its target by the $id of the
// resource holding it.
func TestBundle_Shape(t *testing.T) {
	document := `{"openbindings":"0.2.0","operations":{"op":{"input":{"$ref":"#/schemas/Task"}}},
		"schemas":{"Task":{"type":"object","properties":{"tags":{"type":"array","items":{"$ref":"#/schemas/Tag"}},"when":{"$ref":"#/schemas/Task/$defs/when"}},"$defs":{"when":{"type":"string"}}},
		"Tag":{"type":"string"},"Unused":{"type":"null"}}}`
	bundle := bundleOf(t, document, "/operations/op/input")
	root, _ := bundle["$id"].(string)
	if !strings.HasPrefix(root, "https://bundle-0.openbindings.invalid/") || bundle["$schema"] != draft202012URI {
		t.Fatalf("root: %v", bundle)
	}
	defs := bundle["$defs"].(map[string]any)
	if len(defs) != 3 {
		t.Fatalf("want the entry, Task, and Tag copied, got %d: %v", len(defs), defs)
	}
	ids := map[string]map[string]any{}
	for _, unit := range defs {
		object := unit.(map[string]any)
		id, _ := object["$id"].(string)
		if !strings.HasPrefix(id, root) {
			t.Fatalf("a copied position without a generated $id: %v", object)
		}
		ids[id] = object
	}
	entry := ids[bundle["$ref"].(string)]
	task := ids[entry["$ref"].(string)]
	if task == nil || task["type"] != "object" {
		t.Fatalf("the entry does not name Task by its $id: %v", entry)
	}
	properties := task["properties"].(map[string]any)
	tag := ids[properties["tags"].(map[string]any)["items"].(map[string]any)["$ref"].(string)]
	if tag == nil || tag["type"] != "string" {
		t.Fatalf("Task does not name Tag by its $id: %v", properties)
	}
	if when := properties["when"].(map[string]any)["$ref"]; when != task["$id"].(string)+"#/$defs/when" {
		t.Fatalf("a pointer within Task: %v", when)
	}
	if got := verdicts(t, document, "op", map[string]any{"tags": []any{"a"}, "when": "now"}, map[string]any{"tags": []any{json.Number("1")}}); !equalStrings(got, []string{"valid", "mismatch"}) {
		t.Fatalf("verdicts %v", got)
	}
}

// A reference evaluation never reaches, whose target the bundle does not hold
// as a schema, names a false placeholder, so every reference resolves within
// the bundle.
func TestBundle_Placeholder(t *testing.T) {
	document := `{"openbindings":"0.2.0","operations":{"op":{"input":{"type":"string","$defs":{
		"external":{"$ref":"https://nowhere.example/x"},
		"uncopied":{"$ref":"https://ex.test/other"},
		"enum":{"$ref":"#/operations/op/input/$defs/values/enum"},
		"values":{"enum":[1,2]}}}}},
		"schemas":{"Other":{"$id":"https://ex.test/other","type":"null"}}}`
	bundle := bundleOf(t, document, "/operations/op/input")
	defs := bundle["$defs"].(map[string]any)
	var placeholder string
	for key, unit := range defs {
		if unit == false {
			placeholder = key
		}
	}
	if placeholder == "" || len(defs) != 2 {
		t.Fatalf("want the entry and the placeholder only: %v", defs)
	}
	for _, unit := range defs {
		object, ok := unit.(map[string]any)
		if !ok {
			continue
		}
		for _, name := range []string{"external", "uncopied", "enum"} {
			ref := object["$defs"].(map[string]any)[name].(map[string]any)["$ref"]
			if ref != bundle["$id"].(string)+"#/$defs/"+placeholder {
				t.Errorf("%s: %v, want the placeholder", name, ref)
			}
		}
	}
	if got := verdicts(t, document, "op", "a", json.Number("1")); !equalStrings(got, []string{"valid", "mismatch"}) {
		t.Fatalf("verdicts %v", got)
	}
}

// Schemas under definitions and schema values of dependencies move into
// their resource's $defs, and references to them follow; an array of names
// under dependencies stays.
func TestBundle_LegacyPositionsMove(t *testing.T) {
	document := `{"openbindings":"0.2.0","operations":{"op":{"input":{
		"properties":{"a":{"$ref":"#/operations/op/input/definitions/A"},"b":{"$ref":"#/operations/op/input/dependencies/b"}},
		"definitions":{"A":{"type":"string"}},
		"dependencies":{"b":{"type":"number"},"c":["a"]},
		"$defs":{"moved0":{"type":"null"}}}}}}`
	bundle := bundleOf(t, document, "/operations/op/input")
	var entry map[string]any
	for _, unit := range bundle["$defs"].(map[string]any) {
		entry = unit.(map[string]any)
	}
	if _, present := entry["definitions"]; present {
		t.Fatalf("definitions remain: %v", entry)
	}
	if dependencies := entry["dependencies"].(map[string]any); len(dependencies) != 1 || dependencies["c"] == nil {
		t.Fatalf("dependencies: %v", dependencies)
	}
	defs := entry["$defs"].(map[string]any)
	if len(defs) != 3 || defs["moved0"].(map[string]any)["type"] != "null" {
		t.Fatalf("$defs: %v", defs)
	}
	properties := entry["properties"].(map[string]any)
	for name, want := range map[string]string{"a": "string", "b": "number"} {
		ref := properties[name].(map[string]any)["$ref"].(string)
		key := strings.TrimPrefix(ref, entry["$id"].(string)+"#/$defs/")
		if moved, _ := defs[key].(map[string]any); moved == nil || moved["type"] != want {
			t.Errorf("%s: %s does not name the moved schema: %v", name, ref, defs)
		}
	}
	if got := verdicts(t, document, "op", map[string]any{"a": "x", "b": json.Number("1")}, map[string]any{"a": json.Number("1")}); !equalStrings(got, []string{"valid", "mismatch"}) {
		t.Fatalf("verdicts %v", got)
	}
}

// An evaluation beginning in the document resource holds the document
// resource outermost in its dynamic scope (§7.2): the root declares a scope
// wrapper for each of its $dynamicAnchors a reached $dynamicRef can look up.
func TestBundle_ScopeWrappers(t *testing.T) {
	document := `{"openbindings":"0.2.0","operations":{
		"inDocument":{"input":{"$ref":"https://ex.test/list"}},
		"inResource":{"input":{"$id":"https://ex.test/entry","$ref":"https://ex.test/list"}}},
		"schemas":{
		"List":{"$id":"https://ex.test/list","type":"array","items":{"$dynamicRef":"#item"},"$defs":{"item":{"$dynamicAnchor":"item","type":"string"}}},
		"Override":{"$dynamicAnchor":"item","type":"number"}}}`
	bundle := bundleOf(t, document, "/operations/inDocument/input")
	wrapped := false
	for _, unit := range bundle["$defs"].(map[string]any) {
		if object, _ := unit.(map[string]any); object["$dynamicAnchor"] == "item" && object["$ref"] != nil {
			wrapped = true
		}
	}
	if !wrapped {
		t.Fatalf("no scope wrapper: %v", bundle)
	}
	if got := verdicts(t, document, "inDocument", []any{json.Number("1")}, []any{"a"}); !equalStrings(got, []string{"valid", "mismatch"}) {
		t.Fatalf("an evaluation beginning in the document resource: %v", got)
	}
	if got := verdicts(t, document, "inResource", []any{"a"}, []any{json.Number("1")}); !equalStrings(got, []string{"valid", "mismatch"}) {
		t.Fatalf("an evaluation beginning in a resource with its own $id: %v", got)
	}
}

// A reference to a JSON Schema 2020-12 meta-schema nothing in the space
// declares is satisfied by embedding it.
func TestBundle_EmbedsMetaSchemas(t *testing.T) {
	document := `{"openbindings":"0.2.0","operations":{"op":{"input":{"$ref":"https://json-schema.org/draft/2020-12/schema"}}}}`
	bundle := bundleOf(t, document, "/operations/op/input")
	if defs := bundle["$defs"].(map[string]any); len(defs) != 9 {
		t.Fatalf("want the entry and eight meta-schemas, got %d", len(defs))
	}
	if got := verdicts(t, document, "op", map[string]any{"type": "string"}, map[string]any{"type": json.Number("5")}); !equalStrings(got, []string{"valid", "mismatch"}) {
		t.Fatalf("verdicts %v", got)
	}
}

// Resources the application supplies are named by their URI and their root's
// own $id; references into them are rewritten to the $id they carry.
func TestBundle_SuppliedResources(t *testing.T) {
	money := Resource{URI: "https://schemas.example.com/money.json", Document: json.RawMessage(`{"$id":"https://schemas.example.com/v1/money","type":"object","required":["amount"],"properties":{"amount":{"$ref":"#/$defs/amount"}},"$defs":{"amount":{"type":"string"}}}`)}
	document := `{"openbindings":"0.2.0","operations":{
		"byURI":{"input":{"$ref":"https://schemas.example.com/money.json"}},
		"byID":{"input":{"$ref":"https://schemas.example.com/v1/money#/$defs/amount"}}}}`
	bundle := bundleOf(t, document, "/operations/byURI/input", money)
	found := false
	for _, unit := range bundle["$defs"].(map[string]any) {
		object := unit.(map[string]any)
		if object["$id"] == "https://schemas.example.com/v1/money" {
			found = true
		}
		if ref, ok := object["$ref"].(string); ok && ref != "https://schemas.example.com/v1/money" {
			t.Errorf("the entry names the resource by %q", ref)
		}
	}
	if !found {
		t.Fatalf("the resource is not copied under its $id: %v", bundle)
	}
	contracts := contractsFor(t, mustDecodeInterface(t, document), money)
	for operation, values := range map[string][2]any{
		"byURI": {map[string]any{"amount": "1.00"}, map[string]any{}},
		"byID":  {"1.00", json.Number("1")},
	} {
		contract, _ := contracts.CompileInput(context.Background(), operation)
		for i, want := range []string{"valid", "mismatch"} {
			if got := verdictOf(t, contract.Validate(context.Background(), values[i])); got != want {
				t.Errorf("%s: %v: %s, want %s", operation, values[i], got, want)
			}
		}
	}
}

func TestNewValueContractCompiler_RefusesResources(t *testing.T) {
	for name, resources := range map[string][]Resource{
		"not JSON":        {{URI: "https://ex.test/a", Document: json.RawMessage(`{`)}},
		"repeated member": {{URI: "https://ex.test/a", Document: json.RawMessage(`{"type":"string","type":"number"}`)}},
		"relative URI":    {{URI: "a.json", Document: json.RawMessage(`{}`)}},
		"fragment":        {{URI: "https://ex.test/a#x", Document: json.RawMessage(`{}`)}},
		"one URI twice":   {{URI: "https://ex.test/a", Document: json.RawMessage(`{}`)}, {URI: "HTTPS://EX.TEST:443/a", Document: json.RawMessage(`{}`)}},
	} {
		if _, err := NewValueContractCompiler(testEvaluator{}, resources...); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if _, err := NewValueContractCompiler(testEvaluator{}, Resource{URI: "https://ex.test/a#", Document: json.RawMessage(`true`)}); err != nil {
		t.Errorf("an empty fragment is removed: %v", err)
	}
}

// Core refuses, before calling the evaluator, what the specification leaves
// undefined (ErrUndefined), what it lacks the capability for, and what its
// conservative policy refuses, each located.
func TestRefusals(t *testing.T) {
	cases := []struct {
		name, document string
		undefined      bool
		location       string
	}{
		{"a reached invalid pattern", `{"openbindings":"0.2.0","operations":{"op":{"input":{"pattern":"("}}}}`, true, "#/operations/op/input"},
		{"a copied, unreached invalid pattern", `{"openbindings":"0.2.0","operations":{"op":{"input":{"$defs":{"x":{"pattern":"("}}}}}}`, false, "#/operations/op/input/$defs/x"},
		{"a reached invalid schema", `{"openbindings":"0.2.0","operations":{"op":{"input":{"properties":{"a":{"type":5}}}}}}`, true, "#/operations/op/input/properties/a/type"},
		{"a pointer to no schema", `{"openbindings":"0.2.0","operations":{"op":{"input":{"$ref":"#/operations/op"}}}}`, true, "#/operations/op/input"},
		{"a pointer inside a resource", `{"openbindings":"0.2.0","operations":{"op":{"input":{"$ref":"#/schemas/R/type"}}},"schemas":{"R":{"$id":"https://ex.test/r","type":"string"}}}`, true, "#/operations/op/input"},
		{"a plain name declared twice", `{"openbindings":"0.2.0","operations":{"op":{"input":{"$ref":"#a"}}},"schemas":{"A":{"$anchor":"a"},"B":{"$anchor":"a"}}}`, true, "#/operations/op/input"},
		{"an $id of #", `{"openbindings":"0.2.0","operations":{"op":{"input":{"$ref":"#/schemas/R"}}},"schemas":{"R":{"$id":"https://ex.test/r","$defs":{"x":{"$id":"#"}},"$ref":"#/$defs/x"}}}`, true, "#/schemas/R/$defs/x"},
		{"a resource nobody supplied", `{"openbindings":"0.2.0","operations":{"op":{"input":{"$ref":"https://ex.test/missing"}}}}`, false, "#/operations/op/input"},
		{"a relative $id at an OBI position", `{"openbindings":"0.2.0","operations":{"op":{"input":{"$id":"relative","type":"string"}}}}`, false, "#/operations/op/input"},
		{"a cycle in place", `{"openbindings":"0.2.0","operations":{"op":{"input":{"$ref":"#/schemas/A"}}},"schemas":{"A":{"allOf":[{"$ref":"#/schemas/A"}]}}}`, false, "#/schemas/A"},
		{"names colliding in normal form", `{"openbindings":"0.2.0","operations":{"op":{"input":{"allOf":[{"$ref":"https://ex.test/a"},{"$ref":"HTTPS://EX.test/a"}]}}},"schemas":{"A":{"$id":"https://ex.test/a"},"B":{"$id":"HTTPS://EX.test/a"}}}`, false, "#/operations/op/input/allOf/0"},
		{"a dialect other than 2020-12", `{"openbindings":"0.2.0","operations":{"op":{"input":{"$ref":"#/schemas/R"}}},"schemas":{"R":{"$id":"https://ex.test/r","$schema":"http://json-schema.org/draft-07/schema#"}}}`, false, "#/schemas/R"},
		{"nesting past core's limit", `{"openbindings":"0.2.0","operations":{"op":{"input":` + strings.Repeat(`{"not":`, 300) + `{}` + strings.Repeat(`}`, 300) + `}}}`, false, "#/operations/op/input"},
	}
	for _, c := range cases {
		refusal := refusalOf(t, c.document, "op")
		if errors.Is(refusal, ErrUndefined) != c.undefined {
			t.Errorf("%s: ErrUndefined %v, want %v: %v", c.name, errors.Is(refusal, ErrUndefined), c.undefined, refusal)
		}
		if refusal.Location != c.location {
			t.Errorf("%s: located at %q, want %q: %v", c.name, refusal.Location, c.location, refusal)
		}
	}
}

// A reference into a part of a supplied resource that is not a schema is a
// capability core lacks: the resource may be another format.
func TestRefusals_SuppliedNonSchema(t *testing.T) {
	openapi := Resource{URI: "https://ex.test/openapi.json", Document: json.RawMessage(`{"openapi":"3.1.0","components":{"schemas":{"Pet":{"type":"object"}}}}`)}
	refusal := refusalOf(t, `{"openbindings":"0.2.0","operations":{"op":{"input":{"$ref":"https://ex.test/openapi.json#/components/schemas/Pet"}}}}`, "op", openapi)
	if errors.Is(refusal, ErrUndefined) || refusal.Location != "#/operations/op/input" {
		t.Fatalf("%v", refusal)
	}
}

// An operation with no schema at a direction states no value contract there:
// every value gets a located ErrNoValueContract.
func TestCompile_NoValueContract(t *testing.T) {
	contracts := contractsFor(t, mustDecodeInterface(t, `{"openbindings":"0.2.0","operations":{"op":{"aliases":["alias"]}}}`))
	contract, err := contracts.CompileOutput(context.Background(), "alias")
	if err != nil {
		t.Fatal(err)
	}
	var refusal *NoVerdictError
	if err := contract.Validate(context.Background(), "x"); !errors.As(err, &refusal) || !errors.Is(err, ErrNoValueContract) || refusal.Location != "#/operations/op/output" {
		t.Fatalf("%v", err)
	}
	if _, err := contracts.CompileInput(context.Background(), "missing"); !errors.Is(err, ErrOperationNotFound) {
		t.Fatalf("%v", err)
	}
}

// A name two operations carry resolves to neither (OBI-T-07).
func TestCompile_AmbiguousName(t *testing.T) {
	contracts := contractsFor(t, mustDecodeInterface(t, `{"openbindings":"0.2.0","operations":{"a":{"aliases":["x"]},"b":{"aliases":["x"]}}}`))
	if _, err := contracts.CompileInput(context.Background(), "x"); !errors.Is(err, ErrOperationNotFound) {
		t.Fatalf("%v", err)
	}
}

// The spellings core generates are not part of the contract: the kit varies
// them, and a correct evaluator gives the same verdicts under each.
func TestBundle_SpellingsVary(t *testing.T) {
	document := `{"openbindings":"0.2.0","operations":{"op":{"input":{"$ref":"#/schemas/T"}}},"schemas":{"T":{"type":"string","definitions":{"x":{}}}}}`
	contracts := contractsFor(t, mustDecodeInterface(t, document))
	first, _ := contracts.space.bundle("/operations/op/input", bundleSpelling{0})
	second, _ := contracts.space.bundle("/operations/op/input", bundleSpelling{1})
	if string(first) == string(second) {
		t.Fatal("the spellings do not vary")
	}
	for _, raw := range []json.RawMessage{first, second} {
		compiled, err := testEvaluator{}.Compile(context.Background(), SchemaBundle{Document: raw})
		if err != nil {
			t.Fatal(err)
		}
		if compiled.Validate(context.Background(), "a") != nil || compiled.Validate(context.Background(), json.Number("1")) == nil {
			t.Fatalf("verdicts differ under %s", raw)
		}
	}
}

// The namespace avoids every host the document mentions.
func TestBundle_NamespaceAvoidsTheDocument(t *testing.T) {
	document := `{"openbindings":"0.2.0","operations":{"op":{"input":{"$ref":"#/schemas/T"}}},"schemas":{"T":{"$id":"https://BUNDLE-0.openbindings.invalid/t","type":"string"}}}`
	bundle := bundleOf(t, document, "/operations/op/input")
	if !strings.HasPrefix(bundle["$id"].(string), "https://bundle-1.openbindings.invalid/") {
		t.Fatalf("root %v", bundle["$id"])
	}
}
