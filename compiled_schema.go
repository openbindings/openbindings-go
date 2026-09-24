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
	// comparison states where the schema graph tells numbers apart by more
	// than type and equality, or is "".
	comparison string
}

// Validate validates a value: a JSON value as generic decoding produces it
// (nil, a bool, a string, a number, a []any, or a map[string]any). A nil
// error means the value validates, a *SchemaValidationError is an established
// mismatch, and a *SchemaGraphUnavailableError means no verdict could be
// reached. A value outside that domain returns another error: it has no JSON
// meaning to validate.
//
// A number beyond the numeric limits of schema evaluation is never handed to
// the schema library. Where the schema graph tells numbers apart only by type
// and equality, it is validated as a stand-in the graph cannot tell from it
// (schemacompiler.Substitute); where the graph compares numbers otherwise,
// such a value reaches no verdict.
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
