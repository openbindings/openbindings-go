// Package schemaprofile implements the OpenBindings Schema Comparison
// Profile (profile identifier OB-2020-12), published in the interfaces
// repository under schema-comparison/. It is the decision procedure the
// compare package uses for cross-document operation matching and schema
// subsumption checks.
//
// This is a tooling convention, not a spec requirement. OpenBindings 0.2.0
// leaves schema comparison, matching, and selection to tools (openbindings.md
// §1.3, Authority and deferral). Third-party tools may publish their own
// profiles or skip the concept entirely; conformance to the OpenBindings
// spec does not depend on this package.
//
// The package is intentionally:
//   - pure (no file/network IO; callers provide any external fetchers if needed)
//   - deterministic (stable results across executions)
//   - profile-scoped (fails closed for keywords outside the supported subset)
//
// Fail-closed is decided at comparison time, not during normalization.
// Normalization retains a keyword outside the profile verbatim and thereby
// marks its position (an allOf whose siblings or branches carry such a
// keyword is retained unmerged for the same reason). Comparison then runs
// the identity rule first at every position: two normalized sub-schemas
// that are structurally identical (EqualNormalizedSchemas) are compatible
// in both directions whatever keywords they carry, because a schema stands
// in for itself, and the walk does not descend. Only a position that is not
// identical and is marked outside the profile on either side is
// indeterminate — reported as an *OutsideProfileError naming the position
// and keyword — and the profile's directional rules decide everything
// else. Identical mode (EqualNormalizedSchemas over whole documents)
// compares retained keywords as ordinary values, so a difference at one is
// a difference, never indeterminate.
//
// It is not a general-purpose JSON Schema validator or subschema checker. It
// answers a different question: can schema A stand in for schema B as an
// input/output contract under the published profile?
package schemaprofile
