#!/usr/bin/env bash
set -euo pipefail

version="${1:-}"
if [[ ! "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([+-][0-9A-Za-z.-]+)?$ ]]; then
  echo "usage: $0 vX.Y.Z" >&2
  exit 2
fi

root="github.com/openbindings/openbindings-go"
scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT

export GOWORK=off

go list -m "${root}@${version}" >/dev/null

cd "$scratch"
go mod init example.com/openbindings-release-consumer >/dev/null
go get "${root}@${version}"

{
  echo 'package main'
  echo "import _ \"${root}\""
  echo 'func main() {}'
} > main.go

go build .
echo "verified public OpenBindings Go module at ${version}"
