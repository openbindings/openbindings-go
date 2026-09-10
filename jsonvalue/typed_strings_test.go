package jsonvalue

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/openbindings/openbindings-go/internal/jstring"
)

type namedString string
type typedStrings struct {
	Direct  string            `json:"direct"`
	Named   namedString       `json:"named"`
	Generic any               `json:"generic"`
	Pointer *string           `json:"pointer"`
	Array   [1]string         `json:"array"`
	Slice   []string          `json:"slice"`
	Object  map[string]string `json:"object"`
	Raw     json.RawMessage   `json:"raw"`
	Number  json.Number       `json:"number"`
}

func TestTypedStringCodeUnitCarriage(t *testing.T) {
	for unit := 0xd800; unit <= 0xdfff; unit++ {
		s := fmt.Sprintf(`"\u%04x"`, unit)
		raw := []byte(fmt.Sprintf(`{"direct":%s,"named":%s,"generic":%s,"pointer":%s,"array":[%s],"slice":[%s],"object":{%s:%s},"raw":%s,"number":9007199254740993}`, s, s, s, s, s, s, s, s, s))
		var value typedStrings
		if err := Unmarshal(raw, &value); err != nil {
			t.Fatal(err)
		}
		want := jstring.CodeUnit(uint16(unit))
		if value.Direct != want || string(value.Named) != want || value.Generic != want || *value.Pointer != want || value.Array[0] != want || value.Slice[0] != want || value.Object[want] != want || value.Number != json.Number("9007199254740993") {
			t.Fatalf("typed decode %x: %#v", unit, value)
		}
		encoded, err := Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var before, after any
		if err := Unmarshal(raw, &before); err != nil {
			t.Fatal(err)
		}
		if err := Unmarshal(encoded, &after); err != nil {
			t.Fatal(err)
		}
		if same, err := Equal(before, after); err != nil || !same {
			t.Fatalf("typed encode %x: %s %v", unit, encoded, err)
		}
	}
}
