#!/bin/sh
set -eu
[ -z "$(gofmt -l cmd internal examples)" ]
go mod tidy
git diff --exit-code -- go.mod go.sum
go test ./...
go test -race ./...
go vet ./...
tools/check-cross.sh
for file in tests/*_ui.cjs; do node "$file"; done
python3 -m unittest discover -s tests -q
python3 tools/check_release.py
python3 tools/check_parity.py
node tools/check-release.mjs
git diff --check
