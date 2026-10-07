# Ticket 8: Files repository (`store.Repo`, CRUD over one owner goroutine) (GH-7)

Source: `requirements.md` section 14, ticket 8 (M2 One-shot sync). Depends on 7 (done).
Done when: **tests pass under `-race`.**
Final location after approval: `docs/plans/008-files-repository.md` (same format as `docs/plans/007-*.md`).

## To-do

Vertical slices; each one is demoable through its tests, so review after each.

| # | Slice | Step | Done |
| --- | --- | --- | --- |
| 1 | **Slice 1: write and read one row** | `internal/store/file.go`: `File` type, `ErrNotFound`. `internal/store/repo.go`: `Repo` with the owner goroutine, `OpenRepo(path)`, `Upsert`, `Get`, `Close`, panic recovery. Tests: round trip, upsert updates the same row, `Get` of a missing path returns `ErrNotFound`, NULL mapping, `Close` then call returns `ErrClosed`. **Review point.** | [x] |
| 2 | **Slice 2: look up and list** | Add the unique partial index on `drive_file_id` to `00001_init.sql` (pre-release, so no second migration); `GetByDriveID`, `List` (ordered by `rel_path`). Tests for both methods and the unique index. **Review point.** | [x] |
| 3 | **Slice 3: delete** | `Delete(relPath)` returning `ErrNotFound` when absent. Tests: delete then `Get` fails, `List` shrinks, deleting twice errors. **Review point.** | [x] |
| 4 | **Slice 4: concurrency and cancellation** | Tests under `-race`: many goroutines calling `Upsert`/`Get` together, a cancelled context returns without hanging, a panic in the owner goroutine is returned as an error and later calls still work, `Close` is safe to call twice. **Review point.** | [x] |
| 5 | **Slice 5: docs** | Wiki "State database" section (repository, owner goroutine rule, NULL mapping, unique Drive ID index), README line only if a user-visible fact changed, `internal/store/doc.go`; mark ticket 8 `=> Done` in `requirements.md`. **Review point.** | [x] |

## Context

Ticket 7 delivered the opened, migrated database and the first `files` table (`rel_path` unique, `drive_file_id`, `size`, `mtime`, `inode`, `local_md5`, `synced_md5`, `base_md5`, `base_revision_id`). `store.Open` returns a bare `*sql.DB` capped at one connection and documents that one goroutine must own it. This ticket builds that owner. Later tickets depend on it: 11 (one-shot sync) stores Drive IDs, 12 (MD5 skip) reads `synced_md5`, 13 (revisions) writes `base_*`, 17 (queue) adds its own table through the same owner, 24 and 25 (inode, rename) read and rewrite rows.

Drive stays the source of truth, so every row is rebuildable cache. The repository therefore never needs to defend against "the only copy" scenarios; it needs to be small, race-free and hard to misuse.

## Decisions

1. **Typed methods over a channel.** `Repo` starts one goroutine that owns the `*sql.DB`. Each method builds a request, sends it on a channel and waits on a per-request reply channel. Callers never see channels, SQL or `*sql.DB`. The alternative (a generic `Do(func(*sql.DB))`) is rejected because it leaks the connection into every caller and lets later tickets bypass the repository.
2. **Key is `rel_path`.** `Upsert` is `INSERT ... ON CONFLICT(rel_path) DO UPDATE`, so one method covers create and update and callers do not need a read first. The surrogate `id` column stays internal and is not exposed on `File`. Tracking by Drive file ID is supported through `GetByDriveID`; names are never the identity, since `rel_path` is only the local key and the Drive ID is set once known.
3. **`File` type** (`internal/store/file.go`):
   - `RelPath string` (slash form, relative to the root, no machine-specific parts)
   - `DriveFileID string`
   - `Size int64`
   - `MTime int64` (Unix nanoseconds, so equality checks are exact)
   - `Inode uint64` (0 means unknown)
   - `LocalMD5`, `SyncedMD5`, `BaseMD5`, `BaseRevisionID string`
4. **NULL mapping.** Empty strings and a zero `Inode` are stored as NULL and read back as the zero value, so "no Drive ID yet" is a real NULL and the unique index on `drive_file_id` ignores unuploaded rows. Done in two small helpers in `repo.go`.
5. **Index in `00001_init.sql`**: `CREATE UNIQUE INDEX files_drive_file_id ON files (drive_file_id) WHERE drive_file_id IS NOT NULL`, added to the first migration rather than a second one because the project is pre-release. Drive IDs are unique, a duplicate means a bug, and `GetByDriveID` needs the index anyway. A dev DB created before this change must be deleted (it is a disposable cache) to get the index.
6. **Errors.** `ErrNotFound` for `Get`, `GetByDriveID` and `Delete` of a missing row; `ErrClosed` after `Close`. Wrapped with `%w`, never include argument values in messages beyond `rel_path`.
7. **Context.** Every method takes a `context.Context`. A cancelled context is honoured while sending and while waiting for the reply; the owner goroutine still finishes the statement it started, so the database is never left mid-write.
8. **Panic recovery.** The owner loop recovers a panic inside a request, replies with an error, and keeps serving, as CLAUDE.md requires for every top-level goroutine. Recovered panics are logged with `slog` (message only, no arguments) once ticket 14 adds logging; until then they are returned as the error.
9. **`Close`** stops accepting requests, drains the goroutine, closes the `*sql.DB`, and is safe to call more than once.
10. **No CLI in this ticket.** The ticket row asks only for passing tests. Ticket 11 is the first real consumer.
11. **Constructors.** `OpenRepo(path)` calls `Open` and starts the goroutine. `Open` stays public for `db status`, which needs a one-shot connection and no goroutine.
12. Never log SQL arguments.

## Shape

```go
// internal/store/repo.go
type Repo struct {
	reqs chan request
	done chan struct{}
	once sync.Once
}

func OpenRepo(path string) (*Repo, error)
func (r *Repo) Upsert(ctx context.Context, f File) error
func (r *Repo) Get(ctx context.Context, relPath string) (File, error)
func (r *Repo) GetByDriveID(ctx context.Context, id string) (File, error)
func (r *Repo) List(ctx context.Context) ([]File, error)
func (r *Repo) Delete(ctx context.Context, relPath string) error
func (r *Repo) Close() error

// request is run on the owner goroutine; reply carries the result back.
type request struct {
	run   func(*sql.DB) (any, error)
	reply chan result
}
```

The `request.run` closure is private to the package; only the typed methods build one, so no caller ever receives the `*sql.DB`.

## Files to create/modify

| File | Change |
| --- | --- |
| `internal/store/file.go` | `File`, `ErrNotFound`, `ErrClosed` |
| `internal/store/repo.go` (+ `repo_test.go`) | `Repo`, owner goroutine, methods |
| `internal/store/migrations/00001_init.sql` | Add unique partial index |
| `internal/store/doc.go` | Mention `Repo` |
| `docs/wiki/index.md`, `requirements.md` | Docs and status |

Reused: `store.Open` and its pragmas, `t.TempDir()` test helpers from `store_test.go`.

## Testing plan

Table-driven where it fits, always on `t.TempDir()` databases, all under `go test -race ./...`.

- Round trip of every field, including empty optional fields reading back as the zero value and stored as NULL (checked with a raw query on a second `Open` of the same file after `Close`).
- Upsert twice with the same `rel_path` leaves one row with the new values.
- `Get`, `GetByDriveID`, `Delete` of a missing row return `ErrNotFound` (`errors.Is`).
- Two rows with the same non-empty `drive_file_id` fail; any number with an empty one succeed.
- `List` order is by `rel_path`.
- 50 goroutines mixing `Upsert` and `Get`: no race, final row count correct.
- A cancelled context returns promptly with `context.Canceled`.
- A panic in a request (test-only hook that panics inside `run`) returns an error and the next call works.
- `Close` twice is fine; any method after `Close` returns `ErrClosed`.

## Verification

1. `make vet test` passes under `-race`.
2. `go test -race -count=20 ./internal/store/` is stable (no flakes from the goroutine hand-off).
3. `bin/syncd db status` shows version 1 and the `files` table; delete the old dev DB first so it is recreated with the index.
4. Delete `burrow.db` and run `db status` again: it is recreated, which keeps the cache disposable.
