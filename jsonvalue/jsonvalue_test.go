package jsonvalue

import (
	"encoding/json"
	"testing"
)

func TestExactGenericNumbers(t *testing.T) {
	for _, token := range []string{"42", "9007199254740993", "1.234567890123456789", "1e400", "1e-400"} {
		var v any
		if err := Unmarshal([]byte(token), &v); err != nil {
			t.Fatal(err)
		}
		if v != json.Number(token) {
			t.Fatalf("lost %s: %#v", token, v)
		}
	}
	for _, token := range []string{"", "null", "1 2", " 1", "1 ", "NaN", "01", "true", "[]"} {
		if IsNumber(json.Number(token)) {
			t.Fatalf("accepted number %q", token)
		}
	}
	var v any
	if Unmarshal([]byte("1 2"), &v) == nil {
		t.Fatal("accepted trailing value")
	}
}
