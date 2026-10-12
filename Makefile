VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build build-ui vet test release-check release

build:
	go build -ldflags "-X main.Version=$(VERSION)" -o bin/syncd ./cmd/syncd

# The tray app, a separate binary (needs cgo on macOS; on Windows add -H=windowsgui).
build-ui:
	go build -o bin/syncui ./cmd/syncui

# The macOS vet skips syncui: it needs cgo there (ADR-001), which a cross vet from Linux lacks.
vet:
	go vet ./...
	GOOS=windows go vet ./...
	GOOS=darwin go vet $$(go list ./... | grep -v /cmd/syncui)

test:
	go test -race ./...

# Local snapshot of the release archives in dist/. Loads the OAuth client from .env.
release-check:
	set -a; . ./.env; set +a; goreleaser check && goreleaser release --snapshot --clean

# Publishes the GitHub release for the tag at HEAD (git tag vX.Y.Z first). Needs gh signed in.
release:
	@test -z "$$(git status --porcelain)" || { echo "release: working tree is not clean"; exit 1; }
	@git describe --exact-match --tags HEAD >/dev/null 2>&1 || { echo "release: HEAD is not on a tag (git tag vX.Y.Z)"; exit 1; }
	set -a; . ./.env; set +a; GITHUB_TOKEN=$$(gh auth token) goreleaser release --clean
