#!/bin/sh
set -eu
mkdir -p dist/bin
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o dist/bin/jevernetes-go ./cmd/jevernetes
if [ "$(uname -s)" = Darwin ]; then
  # Finalize the local build's ad-hoc signature before first execution. Some
  # macOS hosts otherwise reject the freshly linked binary after provenance
  # evaluation even though static signature verification succeeds.
  codesign --force --sign - --timestamp=none dist/bin/jevernetes-go
  xattr -d com.apple.provenance dist/bin/jevernetes-go 2>/dev/null || true
fi
unlink dist/bin/jev-go 2>/dev/null || true
ln dist/bin/jevernetes-go dist/bin/jev-go
node tools/smoke.mjs
python3 tests/go_tui_pty.py dist/bin/jevernetes-go
python3 tools/go-dashboard-smoke.py dist/bin/jevernetes-go
# Only explicit product files enter the binary archive.
tar -czf dist/jevernetes-go-binary.tar.gz -C dist/bin jevernetes-go jev-go -C ../.. LICENSE README.md
# Use the worktree file list so a verified but uncommitted tree can be packaged.
node tools/check-release.mjs --write-list dist/source-files.txt
tar -czf dist/jevernetes-source.tar.gz -T dist/source-files.txt
node tools/check-release.mjs --archive dist/jevernetes-source.tar.gz
