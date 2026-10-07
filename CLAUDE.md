# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Status

Pre-code. The repo contains only `requirements.md`, the source of truth for scope, architecture and the 41-ticket build plan (section 14). Read it before starting any ticket. No Go module exists yet, so the commands below are the planned ones (ticket 1) rather than verified ones.

## Planned commands

- `go vet ./...` and `go test -race ./...` (`make vet` and `make test` run them locally; there is no CI workflow, see ADR-002; the race detector is required because of the goroutine-ownership design)
- Single test: `go test -race -run TestName ./internal/store/`
- Local debug logging: `SYNC_LOG=debug syncd run`
- CLI surface is the dev interface, built before any UI: `syncd login|logout|whoami|put|stat|scan --dry-run|sync|history|watch|run|errors|restore --root|remote ls|ctl status`

## What this is

A Go daemon (`cmd/syncd`) that watches one local sync root and mirrors it to Google Drive. Packages are `internal/sync` (watcher, debounce, queue, uploader), `internal/store` (SQLite and migrations) and `internal/drive` (client and auth). An optional tray UI (`cmd/syncui`) is a separate process that talks to the daemon over a local socket or localhost HTTP.

## Invariants that span multiple files

- **Drive is the source of truth; SQLite is a disposable cache.** Deleting the DB must lose nothing. It is rebuilt from Drive (via `appProperties`) and the local folder. Never sync or copy the DB, and never place it inside the sync root. It lives in the `adrg/xdg` data dir (Local, not Roaming, on Windows).
- **Track files by Drive file ID, never by name** (Drive allows duplicate names in a folder). Every upload writes `appProperties` `{rel_path}`. `rel_path` is relative to the root and has no machine-specific parts. A `watch_id` tag is deferred until multi-folder support exists.
- **Auth constraints:**
  - Use a user OAuth flow (loopback and PKCE), not a service account (no quota on personal Drive).
  - Scope is `drive.file` only, so the app can only see files it created. It creates and owns `MySync/` itself.
  - One OAuth client ID ships with the app and never changes, because access is tied to it.
  - The refresh token lives only in the OS keychain, never in the DB or config.
- **One goroutine owns the SQLite connection** (WAL mode); other goroutines use channels. Use a pure-Go driver (`modernc.org/sqlite`), with migrations via goose or golang-migrate from day one.
- **Safety:** deletes set `trashed: true` (never hard delete). Downloads go to a temp file and are then renamed atomically. Large files are streamed (8 MB resumable chunks, streamed MD5), never loaded whole.
- **Durability:** the job queue is persistent. A startup scan of the root, diffed against the DB, feeds the same queue, because `fsnotify` misses events while the app is closed or asleep.
- **`fsnotify` is not recursive.** Register every subfolder, including new ones. Surface `max_user_watches` exhaustion as a clear error. Rename events differ per OS, so detect renames by inode/file ID plus hash, then `files.update` with `addParents`/`removeParents`.
- **Record `base_md5`, `base_revision_id` and `file_revisions` rows from v1.** Phase 2 three-way conflict detection (local vs Drive vs base) depends on them.
- **Retries:** exponential backoff with jitter on 403, 429 and 5xx.
- **Never log tokens, auth headers or request bodies.** User-facing errors come from the `sync_errors` table, not from log parsing. Recover panics in every top-level goroutine.

## Scope guardrails

- **v1 is one-way (local to Drive) plus restore.** Two live machines on the same Drive folder is out of scope until phase 2 (two-way via the `changes` API, with keep-both conflict copies). A second machine is restore-only, or uses its own root.
- **Files must live inside the sync root.** Arbitrary-folder mapping and symlinks are deferred.
- **Open questions** are listed in `requirements.md` section 12 (tray menu vs settings window first, per-device roots, rclone stopgap). Don't assume answers.
