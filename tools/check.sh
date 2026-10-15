#!/bin/sh
set -eu
# Keep nested modules and offline cross-builds on the selected patched toolchain.
export PATH="$(go env GOROOT)/bin:$PATH"
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
for file in internal/investigation/web/*.js internal/dashboard/web/*.js; do node --check "$file"; done
for file in tests/*_ui.cjs; do node "$file"; done
mkdir -p .runs/native-ui-check
go build -o .runs/native-ui-check/jevernetes ./cmd/jevernetes
python3 tests/go_tui_pty.py .runs/native-ui-check/jevernetes
python3 tools/go-dashboard-smoke.py .runs/native-ui-check/jevernetes
python3 -m unittest discover -s tests -p test_native_release.py -q
python3 tools/check_parity.py
node tools/check-release.mjs
git diff --check
