# Contributing to openbindings-go

## Workflow

The branch changes land on is set by the project catalog
(`openbindings/project`, `repositories.json`, `integrationRef`); for the 0.2
preparation it is `release/0.2`.

1. Branch from the integration ref: `git checkout -b <type>/<short-description> origin/release/0.2`.
   Types: `fix`, `feat`, `docs`, `chore`, `refactor`.
2. Commit and push.
3. `gh pr create --fill --base release/0.2`.
4. Squash-merge when CI is green (`gh pr merge --squash --auto --delete-branch`).

All changes land via squash-merged PRs. No direct commits to the integration
ref.

## Working on this repo

The repository is a single Go module: the core SDK. It carries only what the
core specification defines; see the README's "Scope, and the rebuild" for the
layers removed on 2026-09-24 and the `legacy/pre-core-rebuild` branch that
preserves them.

## Testing

```bash
go test ./...
```

The core conformance corpus lives in the spec repository. Check it out
alongside this one (at `../spec`), or point `OB_SPEC_CORPUS` at its
`conformance` directory; without it the corpus tests skip. Set
`OB_CORPUS_REQUIRED=1` to make a missing corpus fail instead, as CI does.

## Releasing

See [RELEASING.md](RELEASING.md) for tags, changelog conventions, and the
pre-1.0 version policy.

## Spec compatibility

This SDK declares which spec versions it supports (§8.1) via:

- `openbindings.SupportedVersions`, the supported set, and
  `openbindings.IsSupportedVersion(v)`, which decides membership
- `openbindings.AuthoringVersion`, the version a document written with the
  SDK declares

Located in `version.go`. When the spec bumps, update them in the same PR that
adds support for the new version.

## Broader context

This repo is part of the openbindings-project. See the monorepo-wide
orientation doc at `ob-pj/CLAUDE.md` (local to contributor machines) for
cross-repo conventions, release flow, and the "spec doesn't privilege any
implementation" principle.
