// Package openbindings is the core OpenBindings SDK for Go: the OBI
// document model with lossless JSON handling, validation and compatibility
// checking, and the runtime invocation stack — operation invokers, the
// cardinality-agnostic Invocation handle, interface discovery, and the
// seams (binding invokers, transform evaluators, consumer hooks) that
// concrete formats and consuming tools plug into.
//
// The package is dependency-light and format-agnostic: binding formats
// (openapi, asyncapi, graphql, grpc, mcp, usage, ...) live in formats/*
// submodules and contribute BindingInvoker / InterfaceSynthesizer
// implementations to this package's seams.
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
// JSON schema fields are represented as JSON objects (map[string]any); this
// preserves structure but does not capture non-object schema roots. Every
// OBI declares its target spec version via the top-level openbindings
// field, checked against [MinSupportedVersion] through [MaxTestedVersion].
//
// # Invocation
//
// An OperationInvoker dispatches operations through registered binding
// invokers. Every operation returns a cardinality-agnostic Invocation[I, O]
// handle — one shape for unary, server-streaming, client-streaming, and
// bidirectional bindings. Creation is inert (no I/O until the handle is
// driven), wiring failures surface as already-errored handles rather than
// panics, and missing credentials surface as CONTEXT_REQUIRED terminal
// errors raised before any side effect:
//
//	opInv := openbindings.NewOperationInvoker(openapi.NewInvoker())
//	call := openbindings.Invoke(ctx, opInv, iface,
//	    openbindings.NewOperationSignature[any, any]("listItems"))
//	_ = call.Write(ctx, map[string]any{"limit": 10})
//	out, err := openbindings.Single(ctx, call.Outputs())
//
// FetchInterface resolves an OBI from a base URL (well-known discovery,
// with synthesis from a raw spec as the fallback). InvokeHooks is the
// consumer seam for wire questions a source artifact leaves open;
// TransformEvaluator is the seam for JSONata binding transforms (the SDK
// bundles no JSONata runtime; the README carries the adapter recipe).
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
