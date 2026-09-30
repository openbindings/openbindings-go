package openbindingstest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/openbindings/openbindings-go"
)

// invariants checks what no single case shows: concurrent use, isolation
// between bundles sharing identifiers, retained errors, cancellation, and a
// reference the evaluator's library cannot resolve.
func (k *kit) invariants() {
	k.concurrency()
	k.retention()
	k.cancellation()
	k.unresolvable()
}

// bundleOf writes the bundle core gives the evaluator for an adversary-style
// document.
func (k *kit) bundleOf(positions string, spelling int) json.RawMessage {
	compiler, err := openbindings.NewValueContractCompiler(k.e)
	if err != nil {
		panic(err)
	}
	var iface openbindings.Interface
	if err := json.Unmarshal([]byte(documentAt(positions)), &iface); err != nil {
		panic(err)
	}
	contracts, err := compiler.Resolve(context.Background(), &iface)
	if err != nil {
		panic(err)
	}
	return mustBundle(contracts, spelling)
}

// concurrency compiles bundles that share their root's $id and an inner $id
// with different content, on one evaluator at once, and validates each
// compiled schema from many goroutines: each gives its own answers. Run
// under -race.
func (k *kit) concurrency() {
	types := []string{"string", "integer"}
	values := []any{"s", json.Number("1")}
	var bundles [2]json.RawMessage
	for i := range bundles {
		bundles[i] = k.bundleOf(`{"/operations/op/input":{"$ref":"https://kit.invalid/shared"},"/schemas/S":{"$id":"https://kit.invalid/shared","type":"`+types[i]+`"}}`, 0)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			which := i % 2
			compiled, panicked, err := compileDirect(k.e, bundles[which])
			if panicked != "" || err != nil {
				mu.Lock()
				k.t.Errorf("invariant, concurrency: compiling: %v %s", err, panicked)
				mu.Unlock()
				return
			}
			for j := range 4 {
				_, answer := validateDirect(compiled, values[(which+j)%2])
				if got, want := classifyAnswer(answer), []outcome{valid, mismatch}[j%2]; got != want {
					mu.Lock()
					k.t.Errorf("invariant, concurrency and isolation: a bundle of type %s on %v: %v, want %v", types[which], values[(which+j)%2], got, want)
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
}

// retention checks that an error the evaluator returned is not changed by
// later calls.
func (k *kit) retention() {
	compiled, panicked, err := compileDirect(k.e, k.bundleOf(`{"/operations/op/input":{"properties":{"a":{"type":"string"},"b":{"minimum":3}}}}`, 0))
	if err != nil || panicked != "" {
		k.t.Errorf("invariant, retention: compiling: %v %s", err, panicked)
		return
	}
	_, first := validateDirect(compiled, decode(`{"a":1}`))
	kept := describe(first)
	for _, v := range []string{`{"a":2,"b":1}`, `{"b":0}`, `{"a":{"b":1}}`} {
		validateDirect(compiled, decode(v))
	}
	if now := describe(first); now != kept {
		k.t.Errorf("invariant, retention: a retained error changed: %s became %s", kept, now)
	}
}

func describe(err error) string {
	var held *openbindings.MismatchError
	if errors.As(err, &held) && held != nil {
		return fmt.Sprintf("%v %#v", err, held.Problems)
	}
	return fmt.Sprint(err)
}

// cancellation checks that, given a done ctx, Compile and Validate complete
// or return an error matching ctx.Err(), and that through core a done ctx
// gives no verdict.
func (k *kit) cancellation() {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	bundle := k.bundleOf(`{"/operations/op/input":{"type":"string"}}`, 0)
	compiled, panicked, err := compileWithin(ctx, k.e, bundle)
	switch {
	case panicked != "":
		k.t.Errorf("invariant, cancellation: Compile panicked given a done ctx: %s", panicked)
	case err != nil && !errors.Is(err, ctx.Err()):
		k.t.Errorf("invariant, cancellation: Compile given a done ctx returned an error not matching ctx.Err(): %v", err)
	}
	if compiled == nil {
		if compiled, panicked, err = compileDirect(k.e, bundle); err != nil || panicked != "" {
			k.t.Errorf("invariant, cancellation: compiling: %v %s", err, panicked)
			return
		}
	}
	switch panicked, err := validateWithin(ctx, compiled, "s"); {
	case panicked != "":
		k.t.Errorf("invariant, cancellation: Validate panicked given a done ctx: %s", panicked)
	case err != nil && !errors.Is(err, ctx.Err()):
		k.t.Errorf("invariant, cancellation: Validate given a done ctx returned an error not matching ctx.Err(): %v", err)
	}
	compiler, _ := openbindings.NewValueContractCompiler(k.e)
	var iface openbindings.Interface
	_ = json.Unmarshal([]byte(documentAt(`{"/operations/op/input":{"type":"string"}}`)), &iface)
	contracts, _ := compiler.Resolve(context.Background(), &iface)
	contract, _ := contracts.CompileInput(context.Background(), "op")
	if err := contract.Validate(ctx, "s"); !errors.Is(err, openbindings.ErrNoVerdict) || !errors.Is(err, context.Canceled) {
		t := k.t
		t.Errorf("invariant, cancellation: through core, a done ctx: %v", err)
	}
}

// unresolvable hands the evaluator bundles outside core's guarantees, each
// holding a reference to a URI nothing in it names: whether the library fails
// to compile or evaluates, no value whose evaluation reaches the reference
// may get a verdict, valid or mismatch, under not too (OBI-T-08).
func (k *kit) unresolvable() {
	// Each schema with values whose evaluation reaches the reference.
	for root, values := range map[string][]string{
		`{"$ref":"https://kit.invalid/missing"}`:                         {`{"a":1}`, `"s"`, `null`},
		`{"not":{"$ref":"https://kit.invalid/missing"}}`:                 {`{"a":1}`, `"s"`, `null`},
		`{"properties":{"a":{"$ref":"https://kit.invalid/missing#/x"}}}`: {`{"a":1}`, `{"a":"s"}`},
	} {
		var schema map[string]any
		_ = json.Unmarshal([]byte(root), &schema)
		schema["$schema"] = "https://json-schema.org/draft/2020-12/schema"
		schema["$id"] = "https://kit.invalid/root"
		bundle, _ := json.Marshal(schema)
		compiled, panicked, err := compileDirect(k.e, bundle)
		if panicked != "" {
			k.t.Errorf("invariant, unresolved references: Compile panicked on %s: %s", root, panicked)
			continue
		}
		k.checkAnswer("invariant, unresolved references (Compile)", err, true, context.Background())
		if compiled == nil {
			continue
		}
		for _, v := range values {
			panicked, answer := validateDirect(compiled, decode(v))
			if panicked != "" {
				k.t.Errorf("invariant, unresolved references: Validate panicked on %s: %s", root, panicked)
				continue
			}
			if got := classifyAnswer(answer); got != noVerdict {
				k.t.Errorf("invariant, unresolved references: %s on %s: %v, want no verdict", root, v, got)
			}
		}
	}
}
