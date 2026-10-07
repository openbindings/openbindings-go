# Releasing openbindings-go

This repository holds three Go modules: the core SDK at the repository root,
`schemaeval`, the project's schema evaluator, and `httpdiscovery`, the optional
HTTP Discovery companion implementation. The optional modules require core and
are tagged after the core release they require. Until then their `go.mod` files
develop against the core beside them through a `replace` directive, which a
release drops in favor of the tagged core version. Their versions are independent
of the core module and of the specifications they implement.

The eight `formats/*` modules that used to live here were removed on
2026-09-24 (preserved on the `legacy/pre-core-rebuild` branch). Their published
`formats/<name>/v0.1.0` tags remain on the remote and resolvable through the Go
module proxy; do not delete or move them. When a rebuilt binding module lands,
this document gains its tagging and ordering rules again.

## Naming the released text

While the specification release this SDK applies is a working draft,
`appliedRevision` (`version.go`) names the spec commit of its text, and Go
CI checks the spec out at that commit. A release of this SDK made after
that specification release is published names it by `appliedRelease`
alone: `appliedRevision` becomes `""`, and Go CI checks out the tag
`v<appliedRelease>` instead. The SDK verifies the text a version names only
through its revision today, so before `appliedRevision` is set to `""`:

1. Give the applied-text verification (`verifyAppliedText` in
   `conformance_test.go`) a path that verifies a release against its
   published snapshot (the spec's `versions/X.Y.Z/` and its tag), with that
   snapshot's hash pinned beside it. Without one, a release named alone is
   reported unverified, which `OB_CORPUS_REQUIRED` (set in CI) turns into a
   failure of `TestAppliedText_IsVerified`.
2. Then set `appliedRevision` to `""`, and confirm that Go CI checks out
   `v<appliedRelease>` and passes, before the release is tagged.

## Tags

- Core SDK: `vX.Y.Z`
- Schema evaluator: `schemaeval/vX.Y.Z`, after the core tag it requires
- HTTP discovery: `httpdiscovery/vX.Y.Z`, after the core tag it requires

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

That script covers the root module. For either optional module, remove its local
replacement before tagging, run its tests against the required tagged core, and
verify a fresh external consumer resolves and builds the tagged optional module
with `GOWORK=off` and no local replacement. HTTP discovery also requires its
companion conformance tests, pinned-authority check, and `go vet`/race tests to
pass; its [README](httpdiscovery/README.md) states its separate conformance scope.

## Release readiness

A release is judged by these rows, each met or not on evidence, rather than
by a grade. Every row is met before a release is tagged, unless the
maintainers waive it with a reason recorded in the release's CHANGELOG
entry, which also records each row's final state.

| Row | Met when |
|---|---|
| Every rule | Every core rule is implemented, and what the SDK computes from a document (an operation's identifiers and bindings, kind comparison, reference resolution, value contracts) follows the section that defines it; CI runs the specification's core corpus with `OB_CORPUS_REQUIRED` against the text the release applies. |
| Exact model | A document read and written back is unchanged, and the embedded document schema is byte-identical to the specification's at the applied revision. |
| Only the core | Every exported name in the root module maps to the core specification or a convenience it implies; the root module holds no invocation, synthesis, discovery, or binding-specification code. Separately installed companion modules name their own authority. |
| One vocabulary | Public names use the specification's terms. |
| The text named | `appliedRelease` names the release applied, and `appliedRevision` its revision while that release is a working draft (`""` once released), so a validation report names the text it applied. |
| Parity | The TypeScript SDK matches at the observable OpenBindings boundary: `IMPLEMENTATION_PARITY.md` lists nothing pending. |

Peer rankings are recorded beside the rows, not as one of them: reviewers
rank the SDK among the reference libraries of other interface-description
specifications (kin-openapi, libopenapi, gqlparser, protocompile with
protobuf-go's descriptors, Smithy's smithy-model) on named criteria, and the
release's CHANGELOG entry states the result.

### Readiness of the 0.2.0 working draft (2026-10-06)

| Row | State |
|---|---|
| Every rule | Met at spec `349e67b`. The SDK checks no example against its value contract (§5.1) and derives no form from a schema. |
| Exact model | Met. |
| Only the core | Met. `openbindingstest` and `schemaeval` serve value validation (§5.2), and `httpdiscovery` names the HTTP Discovery text it applies by revision and SHA-256. |
| One vocabulary | Met. `Reference.Target` and `Position` keep their names: a schema reference's target and a finding's place in the input text share no context with the specification's binding target and OBI position. A report names the applied `Release`, and `ErrInconclusive` marks only input beyond this SDK's own limits. |
| The text named | Met. |
| Parity | Not met: see `IMPLEMENTATION_PARITY.md`. |

Peer ranking (2026-10-06, at 14da31f, one reviewer): 3rd of 6, behind
protocompile with protobuf-go's descriptors and smithy-model, ahead of
gqlparser, libopenapi, and kin-openapi. First on fidelity, validation rigor,
conformance evidence, and scope; third on API design; fourth on
diagnostics, documentation, and robustness. The robustness finding behind
that place, validation work quadratic in refused member names, is fixed
(#143).

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
