#!/bin/sh
set -eu
[ -z "$(gofmt -l cmd internal examples third_party/spdystream)" ]
go mod tidy
git diff --exit-code -- go.mod go.sum
go test -C third_party/spdystream ./...
go test ./...
go test -C third_party/spdystream -race ./...
go test -race ./...
go vet -C third_party/spdystream ./...
go vet ./...
tools/check-cross.sh
for file in tests/*_ui.cjs; do node "$file"; done
python3 tools/check_native_ui.py
mkdir -p .runs/native-ui-check
go build -o .runs/native-ui-check/jevernetes-go ./cmd/jevernetes
python3 tests/go_tui_pty.py .runs/native-ui-check/jevernetes-go
python3 tools/go-dashboard-smoke.py .runs/native-ui-check/jevernetes-go
python3 -m unittest discover -s tests -q
python3 tools/check_release.py
python3 -m unittest discover -s tools -p test_retained_parity.py -q
python3 tools/check_parity.py
node tools/check-release.mjs
git diff --check
