# Releasing openbindings-go

This is a Go multi-module monorepo: the core SDK at the repository root plus
eight active sub-modules under `formats/`. Each module versions and tags
independently.

**Upstream tag prerequisites:** native-client dependencies must be published
before tagging their adapters. In particular, the OpenAPI and AsyncAPI adapters
require the versions of `openapi-client/go` and `asyncapi-client/go` named in
their respective manifests. The internal ordering is core before formats
(below). The root SDK itself has no native-client dependency.

## Pre-release OpenAPI candidate verification

The checked-in adapter manifests keep the coordinated release targets even
before those tags exist. To test an exact pushed candidate independently of a
local Go workspace, run:

```bash
bash scripts/verify-openapi-candidate.sh CORE_PSEUDOVERSION CLIENT_PSEUDOVERSION
```

Supply full Go pseudo-versions for the pushed core and OpenAPI client commits.
The verifier tests the current OpenAPI and usage adapter sources with temporary
modfiles mapping the future versions to those exact remote candidates. It
disables workspaces, resolves and tidies only temporary manifests, then runs
readonly race tests and builds, checking that neither the temporary lock files
nor the checked-in manifests changed during verification. The exact candidate
spec corpus must be available beside this repository, or via `OB_SPEC_CORPUS`.
The caller records that corpus's clean commit in the candidate ledger; this
script checks corpus availability, not its Git identity.
This proves candidate dependency integrity, not installation of unpublished
release versions. The post-tag external-consumer gate below remains mandatory.

## Tags

- Core SDK: `vX.Y.Z`
- Format sub-module: `formats/<name>/vX.Y.Z`

All release tags are annotated (from 0.2.0 on; the ten 0.1.0 tags
predate this convention and are lightweight):

```bash
git tag -a vX.Y.Z -m "openbindings-go vX.Y.Z"
git push origin vX.Y.Z
git tag -a formats/<name>/vX.Y.Z -m "formats/<name> vX.Y.Z"
git push origin formats/<name>/vX.Y.Z
```

Push each tag as it is cut: the pre-tag check below resolves against
*published* tags, so the core tag must be pushed — not merely created
locally — before any format module is tagged.

`pkg.go.dev` auto-discovers pushed tags; there is no publish step.

After the core and all eight format tags are public, run the external-consumer
gate:

```bash
scripts/verify-published-release.sh vX.Y.Z
```

It disables every local workspace, resolves each module through the public Go
module path, and compiles a fresh consumer importing the core plus all format
packages. A release is incomplete until this passes.

## Tag ordering (normative)

**Never tag a `formats/<name>` module whose `go.mod` requires a core version
that is not yet a published tag.** A format tag cut before its required core
tag produces `unknown revision` for every consumer that fetches the format
module: the module proxy cannot resolve the core requirement. Tag the core
first, formats after.

Pre-tag check, for the core version the format's `go.mod` requires:

```bash
GOWORK=off go list -m github.com/openbindings/openbindings-go@vX.Y.Z
```

If this fails, the required core tag does not exist yet (or is not pushed) —
stop and cut the core tag first.

Note: once the required core and native-client tags are published, run
`GOWORK=off go mod tidy` in each affected adapter and commit its manifest and
checksum changes **before creating that adapter's tag**.
During development the `go.work` workspace masks module resolution, so a
format module's requirement on a new core version only resolves (and
`go mod tidy` only runs meaningfully) once the core tag is published.
Consequence: between bumping a format's core requirement and cutting the
core tag, that format module on `main` is unresolvable for non-workspace
consumers (`go get .../formats/<name>@main` fails with `unknown
revision`); resolvability returns when the release's tags land.

## Changelogs

- Per-module changelogs: the root `CHANGELOG.md` covers the core SDK; every
  tagged module MUST have its own `CHANGELOG.md`
  (`formats/<name>/CHANGELOG.md`).
- Unreleased work accumulates under the heading `## X.Y.Z (working draft)`.
- At release, retitle the working-draft heading to `## X.Y.Z — YYYY-MM-DD`,
  where the date is the tag date (the convention from 0.2.0 on).

## Version policy (pre-1.0)

- Minor versions MAY include breaking changes; document them under
  **Changed** or **Removed** in the module's changelog.
- Patch versions are for bug fixes and non-breaking changes.
- When bumping `MaxTestedVersion` in `version.go`, call that out in the
  CHANGELOG entry.
