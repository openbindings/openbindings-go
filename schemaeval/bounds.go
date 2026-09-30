package schemaeval

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
)

// bound is a comparison keyword whose number is beyond the library's
// numeric limits, decided here exactly instead, as a format: it compares
// decimal numbers by their digits and powers of ten, never building a value
// of the size its exponent names. A bound with a type is instead a lower
// count bound past math.MaxInt, which no instance of that type meets.
type bound struct {
	keyword, limit, typ string
}

func (b bound) validate(v any) error {
	if b.typ != "" {
		if jsonType(v) == b.typ {
			return fmt.Errorf("%s: want at least %s, more than any %s holds", b.keyword, b.limit, b.typ)
		}
		return nil
	}
	n, isNumber := v.(json.Number)
	if !isNumber {
		return nil
	}
	order := compareNumbers(string(n), b.limit)
	holds := true
	switch b.keyword {
	case "minimum":
		holds = order >= 0
	case "maximum":
		holds = order <= 0
	case "exclusiveMinimum":
		holds = order > 0
	case "exclusiveMaximum":
		holds = order < 0
	case "multipleOf":
		holds = multipleOf(string(n), b.limit)
	}
	if !holds {
		return fmt.Errorf("%s: got %s, want %s", b.keyword, n, b.limit)
	}
	return nil
}

// decimal is a number's value as significant digits (no leading or trailing
// zeros; "" for zero) times ten to a power.
type decimal struct {
	negative    bool
	significant string
	power       *big.Int
}

func decimalOf(token string) decimal {
	key, _ := numberKey(token)
	if key == "0" {
		return decimal{power: new(big.Int)}
	}
	d := decimal{negative: strings.HasPrefix(key, "-")}
	digits, power, _ := strings.Cut(strings.TrimPrefix(key, "-"), "e")
	d.significant = digits
	d.power, _ = new(big.Int).SetString(power, 10)
	return d
}

// compareNumbers compares two JSON numbers exactly, returning -1, 0, or +1.
func compareNumbers(a, b string) int {
	x, y := decimalOf(a), decimalOf(b)
	sign := func(d decimal) int {
		switch {
		case d.significant == "":
			return 0
		case d.negative:
			return -1
		}
		return 1
	}
	if sx, sy := sign(x), sign(y); sx != sy || sx == 0 {
		return compareInts(sx, sy)
	}
	magnitude := compareMagnitudes(x, y)
	if x.negative {
		return -magnitude
	}
	return magnitude
}

// compareMagnitudes compares two nonzero numbers' absolute values: first by
// where their leading digits lie, then digit by digit.
func compareMagnitudes(x, y decimal) int {
	lead := func(d decimal) *big.Int { return new(big.Int).Add(d.power, big.NewInt(int64(len(d.significant)))) }
	if order := lead(x).Cmp(lead(y)); order != 0 {
		return order
	}
	width := max(len(x.significant), len(y.significant))
	return strings.Compare(x.significant+strings.Repeat("0", width-len(x.significant)), y.significant+strings.Repeat("0", width-len(y.significant)))
}

// multipleOf reports whether a number is an integer multiple of a positive
// one.
func multipleOf(value, divisor string) bool {
	v, h := decimalOf(value), decimalOf(divisor)
	if v.significant == "" {
		return true
	}
	sv, _ := new(big.Int).SetString(v.significant, 10)
	sh, _ := new(big.Int).SetString(h.significant, 10)
	shift := new(big.Int).Sub(v.power, h.power)
	if shift.Sign() >= 0 {
		// sv × 10^shift divisible by sh.
		residue := new(big.Int).Exp(big.NewInt(10), shift, sh)
		residue.Mul(residue, sv).Mod(residue, sh)
		return residue.Sign() == 0
	}
	// sv divisible by sh × 10^-shift, which exceeds sv when -shift has more
	// digits than sv.
	if new(big.Int).Neg(shift).Cmp(big.NewInt(int64(len(v.significant)))) > 0 {
		return false
	}
	scaled := new(big.Int).Mul(sh, new(big.Int).Exp(big.NewInt(10), new(big.Int).Neg(shift), nil))
	return new(big.Int).Mod(sv, scaled).Sign() == 0
}

func compareInts(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
