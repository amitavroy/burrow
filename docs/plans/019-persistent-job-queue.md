# Ticket 17: Persistent job queue (GH-17)

Source: `requirements.md` section 14, ticket 17 (M3 Robust uploads), and section 7 "Durability". Depends on 8 and 12, done; retries (ticket 16) are in. Unblocks 18 (error events), 21 (foreground daemon) and 22 (startup reconciliation), which all feed or read this queue.
Done when: **kill the process mid-sync; on restart it resumes.**
Final location: `docs/plans/019-persistent-job-queue.md` (same format as `docs/plans/archive/017-*.md`).

## To-do

Vertical slices; review after each. Every slice leaves `make vet test` green.

| # | Slice | Step | Done |
| --- | --- | --- | --- |
| 1 | **Slice 1: sync goes through a durable queue (one worker)** | Migration `00004_jobs.sql` and `Repo` methods in `internal/store/jobs.go`: `EnqueueJob(rel_path)`, `ClaimJob()`, `CompleteJob(id)`, `FailJob(id, msg)`, `RequeueJob(id)`, `ResetRunningJobs()`, `CountJobs()`. In `internal/sync`, split the per-file body of `Sync` into `process(job)` and make `Sync` = reset running jobs, scan, enqueue every entry, drain with one worker. A job whose file is gone is completed and dropped. A cancelled or sign-in-aborted job goes back to `pending`. `syncd sync` prints `resuming N queued jobs` when it starts with leftovers. Log lines carry the real job ID in place of the per-run counter. Tests: store (enqueue is idempotent, claim order, claim is atomic, reset, fail keeps the message, `-race`); sync (existing tests still pass unchanged, leftover jobs resume without a re-scan, cancel mid-run leaves pending jobs, a vanished file is dropped, a failed job is retried by the next run). **Review point (demo: Ctrl+C or kill a `syncd sync` on a big tree, rerun, it resumes and finishes).** | [x] |
| 2 | **Slice 2: bounded concurrency** | `Sync` takes a worker count; `syncd sync --workers N` (default 3, min 1, max 16). Make `Uploader` safe for concurrent use: `connect` guarded once, `ensureDir` single-flight per folder so two workers never create the same folder, folder cache writes stay on the repo's owner goroutine. `Summary` counters and `report` become safe for concurrent workers (one mutex). First fatal error (sign-in lost, repo failure, cancel) stops all workers and requeues their jobs. Tests under `-race`: N workers upload N files in parallel (fake blocks until all arrive), the same new folder is created once for files in it, fatal error stops the others and leaves their jobs pending, summary adds up. **Review point (demo: `--workers 1` vs `--workers 4` on 200 small files, same result, and faster).** | [x] |
| 3 | **Slice 3: docs** | Wiki new "Job queue" section (table, states, resume rules, worker count), README note on `--workers` and resuming, `internal/store/doc.go`, `internal/sync/doc.go`, `=> Done` for ticket 17 in `requirements.md`, remove the "single-flight guard belongs in ticket 17" note in the wiki. **Review point.** | [x] |

## Context

`Sync` already survives an interrupt in a limited way: each uploaded file gets its `files` row straight away, so the next run's scan finds the rest. That is a re-scan, not a queue. There is no record of work that was decided but not yet done, nothing a watcher (tickets 19 to 21) can add to while the worker runs, and no place to hold a failure for ticket 18 to show. The upload loop is also strictly sequential, which is slow for many small files under Drive's per-request latency. This ticket adds the queue, drains it with a small pool, and keeps `syncd sync` behaving the same from the outside.

## Decisions

1. **The queue sits between scan and upload.** Scan decides what might need work and enqueues every non-ignored file; the job handler keeps the existing skip logic (size and mtime match, then streamed MD5), so a no-op file costs one row lookup, as today. Filtering at enqueue time would duplicate that logic and make the queue disagree with `Summary`.
2. **One job per `rel_path`.** `UNIQUE (rel_path)`; enqueue is an upsert that sets `pending` unless the job is `running`. The same file changed twice, or enqueued again by a re-scan, is still one job. Paths, not Drive IDs: a new file has no ID yet.
3. **States: `pending`, `running`, `failed`.** A finished job is deleted: `files` and `file_revisions` already record the outcome, and a `done` history would grow forever. `failed` keeps `attempts` and `last_error` (the error text, never a request body or token) for ticket 18 to build on; the next scan re-enqueues it as `pending`.
4. **Crash recovery by reset.** At the start of a run, every `running` job goes back to `pending`. Safe because one process owns the database; two `syncd` processes at once are not supported (and the DB is not shared between machines). Cancellation (Ctrl+C) and a lost sign-in requeue the in-flight job with `context.WithoutCancel`, like the existing row writes.
5. **Claim is atomic by construction.** The repo's single owner goroutine runs `UPDATE jobs SET status='running' ... WHERE id = (SELECT id ... ORDER BY id LIMIT 1) RETURNING ...` as one statement, so no two workers get the same job, with no extra locking.
6. **Order.** Claims go oldest first (by `id`), and scan enqueues in `rel_path` order, so a single worker behaves exactly like today.
7. **A vanished file is not a failure.** If the file is gone when its job runs (deleted between scan and upload), the job is dropped. Delete handling (trash in Drive) is ticket 26.
8. **Bounded concurrency.** A fixed pool of workers, default 3 (`--workers`, 1 to 16). Drive tolerates a few parallel requests; the retry layer handles 429s. Run-wide adaptive throttling is out of scope.
9. **`Uploader` becomes concurrency-safe.** `connect` is guarded by a `sync.Once`-style lock; `ensureDir` gets a single-flight per `relDir` so two workers creating files in a new folder create it once (the wiki already says duplicates are tolerated, but avoiding them is cheap here and was deferred to this ticket). `Uploader.Upload` already holds no other shared mutable state.
10. **Fatal errors stop everyone.** Sign-in lost, repo error or cancellation cancels a derived context; workers finish or abandon their current file, requeue their job and exit. Per-file errors stay per-file (`failed` job, `Failed++`, run goes on).
11. **No new dependency.** `database/sql` and `sync` are enough. No separate queue library, no generic "job kind" column: later tickets add a column in their own migration when they need deletes or renames.
12. **Job IDs in logs.** The `job` attribute in `Sync`'s log lines becomes the real job ID, which closes the "job ID" part of ticket 14's output.

## Schema

```sql
-- +goose Up
CREATE TABLE jobs (
    id         INTEGER PRIMARY KEY,
    rel_path   TEXT    NOT NULL UNIQUE,
    status     TEXT    NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'failed')),
    attempts   INTEGER NOT NULL DEFAULT 0,
    last_error TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX jobs_status_id ON jobs (status, id);

-- +goose Down
DROP INDEX jobs_status_id;
DROP TABLE jobs;
```

## Shape

```go
// internal/store/jobs.go
type Job struct {
    ID       int64
    RelPath  string
    Attempts int
}

func (r *Repo) EnqueueJob(ctx context.Context, relPath string) error      // upsert; leaves a running job alone
func (r *Repo) ClaimJob(ctx context.Context) (Job, error)                  // ErrNoJob when none pending
func (r *Repo) CompleteJob(ctx context.Context, id int64) error            // delete
func (r *Repo) FailJob(ctx context.Context, id int64, msg string) error    // failed, attempts+1, last_error
func (r *Repo) RequeueJob(ctx context.Context, id int64) error             // back to pending (cancel, abort)
func (r *Repo) ResetRunningJobs(ctx context.Context) (int, error)          // crash recovery
func (r *Repo) CountJobs(ctx context.Context) (int, error)                 // pending + running, for "resuming N"
```

```go
// internal/sync/sync.go (slice 1: one worker; slice 2 runs the loop in N goroutines)
func Sync(ctx context.Context, root string, repo *store.Repo, upload UploadFunc, report func(Event)) (Summary, error) {
    // ResetRunningJobs; Scan; EnqueueJob for each entry (keep the scan entry in a map for size and mtime);
    // loop: ClaimJob -> process -> Complete/Fail/Requeue; stop on ErrNoJob.
}
```

`process` is the current loop body unchanged apart from reading the scan entry for the claimed `rel_path`. A job left over from an earlier run whose path is not in this scan is re-stat'ed by `process`; missing means drop.

## Files to create/modify

| File | Change |
| --- | --- |
| `internal/store/migrations/00004_jobs.sql` | `jobs` table |
| `internal/store/jobs.go` (+ `jobs_test.go`) | queue methods |
| `internal/sync/sync.go` (+ `sync_test.go`) | scan, enqueue, drain; `process`; worker pool (slice 2) |
| `internal/drive/upload.go` (+ `upload_test.go`) | concurrency-safe `connect`, single-flight `ensureDir` (slice 2) |
| `cmd/syncd/main.go` (+ `main_test.go`) | `--workers`, `resuming N queued jobs` |
| `README.md`, `docs/wiki/index.md`, `internal/store/doc.go`, `internal/sync/doc.go`, `requirements.md` | Docs and status |

## Testing plan

- Store (`-race`): enqueue twice gives one job; enqueue leaves a running job running; claim returns oldest first and marks running; two concurrent claimers never get the same job; complete deletes; fail records the message and bumps attempts; requeue and reset return jobs to pending; `ErrNoJob` on empty; the table survives reopen (the durability the ticket is about).
- Sync: all existing `Sync` tests pass unchanged; leftover `running` jobs resume; cancel mid-run leaves unfinished jobs `pending` and a second run finishes them with no duplicate upload; a vanished file is dropped; a failed file's job is `failed` and a second run retries it; sign-in lost requeues and aborts.
- Concurrency (slice 2, `-race`): a fake uploader that blocks until N calls are in flight proves N workers run together; files in one new folder create it once; the first fatal error stops the other workers; the summary totals equal the file count.
- `syncd sync`: flag parsing for `--workers`; the `resuming` line.

## Verification

1. `make vet test` under `-race`.
2. Manual kill test: `syncd sync` on a tree of a few hundred files, `kill -9` mid-run, rerun: it prints `resuming N queued jobs`, finishes, and Drive holds each file once.
3. `--workers 1` and `--workers 4` end with the same Drive contents; the second is faster on many small files.

## Out of scope (say if you want any)

- Surfacing failed jobs as readable lines: ticket 18 (`sync_errors`, `syncd errors`).
- The watcher and debounce feeding the queue, the foreground daemon and startup reconciliation: tickets 19 to 23.
- Job kinds for delete, rename and move: tickets 25 to 27.
- Run-wide rate limiting, a circuit breaker, or priorities.
- Backoff between job-level retries: a failed job waits for the next scan or run.
