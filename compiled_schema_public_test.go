package openbindings_test

import (
	"encoding/json"
	"errors"
	"testing"

	ob "github.com/openbindings/openbindings-go"
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
	// A number beyond the numeric limits reaches no verdict: it is neither a
	// mismatch nor a success.
	err = validator.Validate(json.Number("1e10001"))
	if !errors.As(err, new(*ob.SchemaGraphUnavailableError)) || errors.As(err, &mismatch) {
		t.Fatalf("a limit met is no verdict, not a mismatch: %v", err)
	}
	if err = validator.Validate(1); err != nil {
		t.Fatal(err)
	}
}
