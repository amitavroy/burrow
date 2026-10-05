# Ticket 7: State DB setup (`syncd db path`, `syncd db status`) (GH-6)

Source: `requirements.md` section 14, ticket 7 (M2 One-shot sync). Depends on 1 (done).
Done when: **`syncd db path` prints the location; tables exist.**
Final location after approval: `docs/plans/007-state-db-setup.md` (same format as `docs/plans/005-*.md`).

## Context

Tickets 1-5 are done; ticket 6 is planned (`docs/plans/006-single-file-upload.md`, GH-5) but not built. It does not block this ticket. Ticket 7 is the first step of M2: the local SQLite cache (`modernc.org/sqlite`, WAL, `goose` migrations, path via `adrg/xdg`). Drive stays the source of truth, so the DB is disposable, never synced and never inside the sync root. Ticket 8 builds the files repository on top and ticket 17 the job queue, so this ticket delivers only the opened, migrated database plus a first schema.

## Decisions

1. **Driver and migrations:** `modernc.org/sqlite` (pure Go, no cgo) with `github.com/pressly/goose/v3`, using embedded SQL files (`//go:embed migrations/*.sql`) through `goose.NewProvider`, so there is no global state and no external files to ship. Goose is chosen over golang-migrate for its embed support and plain `.sql` files.
2. **Location:** `xdg.DataFile("burrow/burrow.db")`, next to `state.json` (Local, not Roaming, on Windows). `store.DefaultPath()` returns it; `store.Open(path)` takes any path so tests use `t.TempDir()`. The default path is outside any sync root by construction.
3. **Connection setup** (applied by `Open` through DSN `_pragma`): `journal_mode(WAL)`, `busy_timeout(5000)`, `foreign_keys(on)`, `synchronous(NORMAL)`. `SetMaxOpenConns(1)`, because one goroutine owns the connection. The owner-goroutine wrapper lands with the repository in ticket 8; `Open` here returns a `*sql.DB` and documents that rule.
4. **First migration** `00001_init.sql` creates the `files` table from requirements section 6: `rel_path` (unique), `drive_file_id`, `size`, `mtime`, `inode`, `local_md5`, `synced_md5`, `base_md5`, `base_revision_id`. Ticket 8 adds CRUD; ticket 13 adds `file_revisions`; ticket 17 adds the jobs table; ticket 18 adds `sync_errors`. Each gets its own migration, so we do not guess future schemas now.
5. **Directory:** `MkdirAll(dir, 0o700)`, matching `FileStore`.
6. **CLI:** `syncd db path` prints the path only and creates nothing. `syncd db status` opens and migrates the DB, then prints the path, the migration version and the table names, which is the visible check that tables exist. A missing or unknown subcommand exits 2 with usage.
7. **`state.json` stays** for now. Folding the root folder ID into the DB is a later cleanup, out of scope here.
8. Never log SQL arguments. Nothing sensitive lives in this DB, but the rule applies from the start.

## To-do

Vertical slices; review after each.

| # | Slice | Step | Done |
| --- | --- | --- | --- |
| 1 | **Slice 1: `syncd db path`** | `internal/store/path.go`: `DefaultPath()`; `cmd/syncd/main.go`: `db` command with the `path` subcommand, usage and exit codes. **Review point (demo: prints the location).** | [x] |
| 2 | **Slice 2: DB opens and migrates** | `go get modernc.org/sqlite github.com/pressly/goose/v3`; `internal/store/store.go`: `Open(path)` with pragmas, `MaxOpenConns(1)`, 0700 dir; embedded `migrations/00001_init.sql`; goose provider `Up` | [x] |
| 3 | | Tests: Open creates the file, WAL is on, `foreign_keys` is on, migrations are idempotent (open twice), `files` has the expected columns, `rel_path` is unique. Run under `-race`. **Review point.** | [x] |
| 4 | **Slice 3: `syncd db status`** | `store.Status(db)` returns the version and table names; the `db status` command prints them; tests through `run(args, ...)` with an overridden path. **Review point (demo: tables listed).** | [x] |
| 5 | **Slice 4: docs** | README and `docs/wiki/index.md` (new "State database" section, command usage block, ADR note on goose); mark ticket 7 `=> Done` in `requirements.md`. **Review point.** | [x] |

## Files to create/modify

| File | Change |
| --- | --- |
| `internal/store/path.go` (+ test) | `DefaultPath` |
| `internal/store/store.go` (+ test) | `Open`, `Status`, goose provider |
| `internal/store/migrations/00001_init.sql` | `files` table |
| `internal/store/doc.go` | Update the package comment |
| `cmd/syncd/main.go` | `db` command with `path` and `status` |
| `cmd/syncd/main_test.go` | Cases for `db` |
| `go.mod`, `go.sum` | Add `modernc.org/sqlite`, `goose/v3` |
| `README.md`, `docs/wiki/index.md`, `requirements.md` | Docs and status |

Reused: `xdg.DataFile` as in `internal/drive/rootstore.go`, the `run(args, stdout, stderr)` dispatch and its `func name(stdout, stderr io.Writer) int` command shape, and the 0 / 1 / 2 exit-code convention.

## Testing plan

Table-driven with `t.TempDir()`; no real home dir. For the CLI, make the DB path injectable (a package var used only by tests) so `run` never touches `~/.local/share`. Verify WAL through `PRAGMA journal_mode`, and idempotent migrations by opening the same file twice and checking the version stays 1. Run `go test -race ./...`.

## Verification

1. `make vet test` passes under `-race`.
2. `bin/syncd db path` prints `~/.local/share/burrow/burrow.db` and creates nothing.
3. `bin/syncd db status` prints the path, version 1 and the `files` table (plus goose's version table); `sqlite3` on the file shows `journal_mode=wal`.
4. Run `db status` twice: same version, no errors. Delete the DB file and run again: it is recreated (disposable cache).
5. `bin/syncd db` and `bin/syncd db nope` exit 2 with usage.
