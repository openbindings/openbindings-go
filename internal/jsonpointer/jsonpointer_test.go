package jsonpointer

import (
	"slices"
	"testing"
)

func TestFormatAndParseRoundTrip(t *testing.T) {
	for pointer, tokens := range map[string][]string{
		"":         nil,
		"/":        {""},
		"/a/b":     {"a", "b"},
		"/a~1b/~0": {"a/b", "~"},
		"/~01":     {"~1"},
		"/0/-":     {"0", "-"},
	} {
		if got := Format(tokens...); got != pointer {
			t.Errorf("Format(%q) = %q, want %q", tokens, got, pointer)
		}
		if got, ok := Parse(pointer); !ok || !slices.Equal(got, tokens) {
			t.Errorf("Parse(%q) = %q, %v; want %q", pointer, got, ok, tokens)
		}
	}
}

func TestParseRefusesWhatIsNoPointer(t *testing.T) {
	for _, pointer := range []string{"a", "a/b", "/~", "/~2", "/a~"} {
		if tokens, ok := Parse(pointer); ok {
			t.Errorf("Parse(%q) = %q, want no pointer", pointer, tokens)
		}
	}
}

func TestResolve(t *testing.T) {
	document := map[string]any{"a": []any{"x", map[string]any{"b/c": 1}}, "": "empty name"}
	for pointer, want := range map[string]any{
		"/a/0":      "x",
		"/a/1/b~1c": 1,
		"/":         "empty name",
	} {
		if got, ok := Resolve(document, pointer); !ok || got != want {
			t.Errorf("Resolve(%q) = %v, %v; want %v", pointer, got, ok, want)
		}
	}
	if got, ok := Resolve(document, ""); !ok || got == nil {
		t.Errorf("the empty pointer addresses the whole value, got %v, %v", got, ok)
	}
	// An array index is "0" or digits without a leading zero, within bounds;
	// "-" names no element (RFC 6901 §4).
	for _, pointer := range []string{"/a/01", "/a/-", "/a/2", "/a/-1", "/a/1x", "/a/", "/missing", "/a/0/deeper", "no-slash"} {
		if got, ok := Resolve(document, pointer); ok {
			t.Errorf("Resolve(%q) = %v, want no location", pointer, got)
		}
	}
}
