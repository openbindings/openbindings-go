package jsonvalue

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/openbindings/openbindings-go/internal/jstring"
)

func TestStringValueCarriage(t *testing.T) {
	for unit := 0xd800; unit <= 0xdfff; unit++ {
		want := jstring.CodeUnit(uint16(unit))
		raw := []byte(fmt.Sprintf(`{"\u%04x":["\u%04x",9007199254740993]}`, unit, unit))
		var value any
		if err := Unmarshal(raw, &value); err != nil {
			t.Fatal(err)
		}
		object := value.(map[string]any)
		array, found := object[want].([]any)
		if !found || array[0] != want || array[1] != json.Number("9007199254740993") {
			t.Fatalf("decode %x: %#v", unit, value)
		}
		encoded, err := Marshal(value)
		if err != nil || !json.Valid(encoded) {
			t.Fatalf("encode %x: %s %v", unit, encoded, err)
		}
		var reloaded any
		if err := Unmarshal(encoded, &reloaded); err != nil {
			t.Fatal(err)
		}
		if same, err := Equal(value, reloaded); err != nil || !same {
			t.Fatalf("equality %x: %v %v", unit, same, err)
		}
		if same, err := Equal(want, "�"); err != nil || same {
			t.Fatalf("replacement collision %x: %v %v", unit, same, err)
		}
	}
	var value any
	if err := Unmarshal([]byte(`{"\ud800":1,"\udc00":2,"�":3}`), &value); err != nil {
		t.Fatal(err)
	}
	if len(value.(map[string]any)) != 3 {
		t.Fatalf("member collision: %#v", value)
	}
	set, err := NewValueSet([]any{jstring.CodeUnit(0xd800), jstring.CodeUnit(0xdc00), "�"})
	if err != nil || set.Len() != 3 {
		t.Fatalf("set collision: %v %v", set, err)
	}
}

func TestStringBoundaryControls(t *testing.T) {
	for _, raw := range []string{`{"a":1,"\u0061":2}`, `"\ud83d\ude00"`, `"<&>\n\u2028"`, `"a😀é"`, `null`} {
		var got, want any
		if err := Unmarshal([]byte(raw), &got); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(raw), &want); err != nil {
			t.Fatal(err)
		}
		b, err := Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		standard, err := json.Marshal(want)
		if err != nil || string(b) != string(standard) {
			t.Fatalf("ordinary behavior %s: %s %s %v", raw, b, standard, err)
		}
	}
	for _, raw := range []string{`"\ud800" 1`, `["\ud800",]`, `{"\ud800":}`, `"\ud80z"`} {
		var value any
		if err := Unmarshal([]byte(raw), &value); err == nil {
			t.Errorf("accepted invalid JSON %s", raw)
		}
	}
	cycle := map[string]any{}
	cycle["self"] = cycle
	if _, err := Marshal(cycle); err == nil {
		t.Fatal("accepted cyclic value")
	}
	badNative := string([]byte{0xff})
	got, _ := Marshal(badNative)
	want, _ := json.Marshal(badNative)
	if string(got) != string(want) {
		t.Fatalf("changed arbitrary invalid-byte policy: %s %s", got, want)
	}
}
