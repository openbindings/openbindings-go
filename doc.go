// Package openbindings provides a Go SDK for the OpenBindings specification.
//
// The SDK offers lossless JSON handling for OpenBindings documents, preserving
// unknown fields and extensions for forward compatibility.
//
// JSON schema fields are represented as JSON objects (map[string]any). This
// preserves structure but does not capture non-object schema roots or raw bytes.
//
// # Quick Start
//
//	var iface openbindings.Interface
//	if err := json.Unmarshal(data, &iface); err != nil {
//	    log.Fatal(err)
//	}
//
//	fmt.Println(iface.Name, iface.Version)
//	for name, op := range iface.Operations {
//	    fmt.Println(name, op.Kind, op.Description)
//	}
//
//	if err := iface.Validate(); err != nil {
//	    log.Fatal(err)
//	}
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
//   - canonicaljson: RFC 8785 (JCS) deterministic JSON serialization
//   - formattoken: Parse and normalize <name>@<version> format tokens
//   - schemaprofile: OpenBindings Schema Compatibility Profile v0.1
package openbindings
