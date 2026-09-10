# Maintained codec provenance

This is an adopted implementation dependency of the protocol-neutral `jsonvalue`
package, not an OpenBindings conformance requirement. `UPSTREAM.json` records the
original Go 1.25.12 relocation checkpoint (including its historical experimental
status); it does not describe today's adoption status or the later local patches.
The relocation script deliberately refuses to overwrite this maintained tree.

Local behavior corrections and their tests remain in this directory's Git history.
Do not regenerate over them or treat the initial relocation hashes as current hashes.

The landing pass additionally represents two upstream negative-test fixtures using
`reflect.StructOf`: the tagged unexported field and malformed struct tag. The tested
tags and expected outcomes are unchanged. This lets ordinary `go vet ./...` run
without suppressing the struct-tag analyzer. `scripts/jsoncodec-vet-fixtures.patch`
records that test-only adaptation against the qualified pre-landing tree. Replay it
after those existing codec corrections when rebuilding the dependency.

Acceptance: full `go vet ./...`, codec race tests, exact-string/number carriage
tests and the SDK corpus-required suite. No production codec source was changed by
the test-fixture adaptation.
