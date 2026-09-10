package openbindings

import (
	"fmt"

	"github.com/openbindings/openbindings-go/internal/thirdparty/jsonschema"
)

// CompiledSchema names the SDK's compiled schema artifact without requiring
// consumers to import or mutate a private backend graph. Call Validate for an
// instance verdict: nil means valid, SchemaValidationError means invalid, and
// any other error means a verdict could not be reached.
type CompiledSchema struct{ backend *jsonschema.Schema }

func (s *CompiledSchema) Validate(value any) error {
	if s == nil || s.backend == nil {
		return fmt.Errorf("openbindings: compiled schema is unavailable")
	}
	return projectSchemaValidationError(s.backend.Validate(value))
}
