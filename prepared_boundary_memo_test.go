package openbindings

import (
	"encoding/json"
	"reflect"
	"sync"
	"testing"
)

func TestPreparedBoundaryMemoOwnershipBoundsAndRefusal(t *testing.T) {
	makeContract := func(token string) PreparedBoundaryContract {
		p, err := PrepareInterface(&Interface{OpenBindings: "0.2.0", Operations: map[string]Operation{"x": {Input: map[string]any{"const": json.Number(token)}}}})
		if err != nil {
			t.Fatal(err)
		}
		c, ok, err := p.BoundaryContract("x")
		if err != nil || !ok {
			t.Fatal(err)
		}
		return c
	}
	left, right := makeContract("9007199254740993"), makeContract("9007199254740993.0")
	for i := 0; i < 3; i++ {
		if got, err := CompareBoundaryContracts(left, right); err != nil || got != "equal" {
			t.Fatal(got, err)
		}
	}
	if len(left.memo.results) != 1 {
		t.Fatal("immutable pair was not memoized")
	}
	// Keys must have no reference back to a foreign memo. A bounded local
	// map otherwise retains an unbounded transitive chain of pair caches.
	for token := range left.memo.results {
		if reflect.TypeOf(*token).NumField() != 1 || reflect.TypeOf(*token).Field(0).Type.Kind() != reflect.Uint8 {
			t.Fatal("counterpart token contains retaining state")
		}
	}
	different := makeContract("9007199254740992")
	if got, err := CompareBoundaryContracts(left, different); err != nil || got != "different" {
		t.Fatal(got, err)
	}
	for i := 0; i < 130; i++ {
		_, _ = CompareBoundaryContracts(left, makeContract("1"))
	}
	if len(left.memo.results) > 64 {
		t.Fatal("unbounded retained counterparts")
	}
	var wait sync.WaitGroup
	for i := 0; i < 20; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if got, err := CompareBoundaryContracts(left, right); err != nil || got != "equal" {
				t.Errorf("%s %v", got, err)
			}
		}()
	}
	wait.Wait()
	huge := makeContract("1e10001")
	for i := 0; i < 2; i++ {
		if _, err := CompareBoundaryContracts(huge, huge); err == nil {
			t.Fatal("capability refusal became equality")
		}
	}
	if len(huge.memo.results) != 0 {
		t.Fatal("failure was memoized as a verdict")
	}
}
