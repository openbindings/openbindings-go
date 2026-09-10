package jstring_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/openbindings/openbindings-go/internal/jstring"
)

func TestJSONCodeUnitRoundTrip(t *testing.T) {
	for unit := 0; unit <= 0xffff; unit++ {
		raw := []byte(fmt.Sprintf(`"\u%04x"`, unit))
		value, err := jstring.Unquote(raw)
		if err != nil || value != jstring.CodeUnit(uint16(unit)) {
			t.Fatalf("decode %x: %x %v", unit, value, err)
		}
		encoded, err := jstring.Quote(value)
		if err != nil || !json.Valid(encoded) {
			t.Fatalf("encode %x: %q %v", unit, encoded, err)
		}
		reloaded, err := jstring.Unquote(encoded)
		if err != nil || reloaded != value {
			t.Fatalf("reload %x: %x %v", unit, reloaded, err)
		}
	}
	for _, raw := range []string{`"a\\b\"c\nd"`, `"\ud83d\ude00"`, `"a<>&😀é"`} {
		var want string
		if err := json.Unmarshal([]byte(raw), &want); err != nil {
			t.Fatal(err)
		}
		got, err := jstring.Unquote([]byte(raw))
		if err != nil || got != want {
			t.Fatalf("ordinary Unicode: %q %q %v", want, got, err)
		}
	}
}
