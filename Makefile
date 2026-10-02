VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build vet test

build:
	go build -ldflags "-X main.Version=$(VERSION)" -o bin/syncd ./cmd/syncd

vet:
	go vet ./...

test:
	go test -race ./...
