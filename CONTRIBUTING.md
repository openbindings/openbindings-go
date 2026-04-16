# Contributing to openbindings-go

## Workflow

1. Branch from `main`: `git checkout -b <type>/<short-description>`.
   Types: `fix`, `feat`, `docs`, `chore`, `refactor`.
2. Commit and push.
3. `gh pr create --fill --base main`.
4. Squash-merge when CI is green (`gh pr merge --squash --auto --delete-branch`).

All changes land on `main` via squash-merged PRs. No direct commits to `main`.

## Testing

```bash
# Core SDK
go test ./...

# Each format sub-module
for d in formats/*/; do (cd "$d" && go test ./...) || exit 1; done
```

## Releasing

This is a Go multi-module monorepo. Each module tags independently.

- Core SDK: `git tag vX.Y.Z && git push origin vX.Y.Z`
- Format sub-module: `git tag formats/<name>/vX.Y.Z && git push origin formats/<name>/vX.Y.Z`

`pkg.go.dev` auto-discovers tags; no publish step needed.

Pre-1.0, minor versions may include breaking changes; document under **Changed**
or **Removed** in `CHANGELOG.md`. When bumping `MaxTestedVersion` in
`version.go`, call that out in the CHANGELOG entry.

## Spec compatibility

This SDK declares which spec versions it supports via:

- `openbindings.MinSupportedVersion` / `openbindings.MaxTestedVersion` (constants)
- `openbindings.SupportedRange()` / `openbindings.IsSupportedVersion(v)`

Located in `version.go`. When the spec bumps, update these constants in the
same PR that adds support for the new version.

## Broader context

This repo is part of the openbindings-project. See the monorepo-wide
orientation doc at `ob-pj/CLAUDE.md` (local to contributor machines) for
cross-repo conventions, release flow, and the "spec doesn't privilege any
implementation" principle.
