package canonicaljson

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestMarshal_DeterministicAcrossKeyOrder(t *testing.T) {
	inA := []byte(`{
  "b": 1,
  "a": {"y":2,"x":1},
  "arr": [{"b":2,"a":1}]
}`)
	inB := []byte(`{
  "arr": [{"a":1,"b":2}],
  "a": {"x":1,"y":2},
  "b": 1
}`)

	ca, err := Marshal(json.RawMessage(inA))
	if err != nil {
		t.Fatalf("canonical a: %v", err)
	}
	cb, err := Marshal(json.RawMessage(inB))
	if err != nil {
		t.Fatalf("canonical b: %v", err)
	}
	if !bytes.Equal(ca, cb) {
		t.Fatalf("expected identical canonical JSON\nA: %s\nB: %s", string(ca), string(cb))
	}
}

func TestMarshal_ControlCharShorthandEscapes(t *testing.T) {
	// RFC 8785 §3.2.2.2: \b \t \n \f \r MUST use shorthand, others use \u00XX.
	input := map[string]string{
		"bs":  "\b",
		"tab": "\t",
		"nl":  "\n",
		"ff":  "\f",
		"cr":  "\r",
		"nul": "\x00",
		"esc": "\x1b",
	}
	out, err := Marshal(input)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(out)

	// Shorthand escapes (NOT \u00XX).
	for _, tc := range []struct {
		key, want, notWant string
	}{
		{"bs", `\b`, `\u0008`},
		{"tab", `\t`, `\u0009`},
		{"nl", `\n`, `\u000a`},
		{"ff", `\f`, `\u000c`},
		{"cr", `\r`, `\u000d`},
	} {
		if !bytes.Contains(out, []byte(`"`+tc.key+`":"`+tc.want+`"`)) {
			t.Errorf("%s: expected shorthand %s in output, got %s", tc.key, tc.want, s)
		}
		if bytes.Contains(out, []byte(tc.notWant)) {
			t.Errorf("%s: should NOT contain %s, got %s", tc.key, tc.notWant, s)
		}
	}

	// Non-shorthand control chars use \u00XX.
	if !bytes.Contains(out, []byte(`\u0000`)) {
		t.Errorf("nul: expected \\u0000 in output, got %s", s)
	}
	if !bytes.Contains(out, []byte(`\u001b`)) {
		t.Errorf("esc: expected \\u001b in output, got %s", s)
	}
}

func TestMarshal_NumberExponentPaddingNormalized(t *testing.T) {
	// Go's 'e' formatting pads exponent; JCS requires no exponent padding.
	out, err := Marshal(json.RawMessage(`{"n":1e-6,"m":1e-7}`))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// For 1e-6, canonical form is decimal; for 1e-7, exponent form.
	if !bytes.Contains(out, []byte(`"n":0.000001`)) {
		t.Fatalf("expected decimal for 1e-6, got %s", string(out))
	}
	if !bytes.Contains(out, []byte(`"m":1e-7`)) {
		t.Fatalf("expected exponent without padding for 1e-7, got %s", string(out))
	}
}
