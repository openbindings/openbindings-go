// Package compare is the OpenBindings schema-comparison layer: interface
// and operation compatibility checking under the published Schema
// Comparison Profile (OB-2020-12), realized on top of the schemaprofile
// package.
//
// This is a tooling convention, not a spec requirement: the OpenBindings
// specification leaves matching, comparison, and selection to tools
// (openbindings.md §1.3, Authority and deferral).
//
// The profile fails closed at comparison time. Each side's schemas are
// normalized against their own document (keywords outside the profile are
// retained and mark their position), then compared directionally. At every
// position the identity rule runs first: structurally identical sub-schemas
// are compatible whatever keywords they carry, so identical contracts —
// including ones using pattern or other outside-profile keywords — report
// no issue. A position that differs and is marked outside the profile on
// either side cannot be decided.
//
// Every issue is classified by CompatibilityIssue.Undecidable. A proven
// contradiction (a missing operation, or a differing keyword the profile
// reads and finds incompatible) is not undecidable. An undecidable issue
// reports the profile's inability to decide, never a contradiction: a
// differing keyword outside the profile, a $ref the SDK declines to fetch,
// or a schema that does not normalize or is not a JSON Schema object or
// boolean. Its detail begins "input schema check failed:" or
// "output schema check failed:" and carries the profile's finding. A name or
// alias correspondence is the provider's compatibility claim; consumers set a
// candidate aside on a proven contradiction and let the claim stand on an
// undecidable issue, keeping it as evidence.
package compare
