# Releasing openbindings-go

This repository is a single Go module: the core SDK at the repository root. It
has no upstream tag prerequisites.

The eight `formats/*` modules that used to live here were removed on
2026-09-24 (preserved on the `legacy/pre-core-rebuild` branch). Their published
`formats/<name>/v0.1.0` tags remain on the remote and resolvable through the Go
module proxy; do not delete or move them. When a rebuilt binding module lands,
this document gains its tagging and ordering rules again.

## Tags

- Core SDK: `vX.Y.Z`

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
