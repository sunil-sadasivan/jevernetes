#!/bin/sh
# Offline ABI regression gate: SQLite native allocations must compile on 32-bit too.
set -eu
export GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local CGO_ENABLED=0 GOOS=linux
mkdir -p .cache/cross
for arch in amd64 386 arm; do
  GOARCH="$arch" go build -trimpath -ldflags='-s -w' -o ".cache/cross/jevernetes-$arch" ./cmd/jevernetes
  file ".cache/cross/jevernetes-$arch"
done
