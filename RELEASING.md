# Releasing openbindings-go

This is a Go multi-module monorepo: the core SDK at the repository root plus
nine sub-modules under `formats/`. Each module versions and tags
independently.

**Upstream tag prerequisites:** none outside this repository — the SDK
depends on no other openbindings repo's tags. The only ordering constraint is
internal: core before formats (below).

## Tags

- Core SDK: `vX.Y.Z`
- Format sub-module: `formats/<name>/vX.Y.Z`

All release tags are annotated:

```bash
git tag -a vX.Y.Z -m "openbindings-go vX.Y.Z"
git tag -a formats/<name>/vX.Y.Z -m "formats/<name> vX.Y.Z"
```

`pkg.go.dev` auto-discovers pushed tags; there is no publish step.

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

Note: `go.mod` tidying that depends on the new core tag happens at tag time.
During development the `go.work` workspace masks module resolution, so a
format module's requirement on a new core version only resolves (and
`go mod tidy` only runs meaningfully) once the core tag is published.

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
