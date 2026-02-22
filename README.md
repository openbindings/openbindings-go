# `openbindings-go`

Go SDK for OpenBindings types and (eventually) lightweight utilities.

**Scope (v0):**

- Core Go types for the OpenBindings interface shape (operations + bindings).
- JSON struct tags suitable for reading/writing OpenBindings interface documents.
- Schema compatibility profile implementation (`schemaprofile`) for deterministic comparisons under the OpenBindings Profile v0.1 (not a full JSON Schema subschema checker).

This SDK is intended to be usable by third-party Go tooling and providers.

**Tiny usage sketch:**

```go
var iface openbindings.Interface
if err := json.Unmarshal(data, &iface); err != nil { /* ... */ }
if err := iface.Validate(); err != nil { /* ... */ }

norm := schemaprofile.Normalizer{Root: map[string]any{"schemas": iface.Schemas}}
ok, err := norm.InputCompatible(targetSchema, candidateSchema)
```
