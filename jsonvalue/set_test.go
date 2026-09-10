package jsonvalue

import (
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"math/rand"
	"testing"
)

func TestNativeScalarDetachmentMatchesStandardJSONValue(t *testing.T) {
	rng := rand.New(rand.NewSource(0x58a29417))
	for i := 0; i < 10000; i++ {
		for _, native := range []any{math.Float64frombits(rng.Uint64()), math.Float32frombits(rng.Uint32())} {
			standard, stdErr := json.Marshal(native)
			got, err := detached(native)
			if stdErr != nil {
				if err == nil {
					t.Fatal("accepted nonfinite value", native)
				}
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			a, aok := new(big.Rat).SetString(string(standard))
			b, bok := new(big.Rat).SetString(string(got.(json.Number)))
			if !aok || !bok || a.Cmp(b) != 0 {
				t.Fatal("native numeric value changed", native, string(standard), got)
			}
		}
	}
}

func TestValueSetExactOwnershipAndIndex(t *testing.T) {
	var v any
	if err := Unmarshal([]byte(`{"__proto__":{"n":9007199254740993}}`), &v); err != nil {
		t.Fatal(err)
	}
	s, err := NewValueSet([]any{json.Number("0.10"), json.Number("1e-1"), v, "0.1", nil})
	if err != nil || s.Len() != 4 {
		t.Fatal(err, s)
	}
	for _, token := range []string{"0.1", "0.100", "1e-1"} {
		if ok, err := s.Contains(json.Number(token)); err != nil || !ok {
			t.Fatal(token, err)
		}
	}
	if ok, err := s.Contains(json.Number("0.10000000000000001")); err != nil || ok {
		t.Fatal(ok, err)
	}
	values := s.Values()
	values[1].(map[string]any)["__proto__"] = false
	v.(map[string]any)["__proto__"] = false
	var original any
	_ = Unmarshal([]byte(`{"__proto__":{"n":9007199254740993}}`), &original)
	if ok, err := s.Contains(original); err != nil || !ok {
		t.Fatal("set leaked ownership", err)
	}
	if s.Values()[0] != json.Number("0.10") {
		t.Fatal("first-authored token changed")
	}
	var capability *CapabilityError
	if _, err := s.Contains(json.Number("1e10001")); !errors.As(err, &capability) {
		t.Fatal("missing capability refusal", err)
	}
}

func TestValueSetIndexCollisionCannotEstablishMembership(t *testing.T) {
	s, _ := NewValueSet(nil)
	s.key = func(any) (string, error) { return "collision", nil }
	_, _ = s.Add(json.Number("9007199254740993"))
	_, _ = s.Add("9007199254740993")
	if ok, err := s.Contains(json.Number("9007199254740992")); err != nil || ok {
		t.Fatal(ok, err)
	}
	if ok, err := s.Contains(json.Number("9007199254740993.0")); err != nil || !ok {
		t.Fatal(ok, err)
	}
}

func TestValueSetZeroValue(t *testing.T) {
	var set ValueSet
	if ok, err := set.Contains(1); err != nil || ok {
		t.Fatal(ok, err)
	}
	if added, err := set.Add(1); err != nil || !added {
		t.Fatal(added, err)
	}
	if ok, err := set.Contains(json.Number("1.0")); err != nil || !ok {
		t.Fatal(ok, err)
	}
}

func TestScalarDetachmentRetainsStandardStringEncoding(t *testing.T) {
	for _, raw := range []string{"\xff", "\xff\xfe", "a\xc0\xafz"} {
		encoded, _ := json.Marshal(raw)
		var want string
		_ = json.Unmarshal(encoded, &want)
		if same, err := Equal(raw, want); err != nil || !same {
			t.Fatal("native encoding changed", err)
		}
		set, err := NewValueSet([]any{raw})
		if err != nil {
			t.Fatal(err)
		}
		if same, err := set.Contains(want); err != nil || !same {
			t.Fatal("set encoding changed", err)
		}
	}
}
