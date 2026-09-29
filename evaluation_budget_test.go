package openbindings

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// A small acyclic graph that applies a schema twice at each level would have
// the library apply it 2^n times: validation reaches no verdict, a resource
// limit met, and says so at once.
func TestValidate_EvaluationBudget(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"openbindings":"0.2.0","schemas":{"S0":true`)
	for i := 1; i <= 40; i++ {
		fmt.Fprintf(&b, `,"S%d":{"allOf":[{"$ref":"#/schemas/S%d"},{"$ref":"#/schemas/S%d"}]}`, i, i-1, i-1)
	}
	b.WriteString(`},"operations":{"op":{"input":{"$ref":"#/schemas/S40"}}}}`)
	start := time.Now()
	err := ValidateOperationInput(json.Number("0"), mustDecodeInterface(t, b.String()), "op")
	if outcome(err) != "unavailable" || !strings.Contains(err.Error(), "resource limit") {
		t.Fatalf("want the evaluation budget met, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("finding the budget met took %v", elapsed)
	}
}

// The budget counts the one place a $dynamicRef lands and the one branch of
// an if that applies, so recursive schemas and large values are evaluated.
func TestValidate_EvaluationBudgetAdmitsOrdinaryWork(t *testing.T) {
	tree := `{"openbindings":"0.2.0","schemas":{
		"Tree":{"$id":"https://e.example/tree","$dynamicAnchor":"node","type":"object","properties":{"data":true,"children":{"type":"array","items":{"$dynamicRef":"#node"}}}},
		"Strict":{"$id":"https://e.example/strict","$dynamicAnchor":"node","$ref":"tree","unevaluatedProperties":false}},
		"operations":{"op":{"input":{"$ref":"https://e.example/strict"}}}}`
	deep := func(leaf string) any {
		return decodeValue(t, []byte(strings.Repeat(`{"children":[`, 200)+leaf+strings.Repeat(`]}`, 200)))
	}
	if got := inputOutcome(t, tree, deep(`{}`)); got != "valid" {
		t.Errorf("a 200-level tree: %s", got)
	}
	if got := inputOutcome(t, tree, deep(`{"extra":1}`)); got != "mismatch" {
		t.Errorf("a 200-level tree with a member the strict extension refuses: %s", got)
	}
	records := `{"openbindings":"0.2.0","schemas":{"R":{"type":"object","required":["a"],"properties":{"a":{"type":"string"},
		"c":{"if":{"type":"string"},"then":{"minLength":1},"else":{"type":"number"}}}}},
		"operations":{"op":{"input":{"type":"array","items":{"$ref":"#/schemas/R"}}}}}`
	var items []any
	for range 50000 {
		items = append(items, map[string]any{"a": "x", "c": "y"})
	}
	if got := inputOutcome(t, records, items); got != "valid" {
		t.Errorf("50,000 records: %s", got)
	}
	var branches strings.Builder
	branches.WriteString(`{"openbindings":"0.2.0","schemas":{"B0":{"type":"integer"}`)
	for i := 1; i <= 40; i++ {
		fmt.Fprintf(&branches, `,"B%d":{"if":{"type":"integer"},"then":{"$ref":"#/schemas/B%d"},"else":{"$ref":"#/schemas/B%d"}}`, i, i-1, i-1)
	}
	branches.WriteString(`},"operations":{"op":{"input":{"$ref":"#/schemas/B40"}}}}`)
	if got := inputOutcome(t, branches.String(), json.Number("1")); got != "valid" {
		t.Errorf("40 levels of if, then, and else: %s", got)
	}
}

// Each of these grows the SDK's own work with the product or square of what
// the document holds unless handled with care. Memory allocated tracks the
// work, and 4 times the input must not allocate much more than 4 times as
// much.
func TestLinearWork(t *testing.T) {
	join := func(n int, format func(i int) string) string {
		var parts []string
		for i := range n {
			parts = append(parts, format(i))
		}
		return strings.Join(parts, ",")
	}
	compile := func(document string) func() {
		iface := mustDecodeInterface(t, document)
		return func() { CompileOperationSchema(iface, "op", "input") }
	}
	validate := func(document string) func() {
		return func() { ValidateDocument([]byte(document), ValidateOptions{}) }
	}
	for _, c := range []struct {
		name        string
		small, size int
		work        func(n int) func()
	}{
		{"dynamic references to many anchors of one name", 250, 1000, func(n int) func() {
			return compile(`{"openbindings":"0.2.0","schemas":{` + join(n, func(i int) string {
				return fmt.Sprintf(`"R%d":{"$id":"https://e.example/r%d","$dynamicAnchor":"n"}`, i, i)
			}) +
				`},"operations":{"op":{"input":{"$id":"https://e.example/in","allOf":[` + join(n, func(int) string { return `{"$dynamicRef":"#n"}` }) + `]}}}}`)
		}},
		{"references to a URI many schemas declare", 250, 1000, func(n int) func() {
			return compile(`{"openbindings":"0.2.0","schemas":{` + join(n, func(i int) string { return fmt.Sprintf(`"S%d":{"$id":"https://e.example/same"}`, i) }) +
				`},"operations":{"op":{"input":{"allOf":[` + join(n, func(int) string { return `{"$ref":"https://e.example/same"}` }) + `]}}}}`)
		}},
		{"an $id of many dot segments", 10000, 40000, func(n int) func() {
			id := "https://e.example/" + strings.Repeat("a/", n) + strings.Repeat("../", n)
			return validate(`{"openbindings":"0.2.0","schemas":{"R":{"$id":"https://e.example/r","$defs":{"x":{"$id":"` + id + `"}}}},"operations":{}}`)
		}},
		{"a reference at every level of a deep schema", 1000, 4000, func(n int) func() {
			return validate(`{"openbindings":"0.2.0","schemas":{"T":{},"D":` + strings.Repeat(`{"$ref":"#/schemas/T","not":`, n) + `{}` + strings.Repeat(`}`, n) + `},"operations":{}}`)
		}},
	} {
		if ratio := float64(allocated(c.work(c.size))) / float64(allocated(c.work(c.small))); ratio > 6 {
			t.Errorf("%s: 4 times the input allocated %.1f times the memory", c.name, ratio)
		}
	}
}
