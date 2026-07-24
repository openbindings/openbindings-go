#!/usr/bin/env bash
set -euo pipefail

version="${1:-}"
if [[ ! "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([+-][0-9A-Za-z.-]+)?$ ]]; then
  echo "usage: $0 vX.Y.Z" >&2
  exit 2
fi

root="github.com/openbindings/openbindings-go"
formats=(asyncapi connect graphql grpc mcp openapi operationgraph usage)
scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT

export GOWORK=off

go list -m "${root}@${version}" >/dev/null
for format in "${formats[@]}"; do
  go list -m "${root}/formats/${format}@${version}" >/dev/null
done

cd "$scratch"
go mod init example.com/openbindings-release-consumer >/dev/null
go get "${root}@${version}"
for format in "${formats[@]}"; do
  go get "${root}/formats/${format}@${version}"
done

{
  echo 'package main'
  echo 'import ('
  echo "  _ \"${root}\""
  for format in "${formats[@]}"; do
    echo "  _ \"${root}/formats/${format}\""
  done
  echo ')'
  echo 'func main() {}'
} > main.go

go build .
echo "verified public OpenBindings Go modules at ${version}"
