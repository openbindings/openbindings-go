package openbindings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// bundleOf returns the bundle core writes for a value contract, decoded.
func bundleOf(t *testing.T, document, entry string, resources ...Resource) map[string]any {
	t.Helper()
	contracts := contractsFor(t, mustDecodeDocument(t, document), resources...)
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
	contract, err := contractsFor(t, mustDecodeDocument(t, document), resources...).CompileInput(context.Background(), operation)
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
	contract, err := contractsFor(t, mustDecodeDocument(t, document)).CompileInput(context.Background(), operation)
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

// An evaluation beginning in the document resource makes it outermost in the
// dynamic scope (§7.2), so a $dynamicRef whose initial target declares a
// $dynamicAnchor looks the name up there first (JSON Schema Core §8.2.3.2).
// A name the document resource declares more than once, by $anchor or
// $dynamicAnchor, leaves that capture undefined (Core §8.2.2): no verdict,
// whichever schema would have won. A name declared once captures as before,
// and one past core's index is core's limit, not a verdict.
func TestDynamicScope_DuplicateDocumentNames(t *testing.T) {
	inner := `"Inner":{"$id":"https://e.test/inner","$dynamicAnchor":"node","type":"string"}`
	entry := `"operations":{"op":{"input":{"$dynamicRef":"https://e.test/inner#node"}}}`
	for _, c := range []struct {
		name, schemas string
		values        []any
		want          []string
		undefined     bool
	}{
		{"a $dynamicAnchor and an $anchor", `"Outer":{"$dynamicAnchor":"node","type":"integer"},` + inner + `,"AlsoOuter":{"$anchor":"node"}`, []any{json.Number("1")}, []string{"no verdict"}, true},
		{"two $dynamicAnchors", `"Outer":{"$dynamicAnchor":"node","type":"integer"},` + inner + `,"AlsoOuter":{"$dynamicAnchor":"node"}`, []any{json.Number("1")}, []string{"no verdict"}, true},
		{"two $anchors", `"Outer":{"$anchor":"node","type":"integer"},` + inner + `,"AlsoOuter":{"$anchor":"node"}`, []any{json.Number("1")}, []string{"no verdict"}, true},
		{"one $dynamicAnchor captures", `"Outer":{"$dynamicAnchor":"node","type":"integer"},` + inner, []any{json.Number("1"), "s"}, []string{"valid", "mismatch"}, false},
		{"one $anchor does not capture", `"Outer":{"$anchor":"node","type":"integer"},` + inner, []any{"s", json.Number("1")}, []string{"valid", "mismatch"}, false},
		{"a name declared outside the grammar twice", `"Outer":{"$dynamicAnchor":"1node"},"AlsoOuter":{"$anchor":"1node"},` + inner, []any{"s", json.Number("1")}, []string{"valid", "mismatch"}, false},
	} {
		document := `{"openbindings":"0.2.0",` + entry + `,"schemas":{` + c.schemas + `}}`
		if got := verdicts(t, document, "op", c.values...); !equalStrings(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
			continue
		}
		if c.undefined {
			if refusal := refusalOf(t, document, "op"); !errors.Is(refusal, ErrUndefined) || refusal.Location != "#/operations/op/input" || !strings.Contains(refusal.Error(), "declares more than once") {
				t.Errorf("%s: %v at %s", c.name, refusal, refusal.Location)
			}
		}
	}
	// The only declaration past core's index: its limit, never a verdict
	// that leaves the capture out.
	deep := strings.Repeat(`{"not":`, 300) + `{"$dynamicAnchor":"node","type":"integer"}` + strings.Repeat(`}`, 300)
	refusal := refusalOf(t, `{"openbindings":"0.2.0",`+entry+`,"schemas":{"Deep":`+deep+`,`+inner+`}}`, "op")
	if errors.Is(refusal, ErrUndefined) || !strings.Contains(refusal.Error(), "does not index") {
		t.Errorf("a declaration past the index: %v", refusal)
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
	contracts := contractsFor(t, mustDecodeDocument(t, document), money)
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

// A supplied schema root may identify itself by an empty URI reference.
func TestBundle_SuppliedRootIdentity(t *testing.T) {
	const uri = "https://ex.test/schema"
	for _, declaration := range []string{``, `"$id":"",`, `"$id":"#",`, `"$id":"schema",`, `"$id":"https://ex.test/schema",`} {
		t.Run(declaration, func(t *testing.T) {
			resource := Resource{URI: uri, Document: json.RawMessage(`{` + declaration + `"$ref":"#/$defs/value","$defs":{"value":{"type":"string"}}}`)}
			doc := mustDecodeDocument(t, `{"openbindings":"0.2.0","operations":{"op":{"input":{"$ref":"`+uri+`"}}}}`)
			contract, err := contractsFor(t, doc, resource).CompileInput(context.Background(), "op")
			if err != nil {
				t.Fatal(err)
			}
			if err := contract.Validate(context.Background(), "yes"); err != nil {
				t.Fatalf("valid string: %v", err)
			}
			if err := contract.Validate(context.Background(), json.Number("42")); !errors.Is(err, ErrMismatch) {
				t.Fatalf("number: want mismatch, got %v", err)
			}
		})
	}
	// The root exception must not authorize a nested resource to reuse its
	// enclosing resource's identity, whether spelled relatively or absolutely.
	for _, id := range []string{"", "#", uri} {
		t.Run("nested/"+id, func(t *testing.T) {
			raw := fmt.Sprintf(`{"$id":"","$ref":"#/$defs/value","$defs":{"value":{"$id":%q,"type":"string"}}}`, id)
			resource := Resource{URI: uri, Document: json.RawMessage(raw)}
			refusal := refusalOf(t, `{"openbindings":"0.2.0","operations":{"op":{"input":{"$ref":"`+uri+`"}}}}`, "op", resource)
			if !errors.Is(refusal, ErrNoVerdict) {
				t.Fatalf("nested identity collision: %v", refusal)
			}
		})
	}
}

// Dialects go by resource (§5.2, JSON Schema Core §9.3.2). The document
// resource's is 2020-12, and a $schema in it declares none, so a schema
// copied with a foreign $schema and no $id is read as 2020-12 and gets a
// verdict, though the $schema still violates OBI-09; so is one below an
// $id resource's root, where $schema is misplaced. A resource whose root
// names another dialect, and a resource inheriting it, get no verdict:
// this SDK evaluates 2020-12 alone.
func TestDialects_ByResource(t *testing.T) {
	const draft07 = `"http://json-schema.org/draft-07/schema#"`
	for _, c := range []struct {
		name, document string
		value          any
		want           string
	}{
		{"misplaced at an operation's input, mismatch", `{"openbindings":"0.2.0","operations":{"op":{"input":{"$schema":` + draft07 + `,"type":"string"}}}}`, json.Number("5"), "mismatch"},
		{"misplaced at an operation's input, valid", `{"openbindings":"0.2.0","operations":{"op":{"input":{"$schema":` + draft07 + `,"type":"string"}}}}`, "x", "valid"},
		{"misplaced, nullable an unknown keyword", `{"openbindings":"0.2.0","operations":{"op":{"input":{"$schema":` + draft07 + `,"type":"string","nullable":true}}}}`, nil, "mismatch"},
		{"misplaced, an https draft-07 spelling", `{"openbindings":"0.2.0","operations":{"op":{"input":{"$schema":"https://json-schema.org/draft-07/schema#","type":"integer"}}}}`, json.Number("1"), "valid"},
		// Draft-07 would ignore the keywords beside $ref; 2020-12 applies them.
		{"misplaced beside a $ref", `{"openbindings":"0.2.0","operations":{"op":{"input":{"$schema":` + draft07 + `,"$ref":"#/schemas/Any","type":"string"}}},"schemas":{"Any":{}}}`, json.Number("5"), "mismatch"},
		{"misplaced at a schemas entry", `{"openbindings":"0.2.0","operations":{"op":{"input":{"$ref":"#/schemas/S"}}},"schemas":{"S":{"$schema":` + draft07 + `,"type":"string"}}}`, json.Number("5"), "mismatch"},
		{"misplaced below an $id resource's root", `{"openbindings":"0.2.0","operations":{"op":{"input":{"$ref":"https://e.example.test/a"}}},"schemas":{"A":{"$id":"https://e.example.test/a","properties":{"p":{"$schema":` + draft07 + `,"type":"string"}}}}}`, map[string]any{"p": json.Number("5")}, "mismatch"},
		{"a 2020-12 resource", `{"openbindings":"0.2.0","operations":{"op":{"input":{"$ref":"https://e.example.test/a"}}},"schemas":{"A":{"$id":"https://e.example.test/a","$schema":"https://json-schema.org/draft/2020-12/schema","type":"string"}}}`, json.Number("5"), "mismatch"},
		{"a draft-07 resource", `{"openbindings":"0.2.0","operations":{"op":{"input":{"$ref":"https://e.example.test/a"}}},"schemas":{"A":{"$id":"https://e.example.test/a","$schema":` + draft07 + `,"type":"string"}}}`, json.Number("5"), "no verdict"},
		{"a resource inheriting draft-07", `{"openbindings":"0.2.0","operations":{"op":{"input":{"$ref":"https://e.example.test/inner"}}},"schemas":{"Outer":{"$id":"https://e.example.test/outer","$schema":` + draft07 + `,"definitions":{"inner":{"$id":"https://e.example.test/inner","type":"string"}}}}}`, json.Number("5"), "no verdict"},
	} {
		if got := inputVerdict(t, c.document, "op", c.value); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
		if report := mustValidateDocument(t, c.document); report.Evidence["OBI-09"] != EvidenceViolated && !strings.Contains(c.name, "2020-12") {
			t.Errorf("%s: OBI-09 is %s, want violated", c.name, report.Evidence["OBI-09"])
		}
	}
	// The refusal is located at the root that declares the dialect, also
	// for a resource inheriting it.
	refusal := refusalOf(t, `{"openbindings":"0.2.0","operations":{"op":{"input":{"$ref":"https://e.example.test/inner"}}},"schemas":{"Outer":{"$id":"https://e.example.test/outer","$schema":`+draft07+`,"definitions":{"inner":{"$id":"https://e.example.test/inner","type":"string"}}}}}`, "op")
	if errors.Is(refusal, ErrUndefined) || refusal.Location != "#/schemas/Outer" || !strings.Contains(refusal.Error(), "does not evaluate") {
		t.Errorf("an inherited dialect: %v at %s", refusal, refusal.Location)
	}
}

// The bundle writes $schema only where it declares a dialect, a resource's
// root, so core's generated $id at an OBI position of the document resource
// never makes a misplaced one declare its unit's dialect, and an evaluator
// never meets a misplaced one.
func TestBundle_WritesOnlyDeclaringSchemas(t *testing.T) {
	const draft07 = `"http://json-schema.org/draft-07/schema#"`
	document := `{"openbindings":"0.2.0","operations":{"op":{"input":{"$schema":` + draft07 + `,"properties":{
		"a":{"$schema":` + draft07 + `},"r":{"$ref":"https://e.example.test/r"}}}}},
		"schemas":{"R":{"$id":"https://e.example.test/r","$schema":"https://json-schema.org/draft/2020-12/schema","items":{"$schema":"https://json-schema.org/draft/2020-12/schema"}}}}`
	bundle := bundleOf(t, document, "/operations/op/input")
	var declaring []string
	var walk func(value any, at string)
	walk = func(value any, at string) {
		switch v := value.(type) {
		case map[string]any:
			if _, present := v["$schema"]; present {
				declaring = append(declaring, fmt.Sprint(v["$id"]))
			}
			for key, member := range v {
				walk(member, at+"/"+key)
			}
		case []any:
			for _, item := range v {
				walk(item, at)
			}
		}
	}
	for _, unit := range bundle["$defs"].(map[string]any) {
		walk(unit, "")
	}
	if !slices.Equal(declaring, []string{"https://e.example.test/r"}) {
		t.Fatalf("$schema written at %v, want only the resource root https://e.example.test/r", declaring)
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
	// A configuration error never reads as a value's outcome.
	if _, err := NewValueContractCompiler(testEvaluator{}, Resource{URI: "https://ex.test/a", Document: json.RawMessage(`{"a":1,"a":2}`)}); errors.Is(err, ErrNoVerdict) || errors.Is(err, ErrUndefined) {
		t.Errorf("a repeated member: %v", err)
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
		{"a cycle every evaluation enters", `{"openbindings":"0.2.0","operations":{"op":{"input":{"$ref":"#/schemas/A"}}},"schemas":{"A":{"allOf":[{"$ref":"#/schemas/A"}]}}}`, true, "#/schemas/A"},
		{"a schema that is only a reference to itself", `{"openbindings":"0.2.0","operations":{"op":{"input":{"$ref":"#/operations/op/input"}}}}`, true, "#/operations/op/input"},
		{"a cycle only some values enter", `{"openbindings":"0.2.0","operations":{"op":{"input":{"properties":{"a":{"$ref":"#/schemas/L"}}}}},"schemas":{"L":{"$ref":"#/schemas/L"}}}`, false, "#/schemas/L"},
		{"a cycle behind a condition", `{"openbindings":"0.2.0","operations":{"op":{"input":{"if":{"type":"string"},"then":{"$ref":"#/schemas/A"}}}},"schemas":{"A":{"allOf":[{"$ref":"#/schemas/A"}]}}}`, false, "#/schemas/A"},
		{"names colliding in normal form", `{"openbindings":"0.2.0","operations":{"op":{"input":{"allOf":[{"$ref":"https://ex.test/a"},{"$ref":"HTTPS://EX.test/a"}]}}},"schemas":{"A":{"$id":"https://ex.test/a"},"B":{"$id":"HTTPS://EX.test/a"}}}`, false, "#/operations/op/input/allOf/0"},
		{"a dialect other than 2020-12", `{"openbindings":"0.2.0","operations":{"op":{"input":{"$ref":"#/schemas/R"}}},"schemas":{"R":{"$id":"https://ex.test/r","$schema":"http://json-schema.org/draft-07/schema#"}}}`, false, "#/schemas/R"},
		{"nesting past core's limit", `{"openbindings":"0.2.0","operations":{"op":{"input":` + strings.Repeat(`{"not":`, 300) + `{}` + strings.Repeat(`}`, 300) + `}}}`, false, "#/operations/op/input"},
		{"a pointer to a resource inside another", `{"openbindings":"0.2.0","operations":{"op":{"input":{"$ref":"#/schemas/A/properties/x"}}},"schemas":{"A":{"$id":"https://ex.test/a","properties":{"x":{"$id":"https://ex.test/b","type":"string"}}}}}`, true, "#/operations/op/input"},
		{"a pointer past core's limit", `{"openbindings":"0.2.0","operations":{"op":{"input":{"$ref":"#/schemas/Deep` + strings.Repeat(`/not`, 290) + `"}}},"schemas":{"Deep":` + strings.Repeat(`{"not":`, 300) + `{}` + strings.Repeat(`}`, 300) + `}}`, false, "#/operations/op/input"},
		{"a plain name past core's limit", `{"openbindings":"0.2.0","operations":{"op":{"input":{"$ref":"#deep"}}},"schemas":{"Deep":` + strings.Repeat(`{"not":`, 290) + `{"$anchor":"deep"}` + strings.Repeat(`}`, 290) + `}}`, false, "#/operations/op/input"},
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

// OBI-12 and a value contract look a same-document reference in the
// document resource up alike (§7.2): a pointer landing on a resource nested
// inside another resource fails both.
func TestRefusals_SameDocumentLookupIsShared(t *testing.T) {
	document := `{"openbindings":"0.2.0","operations":{"op":{"input":{"$ref":"#/schemas/A/properties/x"}}},"schemas":{"A":{"$id":"https://ex.test/a","properties":{"x":{"$id":"https://ex.test/b"}}}}}`
	if _, report, _ := ValidateDocument([]byte(document)); !slices.Contains(report.Violated, "OBI-12") {
		t.Errorf("OBI-12 passes the reference: %+v", report.Findings)
	}
	if refusal := refusalOf(t, document, "op"); !errors.Is(refusal, ErrUndefined) {
		t.Errorf("the value contract resolves the reference: %v", refusal)
	}
}

// Value contracts take work linear in the document: indexing a deeply nested
// schema stops at core's depth limit, and compiling every operation's
// contract checks each unit apart from the rest of the document.
func TestValueContracts_WorkIsLinear(t *testing.T) {
	scaled := func(name string, build func(n int) string, work func(document string, n int)) {
		t.Helper()
		small, large := build(250), build(1000)
		ratio := float64(allocated(func() { work(large, 1000) })) / float64(allocated(func() { work(small, 250) }))
		if ratio > 6 {
			t.Errorf("%s: 4 times the input allocated %.1f times the memory", name, ratio)
		}
	}
	resolve := func(document string, _ int) {
		compiler, _ := NewValueContractCompiler(testEvaluator{})
		if _, err := compiler.Resolve(context.Background(), mustDecodeDocument(t, document)); err != nil {
			t.Fatal(err)
		}
	}
	scaled("a deeply nested schema", func(n int) string {
		return `{"openbindings":"0.2.0","operations":{},"schemas":{"A":` + strings.Repeat(`{"not":`, 8*n) + `{}` + strings.Repeat(`}`, 8*n) + `}}`
	}, resolve)
	scaled("compiling every operation's contract", func(n int) string {
		var operations, schemas []string
		for i := range n {
			operations = append(operations, fmt.Sprintf(`"o%d":{"input":{"$ref":"#/schemas/S%d"}}`, i, i))
			schemas = append(schemas, fmt.Sprintf(`"S%d":{"properties":{"a":{"type":"string"},"b":{"items":{"$ref":"#/schemas/S%d"}}}}`, i, i))
		}
		return `{"openbindings":"0.2.0","operations":{` + strings.Join(operations, ",") + `},"schemas":{` + strings.Join(schemas, ",") + `}}`
	}, func(document string, n int) {
		contracts := contractsFor(t, mustDecodeDocument(t, document))
		for i := range n {
			if _, err := contracts.CompileInput(context.Background(), fmt.Sprintf("o%d", i)); err != nil {
				t.Fatal(err)
			}
		}
	})
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
	contracts := contractsFor(t, mustDecodeDocument(t, `{"openbindings":"0.2.0","operations":{"op":{"aliases":["alias"]}}}`))
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
	// Locations are URI-references, percent-encoded.
	contract, _ = contractsFor(t, mustDecodeDocument(t, `{"openbindings":"0.2.0","operations":{"tasks create":{}}}`)).CompileInput(context.Background(), "tasks create")
	if !errors.As(contract.Err(), &refusal) || refusal.Location != "#/operations/tasks%20create/input" {
		t.Fatalf("%v", contract.Err())
	}
}

// A name two operations carry, in a document violating OBI-05, identifies no
// one operation (§5.1, Aliases), so it resolves to neither.
func TestCompile_AmbiguousName(t *testing.T) {
	contracts := contractsFor(t, mustDecodeDocument(t, `{"openbindings":"0.2.0","operations":{"a":{"aliases":["x"]},"b":{"aliases":["x"]}}}`))
	if _, err := contracts.CompileInput(context.Background(), "x"); !errors.Is(err, ErrOperationNotFound) {
		t.Fatalf("%v", err)
	}
}

func TestCompile_OperationIdentifierIndex(t *testing.T) {
	doc := mustDecodeDocument(t, `{"openbindings":"0.2.0","operations":{
		"a":{"aliases":["a","unique","unique","shared"],"input":{"const":"a"}},
		"b":{"aliases":["shared","keyCollision"],"input":{"const":"b"}},
		"c":{"aliases":["shared"],"input":{"const":"c"}},
		"keyCollision":{"input":{"const":"keyCollision"}},
		"":{"aliases":["emptyKey"],"input":{"const":""}}
	}}`)
	contracts := contractsFor(t, doc)
	for _, name := range []string{"a", "unique", "b", "c", "", "emptyKey", "shared", "keyCollision", "Unique", " unique", "missing"} {
		t.Run(name, func(t *testing.T) {
			// Preparing contracts must retain the public lookup's exact,
			// equally authoritative key/alias semantics on invalid documents too.
			key, _, found := doc.ResolveOperation(name)
			contract, err := contracts.CompileInput(context.Background(), name)
			if !found {
				if !errors.Is(err, ErrOperationNotFound) {
					t.Fatalf("ambiguous or missing %q: %v", name, err)
				}
				return
			}
			if err != nil || contract == nil {
				t.Fatalf("%q resolved to %q: %v", name, key, err)
			}
			if err := contract.Validate(context.Background(), key); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// The spellings core generates are not part of the contract: the kit varies
// them, and a correct evaluator gives the same verdicts under each.
func TestBundle_SpellingsVary(t *testing.T) {
	document := `{"openbindings":"0.2.0","operations":{"op":{"input":{"$ref":"#/schemas/T"}}},"schemas":{"T":{"type":"string","definitions":{"x":{}}}}}`
	contracts := contractsFor(t, mustDecodeDocument(t, document))
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
	// Any spelling whose normal form names the host: case, and
	// percent-encoded unreserved characters.
	for _, id := range []string{"https://BUNDLE-0.openbindings.invalid/t", "https://%62undle-0.openbindings.invalid/t", "https://bundle-0.openbindings%2Einvalid/t"} {
		document := `{"openbindings":"0.2.0","operations":{"op":{"input":{"$ref":"` + id + `"}}},"schemas":{"T":{"$id":"` + id + `","type":"string"}}}`
		bundle := bundleOf(t, document, "/operations/op/input")
		if !strings.HasPrefix(bundle["$id"].(string), "https://bundle-1.openbindings.invalid/") {
			t.Errorf("%s: root %v", id, bundle["$id"])
		}
	}
}

// isVersionViolation reports whether err is the *ValidationError naming the
// OBI-03 violation Document.Validate establishes for doc, and nothing else:
// no other finding, no refusal, and not ErrInconclusive.
func isVersionViolation(err error, doc *Document) bool {
	var got, want *ValidationError
	if !errors.As(err, &got) || errors.As(err, new(*VersionRefusalError)) || errors.Is(err, ErrInconclusive) || len(got.Findings) != 1 {
		return false
	}
	if _, verr := doc.Validate(); !errors.As(verr, &want) {
		return false
	}
	for _, finding := range want.Findings {
		if finding.Rule == "OBI-03" {
			return finding == got.Findings[0]
		}
	}
	return false
}

// Resolve interprets only a document whose declared version it supports: it
// refuses one outside SupportedVersions (CheckVersion), and for a document
// declaring no valid version returns its OBI-03 violation, not a refusal.
func TestResolve_DeclaredVersion(t *testing.T) {
	compiler, err := NewValueContractCompiler(testEvaluator{})
	if err != nil {
		t.Fatal(err)
	}
	for version, want := range map[string]string{"0.2.7": "resolved", "0.3.0": "refusal", "0.2": "OBI-03", "": "OBI-03"} {
		doc := &Document{OpenBindings: version, Operations: map[string]Operation{}}
		_, err := compiler.Resolve(context.Background(), doc)
		got := "resolved"
		switch {
		case errors.As(err, new(*VersionRefusalError)) && !errors.Is(err, ErrInconclusive):
			got = "refusal"
		case isVersionViolation(err, doc):
			got = "OBI-03"
		case err != nil:
			got = err.Error()
		}
		if got != want {
			t.Errorf("%q: %s, want %s", version, got, want)
		}
	}
	if _, err := compiler.Resolve(context.Background(), nil); err == nil || errors.Is(err, ErrInconclusive) {
		t.Errorf("a nil document: %v", err)
	}
}
