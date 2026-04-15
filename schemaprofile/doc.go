// Package schemaprofile implements the OpenBindings Schema Compatibility Profile (v0.1).
//
// This package is intentionally:
// - pure (no file/network IO; callers provide any external fetchers if needed)
// - deterministic (stable results across executions)
// - profile-scoped (fails closed for keywords outside the v0.1 profile)
//
// It is not a general-purpose JSON Schema validator or subschema checker. It answers a different question:
// can schema A stand in for schema B as an input/output contract under the OpenBindings profile?
package schemaprofile
