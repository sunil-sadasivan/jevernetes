#!/bin/sh
set -eu
mkdir -p dist/bin
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o dist/bin/jevernetes ./cmd/jevernetes
if [ "$(uname -s)" = Darwin ]; then
  # Finalize the local build's ad-hoc signature before first execution. Some
  # macOS hosts otherwise reject the freshly linked binary after provenance
  # evaluation even though static signature verification succeeds.
  codesign --force --sign - --timestamp=none dist/bin/jevernetes
  xattr -d com.apple.provenance dist/bin/jevernetes 2>/dev/null || true
fi
unlink dist/bin/jev 2>/dev/null || true
ln dist/bin/jevernetes dist/bin/jev
node tools/smoke.mjs
# Only explicit product files enter the binary archive.
tar -czf dist/jevernetes-binary.tar.gz -C dist/bin jevernetes jev -C ../.. LICENSE README.md
# Use the worktree file list so a verified but uncommitted tree can be packaged.
node tools/check-release.mjs --write-list dist/source-files.txt
tar -czf dist/jevernetes-source.tar.gz -T dist/source-files.txt
node tools/check-release.mjs --archive dist/jevernetes-source.tar.gz
