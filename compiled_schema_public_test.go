package openbindings_test

import (
	"encoding/json"
	"errors"
	"testing"

	ob "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/jsonvalue"
)

func TestCompiledSchemaPublicErrorDomain(t *testing.T) {
	iface := &ob.Interface{OpenBindings: "0.2.0", Operations: map[string]ob.Operation{"test": {Input: map[string]any{"minimum": 0}}}}
	validator, err := ob.CompileOperationSchema(iface, "test", "input")
	if err != nil {
		t.Fatal(err)
	}
	var mismatch *ob.SchemaValidationError
	if err = validator.Validate(-1); !errors.As(err, &mismatch) {
		t.Fatalf("missing public mismatch type: %v", err)
	}
	var capability *jsonvalue.CapabilityError
	err = validator.Validate(json.Number("1e10001"))
	if !errors.As(err, &capability) || errors.As(err, &mismatch) {
		t.Fatalf("capability is not a mismatch: %v", err)
	}
	if err = validator.Validate(1); err != nil {
		t.Fatal(err)
	}
}
