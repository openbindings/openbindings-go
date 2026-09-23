package openbindings

import (
	"errors"
	"fmt"

	"github.com/openbindings/openbindings-go/internal/schemacompiler"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// CompiledSchema is a compiled operation schema, ready to validate values
// against without recompiling.
type CompiledSchema struct{ backend *jsonschema.Schema }

// Validate validates a value: a JSON value as generic decoding produces it
// (nil, a bool, a string, a number, a []any, or a map[string]any). A nil
// error means the value validates, a *SchemaValidationError is an established
// mismatch, and a *SchemaGraphUnavailableError means no verdict could be
// reached, as for a value holding a number beyond the numeric limits of
// schema evaluation. A value outside that domain returns another error: it
// has no JSON meaning to validate.
func (s *CompiledSchema) Validate(value any) error {
	if s == nil || s.backend == nil {
		return &SchemaGraphUnavailableError{Cause: errors.New("the schema is not compiled")}
	}
	if problem := schemacompiler.ValueProblem(value); problem != "" {
		return fmt.Errorf("openbindings: not a JSON value: %s", problem)
	}
	if err := schemacompiler.NumericLimit(value); err != nil {
		return &SchemaGraphUnavailableError{Cause: fmt.Errorf("the value holds %w", err)}
	}
	return schemaValidationError(s.backend.Validate(value))
}
