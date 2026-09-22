package jsonvalue_test

import (
	"bytes"
	"github.com/openbindings/openbindings-go/jsonvalue"
	"testing"
)

func TestExplicitBoundedExport(t *testing.T) {
	input := struct {
		Photo []byte `json:"photo"`
	}{[]byte{0, 1, 255}}
	got, err := jsonvalue.MarshalWithOptions(input, jsonvalue.MarshalOptions{})
	if err != nil || !bytes.Equal(got, []byte(`{"photo":"AAH/"}`)) {
		t.Fatalf("%s %v", got, err)
	}
	for _, opts := range []jsonvalue.MarshalOptions{{MaxValueUnits: 64}, {MaxDepth: 1}, {MaxValueUnits: -1}} {
		if _, err := jsonvalue.MarshalWithOptions(input, opts); err == nil {
			t.Fatal("missing export refusal")
		}
	}
	if _, err := jsonvalue.Marshal(input); err != nil {
		t.Fatal(err)
	}
}
