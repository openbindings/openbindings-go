package openbindings

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/openbindings/openbindings-go/internal/jsonpointer"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// testEvaluator is the evaluator core's own tests validate with:
// santhosh-tekuri/jsonschema/v6 as it comes, with format an annotation, a
// loader that fails for every URI, and Go's regexp. It is not the project's
// evaluator (openbindings-go/schemaeval) and departs from the evaluator
// contract where core's tests do not look: Go's regexp is not ECMA-262, and
// the library evaluates dependencies and content keywords.
type testEvaluator struct{}

func (testEvaluator) Compile(_ context.Context, bundle SchemaBundle) (CompiledSchema, error) {
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(bundle.Document))
	if err != nil {
		return nil, err
	}
	id, _ := document.(map[string]any)["$id"].(string)
	c := jsonschema.NewCompiler()
	c.UseLoader(failingLoader{})
	if err := c.AddResource(id, document); err != nil {
		return nil, err
	}
	schema, err := c.Compile(id)
	if err != nil {
		return nil, fmt.Errorf("the library cannot compile the bundle: %w", err)
	}
	return testSchema{schema}, nil
}

type failingLoader struct{}

func (failingLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("%s is not obtained", url)
}

type testSchema struct{ schema *jsonschema.Schema }

func (s testSchema) Validate(_ context.Context, value any) error {
	err := s.schema.Validate(value)
	var invalid *jsonschema.ValidationError
	if !errors.As(err, &invalid) {
		return err
	}
	var problems []SchemaProblem
	var leaves func(e *jsonschema.ValidationError)
	leaves = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			problems = append(problems, SchemaProblem{InstanceLocation: jsonpointer.Format(e.InstanceLocation...), Message: fmt.Sprint(e.ErrorKind)})
		}
		for _, cause := range e.Causes {
			leaves(cause)
		}
	}
	leaves(invalid)
	return &MismatchError{Problems: problems, Cause: err}
}

// contractsFor resolves a document's value contracts with the test
// evaluator.
func contractsFor(t *testing.T, iface *Interface, resources ...Resource) *ValueContracts {
	t.Helper()
	compiler, err := NewValueContractCompiler(testEvaluator{}, resources...)
	if err != nil {
		t.Fatal(err)
	}
	contracts, err := compiler.Resolve(context.Background(), iface)
	if err != nil {
		t.Fatal(err)
	}
	return contracts
}

// verdictOf names what a value contract's answer is, as OBI-T-08 tells
// them apart: "valid", "mismatch", or "no verdict".
func verdictOf(t *testing.T, err error) string {
	t.Helper()
	switch {
	case err == nil:
		return "valid"
	case errors.Is(err, ErrMismatch) && errors.Is(err, ErrNoVerdict):
		t.Fatalf("an answer matches both outcome sentinels: %v", err)
	case errors.Is(err, ErrMismatch):
		return "mismatch"
	case errors.Is(err, ErrNoVerdict):
		return "no verdict"
	}
	t.Fatalf("an answer that is neither outcome: %v", err)
	return ""
}

// validateWithTestEvaluator validates a value against an operation's input
// or output contract with the test evaluator.
func validateWithTestEvaluator(t *testing.T, iface *Interface, operation, direction string, value any) error {
	t.Helper()
	contracts := contractsFor(t, iface)
	compile := contracts.CompileInput
	if direction == "output" {
		compile = contracts.CompileOutput
	}
	contract, err := compile(context.Background(), operation)
	if err != nil {
		t.Fatalf("compiling %s's %s contract: %v", operation, direction, err)
	}
	return contract.Validate(context.Background(), value)
}
