# Ticket 18: Error events table (GH-18)

Source: `requirements.md` section 14, ticket 18 (M3 Robust uploads), and section 10 "UI-facing errors come from an `events` / `sync_errors` table in SQLite". Depends on 17 (job queue), done. Unblocks 36 (tray error log), which will read the same table.
Done when: **a failed upload shows as a readable line, e.g. "quota exceeded, will retry".**
Final location: `docs/plans/020-error-events-table.md` (same format as `docs/plans/archive/017-*.md`).

## To-do

Vertical slices; review after each. Every slice leaves `make vet test` green.

| # | Slice | Step | Done |
| --- | --- | --- | --- |
| 1 | **Slice 1: a failed upload shows up in `syncd errors`** | Migration `00005_sync_errors.sql` and `Repo` methods in `internal/store/errors.go`: `RecordError(SyncError)`, `ClearError(relPath)`, `ListErrors()`. `Describe(err) string` in `internal/drive/describe.go`: Drive quota, rate limit, permission, not found, server error, network error, sign-in problems, else the error text trimmed to one line. In the sync worker, a failed file calls `RecordError` (message from `Describe`, raw text, attempts); a file that syncs, is skipped as unchanged, or is dropped calls `ClearError`. New `syncd errors`: one line per file, newest first: `2026-10-11T10:02:11Z  docs/a.pdf  Drive storage quota exceeded, will retry`; prints `no errors` when empty; reads the database only. Tests: store (record twice keeps one row and counts attempts, clear, list order, survives reopen), `Describe` table (each status and reason, wrapped errors, multi-line text), sync (a failing file is recorded, a later success clears it, a dropped job clears it, a successful skip clears it), CLI output and the empty case. **Review point (demo: make one upload fail, `syncd sync`, `syncd errors` shows it; fix it, `syncd sync`, `syncd errors` says no errors).** | [x] |
| 2 | **Slice 2: unreadable files and a pointer from `sync`** | Entries the scan could not read (`Summary.Unreadable`) are recorded as errors too (`cannot read file: permission denied`) and cleared when a later scan lists the file normally. `syncd sync` ends with `see 'syncd errors' for details` on stderr when any file failed or was unreadable. Tests: unreadable entry recorded then cleared once readable (use a chmod 000 file, skipped when running as root or on Windows), the summary pointer line only when there are failures. **Review point.** | [x] |
| 3 | **Slice 3: docs** | Wiki new "Errors" section (table, what is recorded, when a row is cleared, the `Describe` messages, `syncd errors` output), README note, `internal/store/doc.go`, `internal/drive/doc.go`, `=> Done` for ticket 18 in `requirements.md`. **Review point.** | [x] |

## Context

A file that fails to upload today leaves a `failed` row in `jobs` (with the raw `last_error`), one log line, and one `failed <path>: <error>` line on stderr at the end of `syncd sync`. The raw text is often a Google API dump (`googleapi: Error 403: The user's Drive storage quota has been exceeded., storageQuotaExceeded`), and it disappears when the terminal closes. The jobs table is also the wrong home: a job is deleted when it finishes, and later work (the watcher, the tray UI) needs an error list that does not depend on queue state. Requirements section 10 already fixes the shape: the UI reads errors from SQLite, not from log files. This ticket adds that table, a mapping from raw errors to a readable message, and a `syncd errors` command to read it.

## Decisions

1. **One row per file, latest error wins.** `UNIQUE (rel_path)`; recording the same path again updates the message and counts `attempts`. A full event log of every occurrence would grow without bound and the UI wants "what is wrong now", not history. A row exists exactly while a file is failing.
2. **Cleared on success.** When a file next syncs (uploaded, updated, or skipped because Drive already has the content) or its job is dropped because the file is gone, its row is deleted. So `syncd errors` is always the current list, and "no errors" means every file is in a good state.
3. **Readable message from one function.** `drive.Describe(err)` maps known cases to a short phrase and falls back to the error text on one line. The raw text is kept in its own column for debugging (it can include the Drive message; never a request body or token, same rule as the logs). The mapping lives in `internal/drive` next to the error types it inspects (`*googleapi.Error`, `*oauth2.RetrieveError`, `net.Error`).

   | Cause | Message |
   | --- | --- |
   | 403 `storageQuotaExceeded` | `Drive storage quota exceeded` |
   | 429, 403 rate-limit reasons | `Drive rate limit hit` |
   | other 403 | `permission denied by Drive` |
   | 404 | `file or folder not found in Drive` |
   | 5xx | `Drive is unavailable (HTTP 503)` |
   | network timeout, reset, refused, EOF | `network problem` |
   | `ErrNotSignedIn`, `ErrSessionExpired` | `not signed in` / `sign-in expired` (these abort the run, see decision 5) |
   | local read error (`fs.ErrPermission`, `fs.ErrNotExist`) | `cannot read file: permission denied` / `file is gone` |
   | anything else | first line of the error text, at most 200 characters |

4. **Every line says "will retry".** Today every failed job is queued again by the next `syncd sync` (and, from ticket 21, by the daemon's next scan), so it is true for all of them, including quota and permission errors, which a user can fix. `syncd errors` prints the message plus `, will retry`. If a later ticket stops retrying some error, the `will retry` flag becomes a column then; no column now (YAGNI).
5. **Per-file errors only.** A lost sign-in or a full cancel aborts the run and is already reported by the exit code and message. Run-level errors with no file (sign-in lost in the daemon, `max_user_watches`) have no `rel_path` to key on; they come with tickets 23 and 33 to 36. The table can add a nullable key then.
6. **`syncd errors` needs no sign-in and no network.** It opens the database and prints, like `syncd history`. Order is newest first (`last_seen`). Exit code 0 even when there are errors (they are state, not a failure of this command). Timestamps are UTC RFC 3339, as in `history`.
7. **Unreadable scan entries are errors too (slice 2).** `Scan` already collects entries it could not read; they are skipped today and only printed. They get a row keyed by the entry's rel_path and are cleared when a later scan lists the file normally. A directory that cannot be read has no file list to clear against; it is keyed by the directory's rel_path and cleared when the scan can walk it.
8. **Writes ignore cancellation.** Recording and clearing use `context.WithoutCancel`, like the job outcome writes, so Ctrl+C does not lose the record of a failure that already happened.
9. **No new dependency.** `database/sql`, `errors`, `net` and `io/fs` are enough.

## Schema

```sql
-- +goose Up
CREATE TABLE sync_errors (
    id         INTEGER PRIMARY KEY,
    rel_path   TEXT    NOT NULL UNIQUE,
    message    TEXT    NOT NULL,
    detail     TEXT    NOT NULL DEFAULT '',
    attempts   INTEGER NOT NULL DEFAULT 1,
    first_seen INTEGER NOT NULL,
    last_seen  INTEGER NOT NULL
);

CREATE INDEX sync_errors_last_seen ON sync_errors (last_seen DESC);

-- +goose Down
DROP INDEX sync_errors_last_seen;
DROP TABLE sync_errors;
```

## Shape

```go
// internal/store/errors.go
type SyncError struct {
    RelPath   string
    Message   string // readable, from drive.Describe
    Detail    string // raw error text, for debugging
    Attempts  int
    FirstSeen int64  // Unix nanoseconds
    LastSeen  int64
}

func (r *Repo) RecordError(ctx context.Context, relPath, message, detail string) error // upsert, attempts+1 on conflict
func (r *Repo) ClearError(ctx context.Context, relPath string) error                  // no row is not an error
func (r *Repo) ListErrors(ctx context.Context) ([]SyncError, error)                    // newest first
```

```go
// internal/drive/describe.go
func Describe(err error) string // never empty; one line; no tokens or bodies
```

```
// syncd errors
2026-10-11T10:02:11Z  docs/a.pdf    Drive storage quota exceeded, will retry
2026-10-11T10:02:09Z  img/big.png   network problem, will retry
```

In `internal/sync/sync.go` the worker already has three outcomes per job: `fatal` (requeue, no error row), `fileErr` (FailJob, now also `RecordError`) and success (CompleteJob, now also `ClearError`). The drop branch also clears.

## Files to create/modify

| File | Change |
| --- | --- |
| `internal/store/migrations/00005_sync_errors.sql` | `sync_errors` table |
| `internal/store/errors.go` (+ `errors_test.go`) | `SyncError` and the repo methods |
| `internal/drive/describe.go` (+ `describe_test.go`) | `Describe` |
| `internal/sync/sync.go` (+ `sync_test.go`) | record on failure, clear on success, drop and skip; unreadable entries (slice 2) |
| `cmd/syncd/main.go` (+ `main_test.go`) | `errors` command; `see 'syncd errors'` pointer (slice 2) |
| `internal/store/store_test.go` | schema version 5 and table list |
| `README.md`, `docs/wiki/index.md`, `internal/store/doc.go`, `internal/drive/doc.go`, `requirements.md` | Docs and status |

## Testing plan

- Store: record then list; recording twice keeps one row and `attempts` is 2 with `first_seen` unchanged; clear removes it and clearing a missing row is fine; list is newest first; rows survive reopen.
- `Describe`: a table of errors (quota, each rate-limit reason, plain 403, 404, 429, 500 and 503, wrapped `*googleapi.Error`, timeout, `ECONNRESET`, `ErrNotSignedIn`, `ErrSessionExpired`, `fs.ErrPermission`, multi-line text trimmed to one line, an overlong message cut at 200 characters). The result is never empty and never contains `Bearer` or a newline.
- Sync: a failing file leaves one error row with the readable message; the next run, with the file now working, clears it; a changed-then-reverted file that is skipped clears it; a job dropped because the file vanished clears it; a fatal sign-in error records nothing.
- CLI: `errors` prints the lines in order, prints `no errors` when empty, needs no sign-in, rejects extra arguments (exit 2).
- Slice 2: an unreadable entry gets a row and loses it once readable; the pointer line appears on stderr only when something failed.

## Verification

1. `make vet test` under `-race`.
2. Manual: make an upload fail (revoke network or use a fake that returns the quota error), run `syncd sync`, then `syncd errors` shows `<path>  Drive storage quota exceeded, will retry`; restore it, run `syncd sync`, `syncd errors` prints `no errors`.
3. `syncd errors` works while signed out.

## Out of scope (say if you want any)

- Run-level errors that have no file (sign-in lost in the daemon, `max_user_watches`): tickets 23, 33 and 36.
- A history of every occurrence, retention limits, or `--json` output.
- A separate `will_retry` flag: add it when something stops being retried.
- The tray UI error list (ticket 36), which will read this table.
