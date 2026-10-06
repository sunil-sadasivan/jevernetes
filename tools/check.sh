#!/bin/sh
set -eu
[ -z "$(gofmt -l cmd internal)" ]
go mod tidy
git diff --exit-code -- go.mod go.sum
go test ./...
go test -race ./...
go vet ./...
tools/check-cross.sh
for file in tests/*_ui.cjs; do node "$file"; done
node tools/check-release.mjs
git diff --check
