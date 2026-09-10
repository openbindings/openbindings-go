package jstring_test

import (
	"testing"

	"github.com/openbindings/openbindings-go/internal/jstring"
)

func TestCanonicalCodeUnits(t *testing.T) {
	for unit := uint16(0xd800); unit <= 0xdfff; unit++ {
		if !jstring.Valid(jstring.CodeUnit(unit)) {
			t.Fatalf("invalid encoded unit %x", unit)
		}
	}
	hi, lo := jstring.CodeUnit(0xd83d), jstring.CodeUnit(0xde00)
	if got := jstring.Concat("a"+hi, lo+"b"); got != "a😀b" {
		t.Fatalf("joined halves: %x", got)
	}
	for _, s := range []string{"", "�", "😀", hi, lo, lo + hi} {
		if !jstring.Valid(s) {
			t.Errorf("valid string rejected: %x", s)
		}
	}
	for _, s := range []string{hi + lo, "\xff", "\xed\xa0", "\xc0\x80"} {
		if jstring.Valid(s) {
			t.Errorf("malformed/noncanonical string accepted: %x", s)
		}
	}
}
