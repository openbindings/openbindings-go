package openbindings

import (
	"errors"
	"fmt"

	"github.com/openbindings/openbindings-go/internal/schemacompiler"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// CompiledSchema is a compiled operation schema, ready to validate values
// against without recompiling.
type CompiledSchema struct {
	backend *jsonschema.Schema
	// comparison states where what the library compiles for the schema
	// compares a number by order or divisibility, holds one in const or
	// enum, or reaches a meta-schema, or is "".
	comparison string
}

// Validate validates a value: a JSON value, nil, a bool, a string, a number
// (a json.Number, a finite float, or a Go integer type), a []any, or a
// map[string]any. Decode a value with json.Decoder.UseNumber, so its numbers
// keep their exact value. A value outside that domain returns another error:
// it has no JSON meaning to validate.
//
// A nil error means the value validates, and a *SchemaValidationError is an
// established mismatch. A *SchemaGraphUnavailableError means no verdict was
// reached: the schema graph could not be evaluated, the value holds a number
// beyond the numeric limits of schema evaluation where what the schema
// library compiles compares numbers, or the schema was not compiled (a nil or
// zero CompiledSchema).
//
// A number beyond the numeric limits of schema evaluation is never handed to
// the schema library. Where what the library compiles for the schema
// compares no number by order or divisibility, holds none in const or enum,
// and reaches no meta-schema, the number is validated as a stand-in the
// schema cannot tell from it; otherwise such a value reaches no verdict.
func (s *CompiledSchema) Validate(value any) error {
	if s == nil || s.backend == nil {
		return &SchemaGraphUnavailableError{Cause: errors.New("the schema is not compiled")}
	}
	if problem := schemacompiler.ValueProblem(value); problem != "" {
		return fmt.Errorf("openbindings: not a JSON value: %s", problem)
	}
	if s.comparison == "" {
		checked := schemacompiler.Substitute(value)
		return schemaValidationError(s.backend.Validate(checked.Value), checked)
	}
	if at, err := schemacompiler.NumericLimit(value); err != nil {
		return &SchemaGraphUnavailableError{Cause: fmt.Errorf("the value holds, at %q, %w, and %s", at, err, s.comparison)}
	}
	return schemaValidationError(s.backend.Validate(value), schemacompiler.Substitution{})
}
