// Package schemavalidate validates JSON values against standalone JSON
// Schema 2020-12 schemas: schemas that are their own resolution root, such as
// a protocol's own schema for a value it carries.
//
// It is not OpenBindings contract validation. A schema at a position of an
// OBI resolves against the whole document (core §7) and is validated with
// openbindings.ValidateOperationInput and ValidateOperationOutput under
// OBI-T-16. A schema validated here is governed by whatever specification
// carries it, and the dialect it declares.
package schemavalidate

import (
	"errors"
	"fmt"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/internal/schemacompiler"
	"github.com/openbindings/openbindings-go/internal/thirdparty/jsonschema"
)

// Validate validates a value against a standalone schema, in object or
// boolean form. `#` references resolve within the schema itself, and every
// reference outside it is unavailable: no resource is fetched or read.
//
// A nil error means the value validates. An *openbindings.SchemaValidationError
// is an established mismatch; an *openbindings.SchemaGraphUnavailableError
// means the schema could not be compiled or evaluated completely, so no
// verdict was reached.
func Validate(value any, schema any) error {
	switch schema.(type) {
	case map[string]any, bool:
	default:
		return &openbindings.SchemaGraphUnavailableError{Cause: fmt.Errorf("a schema is a JSON Schema object or boolean")}
	}
	c := schemacompiler.New()
	const url = "urn:openbindings:standalone-schema"
	if err := c.AddResource(url, schema); err != nil {
		return &openbindings.SchemaGraphUnavailableError{Cause: err}
	}
	compiled, err := c.Compile(url)
	if err != nil {
		return &openbindings.SchemaGraphUnavailableError{Cause: err}
	}
	verr := compiled.Validate(value)
	if verr == nil {
		return nil
	}
	var mismatch *jsonschema.ValidationError
	if !errors.As(verr, &mismatch) {
		return &openbindings.SchemaGraphUnavailableError{Cause: verr}
	}
	problems := schemacompiler.Problems(mismatch)
	lines := make([]string, len(problems))
	for i, problem := range problems {
		lines[i] = problem.Line()
	}
	return &openbindings.SchemaValidationError{Problems: lines, Cause: verr}
}
