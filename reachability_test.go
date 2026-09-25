package openbindings

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// The reachability acceptance suite: every case in which core's view of an
// operation's statically reachable schema graph (§5.2, OBI-T-08, OBI-D-10)
// has disagreed with what the schema library evaluates, as reviews found
// them. Each case states the outcome the bundle design gives. Where that is
// no verdict for a part of a resource the graph does not reach, it is because
// the schema library compiles a whole resource when any part is used, and is
// given the resource as the document holds it.

type reachabilityCase struct {
	name     string
	document string
	// values maps each input value, as JSON, to the outcome validating it
	// against the operation "op"'s input must give.
	values map[string]string
	// d11 is the OBI-D-10 evidence ValidateDocument must report, when the
	// document carries examples.
	d11 RuleEvidenceStatus
}

func (c reachabilityCase) run(t *testing.T) {
	t.Helper()
	iface := mustDecodeInterface(t, c.document)
	for value, want := range c.values {
		decoder := json.NewDecoder(strings.NewReader(value))
		decoder.UseNumber()
		var decoded any
		if err := decoder.Decode(&decoded); err != nil {
			t.Fatal(err)
		}
		if got := outcome(ValidateOperationInput(decoded, iface, "op")); got != want {
			t.Errorf("%s: validating %s gave %s, want %s", c.name, value, got, want)
		}
	}
	if c.d11 != "" {
		if report := validateBytes(t, c.document); report.Evidence["OBI-D-10"] != c.d11 {
			t.Errorf("%s: OBI-D-10 %q, want %q", c.name, report.Evidence["OBI-D-10"], c.d11)
		}
	}
}

func TestReachability_Acceptance(t *testing.T) {
	for _, c := range []reachabilityCase{{
		// $recursiveRef is not a 2020-12 keyword, so the schema it names is
		// not reached, and its number beyond the limits blocks nothing.
		name:     "a schema reached through $recursiveRef",
		document: `{"openbindings":"0.2.0","schemas":{"S":{"minimum":1e99999999999999999999}},"operations":{"op":{"input":{"$recursiveRef":"#/schemas/S"}}}}`,
		values:   map[string]string{`1`: "valid"},
	}, {
		// A $dynamicRef resolves at run time to a $dynamicAnchor in the outer
		// resource, which holds a pattern Go's regexp cannot compile.
		name: "a dynamic anchor in an outer resource",
		document: `{"openbindings":"0.2.0","schemas":{
			"Outer":{"$id":"https://example.com/outer","$ref":"https://example.com/inner","$defs":{"hidden":{"$dynamicAnchor":"node","type":"string","pattern":"^(?=a)"}}},
			"Inner":{"$id":"https://example.com/inner","$dynamicAnchor":"node","type":["object","string"],"properties":{"child":{"$dynamicRef":"#node"}}}},
			"operations":{"op":{"input":{"$ref":"https://example.com/outer"},"examples":{"e":{"input":{"child":"abc"}}}}}}`,
		values: map[string]string{`{"child":"abc"}`: "unavailable"},
		d11:    EvidenceInconclusive,
	}, {
		// The operation reaches one property of R; another property of R
		// references an external URL the graph never reaches.
		name: "an external reference the graph does not reach",
		document: `{"openbindings":"0.2.0","schemas":{"R":{"$id":"https://example.com/r","properties":{"a":{"type":"integer"},"b":{"$ref":"https://external.example/x"}}}},
			"operations":{"op":{"input":{"$ref":"#/schemas/R/properties/a"},"examples":{"bad":{"input":"not an integer"}}}}}`,
		values: map[string]string{`"not an integer"`: "unavailable", `3`: "unavailable"},
		d11:    EvidenceInconclusive,
	}, {
		// The same with the external reference carrying a fragment.
		name: "an unreached external reference with a fragment",
		document: `{"openbindings":"0.2.0","schemas":{"R":{"$id":"https://example.com/r","properties":{"a":{"type":"integer"},"b":{"$ref":"https://external.example/x#/$defs/y"},"c":{"$ref":"https://external.example/z#w"}}}},
			"operations":{"op":{"input":{"$ref":"#/schemas/R/properties/a"},"examples":{"bad":{"input":"x"}}}}}`,
		values: map[string]string{`"x"`: "unavailable"},
		d11:    EvidenceInconclusive,
	}, {
		// A reached external reference still puts the examples outside the
		// rule and leaves validation without a verdict.
		name:     "a reached external reference",
		document: `{"openbindings":"0.2.0","operations":{"op":{"input":{"properties":{"a":{"$ref":"https://external.example/x#/$defs/y"}}},"examples":{"e":{"input":{"a":1}}}}}}`,
		values:   map[string]string{`{"a":1}`: "unavailable"},
		d11:      EvidenceSatisfied,
	}, {
		// A root member named like a keyword of an older draft is never a
		// schema, and never declares a resource.
		name: "a root member named additionalItems",
		document: `{"openbindings":"0.2.0","additionalItems":{"$id":"https://example.com/a"},
			"schemas":{"A":{"$id":"https://example.com/a","type":"string"}},
			"operations":{"op":{"input":{"$ref":"https://example.com/a"},"examples":{"e":{"input":5}}}}}`,
		values: map[string]string{`5`: "mismatch", `"s"`: "valid"},
		d11:    EvidenceViolated,
	}, {
		// A schema reached outside the OBI schema positions that declares an
		// $id gets no verdict: only schema positions declare resources (§7).
		name: "an $id schema outside the OBI schema positions",
		document: `{"openbindings":"0.2.0","x-shared":{"A":{"$id":"https://example.com/a","$defs":{"b":{"type":"string"}},"$ref":"#/$defs/b"}},
			"operations":{"op":{"input":{"$ref":"#/x-shared/A"},"examples":{"e":{"input":42}}}}}`,
		values: map[string]string{`42`: "unavailable", `"hello"`: "unavailable"},
		d11:    EvidenceInconclusive,
	}, {
		// dependencies is not a 2020-12 keyword, so there is no cycle.
		name:     "a non-advancing cycle through dependencies",
		document: `{"openbindings":"0.2.0","schemas":{"A":{"anyOf":[{"dependencies":{"x":{"$ref":"#/schemas/A"}}},{"type":"object"}]}},"operations":{"op":{"input":{"$ref":"#/schemas/A"}}}}`,
		values:   map[string]string{`{"x":1}`: "valid"},
	}, {
		// §5.2 counts then without if, and contentSchema, as reachable, so a
		// pattern there that Go's regexp cannot compile leaves the graph
		// unevaluable.
		name:     "an uncompilable pattern under then without if",
		document: `{"openbindings":"0.2.0","operations":{"op":{"input":{"type":"string","then":{"pattern":"(?=a)"}}}}}`,
		values:   map[string]string{`"s"`: "unavailable"},
	}, {
		name:     "an uncompilable pattern under contentSchema",
		document: `{"openbindings":"0.2.0","operations":{"op":{"input":{"type":"string","contentMediaType":"application/json","contentSchema":{"pattern":"(?=a)"}}}}}`,
		values:   map[string]string{`"s"`: "unavailable"},
	}, {
		// RFC 3986 resolves other against urn:x:y to urn:other; the schema
		// library resolves it to urn:x:y. Where they part, no verdict.
		name: "a relative reference against an opaque base",
		document: `{"openbindings":"0.2.0","schemas":{
			"R":{"$id":"urn:x:y","$defs":{"d":{"type":"string"}},"properties":{"p":{"$ref":"other#/$defs/d"}}},
			"O":{"$id":"urn:other","$defs":{"d":{"type":"integer"}}}},
			"operations":{"op":{"input":{"$ref":"urn:x:y"},"examples":{"e":{"input":{"p":5}}}}}}`,
		values: map[string]string{`{"p":5}`: "unavailable", `{"p":"s"}`: "unavailable"},
		d11:    EvidenceInconclusive,
	}, {
		// A resource the graph enters is given to the library whole, so a
		// number beyond the limits anywhere in it meets a resource limit.
		name: "unreached data beyond the limits",
		document: `{"openbindings":"0.2.0","schemas":{"R":{"$id":"https://example.com/r","properties":{"a":{"type":"integer"},"b":{"maximum":1e99999}}}},
			"operations":{"op":{"input":{"$ref":"#/schemas/R/properties/a"},"examples":{"e":{"input":"x"}}}}}`,
		values: map[string]string{`"x"`: "unavailable"},
		d11:    EvidenceInconclusive,
	}} {
		t.Run(c.name, c.run)
	}
}

// A reference into a root member resolves, whatever the member is named,
// since the document root is never handed to the schema library.
func TestReachability_ReferencesIntoRootMembers(t *testing.T) {
	document := `{"openbindings":"0.2.0","dependencies":{"d":{"operation":"op","type":"number"}},"operations":{"op":{"input":{"$ref":"#/dependencies/d"}}}}`
	if got := outcome(ValidateOperationInput("text", mustDecodeInterface(t, document), "op")); got != "unavailable" {
		t.Fatalf("got %s", got)
	}
	if report := validateBytes(t, document); report.Evidence["OBI-D-12"] != EvidenceViolated {
		t.Fatalf("OBI-D-12 %q: a dependency entry is not a schema position", report.Evidence["OBI-D-12"])
	}
}

// Compiling one operation costs time in proportion to its own graph, not to
// the number of schemas the document embeds.
func TestReachability_CompileCostFollowsTheGraph(t *testing.T) {
	build := func(n int) *Interface {
		var schemas []string
		for i := range n {
			schemas = append(schemas, fmt.Sprintf(`"s%d":{"$id":"https://ex.test/s%d","type":"string"}`, i, i))
		}
		return mustDecodeInterface(t, `{"openbindings":"0.2.0","schemas":{`+strings.Join(schemas, ",")+`},"operations":{"op":{"input":{"type":"string"}}}}`)
	}
	elapsed := func(iface *Interface) time.Duration {
		start := time.Now()
		if _, err := CompileOperationSchema(iface, "op", "input"); err != nil {
			t.Fatal(err)
		}
		return time.Since(start)
	}
	small, large := elapsed(build(500)), elapsed(build(2000))
	if large > 8*small+50*time.Millisecond {
		t.Fatalf("4 times the unrelated schemas took %v against %v", large, small)
	}
}

// The work is linear in the document however deeply it nests: a deep and wide
// schema is refused at the depth limit before the walk goes deeper.
func TestReachability_WorkIsLinear(t *testing.T) {
	deepWide := func(depth, width int) string {
		var properties []string
		for i := range width {
			properties = append(properties, fmt.Sprintf(`"p%d":{"type":"string"}`, i))
		}
		leaf := `{"properties":{` + strings.Join(properties, ",") + `}}`
		return `{"openbindings":"0.2.0","operations":{"op":{"input":` + strings.Repeat(`{"not":`, depth) + leaf + strings.Repeat(`}`, depth) + `}}}`
	}
	compile := func(document string) uint64 {
		iface := mustDecodeInterface(t, document)
		return allocated(func() {
			if _, err := CompileOperationSchema(iface, "op", "input"); outcome(err) != "unavailable" {
				t.Errorf("want the depth limit met, got %v", err)
			}
		})
	}
	if ratio := float64(compile(deepWide(4000, 20000))) / float64(compile(deepWide(1000, 5000))); ratio > 6 {
		t.Errorf("a deep and wide schema 4 times the size allocated %.1f times the memory", ratio)
	}

}

// The cases a review of the design asked for before trusting it.
func TestReachability_DesignReview(t *testing.T) {
	nested := func(depth int, leaf string) string {
		return strings.Repeat(`{"not":`, depth) + leaf + strings.Repeat(`}`, depth)
	}
	for _, c := range []reachabilityCase{{
		name:     "else without if",
		document: `{"openbindings":"0.2.0","operations":{"op":{"input":{"type":"string","else":{"pattern":"(?=a)"}}}}}`,
		values:   map[string]string{`"s"`: "unavailable"},
	}, {
		name:     "the branch a boolean if rules out",
		document: `{"openbindings":"0.2.0","operations":{"op":{"input":{"if":true,"then":{"type":"string"},"else":{"$ref":"https://external.example/x"}}}}}`,
		values:   map[string]string{`"s"`: "unavailable"},
	}, {
		name:     "a graph nesting 255 subschemas",
		document: `{"openbindings":"0.2.0","operations":{"op":{"input":` + nested(255, `{"type":"string"}`) + `}}}`,
		values:   map[string]string{`"s"`: "mismatch", `5`: "valid"},
	}, {
		name:     "a graph nesting 257 subschemas",
		document: `{"openbindings":"0.2.0","operations":{"op":{"input":` + nested(257, `{}`) + `}}}`,
		values:   map[string]string{`"s"`: "unavailable"},
	}, {
		// The depth limit cuts away a schema declaring the $id a reference
		// names: the graph meets a limit, never reaches outside.
		name: "a cut hiding an absolute $id",
		document: `{"openbindings":"0.2.0","schemas":{"Deep":` + nested(300, `{"$id":"https://example.com/hidden","type":"string"}`) + `},
			"operations":{"op":{"input":{"$ref":"https://example.com/hidden"},"examples":{"e":{"input":5}}}}}`,
		values: map[string]string{`5`: "unavailable"},
		d11:    EvidenceInconclusive,
	}, {
		name: "an unreferenced deep definition",
		document: `{"openbindings":"0.2.0","operations":{"op":{"input":{"type":"string","$defs":{"deep":` + nested(300, `{}`) + `}},
			"examples":{"e":{"input":5}}}}}`,
		values: map[string]string{`5`: "unavailable", `"s"`: "unavailable"},
		d11:    EvidenceInconclusive,
	}, {
		name:     "an oversized number in a reached enum",
		document: `{"openbindings":"0.2.0","operations":{"op":{"input":{"enum":[1,1e99999]}}}}`,
		values:   map[string]string{`1`: "unavailable"},
	}, {
		name:     "an oversized number in a reached default, which evaluation never reads",
		document: `{"openbindings":"0.2.0","operations":{"op":{"input":{"type":"string","default":1e99999}}}}`,
		values:   map[string]string{`"s"`: "valid", `5`: "mismatch"},
	}, {
		// $recursiveAnchor true is not well-formed 2020-12, though 2020-12
		// does not evaluate it: no verdict.
		name: "a recursive reference retargeting",
		document: `{"openbindings":"0.2.0","schemas":{
			"Tree":{"$id":"https://ex.test/tree","$recursiveAnchor":true,"type":"object","properties":{"child":{"$recursiveRef":"#"}}},
			"Other":{"$id":"https://ex.test/other","$recursiveAnchor":true,"$ref":"https://outside.example/x"}},
			"operations":{"op":{"input":{"$ref":"https://ex.test/tree"}}}}`,
		values: map[string]string{`{}`: "unavailable"},
	}, {
		// An unreached outside reference in a resource the graph enters: no
		// verdict, and the examples are not decided to be outside the rule.
		name: "an outside fragment no placeholder can hold",
		document: `{"openbindings":"0.2.0","schemas":{"R":{"$id":"https://example.com/r","properties":{"a":{"type":"integer"},"b":{"$ref":"https://external.example/x#/minimum"}}}},
			"operations":{"op":{"input":{"$ref":"#/schemas/R/properties/a"},"examples":{"e":{"input":"x"}}}}}`,
		values: map[string]string{`"x"`: "unavailable"},
		d11:    EvidenceInconclusive,
	}, {
		name: "additionalItems at the root holding a nested $id",
		document: `{"openbindings":"0.2.0","additionalItems":{"items":{"$id":"https://example.com/a"}},
			"schemas":{"A":{"$id":"https://example.com/a","type":"string"}},
			"operations":{"op":{"input":{"$ref":"https://example.com/a"}}}}`,
		values: map[string]string{`5`: "mismatch"},
	}} {
		t.Run(c.name, c.run)
	}
}
