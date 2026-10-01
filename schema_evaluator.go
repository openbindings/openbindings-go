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
// a closed 2020-12 compound document for one value contract (see
// SchemaBundle for what core guarantees about it), whose schemas core has
// already found well-formed and free of what the specification leaves
// undefined. The evaluator:
//   - evaluates the document's root as JSON Schema 2020-12, with format an
//     annotation, patterns matched as ECMA-262 regular expressions with the u
//     flag, and keywords 2020-12 does not define ignored;
//   - returns a *MismatchError only when the value fails the schema, and any
//     other error when it cannot decide (a pattern it cannot match as
//     ECMA-262 does, a number beyond its arithmetic, a resource limit);
//   - locates each problem as described below;
//   - obtains nothing: no network, no file system. Every reference resolves
//     within the bundle, so a loader that fails for every URI is safe. A
//     reference its library leaves unresolved must never yield a verdict:
//     check after compiling that the library resolved every reference, and
//     fail Compile if not;
//   - returns fresh errors, keeps no state one bundle could affect in
//     another's (bundles may use the same identifiers, the root's included),
//     and does not use a value after Validate returns;
//   - returns, when it stops because its ctx ended, an error matching
//     ctx.Err(), and never reports its own time limits as the ctx's (a budget
//     built with context.WithTimeout must not surface its DeadlineExceeded);
//     cancellation need not be prompt;
//   - answers in three ways only: nil, an error holding a *MismatchError, or
//     any other error, which never matches ErrNoVerdict, ErrUndefined,
//     ErrNoValueContract, ErrOperationNotFound, or ErrInconclusive, never
//     holds a *NoVerdictError, and matches ErrMismatch only by holding a
//     *MismatchError. Compile's errors hold no *MismatchError. Core reads an
//     answer that breaks this as no verdict, and keeps the evaluator's error
//     out of the returned error's chain, in Cause alone.
//
// A problem's InstanceLocation is an RFC 6901 pointer into the value, at the
// instance location of the deepest failing keyword on its failing path:
//   - in-place applicators (allOf, $ref, $dynamicRef, then, else,
//     dependentSchemas) pass their failing subschemas' problems through;
//   - child applicators (properties, patternProperties,
//     additionalProperties, unevaluatedProperties, prefixItems, items,
//     unevaluatedItems) pass them through at the member or item, a failing
//     false there included;
//   - contains (with minContains and maxContains), propertyNames, and not
//     are located at the array, object, or value they apply to;
//   - an anyOf or oneOf that fails because every subschema fails is located
//     there or passes its failing subschemas' problems through, but not
//     both; a oneOf that fails because more than one subschema passes is
//     located at the value it applies to;
//   - if never fails, so its subschema's failures are not problems, and a
//     passing applicator contributes none, whatever its subschemas did (the
//     failed branch of a passing anyOf, the failed subschema of a passing
//     not);
//   - every other keyword is located at the value it applies to.
//
// Every failing keyword the rule locates is reported once per instance
// location it fails at: two keywords failing at one location are two
// problems at one path, and propertyNames failing for two names is one
// problem at the object. A library that reports fewer (stopping at the first
// failure, say) is unlocated where openbindingstest checks paths.
//
// A SchemaEvaluator and the CompiledSchemas it returns are safe for
// concurrent use: a service validates many values against one retained
// CompiledSchema at once.
//
// What an evaluator must do has three sources, and only the first is the
// specification's:
//   - OBI-T-08 requires evaluation under JSON Schema 2020-12, numbers by
//     their exact values and patterns as ECMA-262 regular expressions with
//     the u flag, and no verdict that depends on a reference or capability
//     the evaluator lacks (a pattern it cannot match as ECMA-262 does, a
//     number beyond its arithmetic) or on an undefined result. Core refuses
//     the undefined results and the missing references before an evaluator
//     sees a bundle.
//   - The library an evaluator adapts brings limits of its own: its
//     regular-expression dialect, its arithmetic, its loader, the shape of
//     its errors, and the meta-schemas' own patterns, which a bundle that
//     reaches a meta-schema runs through its engine. Each becomes a
//     no-verdict where evaluation reaches it.
//   - This SDK's diagnostic contract, the problem locations and multiplicity
//     above, is the SDK's, not the specification's. An evaluator that
//     cannot locate problems so still reaches every verdict, and
//     openbindingstest's Options.Unlocated names the cases it cannot locate.
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
	// Document is a JSON Schema 2020-12 compound document ("$schema" names
	// 2020-12) whose root is the schema to evaluate. Immutable; the
	// evaluator may retain it. Core guarantees:
	//   - Shape: the root declares an absolute $id and $refs the entry, and
	//     its $defs holds every schema evaluation can reach, the 2020-12
	//     meta-schemas a reference reaches, and, when some unreached
	//     reference's target is not a schema the bundle holds, a false
	//     placeholder. The root's $id, every identifier core generates, and
	//     every $defs key core chooses are spellings outside this contract
	//     (openbindingstest varies them); an evaluator that rewrites the
	//     bundle may add any $id or $defs key not already in it.
	//   - Identities: every object schema that roots a resource carries an
	//     absolute $id in RFC 3986 normal form: its own, one core generates
	//     for a position of the OBI document resource, or, for a supplied
	//     resource's root without one, its URI. A boolean position carries
	//     none and is addressed by pointer from the root. No two resources
	//     share an $id.
	//   - Canonical references: every $ref and $dynamicRef is written by
	//     where its target lies in the bundle: to a resource's root, its $id
	//     with no fragment; within a resource, its $id, "#", and an RFC 6901
	//     pointer, percent-encoded; to a plain name, its resource's $id,
	//     "#", and the name (a $dynamicRef naming a plain name keeps it).
	//     An unreached reference whose target the bundle does not hold as a
	//     schema names the placeholder, which evaluation never reaches.
	//   - Closed: every reference resolves within the bundle to a schema.
	//   - Dynamic scope: evaluating the root gives every reachable
	//     $dynamicRef the dynamic scope it has in the OBI document; the root
	//     declares, as scope wrappers, the document resource's
	//     $dynamicAnchors a reachable $dynamicRef can look up.
	//   - Legacy positions moved: a schema under definitions, and a schema
	//     value of dependencies, sits in its resource's $defs instead, and
	//     every reference to it names it there, so the bundle holds no
	//     definitions keyword and no schema value of dependencies.
	//   - Well-formed: every schema is valid against the 2020-12
	//     meta-schemas, every pattern is an ECMA-262 11th edition pattern
	//     the u flag accepts, no resource declares a plain name twice, and no
	//     reachable cycle applies schemas in place without advancing into
	//     the value.
	//   - One dialect: every resource's dialect is 2020-12, assigned by
	//     resource (§5.2): the document resource's is 2020-12, and a
	//     resource takes the dialect its root's $schema names or its
	//     enclosing resource's. A $schema appears only at a resource's root,
	//     naming 2020-12; core leaves out one that declares no dialect (in
	//     the document resource, or below a resource's root).
	//   - As written otherwise: every other keyword, 2020-12 or not, and
	//     every number exactly as the document writes it.
	Document json.RawMessage
}
