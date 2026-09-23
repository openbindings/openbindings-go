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
//	if _, err := iface.Validate(); err != nil {
//	    log.Fatal(err)
//	}
//
// (json.Unmarshal into Interface also works and is lossless, but only
// ParseDocument enforces OBI-D-01 on wire bytes.)
//
// Validate returns a *ValidationError listing every violation it establishes,
// which makes it a gate. A nil error is not conformance: a rule this SDK
// cannot decide is inconclusive, not violated. The report beside the error
// carries the conclusion:
//
//	iface, report, err := openbindings.ValidateDocument(data)
//	// report.Conclusion is conformant, non-conformant, or
//	// conformance-undetermined (§10.5); err is a *ValidationError when a
//	// violation was established, and a *VersionRefusalError when the declared
//	// version is outside the supported set.
//
// JSON Schema fields preserve object and boolean schema roots. Every
// OBI declares its target spec version via the top-level openbindings
// field, checked against [MinSupportedVersion] through [MaxTestedVersion].
//
// # An Exact Document Model
//
// Re-encoding a decoded document reproduces every member:
//   - An optional member is absent exactly when its Go value is nil. Optional
//     strings and booleans are pointers (set them with [Present], read them
//     with [Value] where absence and the zero value mean the same); optional
//     collections distinguish nil (absent) from empty (present).
//   - JSON null is carried where it is a value: example values and source
//     content are json.RawMessage, where the bytes `null` are a present null.
//   - Members the SDK does not model are kept: LosslessFields.Extensions for
//     keys beginning with x-, LosslessFields.Unknown for other keys, at every
//     OBI-defined object, a transform's $ref object included.
//
// A document the model cannot carry exactly fails decoding instead of being
// altered: a JSON null at any other known position, a missing required
// string member, or a binding preference that is not an integer in range.
// ValidateDocument still judges such a document from its bytes.
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
