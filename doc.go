// Package openbindings is the core OpenBindings SDK for Go: the OBI
// document model with lossless JSON handling, document validation,
// operation resolution, schema validation at invocation boundaries, and
// the Core-defined constants (versions and media type).
//
// The package is dependency-light and format-agnostic, and covers exactly
// what the OpenBindings specification defines. The layers above it are
// separate sub-packages mirroring the published interface family: invoke
// (binding-invoker / operation-invoker runtime), synthesize
// (interface-synthesizer / source-inspector), and compare
// (schema-comparison). Binding formats (openapi, asyncapi, graphql, grpc,
// mcp, usage, ...) live in formats/* submodules and plug into those
// sub-packages' seams.
//
// # Documents
//
//	iface, err := openbindings.ParseDocument(data) // rejects duplicate keys (OBI-D-01)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	if err := iface.Validate(); err != nil {
//	    log.Fatal(err)
//	}
//
// (json.Unmarshal into Interface also works and is lossless, but only
// ParseDocument enforces OBI-D-01 on wire bytes.)
//
// JSON Schema fields preserve object and boolean schema roots. Every
// OBI declares its target spec version via the top-level openbindings
// field, checked against [MinSupportedVersion] through [MaxTestedVersion].
//
// # Lossless JSON (Forward Compatibility)
//
// OpenBindings documents may include:
//   - Extension fields (x-*) at any object location
//   - Unknown (future) fields as the spec evolves
//
// This SDK preserves all JSON fields on unmarshal → marshal by storing:
//   - LosslessFields.Extensions for keys beginning with x-
//   - LosslessFields.Unknown for other unknown keys
//
// # Collision Semantics
//
// If a key exists both as a typed field and in Unknown/Extensions,
// the typed field wins during marshaling. This matches the reality that
// future spec versions may claim keys that were previously "unknown".
//
// # Concurrency
//
// All types in this package are safe for concurrent read access. Concurrent
// writes to the same value require external synchronization. The Validate
// method is safe for concurrent use on the same Interface value (read-only).
//
// JSON marshaling and unmarshaling follow standard library semantics:
// concurrent calls on different values are safe; concurrent calls on the
// same value require synchronization.
//
// # Subpackages
//
//   - invoke: the invocation runtime — operation invokers, the
//     cardinality-agnostic Invocation handle, context resolution, and the
//     seams (binding invokers, transform evaluators, consumer hooks)
//   - synthesize: interface synthesis and source inspection
//   - httpdiscovery: optional well-known HTTP discovery of existing OBIs
//   - acquire: application-level ordering of direct retrieval, discovery,
//     and optional synthesis
//   - compare: interface and operation compatibility checking
//   - canonicaljson: RFC 8785 (JCS) deterministic JSON serialization
//   - schemaprofile: the OpenBindings Schema Comparison Profile (OB-2020-12)
package openbindings
