package openbindings

import (
	"context"
	"encoding/json"
)

// SchemaEvaluator compiles JSON Schema 2020-12 for validating values against
// value contracts (OBI-T-08). An application supplies one; the SDK has none
// of its own. The openbindings-go/schemaeval module is the project's, and
// openbindingstest checks any evaluator against this contract.
//
// Core hands an evaluator only what the specification leaves to JSON Schema:
// a closed 2020-12 compound document for one value contract, whose every
// reference resolves within it, and whose schemas core has already found
// well-formed and free of what the specification leaves undefined. The
// evaluator:
//   - evaluates the document's root as JSON Schema 2020-12, with format an
//     annotation, patterns matched as ECMA-262 regular expressions with the u
//     flag, and keywords 2020-12 does not define ignored;
//   - returns a *MismatchError only when the value fails the schema, locating
//     each problem at the instance location of the deepest failing keyword on
//     its failing path, and any other error when it cannot decide;
//   - obtains nothing, and never lets a reference its library leaves
//     unresolved evaluate to a verdict;
//   - returns fresh errors, keeps no state one bundle could affect in
//     another's, and does not use a value after Validate returns;
//   - returns, when it stops because its ctx ended, an error matching
//     ctx.Err(), and never reports its own time limits as the ctx's;
//   - answers in three ways only: nil, an error holding a *MismatchError, or
//     any other error, which never matches ErrNoVerdict, ErrUndefined,
//     ErrNoValueContract, or ErrOperationNotFound, never holds a
//     *NoVerdictError, and matches ErrMismatch only by holding a
//     *MismatchError. Compile's errors hold no *MismatchError.
//
// A SchemaEvaluator and the CompiledSchemas it returns are safe for
// concurrent use.
type SchemaEvaluator interface {
	// Compile returns a usable CompiledSchema, or an error meaning no value
	// validated against this bundle gets a verdict.
	Compile(ctx context.Context, bundle SchemaBundle) (CompiledSchema, error)
}

// CompiledSchema validates values against one compiled bundle.
type CompiledSchema interface {
	// Validate returns nil when the value satisfies the schema, an error
	// holding a *MismatchError when it does not, and any other error when
	// the evaluator cannot decide. The value is a JSON value (nil, bool,
	// string, json.Number, []any, or map[string]any, with no cycle) and is
	// read-only.
	Validate(ctx context.Context, value any) error
}

// SchemaBundle is what core hands an evaluator for one value contract. It is
// a struct so that fields can be added without changing SchemaEvaluator.
type SchemaBundle struct {
	// Document is a JSON Schema 2020-12 compound document whose root is the
	// schema to evaluate. Every reference in it resolves within it.
	// Immutable; the evaluator may retain it.
	Document json.RawMessage
}
