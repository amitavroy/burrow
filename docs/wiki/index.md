# Burrow wiki

## ADR

Architecture decision records. Newest last. Each record states the context, the decision and its consequences.

### ADR-001: Separate headless daemon and optional tray UI

- **Status:** Accepted
- **Date:** 2026-10-02

**Context**

The first design was written for Rust, where a separate daemon and UI was an obvious split. Go is lighter, so the question was whether one process (daemon with the tray inside) would be enough.

The reason for the split is not the language. The UI toolkit drives memory use, not the sync engine. `fyne.io/systray` also needs cgo on macOS and system libraries on Linux, while the rest of the stack is pure Go (`modernc.org/sqlite`, no C compiler needed).

**Decision**

Keep two processes:

- `cmd/syncd`: the headless sync daemon, always running (launchd, systemd user service or Windows startup entry). It contains the whole sync engine and is built and tested through the CLI.
- `cmd/syncui`: an optional tray app. It is a separate process that talks to the daemon over a local socket or localhost HTTP (the control API, ticket 33). It is built last (tickets 35 and 36).

**Consequences**

- `syncd` stays pure Go and cross-compiles without cgo.
- A UI crash or quit does not stop syncing, and sync runs with no desktop session.
- The control API is needed anyway for `syncd ctl status`, so the split adds little extra work.
- Tickets 1 to 32 are daemon and CLI only and do not depend on this decision.
- If a single binary is wanted later, only tickets 33 to 36 change. The cost would be cgo in the main binary and a UI crash taking sync down.

### ADR-002: No CI workflow; run checks locally

- **Status:** Accepted
- **Date:** 2026-10-03

**Context**

Ticket 1 originally included a GitHub Actions workflow running `go vet ./...` and `go test -race ./...` on every push and PR, with "CI is green" as part of its acceptance criterion.

This is a single-developer project and the checks are fast, so a hosted CI workflow adds setup and upkeep without adding much safety.

**Decision**

Do not add a CI workflow. The same checks run locally through the Makefile:

- `make vet` runs `go vet ./...`.
- `make test` runs `go test -race ./...`. The race detector stays required because of the goroutine-ownership design.

Ticket 1 is done when `syncd version` prints a version and `make vet test` passes locally.

**Consequences**

- Nothing enforces the checks on push, so they must be run before committing or merging.
- A workflow can be added later without touching the code. The Makefile targets are the single definition of the checks, so a workflow would only call them.
- `requirements.md` (ticket 1 row), `CLAUDE.md` and the GH-1 plan doc were updated to match.
- The GitHub issue for ticket 1 still mentions CI and needs the same update.
- Ticket 37 (release builds) is unaffected. It still uses a GitHub release as the distribution point.

## Development

How to build and check the project locally. There is no CI (see ADR-002), so run the checks before committing or merging.

| Command | What it does |
| --- | --- |
| `make build` | Builds `bin/syncd` with `Version` set from `git describe --tags --always --dirty` |
| `make vet` | `go vet ./...` |
| `make test` | `go test -race ./...` (race detector required) |

```
syncd <command>
  version  -> prints Version (a git describe string, or "dev" with a plain go build)
  (none)   -> usage on stderr, exit 2
  unknown  -> error on stderr, exit 2
```

Layout: `cmd/syncd` (CLI, plain `os.Args` switch, no framework), `internal/sync`, `internal/store` and `internal/drive` (package stubs for now). `run(args, stdout, stderr) int` holds the logic so it can be tested without spawning a process.
