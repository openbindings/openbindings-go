package openbindingstest

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/openbindings/openbindings-go"
)

// adversary is a case group of the kit's own: the schemas of an OBI document
// by position ("/operations/op/input" is the contract validated), the
// resources supplied by URI, and values with what JSON Schema 2020-12 says of
// each.
type adversary struct {
	name, positions, resources string
	// pinned is why core refuses the contract, when it does.
	pinned string
	values []value
}

// value is a case: JSON text, its verdict, and, for a mismatch, the problem
// paths the contract decides, as acceptable sorted lists (none: every path
// must resolve).
type value struct {
	text  string
	want  outcome
	paths [][]string
}

func paths(lists ...[]string) [][]string { return lists }

func at(locations ...string) [][]string { return [][]string{locations} }

const tree = `{"https://ex.invalid/tree":{"$dynamicAnchor":"node","type":"object","properties":{"kids":{"type":"array","items":{"$dynamicRef":"#node"}}}}}`

func dynamicStatic(ref string) string {
	return `{"/operations/op/input":{"$id":"https://ex.invalid/outer","$dynamicAnchor":"t","type":"object",
		"properties":{"x":{"$dynamicRef":"` + ref + `"}},
		"$defs":{"inner":{"$id":"https://ex.invalid/inner","$anchor":"i","$dynamicAnchor":"t","type":"string"}}}}`
}

var adversaries = []adversary{
	// Legacy keywords are ignored; legacy positions are reference targets
	// and declare what they hold.
	{"legacy-ignored", `{"/operations/op/input":{"dependencies":{"a":["b"]},"items":{"type":"integer"},"additionalItems":false,"$recursiveRef":"#"}}`, "", "",
		[]value{{`{"a":1}`, valid, nil}, {`[1,2]`, valid, nil}, {`["s"]`, mismatch, at("/0")}}},
	{"legacy-definitions-beside-defs", `{"/operations/op/input":{"$defs":{"x":{"type":"string"}},"definitions":{"x":{"type":"integer"}},
		"properties":{"p":{"$ref":"#/operations/op/input/definitions/x"},"q":{"$ref":"#/operations/op/input/$defs/x"}}}}`, "", "",
		[]value{{`{"p":1,"q":"s"}`, valid, nil}, {`{"p":"s"}`, mismatch, at("/p")}, {`{"q":1}`, mismatch, at("/q")}}},
	{"legacy-dependencies-value-and-nested-definitions", `{"/operations/op/input":{"dependencies":{"x":{"type":"integer","definitions":{"y":{"minimum":5}}}},
		"properties":{"p":{"$ref":"#/operations/op/input/dependencies/x"},"q":{"$ref":"#/operations/op/input/dependencies/x/definitions/y"}}}}`, "", "",
		[]value{{`{"p":1,"q":7}`, valid, nil}, {`{"p":"s"}`, mismatch, at("/p")}, {`{"q":4}`, mismatch, at("/q")}, {`{"x":"s"}`, valid, nil}}},
	{"legacy-id-in-dependencies", `{"/operations/op/input":{"dependencies":{"x":{"$id":"https://ex.invalid/dep","type":"integer"}},"properties":{"p":{"$ref":"https://ex.invalid/dep"}}}}`, "", "",
		[]value{{`{"p":1}`, valid, nil}, {`{"p":"s"}`, mismatch, at("/p")}}},
	{"legacy-dynamic-anchor-in-definitions", `{"/operations/op/input":{"$id":"https://ex.invalid/strict","$ref":"https://ex.invalid/tree",
		"definitions":{"n":{"$dynamicAnchor":"node","$ref":"https://ex.invalid/tree","unevaluatedProperties":false}}}}`, tree, "",
		[]value{{`{"kids":[{"kids":[]}]}`, valid, nil}, {`{"kids":[{"x":1}]}`, mismatch, at("/kids/0/x")}, {`{"x":1}`, valid, nil}}},
	{"content-schema-target", `{"/operations/op/input":{"contentSchema":{"type":"integer"},"properties":{"p":{"$ref":"#/operations/op/input/contentSchema"}}}}`, "", "",
		[]value{{`{"p":1}`, valid, nil}, {`{"p":"s"}`, mismatch, at("/p")}}},
	{"pattern-properties-target", `{"/operations/op/input":{"patternProperties":{"^\\d$":{"type":"integer"}},"properties":{"p":{"$ref":"#/operations/op/input/patternProperties/%5E%5Cd$"}}}}`, "", "",
		[]value{{`{"p":1}`, valid, nil}, {`{"p":"s"}`, mismatch, at("/p")}, {`{"7":"s"}`, mismatch, at("/7")}}},

	// content, propertyNames, dependentSchemas.
	{"content-not-asserted", `{"/operations/op/input":{"contentMediaType":"application/json","contentEncoding":"base64","contentSchema":{"type":"object"}}}`, "", "",
		[]value{{`"not base64 json"`, valid, nil}}},
	{"dynamic-ref-under-property-names", `{"/operations/op/input":{"$id":"https://ex.invalid/pn","$dynamicAnchor":"n","type":["object","string"],"maxLength":2,
		"propertyNames":{"$dynamicRef":"https://ex.invalid/pn#n"}}}`, "", "",
		[]value{{`{"ab":1}`, valid, nil}, {`{"abc":1}`, mismatch, at("")}}},
	{"dependent-schemas-path", `{"/operations/op/input":{"dependentSchemas":{"a":{"required":["b"]}}}}`, "", "",
		[]value{{`{"a":1}`, mismatch, at("")}}},

	// Dynamic scope.
	{"dynamic-ref-to-root", dynamicStatic("https://ex.invalid/inner"), "", "", []value{{`{"x":"s"}`, valid, nil}, {`{"x":{}}`, mismatch, at("/x")}}},
	{"dynamic-ref-to-anchor", dynamicStatic("https://ex.invalid/inner#i"), "", "", []value{{`{"x":"s"}`, valid, nil}, {`{"x":{}}`, mismatch, at("/x")}}},
	{"dynamic-ref-static-beside-ref", `{"/operations/op/input":{"$id":"https://ex.invalid/outer","$dynamicAnchor":"t","type":"object",
		"properties":{"x":{"$ref":"https://ex.invalid/outer#/$defs/short","$dynamicRef":"https://ex.invalid/inner"}},
		"$defs":{"short":{"maxLength":2},"inner":{"$id":"https://ex.invalid/inner","$dynamicAnchor":"t","type":"string"}}}}`, "", "",
		[]value{{`{"x":"s"}`, valid, nil}, {`{"x":"sss"}`, mismatch, at("/x")}, {`{"x":{}}`, mismatch, at("/x")}}},
	{"entered-below-root", `{"/operations/op/input":{"$id":"https://ex.invalid/e","$ref":"https://ex.invalid/x#/$defs/y"},
		"/schemas/X":{"$id":"https://ex.invalid/x","$dynamicAnchor":"t","type":"integer","$defs":{"y":{"$ref":"https://ex.invalid/z"}}},
		"/schemas/Z":{"$id":"https://ex.invalid/z","$dynamicRef":"https://ex.invalid/z#t","$defs":{"t":{"$dynamicAnchor":"t","type":"string"}}}}`, "", "",
		[]value{{`5`, valid, nil}, {`"s"`, mismatch, nil}}},
	{"scope-wrappers", `{"/schemas/StrictTree":{"$dynamicAnchor":"node","$ref":"https://ex.invalid/tree","unevaluatedProperties":false},
		"/operations/op/input":{"$ref":"#/schemas/StrictTree"}}`, tree, "",
		// kids fails, so its annotations are dropped and it is unevaluated
		// too (JSON Schema Core §7.7.1.2).
		[]value{{`{"kids":[{"kids":[]}]}`, valid, nil}, {`{"kids":[{"x":1}]}`, mismatch, at("/kids", "/kids/0/x")}, {`{"x":1}`, mismatch, at("/x")}}},
	{"scope-wrappers-meta", `{"/schemas/A":{"$dynamicAnchor":"meta"},"/operations/op/input":{"$ref":"https://json-schema.org/draft/2020-12/schema"}}`, "", "",
		[]value{{`{"properties":{"p":{"minLength":-1}}}`, valid, nil}, {`{"minLength":-1}`, mismatch, nil}}},
	{"scope-wrappers-none-under-id", `{"/schemas/A":{"$dynamicAnchor":"meta"},"/operations/op/input":{"$id":"https://ex.invalid/e","$ref":"https://json-schema.org/draft/2020-12/schema"}}`, "", "",
		[]value{{`{"properties":{"p":{"minLength":-1}}}`, mismatch, nil}}},
	{"document-anchor-does-not-capture", `{"/schemas/A":{"$anchor":"node","type":"string"},"/operations/op/input":{"$ref":"https://ex.invalid/tree"}}`, tree, "",
		[]value{{`{"kids":[{"kids":[]}]}`, valid, nil}, {`{"kids":["s"]}`, mismatch, at("/kids/0")}}},
	{"unrelated-dynamic-anchor", `{"/schemas/Unused":{"$dynamicAnchor":"n","pattern":"a{"},"/operations/op/input":{"type":"string"}}`, "", "",
		[]value{{`"s"`, valid, nil}, {`1`, mismatch, at("")}}},

	// Identity.
	{"meta-schema-shadowed", `{"/schemas/Fake":{"$id":"https://json-schema.org/draft/2020-12/schema","type":"string"},
		"/operations/op/input":{"$ref":"https://json-schema.org/draft/2020-12/schema"}}`, "", "",
		[]value{{`"s"`, valid, nil}, {`{}`, mismatch, at("")}}},
	{"case-variant-collision", `{"/schemas/A":{"$id":"https://Ex.invalid:443/a","type":"string"},"/schemas/B":{"$id":"https://ex.invalid/a","type":"integer"},
		"/operations/op/input":{"$ref":"https://ex.invalid/a"}}`, "", "two resources carry one name in normal form (core's conservative policy)",
		[]value{{`"s"`, valid, nil}}},
	{"case-variant-exact", `{"/schemas/A":{"$id":"HTTPS://EX.invalid/%7ea/A","type":"string"},"/schemas/B":{"$id":"https://ex.invalid/~a/a","type":"integer"},
		"/operations/op/input":{"properties":{"a":{"$ref":"HTTPS://EX.invalid/%7ea/A"},"b":{"$ref":"https://ex.invalid/~a/a"}}}}`, "", "",
		[]value{{`{"a":"s","b":1}`, valid, nil}, {`{"a":1}`, mismatch, at("/a")}, {`{"b":"s"}`, mismatch, at("/b")}}},
	{"case-variant-unmatched", `{"/schemas/A":{"$id":"HTTPS://EX.invalid/%7ea/A","type":"string"},
		"/operations/op/input":{"$ref":"https://ex.invalid/%7Ea/A"}}`, "", "references match identifiers character for character (§7.4), so it names a resource nobody supplied",
		[]value{{`"s"`, valid, nil}}},
	{"author-id-in-core-namespace", `{"/schemas/X":{"$id":"https://bundle-0.openbindings.invalid/","type":"integer"},
		"/schemas/Y":{"$id":"https://x0.contract.invalid/root/","type":"integer"},"/schemas/Task":{"type":"string"},
		"/operations/op/input":{"properties":{"t":{"$ref":"#/schemas/Task"},"x":{"$ref":"https://bundle-0.openbindings.invalid/"},"y":{"$ref":"https://x0.contract.invalid/root/"}}}}`, "", "",
		[]value{{`{"t":"s","x":1,"y":1}`, valid, nil}, {`{"t":1}`, mismatch, at("/t")}, {`{"x":"s"}`, mismatch, at("/x")}, {`{"y":"s"}`, mismatch, at("/y")}}},
	{"author-id-spelling-core-namespace", `{"/schemas/X":{"$id":"https://%62undle-0.openbindings.invalid/","type":"integer"},
		"/schemas/Y":{"$id":"HTTPS://X%30.Contract.invalid/root/","type":"integer"},"/schemas/Task":{"type":"string"},
		"/operations/op/input":{"properties":{"t":{"$ref":"#/schemas/Task"},"x":{"$ref":"https://%62undle-0.openbindings.invalid/"},"y":{"$ref":"HTTPS://X%30.Contract.invalid/root/"}}}}`, "", "",
		[]value{{`{"t":"s","x":1,"y":1}`, valid, nil}, {`{"t":1}`, mismatch, at("/t")}, {`{"x":"s"}`, mismatch, at("/x")}, {`{"y":"s"}`, mismatch, at("/y")}}},
	{"author-id-spelling-generated-id", `{"/schemas/T":{"type":"string"},"/schemas/X":{"$id":"https://bundle-0.openbindings%2Einvalid/unit1","type":"integer"},
		"/operations/op/input":{"properties":{"a":{"$ref":"#/schemas/T"},"b":{"$ref":"https://bundle-0.openbindings%2Einvalid/unit1"}}}}`, "", "",
		[]value{{`{"a":"s","b":1}`, valid, nil}, {`{"a":1}`, mismatch, at("/a")}, {`{"b":"s"}`, mismatch, at("/b")}}},
	{"trailing-empty-fragment", `{"/schemas/T":{"$id":"https://ex.invalid/t#","type":"string"},
		"/operations/op/input":{"properties":{"a":{"$ref":"https://ex.invalid/t#"},"b":{"$ref":"https://ex.invalid/t"}}}}`, "", "",
		[]value{{`{"a":"s","b":"s"}`, valid, nil}, {`{"a":1}`, mismatch, at("/a")}, {`{"b":1}`, mismatch, at("/b")}}},
	{"resource-relative-root-id", `{"/operations/op/input":{"properties":{"a":{"$ref":"https://ex.invalid/dir/fetched"},"b":{"$ref":"https://ex.invalid/dir/declared#/$defs/n"}}}}`,
		`{"https://ex.invalid/dir/fetched":{"$id":"declared","type":"object","$defs":{"n":{"type":"integer"}}}}`, "",
		[]value{{`{"a":{},"b":1}`, valid, nil}, {`{"a":1}`, mismatch, at("/a")}, {`{"b":"s"}`, mismatch, at("/b")}}},
	{"resource-uri-names-another-id", `{"/schemas/S":{"$id":"https://ex.invalid/shared","type":"integer"},"/operations/op/input":{"$ref":"https://ex.invalid/shared"}}`,
		`{"https://ex.invalid/shared":{"type":"string"}}`, "a supplied resource's URI is another resource's $id (core's conservative policy)",
		[]value{{`1`, valid, nil}}},
	{"resource-uri-names-another-id-unreached", `{"/schemas/S":{"$id":"https://ex.invalid/shared","type":"integer"},"/operations/op/input":{"type":"boolean"}}`,
		`{"https://ex.invalid/shared":{"type":"string"}}`, "",
		[]value{{`true`, valid, nil}, {`1`, mismatch, at("")}}},

	// Numbers, counts, characters, paths.
	{"numbers-in-values", `{"/operations/op/input":{"properties":{"a":{"maximum":5},"c":{"type":"integer"}}}}`, "", "",
		[]value{{`{"a":1e2000000}`, mismatch, at("/a")}, {`{"c":1e-2000000}`, mismatch, at("/c")}, {`{"a":4}`, valid, nil}}},
	{"numbers-in-schema", `{"/operations/op/input":{"maxLength":100000000000000000000,"maximum":1e2000000}}`, "", "",
		[]value{{`"a"`, valid, nil}, {`5`, valid, nil}, {`true`, valid, nil}}},
	{"numbers-unreached", `{"/operations/op/input":{"type":"string","$defs":{"x":{"maximum":1e2000000,"maxItems":1e30}}}}`, "", "",
		[]value{{`"a"`, valid, nil}, {`5`, mismatch, at("")}}},
	{"count-spellings", `{"/operations/op/input":{"properties":{"z":{"maxLength":0e10001},"e":{"minLength":0e10001},"f":{"maxItems":10e-1},"g":{"minProperties":1.0},"p":{"minLength":1e10001}}}}`, "", "",
		[]value{{`{"z":"","e":"","f":[1],"g":{"a":1},"p":5}`, valid, nil}, {`{"z":"x"}`, mismatch, at("/z")}, {`{"f":[1,2]}`, mismatch, at("/f")}, {`{"g":{}}`, mismatch, at("/g")}, {`{"p":"abc"}`, mismatch, at("/p")}}},
	{"dot-and-space", `{"/operations/op/input":{"properties":{"t":{"pattern":"^\\S.{0,79}$"},"d":{"pattern":"^.$"},"s":{"pattern":"^\\s$"}}}}`, "", "",
		[]value{{`{"t":"a "}`, valid, nil}, {`{"t":" x"}`, mismatch, at("/t")}, {`{"d":" "}`, mismatch, at("/d")}, {`{"d":"é"}`, valid, nil}, {`{"s":" "}`, valid, nil}, {`{"s":" "}`, valid, nil}}},
	{"path-escaping", `{"/operations/op/input":{"properties":{"a/b":{"type":"string"},"m~n":{"required":["q"],"properties":{"q":{"type":"integer"}}}}}}`, "", "",
		[]value{{`{"a/b":1,"m~n":{}}`, mismatch, at("/a~1b", "/m~0n")}}},
	{"contains-false", `{"/operations/op/input":{"contains":false}}`, "", "", []value{{`[1]`, mismatch, at("")}}},
	{"property-names-false", `{"/operations/op/input":{"propertyNames":false}}`, "", "", []value{{`{"a":1}`, mismatch, at("")}}},
	{"property-names-false-twice", `{"/operations/op/input":{"propertyNames":false}}`, "", "", []value{{`{"a":1,"b":2}`, mismatch, at("")}}},
	{"type-and-const", `{"/operations/op/input":{"type":"string","const":1}}`, "", "", []value{{`true`, mismatch, at("", "")}}},
	{"missing-required", `{"/operations/op/input":{"required":["q"],"properties":{"q":{"type":"integer"}}}}`, "", "", []value{{`{}`, mismatch, at("")}}},
	{"three-at-one-location", `{"/operations/op/input":{"minimum":5,"multipleOf":2,"maximum":1}}`, "", "", []value{{`3`, mismatch, at("", "", "")}}},
	{"failing-any-of", `{"/operations/op/input":{"anyOf":[{"properties":{"a":{"type":"string"}}},{"required":["b"]}]}}`, "", "",
		[]value{{`{"a":1}`, mismatch, paths([]string{""}, []string{"", "/a"})}}},
	{"one-of-two-passing", `{"/operations/op/input":{"oneOf":[{"type":"object"},{"properties":{"a":{"type":"string"}}},{"required":["a"]}]}}`, "", "",
		[]value{{`{"a":1}`, mismatch, at("")}}},
	{"if-never-fails", `{"/operations/op/input":{"if":{"properties":{"a":{"const":1}}},"else":{"required":["c"]}}}`, "", "",
		[]value{{`{"a":2}`, mismatch, at("")}, {`{"a":1}`, valid, nil}}},

	// Unreached and unresolved.
	{"unreached-outside", `{"/operations/op/input":{"type":"string","$defs":{"x":{"$ref":"https://elsewhere.invalid/x"}}}}`, "", "",
		[]value{{`"s"`, valid, nil}, {`1`, mismatch, at("")}}},
	{"unresolved-in-resource", `{"/operations/op/input":{"$ref":"https://ex.invalid/r"}}`, `{"https://ex.invalid/r":{"type":"integer","$defs":{"x":{"$ref":"missing.json"}}}}`, "",
		[]value{{`1`, valid, nil}, {`"s"`, mismatch, at("")}}},
	{"unreached-uncopied-targets", `{"/schemas/Other":{"$id":"https://ex.invalid/other","type":"null"},"/operations/op/input":{"type":"string","$defs":{
		"id":{"$ref":"https://ex.invalid/other"},"resource":{"$ref":"https://ex.invalid/supplied"},
		"enum":{"$ref":"#/operations/op/input/$defs/values/enum"},"const":{"$ref":"#/operations/op/input/$defs/values/const"},
		"definitions":{"$ref":"#/operations/op/input/$defs/legacy/definitions"},
		"values":{"enum":[1],"const":{"a":1}},"legacy":{"definitions":{"x":{}}}}}}`, `{"https://ex.invalid/supplied":{"type":"null"}}`, "",
		[]value{{`"s"`, valid, nil}, {`1`, mismatch, at("")}}},
	{"unreached-lookahead", `{"/operations/op/input":{"type":"string","$defs":{"x":{"pattern":"^(?=a)","patternProperties":{"\\p{L}":true}}}}}`, "", "",
		[]value{{`"s"`, valid, nil}, {`1`, mismatch, at("")}}},

	// Undecidable only where reached; the path rules per keyword.
	{"lazy-lookahead", `{"/operations/op/input":{"anyOf":[{"type":"integer"},{"pattern":"^(?=a)"}]}}`, "", "",
		[]value{{`5`, valid, nil}, {`true`, valid, nil}, {`"abc"`, valid, nil}, {`null`, valid, nil}}},
	{"lazy-property-escape-not", `{"/operations/op/input":{"not":{"pattern":"^\\p{Lu}"}}}`, "", "", []value{{`5`, mismatch, at("")}, {`"Abc"`, mismatch, nil}}},
	{"lazy-pattern-properties", `{"/operations/op/input":{"patternProperties":{"^\\p{Lu}":{"type":"integer"}},"additionalProperties":false}}`, "", "",
		[]value{{`{}`, valid, nil}, {`5`, valid, nil}, {`{"A":1}`, valid, nil}}},
	{"phantom-reaches-sentinel", `{"/operations/op/input":{"required":["a"],"properties":{"a":{"const":1e2000000}}}}`, "", "", []value{{`{}`, mismatch, at("")}}},
	{"path-additional-false", `{"/operations/op/input":{"additionalProperties":false}}`, "", "", []value{{`{"x":1}`, mismatch, at("/x")}}},
	{"path-items-false", `{"/operations/op/input":{"items":false}}`, "", "", []value{{`[1]`, mismatch, at("/0")}}},
	{"path-prefix-items", `{"/operations/op/input":{"prefixItems":[{"type":"string"}]}}`, "", "", []value{{`[1]`, mismatch, at("/0")}}},
	{"path-unevaluated-properties", `{"/operations/op/input":{"unevaluatedProperties":false}}`, "", "", []value{{`{"x":1}`, mismatch, at("/x")}}},
	{"path-unevaluated-items", `{"/operations/op/input":{"unevaluatedItems":false}}`, "", "", []value{{`[1,2]`, mismatch, at("/0", "/1")}}},
	{"path-unevaluated-items-beside-all-of", `{"/operations/op/input":{"unevaluatedItems":false,"allOf":[{"minItems":5}]}}`, "", "", []value{{`[1]`, mismatch, at("", "/0")}}},
	{"path-pattern-properties", `{"/operations/op/input":{"patternProperties":{"^x":false}}}`, "", "", []value{{`{"x":1}`, mismatch, at("/x")}}},
	{"path-min-contains", `{"/operations/op/input":{"contains":{"type":"string"},"minContains":2}}`, "", "", []value{{`["a",1]`, mismatch, at("")}}},
	{"path-max-contains", `{"/operations/op/input":{"contains":{"type":"string"},"maxContains":1}}`, "", "", []value{{`["a","b"]`, mismatch, at("")}}},
	{"path-passing-any-of", `{"/operations/op/input":{"anyOf":[{"properties":{"a":{"type":"string"}}},true],"required":["b"]}}`, "", "", []value{{`{"a":1}`, mismatch, at("")}}},
	{"path-passing-not", `{"/operations/op/input":{"not":{"properties":{"a":{"type":"string"}}},"required":["b"]}}`, "", "", []value{{`{"a":1}`, mismatch, at("")}}},
	{"path-through-ref", `{"/operations/op/input":{"$defs":{"x":{"type":"string"}},"properties":{"a":{"$ref":"#/operations/op/input/$defs/x"}}}}`, "", "", []value{{`{"a":1}`, mismatch, at("/a")}}},
}

// adversarialGroups returns the adversaries as groups, each case's ID
// "adversarial/", the group's name, "/", and the value's index.
func adversarialGroups() []group {
	var out []group
	for _, a := range adversaries {
		g := group{id: "adversarial/" + a.name, tally: "adversarial/" + a.name, document: documentAt(a.positions), pinned: a.pinned}
		if a.resources != "" {
			var resources map[string]json.RawMessage
			if err := json.Unmarshal([]byte(a.resources), &resources); err != nil {
				panic(fmt.Sprintf("openbindingstest: %s: %v", a.name, err))
			}
			for _, uri := range sortedNames(resources) {
				g.resources = append(g.resources, openbindings.Resource{URI: uri, Document: resources[uri]})
			}
		}
		for i, v := range a.values {
			g.cases = append(g.cases, testCase{id: fmt.Sprintf("adversarial/%s/%d", a.name, i), value: v.text, want: v.want, paths: v.paths})
		}
		out = append(out, g)
	}
	return out
}

// documentAt writes an OBI document holding schemas at positions: operation
// "op"'s input, and entries of schemas.
func documentAt(positions string) string {
	var schemas map[string]json.RawMessage
	if err := json.Unmarshal([]byte(positions), &schemas); err != nil {
		panic(fmt.Sprintf("openbindingstest: %v", err))
	}
	var named []string
	for _, position := range sortedNames(schemas) {
		if name, ok := strings.CutPrefix(position, "/schemas/"); ok {
			key, _ := json.Marshal(name)
			named = append(named, string(key)+":"+string(schemas[position]))
		}
	}
	return `{"openbindings":"0.2.0","operations":{"op":{"input":` + string(schemas["/operations/op/input"]) + `}},"schemas":{` + strings.Join(named, ",") + `}}`
}

func sortedNames[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for name := range m {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
