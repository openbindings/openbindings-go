package jsonvalue

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestExactNumericEqualityAndOrder(t *testing.T) {
	for _, c := range []struct {
		a, b  string
		order int
	}{
		{"9007199254740993", "9007199254740992", 1}, {"1.0", "1e0", 0},
		{"0", "0.1", -1}, {"-0", "0.0", 0}, {"1e400", "9e399", 1}, {"1e-400", "0", 1},
	} {
		got, err := CompareNumbers(json.Number(c.a), json.Number(c.b))
		if err != nil || got != c.order {
			t.Fatalf("%+v: %v %v", c, got, err)
		}
		same, err := Equal(map[string]any{"n": json.Number(c.a)}, map[string]any{"n": json.Number(c.b)})
		if err != nil || same != (c.order == 0) {
			t.Fatalf("equality %+v: %v %v", c, same, err)
		}
	}
	_, err := CompareNumbers(json.Number("1e10001"), 0)
	var capability *CapabilityError
	if !errors.As(err, &capability) {
		t.Fatalf("capability: %v", err)
	}
	if _, err := Equal(json.Number(""), 0); err == nil {
		t.Fatal("invalid number accepted")
	}
	if _, err := Equal(map[string]any{"n": json.Number("")}, map[string]any{"n": 0}); err == nil {
		t.Fatal("nested invalid number accepted")
	}
}
