VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build build-ui vet test

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
