package schemaeval

import (
	"math"
	"math/big"
	"testing"
)

// Exact comparison agrees with math/big wherever math/big can hold the
// numbers.
func TestCompareNumbers(t *testing.T) {
	numbers := []string{"0", "-0", "0.0", "1", "-1", "1e3", "1000", "999.999", "1.5e-3", "-2e5", "12345678901234567890", "1E+2", "100.0", "-0.001", "1e-2", "5e-1"}
	for _, a := range numbers {
		for _, b := range numbers {
			x, _ := new(big.Rat).SetString(a)
			y, _ := new(big.Rat).SetString(b)
			if got, want := compareNumbers(a, b), x.Cmp(y); got != want {
				t.Errorf("compare %s %s: %d, want %d", a, b, got, want)
			}
			if y.Sign() > 0 {
				quotient := new(big.Rat).Quo(x, y)
				if got, want := multipleOf(a, b), quotient.IsInt(); got != want {
					t.Errorf("%s multiple of %s: %v, want %v", a, b, got, want)
				}
			}
		}
	}
	if compareNumbers("5", "1e2000000") >= 0 || compareNumbers("-5", "-1e2000000") <= 0 || compareNumbers("1e-2000000", "0") <= 0 {
		t.Error("comparison with huge exponents")
	}
	if multipleOf("5", "1e2000000") || !multipleOf("0", "1e2000000") || !multipleOf("0.25", "1e-2000") {
		t.Error("multipleOf with huge exponents")
	}
}

// A count is read exactly by its digits and power of ten, whatever its
// spelling, and never overflows on a power near the int64 limit.
func TestReadCount(t *testing.T) {
	for _, c := range []struct {
		token      string
		value      int
		past, isOk bool
	}{
		{"0e10001", 0, false, true},
		{"10e-1", 1, false, true},
		{"1.0", 1, false, true},
		{"1e18", 1000000000000000000, false, true},
		{"9223372036854775807", math.MaxInt, false, true},
		{"9223372036854775808", 0, true, true},
		{"1e19", 0, true, true},
		{"1e9223372036854775807", 0, true, true},
		{"11e9223372036854775806", 0, true, true},
		{"100e9223372036854775805", 0, true, true},
		{"1e99999999999999999999", 0, true, true},
		{"-1", 0, false, false},
		{"0.5", 0, false, false},
	} {
		value, past, ok := readCount(c.token)
		if value != c.value || past != c.past || ok != c.isOk {
			t.Errorf("readCount(%s) = %d, %v, %v; want %d, %v, %v", c.token, value, past, ok, c.value, c.past, c.isOk)
		}
	}
}
