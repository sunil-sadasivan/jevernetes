.PHONY: build check release
build:
	go build -o ./jevernetes ./cmd/jevernetes
check:
	tools/check.sh
release:
	tools/release.sh
