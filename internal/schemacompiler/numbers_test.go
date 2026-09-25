package schemacompiler

import (
	"encoding/json"
	"math/big"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
)

// Two numbers share a key exactly when math/big finds them equal, and a key
// says a number is an integer exactly when it is one.
func TestNumberKey_AgreesWithExactArithmetic(t *testing.T) {
	random := rand.New(rand.NewPCG(1, 2))
	digits := func(n int) string {
		var b strings.Builder
		for range n {
			b.WriteByte(byte('0' + random.IntN(10)))
		}
		return b.String()
	}
	token := func() string {
		s := digits(1 + random.IntN(4))
		if random.IntN(2) == 0 {
			s += "." + digits(1+random.IntN(4))
		}
		if random.IntN(2) == 0 {
			s += []string{"e", "E", "e+", "e-", "E-"}[random.IntN(5)] + digits(1+random.IntN(2))
		}
		if random.IntN(3) == 0 {
			s = "-" + s
		}
		return s
	}
	var tokens []string
	for range 3000 {
		tokens = append(tokens, token())
	}
	rat := func(s string) *big.Rat {
		r, ok := new(big.Rat).SetString(s)
		if !ok {
			t.Fatalf("math/big cannot read %q", s)
		}
		return r
	}
	for i, a := range tokens {
		keyA, integer := numberKey(a)
		if integer != rat(a).IsInt() {
			t.Fatalf("%s: integer %v, math/big says %v", a, integer, rat(a).IsInt())
		}
		for _, b := range tokens[i+1 : min(len(tokens), i+40)] {
			keyB, _ := numberKey(b)
			if (keyA == keyB) != (rat(a).Cmp(rat(b)) == 0) {
				t.Fatalf("%s (%s) and %s (%s): keys equal %v, values equal %v", a, keyA, b, keyB, keyA == keyB, rat(a).Cmp(rat(b)) == 0)
			}
		}
	}
}

// Exponents too long for any machine integer are added to exactly, carrying
// across their last digits.
func TestNumberKey_ExponentsOfAnyLength(t *testing.T) {
	huge := "1" + strings.Repeat("0", 30)
	equal := [][2]string{
		{"1e" + huge, "10e" + decrement(huge)},
		{"0.1e" + huge, "1e" + decrement(huge)},
		{"100e-" + huge, "1e-" + decrement(decrement(huge))},
		{"1e999999999999999999", "10e999999999999999998"},
		{"0.01e1000000000000000000", "1e999999999999999998"},
		{"123000e" + huge, "123e" + increment(increment(increment(huge)))},
		{"0e" + huge, "-0.0"},
	}
	for _, pair := range equal {
		a, _ := numberKey(pair[0])
		b, _ := numberKey(pair[1])
		if a != b {
			t.Errorf("%s and %s are equal; keys %s and %s", pair[0], pair[1], a, b)
		}
	}
	unequal := [][2]string{
		{"1e" + huge, "1e" + decrement(huge)},
		{"1e" + huge, "-1e" + huge},
		{"1e-" + huge, "1e" + huge},
	}
	for _, pair := range unequal {
		a, _ := numberKey(pair[0])
		b, _ := numberKey(pair[1])
		if a == b {
			t.Errorf("%s and %s differ; both keyed %s", pair[0], pair[1], a)
		}
	}
	if _, integer := numberKey("1.5e" + huge); !integer {
		t.Error("1.5e" + huge + " is an integer")
	}
	if _, integer := numberKey("15e-" + huge); integer {
		t.Error("15e-" + huge + " is not an integer")
	}
}

func increment(digits string) string { return carry(digits, true) }
func decrement(digits string) string { return strings.TrimLeft(carry(digits, false), "0") }

// A stand-in keeps what a schema telling numbers apart only by type, by
// equality, and by comparison with zero can observe.
func TestSubstitute(t *testing.T) {
	value := decode(t, `{"same":[1e99999, 10e99998, 5, 5.000`+strings.Repeat("0", 5000)+`, 0e99999],
		"signs":[-1e99999, 1e-99999, -1.5e-99999, 1.5e99999], "within": [1, 2.5, -1]}`)
	before := mustEncode(t, value)
	got := Substitute(value)
	if _, err := NumericLimit(got.Value); err != nil {
		t.Fatalf("the Value holds a number beyond the limits: %v", err)
	}
	same := got.Value.(map[string]any)["same"].([]any)
	if same[0] != same[1] || same[3] != json.Number("5") || same[4] != json.Number("0") {
		t.Errorf("equal numbers were not given equal stand-ins: %v", same)
	}
	numbers := map[string]bool{}
	forEachNumber(value.(map[string]any)["within"], func(n json.Number) { numbers[string(n)] = true })
	for i, n := range got.Value.(map[string]any)["signs"].([]any) {
		standIn := rat(t, n.(json.Number))
		original := rat(t, value.(map[string]any)["signs"].([]any)[i].(json.Number))
		if standIn.Sign() != original.Sign() || standIn.IsInt() != original.IsInt() || numbers[string(n.(json.Number))] {
			t.Errorf("%v stands for %v", n, original)
		}
	}
	if mustEncode(t, value) != before {
		t.Error("Substitute changed its argument")
	}
	within := decode(t, `[1, 2.5]`)
	if got := Substitute(within); !reflect.DeepEqual(got.Value, within) {
		t.Errorf("a value within the limits was changed: %v", got.Value)
	}
}

// A finding the schema reached on a stand-in states the number it stands for.
func TestSubstitution_OutcomeStatesTheNumber(t *testing.T) {
	c := New()
	if err := c.AddResource("urn:test:minimum", map[string]any{"items": map[string]any{"minimum": json.Number("0")}}); err != nil {
		t.Fatal(err)
	}
	schema, err := c.Compile("urn:test:minimum")
	if err != nil {
		t.Fatal(err)
	}
	checked := Substitute(decode(t, `[-1e99999, -2]`))
	problems, mismatch := checked.Outcome(schema.Validate(checked.Value))
	want := []Problem{
		{Location: []string{"0"}, Message: "minimum: got -1e99999, want 0"},
		{Location: []string{"1"}, Message: "minimum: got -2, want 0"},
	}
	if !mismatch || !reflect.DeepEqual(problems, want) {
		t.Errorf("got %v %v, want %v", problems, mismatch, want)
	}
}

func decode(t *testing.T, data string) any {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(data))
	decoder.UseNumber()
	var v any
	if err := decoder.Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func mustEncode(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func rat(t *testing.T, n json.Number) *big.Rat {
	t.Helper()
	r, ok := new(big.Rat).SetString(string(n))
	if !ok {
		t.Fatalf("math/big cannot read %q", n)
	}
	return r
}
