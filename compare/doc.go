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
// either side cannot be decided; it surfaces as an issue whose detail
// begins "input schema check failed:" or "output schema check failed:"
// carrying the profile's outside-profile finding, which consumers treat as
// indeterminate rather than incompatible.
package compare
