#!/usr/bin/env bash
set -euo pipefail

# Qualify unreleased adapters without changing their coordinated-release targets.
core_version="${1:-}"
client_version="${2:-}"
for version in "$core_version" "$client_version"; do
  if [[ ! "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+-(0\.)?[0-9]{14}-[0-9a-f]{12}$ ]]; then
    echo "usage: bash scripts/verify-openapi-candidate.sh CORE_PSEUDOVERSION CLIENT_PSEUDOVERSION" >&2
    exit 2
  fi
done

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
candidate_dir="$(mktemp -d)"
trap 'rm -rf "$candidate_dir"' EXIT
export GOWORK=off
export OB_CORPUS_REQUIRED=1
export OB_SPEC_CORPUS="${OB_SPEC_CORPUS:-$repo_dir/../spec/conformance}"
if [[ ! -d "$OB_SPEC_CORPUS/binding-specs" ]]; then
  echo "set OB_SPEC_CORPUS to the exact candidate spec conformance directory" >&2
  exit 2
fi

core_module=github.com/openbindings/openbindings-go
client_module=github.com/openbindings/openapi-client/go
go list -m "$core_module@$core_version"
go list -m "$client_module@$client_version"

for adapter in openapi usage; do
  (
    cd "$repo_dir/formats/$adapter"
    candidate_mod="$candidate_dir/$adapter.mod"
    cp go.mod "$candidate_mod"
    cp go.sum "$candidate_dir/$adapter.sum"
    cp go.mod "$candidate_dir/$adapter.source.mod"
    cp go.sum "$candidate_dir/$adapter.source.sum"
    go mod edit -modfile="$candidate_mod" \
      -replace="$core_module@v0.2.0=$core_module@$core_version"
    if [[ "$adapter" == openapi ]]; then
      go mod edit -modfile="$candidate_mod" \
        -replace="$client_module@v0.1.0=$client_module@$client_version"
    fi
    go mod tidy -modfile="$candidate_mod"
    local_replacements="$(go list -modfile="$candidate_mod" -mod=readonly -m \
      -f '{{if .Replace}}{{if not .Replace.Version}}{{.Path}} => {{.Replace.Path}}{{end}}{{end}}' all | sed '/^$/d')"
    if [[ -n "$local_replacements" ]]; then
      echo "candidate verification refuses local replacements: $local_replacements" >&2
      exit 1
    fi
    cp "$candidate_mod" "$candidate_dir/$adapter.readonly.mod"
    cp "$candidate_dir/$adapter.sum" "$candidate_dir/$adapter.readonly.sum"
    go test -modfile="$candidate_mod" -mod=readonly -race -count=1 ./...
    go build -modfile="$candidate_mod" -mod=readonly ./...
    cmp "$candidate_mod" "$candidate_dir/$adapter.readonly.mod"
    cmp "$candidate_dir/$adapter.sum" "$candidate_dir/$adapter.readonly.sum"
    cmp go.mod "$candidate_dir/$adapter.source.mod"
    cmp go.sum "$candidate_dir/$adapter.source.sum"
    echo "verified standalone candidate: formats/$adapter (source manifests unchanged)"
  )
done
