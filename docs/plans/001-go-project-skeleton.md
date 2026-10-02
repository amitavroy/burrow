# Go Project Skeleton (GH-1)

Source: `requirements.md` section 14, ticket 1 (M1 Hello Drive). Issue: https://github.com/amitavroy/burrow/issues/1

## To-do

Slices are vertical: review and confirm after each one.

| # | Slice | Step | Done |
| --- | --- | --- | --- |
| 1 | **Slice 1: `syncd version` works end to end** | `go mod init github.com/amitavroy/burrow`; pin the Go version in `go.mod` | [ ] |
| 2 | | Add `cmd/syncd/main.go` with a `version` subcommand and a `Version` var set via `-ldflags` | [ ] |
| 3 | | Add `Makefile` with `build`, `vet`, `test` targets (build writes `bin/syncd`); add `.gitignore` for `bin/` | [ ] |
| 4 | | Demo: `make build && ./bin/syncd version` prints a version. **Review point.** | [ ] |
| 5 | **Slice 2: layout and green CI** | Add `internal/{sync,store,drive}/doc.go` (package stubs, no logic) | [ ] |
| 6 | | Add a smoke test (e.g. `cmd/syncd` version output) so `go test -race ./...` runs a real test | [ ] |
| 7 | | Add `.github/workflows/ci.yml` running `go vet ./...` and `go test -race ./...` on push and PR | [ ] |
| 8 | | Demo: CI is green on the pushed branch. **Review point.** | [ ] |

## Context

The repo currently holds only `requirements.md` and `CLAUDE.md`. Every later ticket (auth, SQLite store, Drive client, watcher) needs the module and package layout from the requirements (section 11) to exist first. This ticket adds that skeleton and nothing else: no Drive, OAuth, SQLite or watcher code.

## Decisions

1. **Module path `github.com/amitavroy/burrow`.** Matches the `origin` remote, so imports resolve without rewrites later.
2. **One module, split by package from day one** (requirements section 11): `cmd/syncd`, `internal/sync`, `internal/store`, `internal/drive`. `cmd/syncui` is deferred to ticket 35.
3. **No CLI framework yet.** `syncd` dispatches on `os.Args[1]` with a plain switch. Later subcommands (`login`, `put`, `sync`, ...) are added one ticket at a time. Revisit if the surface gets unwieldy; this avoids a dependency the requirements do not list.
4. **Version via `-ldflags`.** `var Version = "dev"` in `main`, overridden with `-ldflags "-X main.Version=$(VERSION)"`, where the Makefile derives `VERSION` from `git describe --tags --always --dirty`. A plain `go build` still prints `dev`.
5. **No third-party dependencies in this ticket.** `go.mod` has only the module and Go version lines. Dependencies arrive with the tickets that use them.
6. **CI runs exactly what CLAUDE.md says:** `go vet ./...` and `go test -race ./...`. The race detector is required by the goroutine-ownership design. The Go version comes from `go.mod` (`go-version-file`), so there is one place to bump it.
7. **Stub packages carry a `doc.go`** with a package comment describing the package's job. This makes the packages compile and show up in `go vet`, without inventing APIs.

## Layout

```
go.mod
Makefile
.gitignore
cmd/syncd/main.go
cmd/syncd/main_test.go
internal/sync/doc.go
internal/store/doc.go
internal/drive/doc.go
.github/workflows/ci.yml
```

`cmd/syncd/main.go` (shape):

```go
package main

import (
	"fmt"
	"io"
	"os"
)

// Version is overridden at build time via -ldflags "-X main.Version=...".
var Version = "dev"

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: syncd <command>")
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Fprintln(stdout, Version)
		return 0
	default:
		fmt.Fprintf(stderr, "syncd: unknown command %q\n", args[0])
		return 2
	}
}
```

`run` takes writers and returns an exit code so it is testable without spawning a process.

`Makefile` (shape):

```make
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build vet test
build:
	go build -ldflags "-X main.Version=$(VERSION)" -o bin/syncd ./cmd/syncd
vet:
	go vet ./...
test:
	go test -race ./...
```

`.github/workflows/ci.yml` (shape): trigger on `push` and `pull_request`; steps are `actions/checkout`, `actions/setup-go` with `go-version-file: go.mod`, `go vet ./...`, `go test -race ./...`.

## Files to create/modify

| File | Change |
| --- | --- |
| `go.mod` | Create (`go mod init`), module `github.com/amitavroy/burrow` |
| `cmd/syncd/main.go` | Create: `version` subcommand, `Version` var |
| `cmd/syncd/main_test.go` | Create: tests for `run` |
| `internal/sync/doc.go` | Create: package stub |
| `internal/store/doc.go` | Create: package stub |
| `internal/drive/doc.go` | Create: package stub |
| `Makefile` | Create: `build`, `vet`, `test` |
| `.gitignore` | Create: ignore `bin/` |
| `.github/workflows/ci.yml` | Create: vet and race tests |
| `CLAUDE.md` | Update "Status" and "Planned commands": the module now exists and these commands are verified |

## Testing plan

Table-driven test of `run` in `cmd/syncd/main_test.go`:

- `version` prints `Version` plus a newline to stdout and returns 0.
- No arguments prints usage to stderr and returns 2.
- Unknown command prints an error to stderr and returns 2.

## Verification

1. `make build && ./bin/syncd version` prints a version (a `git describe` string, or `dev` with a plain `go build`).
2. `go vet ./...` is clean.
3. `go test -race ./...` passes.
4. Push the branch: the CI workflow runs and is green (the ticket's acceptance criterion).
