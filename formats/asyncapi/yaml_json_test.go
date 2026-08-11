package asyncapi

import "testing"

func TestDecodeYAMLAllowsTabAsFirstBlockScalarContentCharacter(t *testing.T) {
	document, err := decodeYAMLObject([]byte("description: >\n  \tInformation about the last close.\nnext: value\n"))
	if err != nil {
		t.Fatalf("decodeYAMLObject: %v", err)
	}
	if got, want := document["description"], "\tInformation about the last close.\n"; got != want {
		t.Fatalf("description = %#v, want %#v", got, want)
	}
}

func TestDecodeYAMLStillRejectsTabIndentation(t *testing.T) {
	_, err := decodeYAMLObject([]byte("root:\n\tchild: value\n"))
	if err == nil {
		t.Fatal("decodeYAMLObject accepted a tab used as indentation")
	}
}

func TestDraft07PlainNameIDGrammar(t *testing.T) {
	for _, valid := range []string{"#A", "#MassCancelRequest", "#a-b_c:d.e9"} {
		if !isDraft07PlainNameID(valid) {
			t.Errorf("isDraft07PlainNameID(%q) = false", valid)
		}
	}
	for _, invalid := range []string{"#", "#9startsWithDigit", "#/pointer", "other.json#name", "#space here"} {
		if isDraft07PlainNameID(invalid) {
			t.Errorf("isDraft07PlainNameID(%q) = true", invalid)
		}
	}
}
