# Contributing to openbindings-go

## Workflow

1. Branch from `main`: `git checkout -b <type>/<short-description>`.
   Types: `fix`, `feat`, `docs`, `chore`, `refactor`.
2. Commit and push.
3. `gh pr create --fill --base main`.
4. Squash-merge when CI is green (`gh pr merge --squash --auto --delete-branch`).

All changes land on `main` via squash-merged PRs. No direct commits to `main`.

## Working on this repo

The repo is a multi-module workspace: the core SDK at the root plus one
sub-module per format under `formats/`. `go.work` is gitignored, so bootstrap
a local workspace once after cloning:

```bash
go work init . formats/*/
ver=$(awk '/github.com\/openbindings\/openbindings-go v/ {print $NF}' formats/openapi/go.mod)
go work edit -replace "github.com/openbindings/openbindings-go@${ver}=."
```

The version-pinned `replace` is required, not a nicety: the format modules
require the core at a version that is not tagged yet, and module-graph
loading still fetches that version's go.mod even under `go work use`
("unknown revision" otherwise). CI constructs the same workspace per module
row (see `.github/workflows/ci.yml`); the replace is harmless after the tag
lands — it then maps the tagged version to your checkout.

After a `go` directive bump in the modules, refresh an existing workspace
with `go work use . formats/*/` (it re-syncs `go.work`'s own `go` line); a
stale `go.work` fails loudly otherwise.

## Testing

```bash
# Core SDK
go test ./...

# Each format sub-module
for d in formats/*/; do (cd "$d" && go test ./...) || exit 1; done
```

## Releasing

See [RELEASING.md](RELEASING.md) for tags, ordering, changelog conventions,
and the pre-1.0 version policy.

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
