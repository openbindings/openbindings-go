package schemaeval

import (
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
