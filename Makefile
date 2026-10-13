VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.DEFAULT_GOAL := help
.PHONY: help build build-ui vet test release-check release purge

help: ## Show this list of commands
	@awk 'BEGIN {FS = ":.*## "} /^[a-z-]+:.*## / {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build the daemon to bin/syncd
	go build -ldflags "-X main.Version=$(VERSION)" -o bin/syncd ./cmd/syncd

# The tray app, a separate binary (needs cgo on macOS; on Windows add -H=windowsgui).
build-ui: ## Build the tray app to bin/syncui
	go build -o bin/syncui ./cmd/syncui

# The macOS vet skips syncui: it needs cgo there (ADR-001), which a cross vet from Linux lacks.
vet: ## Run go vet for Linux, Windows and macOS
	go vet ./...
	GOOS=windows go vet ./...
	GOOS=darwin go vet $$(go list ./... | grep -v /cmd/syncui)

test: ## Run all tests with the race detector
	go test -race ./...

# Loads the OAuth client from .env.
release-check: ## Build a local snapshot of the release archives in dist/
	set -a; . ./.env; set +a; goreleaser check && goreleaser release --snapshot --clean

# Needs gh signed in.
release: ## Publish the GitHub release for the tag at HEAD (git tag vX.Y.Z first)
	@test -z "$$(git status --porcelain)" || { echo "release: working tree is not clean"; exit 1; }
	@git describe --exact-match --tags HEAD >/dev/null 2>&1 || { echo "release: HEAD is not on a tag (git tag vX.Y.Z)"; exit 1; }
	set -a; . ./.env; set +a; GITHUB_TOKEN=$$(gh auth token) goreleaser release --clean

# Never touches the sync folder or Drive. Needs sudo for the apt step.
purge: ## Remove every trace: service, tray, token, app data, the .deb, local builds
	-$$(command -v syncd || echo bin/syncd) uninstall --purge
	-pkill -x syncui
	-pkill -x syncd
	-sudo apt-get remove --purge -y burrow
	rm -rf bin dist
