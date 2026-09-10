package openbindings

import (
	"encoding/json"
	"testing"

	"github.com/openbindings/openbindings-go/jsonvalue"
)

func TestDocumentStringFidelity(t *testing.T) {
	raw := []byte(`{"openbindings":"0.2.0","operations":{"echo":{"input":{"type":"string","maxLength":1},"examples":{"unit":{"input":"\ud800","output":"\udc00"}}}},"transforms":{"literal":"\"\ud800\""},"x-values":{"\ud800":"\udc00","�":"�"}}`)
	var iface Interface
	if err := json.Unmarshal(raw, &iface); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(iface) // public standard encoder calls SDK-owned hooks
	if err != nil {
		t.Fatal(err)
	}
	var before, after any
	if err := jsonvalue.Unmarshal(raw, &before); err != nil {
		t.Fatal(err)
	}
	if err := jsonvalue.Unmarshal(encoded, &after); err != nil {
		t.Fatal(err)
	}
	if same, err := jsonvalue.Equal(before, after); err != nil || !same {
		t.Fatalf("document values changed: %s %v", encoded, err)
	}
	if _, err := ParseDocument(raw); err != nil {
		t.Fatalf("document parse: %v", err)
	}
}
