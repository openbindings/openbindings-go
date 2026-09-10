package jsonata_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	gnata "github.com/openbindings/jsonata-runtime/go"
	"github.com/openbindings/openbindings-go/invoke"
	jsonataevaluator "github.com/openbindings/openbindings-go/invoke/jsonata"
	"github.com/openbindings/openbindings-go/jsonvalue"
)

func evaluator(t *testing.T, options jsonataevaluator.Options) *jsonataevaluator.Evaluator {
	t.Helper()
	e, err := jsonataevaluator.New(options)
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func jsonInput(t *testing.T, raw string) any {
	t.Helper()
	var v any
	if err := jsonvalue.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatal(err)
	}
	return v
}
func equal(a, b any) bool { ok, err := jsonvalue.Equal(a, b); return err == nil && ok }
func TestOfficialJSONBoundary(t *testing.T) {
	e := evaluator(t, jsonataevaluator.Options{})
	for _, raw := range []string{`null`, `false`, `0`, `""`, `[]`, `{}`, `9223372036854775807`, `1e400`, `1e-400`, `0.12345678901234567890123456789`, `{"\ud800":"\udc00","items":[null,false,[],{}],"__proto__":{"constructor":7}}`} {
		input := jsonInput(t, raw)
		for _, expr := range []string{`$`, `($f := function($v){$v}; $f($))`, `$eval("$")`} {
			got, err := e.Evaluate(context.Background(), expr, input)
			if err != nil {
				t.Fatalf("%s / %s: %v", expr, raw, err)
			}
			if !equal(input, got) {
				t.Fatalf("%s changed %s to %#v", expr, raw, got)
			}
			encoded, err := jsonvalue.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			if !equal(jsonInput(t, string(encoded)), input) {
				t.Fatal("result did not reload exactly")
			}
		}
	}
	for _, expr := range []string{`function(){1}`, `{"fn":$string}`, `[/a/]`, `$match("b", /(a)?b/).groups`, `$ ~> |$|{"self":$}|`, `$flatten([])`, `$values({})`, `$eval("$flatten([])")`, `2 ** 3`} {
		if got, err := e.Evaluate(context.Background(), expr, map[string]any{}); err == nil {
			t.Fatalf("non-JSON/extra language escaped %s: %#v", expr, got)
		}
	}
	if _, err := e.Evaluate(context.Background(), "missing", nil); !errors.Is(err, invoke.ErrTransformUndefined) {
		t.Fatalf("undefined: %v", err)
	}
	got, err := e.EvaluateWithBindings(context.Background(), `[$exists($present),$type($present),$wide]`, nil, map[string]any{"present": nil, "wide": json.Number("9007199254740993")})
	if err != nil || !equal(got, jsonInput(t, `[true,"null",9007199254740993]`)) {
		t.Fatalf("bindings: %#v %v", got, err)
	}
	if _, err := e.EvaluateWithBindings(context.Background(), "$evil()", nil, map[string]any{"evil": func() {}}); err == nil {
		t.Fatal("host function admitted")
	}
}

func TestIndependentBudgetsAndCancellation(t *testing.T) {
	smallLimits := gnata.NumericWorkLimits{MaxDigits: 8, MaxExponent: 8}
	small := evaluator(t, jsonataevaluator.Options{NumericWork: &smallLimits, Timeout: 100 * time.Millisecond, MaxCompiledExpressions: 2})
	smallLimits.MaxDigits = 100 // constructor snapshots options
	large := evaluator(t, jsonataevaluator.Options{Timeout: time.Second})
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, e := range []*jsonataevaluator.Evaluator{small, large} {
				got, err := e.Evaluate(context.Background(), `0.1+0.2`, nil)
				if err != nil || !equal(got, json.Number("0.3")) {
					t.Errorf("budget affected value: %#v %v", got, err)
				}
			}
		}()
	}
	wg.Wait()
	if _, err := small.Evaluate(context.Background(), `$power(2,100)`, nil); err == nil {
		t.Fatal("small arithmetic budget ignored")
	}
	if got, err := large.Evaluate(context.Background(), `$power(2,100)`, nil); err != nil || !equal(got, json.Number("1267650600228229401496703205376")) {
		t.Fatalf("large budget: %#v %v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := large.Evaluate(ctx, "$", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancellation: %v", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 3*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := large.Evaluate(ctx, `($f := function($n){$f($n+1)}; $f(0))`, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cooperative termination: %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("cancellation was abandoned, not stopped")
	}
	if got, err := large.Evaluate(context.Background(), `1+1`, nil); err != nil || !equal(got, 2) {
		t.Fatalf("recovery: %#v %v", got, err)
	}
}

func TestDocumentedLibraryExcludesCompatibilityClone(t *testing.T) {
	e := evaluator(t, jsonataevaluator.Options{})
	for _, expression := range []string{`$clone({})`, `$eval("$clone({})")`} {
		if value, err := e.Evaluate(context.Background(), expression, map[string]any{}); err == nil {
			t.Fatalf("nonstandard compatibility helper executed: %#v", value)
		}
	}
	input := jsonInput(t, `{"id":9007199254740993}`)
	for _, expression := range []string{`$ ~> |$|{"flag":true}|`, `($clone:=5;$ ~> |$|{"flag":true}|)`, `$eval("$ ~> |$|{\"flag\":true}|")`} {
		value, err := e.Evaluate(context.Background(), expression, input)
		if err != nil || !equal(value, jsonInput(t, `{"id":9007199254740993,"flag":true}`)) {
			t.Fatalf("internal operator copy: %#v / %v", value, err)
		}
	}
	value, err := e.Evaluate(context.Background(), `($clone:=function($v){$v};$eval("$clone(7)"))`, nil)
	if err != nil || !equal(value, float64(7)) {
		t.Fatalf("declared lambda: %#v / %v", value, err)
	}
}

func TestBoundaryBudgetRejection(t *testing.T) {
	for _, c := range []struct {
		options jsonataevaluator.Options
		expr    string
		input   any
		want    string
	}{
		{jsonataevaluator.Options{MaxExpressionBytes: 2}, "1+1", nil, "expression"},
		{jsonataevaluator.Options{MaxInputBytes: 8}, "$", strings.Repeat("a", 16), "input"},
		{jsonataevaluator.Options{MaxOutputBytes: 8}, `'123456789'`, nil, "output"},
	} {
		e := evaluator(t, c.options)
		if _, err := e.Evaluate(context.Background(), c.expr, c.input); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: %v", c.want, err)
		}
	}
	cached := evaluator(t, jsonataevaluator.Options{MaxCompiledExpressions: 2})
	for i := 0; i < 100; i++ {
		if _, err := cached.Evaluate(context.Background(), fmt.Sprintf("%d", i), nil); err != nil {
			t.Fatal(err)
		}
	}
	// Revisit an evicted expression with fresh data; no earlier input survives.
	for i := 0; i < 5; i++ {
		if got, err := cached.Evaluate(context.Background(), "id", map[string]any{"id": i}); err != nil || !equal(got, i) {
			t.Fatalf("cached evaluator retained input: %#v %v", got, err)
		}
	}
}
