# Private Go JSON Schema backend correction

The ordinary Core validator privately carries the production sources and
metaschemas of `github.com/santhosh-tekuri/jsonschema/v6` **v6.0.3**, under its
original Apache-2.0 license. `internal/thirdparty/jsonschema/UPSTREAM.json`
records upstream and mechanical import-relocation hashes. The upstream README
and license are retained without implying upstream endorsement.

`jsonschema-v6.0.3.patch` is the separate behavioral correction. It bounds work
immediately before numeric predicates convert to `math/big.Rat`. An extreme
exponent otherwise causes a nil dereference or a false validation verdict.
The private refusal signal aborts speculative evaluation and is caught only at
the public compile/validate entry; unrelated panics are not swallowed. Parsing
and carriage through schemas requiring no numeric work are not refused.

The same error-propagation seam accepts an optional error-aware regex matcher.
Core uses the existing `regexp2/v2` **v2.7.1** ECMAScript Unicode mode, which
supports the retained Unicode property-name cases rejected by v1.12.0. Its
100 ms match limit and upstream bounded stack remain enabled. Timeout/stack
refusal is capability failure, never a failed pattern predicate. The legacy
exported regex engine and its consumers are unchanged. This adds the v2 module
alongside v1; it does not migrate JSONata or Operation Graph.

Upstream rationale and license: [regexp2 v2.7.1](https://github.com/dlclark/regexp2/tree/v2.7.1)
(MIT). The module pin and checksums are recorded in go.mod/go.sum. Qualification
includes all four upstream test packages, the complete retained mandatory
schema corpus, and the SDK's existing ECMA lookahead controls.

Traversal, references, applicators, dialect semantics and annotation ownership
remain upstream algorithms. The SDK's existing vocabulary extension separately
corrects large occurrence-count overflow. There is no second schema walker.
JSONata and Operation Graph evaluator dependencies are not migrated here.

Reproduce against the verified module-cache version (no network/store edits):

```sh
node scripts/qualify-jsonschema-source.mjs verify /path/to/github.com/santhosh-tekuri/jsonschema/v6@v6.0.3
```

The verifier reconstructs upstream source in a fresh temporary directory, applies
the maintained patch and compares every production file byte-for-byte. To update
the patch after an intentional correction, run the same command with
`refresh-patch`, review the diff, then run `verify` and all validator gates.
`vendor-jsonschema.mjs` recreates the uncorrected initial import in an empty tree;
it refuses to overwrite the maintained copy. Dependency updates require fresh
provenance and qualification, not an automatic patch rebase.

This is candidate implementation policy, not a Core conformance floor. Numeric
predicate limits are documented in `jsonvalue`; they must never be surfaced as
instance invalidity, equality, or a rounded answer. No upstream contribution or
package publication is asserted by this local adoption.
