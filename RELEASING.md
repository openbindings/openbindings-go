# Releasing openbindings-go

This repository holds two Go modules: the core SDK at the repository root,
and `schemaeval`, the project's schema evaluator, which requires the core.
Neither has an upstream tag prerequisite; `schemaeval` is tagged after the
core release it requires. Until then its `go.mod` develops against the core
beside it through a `replace` directive, which a release drops in favor of
the tagged core version.

The eight `formats/*` modules that used to live here were removed on
2026-09-24 (preserved on the `legacy/pre-core-rebuild` branch). Their published
`formats/<name>/v0.1.0` tags remain on the remote and resolvable through the Go
module proxy; do not delete or move them. When a rebuilt binding module lands,
this document gains its tagging and ordering rules again.

## Tags

- Core SDK: `vX.Y.Z`
- Schema evaluator: `schemaeval/vX.Y.Z`, after the core tag it requires

All release tags are annotated (from 0.2.0 on; the 0.1.0 tags predate this
convention and are lightweight):

```bash
git tag -a vX.Y.Z -m "openbindings-go vX.Y.Z"
git push origin vX.Y.Z
```

`pkg.go.dev` auto-discovers pushed tags; there is no publish step.

After the tag is public, run the external-consumer gate:

```bash
scripts/verify-published-release.sh vX.Y.Z
```

It disables every local workspace, resolves the module through the public Go
module path, and compiles a fresh consumer importing it. A release is
incomplete until this passes.

## Release readiness

A release is judged by these rows, each met or not on evidence, rather than
by a grade. Every row is met before a release is tagged, unless the
maintainers waive it with a reason recorded in the release's CHANGELOG
entry, which also records each row's final state.

| Row | Met when |
|---|---|
| Every rule | Every core document rule, and every tool rule that applies to the SDK, is implemented; CI runs the specification's core corpus with `OB_CORPUS_REQUIRED` against the text the release applies. |
| Exact model | A document read and written back is unchanged, and the embedded document schema is byte-identical to the specification's at the applied revision. |
| Only the core | Every exported name maps to the core specification or a convenience it implies; the module holds no invocation, synthesis, discovery, or binding-specification code. |
| One vocabulary | Public names use the specification's terms. |
| The text named | `appliedRelease` names the release applied, and `appliedRevision` its revision while that release is a working draft (`""` once released), so a validation report names the text it applied (OBI-T-09). |
| Parity | The TypeScript SDK matches at the observable OpenBindings boundary: `IMPLEMENTATION_PARITY.md` lists nothing pending. |

Peer rankings are recorded beside the rows, not as one of them: reviewers
rank the SDK among the reference libraries of other interface-description
specifications (kin-openapi, libopenapi, gqlparser, protocompile with
protobuf-go's descriptors, Smithy's smithy-model) on named criteria, and the
release's CHANGELOG entry states the result.

### Readiness of the 0.2.0 working draft (2026-09-30)

| Row | State |
|---|---|
| Every rule | Met. OBI-T-05 and OBI-T-11 do not apply: the SDK derives no forms and checks no examples. |
| Exact model | Met. |
| Only the core | Met. `openbindingstest` and `schemaeval` serve OBI-T-08's value validation. |
| One vocabulary | Met. |
| The text named | Met. |
| Parity | Not met: see `IMPLEMENTATION_PARITY.md`. |

Peer ranking (2026-09-30, at aa158a9, two reviewers): 2nd and 3rd of 6,
behind smithy-model (and protocompile, for one reviewer), ahead of gqlparser,
libopenapi, and kin-openapi. Strongest on fidelity, validation rigor,
conformance evidence, and scope; weakest on diagnostics (no source
positions; OBI-D-02 map-key findings mislocated by the pinned JSON Schema
library) and documentation.

## Changelog

- The root `CHANGELOG.md` covers the module.
- Unreleased work accumulates under the heading `## X.Y.Z (working draft)`.
- At release, retitle the working-draft heading to `## X.Y.Z — YYYY-MM-DD`,
  where the date is the tag date (the convention from 0.2.0 on).

## Version policy (pre-1.0)

- Minor versions MAY include breaking changes; document them under
  **Changed** or **Removed** in the changelog.
- Patch versions are for bug fixes and non-breaking changes.
- Record the specification versions a release was tested against in its
  CHANGELOG entry, and call out any change to the support declaration in
  `version.go` (`SupportedVersions`, `AuthoringVersion`, or a named
  prerelease).
