// Package schemaprofile implements a JSON Schema compatibility profile used
// by openbindings reference tooling for cross-document operation matching
// and schema subsumption checks.
//
// This is a tooling convention, not a spec requirement. OpenBindings 0.2.0
// removed schema comparison rules and operation-matching algorithms from
// the spec body and made them tool-defined per §2 (Scope principle).
// Third-party tools may publish their own profiles or skip the concept
// entirely; conformance to the OpenBindings spec does not depend on this
// package.
//
// The package is intentionally:
//   - pure (no file/network IO; callers provide any external fetchers if needed)
//   - deterministic (stable results across executions)
//   - profile-scoped (fails closed for keywords outside the supported subset)
//
// It is not a general-purpose JSON Schema validator or subschema checker. It
// answers a different question: can schema A stand in for schema B as an
// input/output contract under the openbindings reference profile?
package schemaprofile
