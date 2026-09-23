package openbindings

import (
	"errors"

	"github.com/openbindings/openbindings-go/internal/thirdparty/jsonschema"
)

// CompiledSchema is a compiled operation schema, ready to validate values
// against without recompiling.
type CompiledSchema struct{ backend *jsonschema.Schema }

// Validate validates a value. A nil error means the value validates, a
// *SchemaValidationError is an established mismatch, and a
// *SchemaGraphUnavailableError means no verdict could be reached.
func (s *CompiledSchema) Validate(value any) error {
	if s == nil || s.backend == nil {
		return &SchemaGraphUnavailableError{Cause: errors.New("the schema is not compiled")}
	}
	return schemaValidationError(s.backend.Validate(value))
}
