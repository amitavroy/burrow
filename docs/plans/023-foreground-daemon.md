# Ticket 21: Foreground daemon (GH-21)

Source: `requirements.md` section 14, ticket 21 (M4 Live watching), and sections 1 and 7. Depends on 17 (job queue) and 20 (debounce), both done. Unblocks 22 (startup reconciliation), 23 (hardening), 25 and 26 (rename and delete), and 33 to 34 (control API, service install), all of which build on this loop.
Done when: **a saved file appears in Drive within a minute.**
Final location: `docs/plans/023-foreground-daemon.md` (same format as `docs/plans/archive/017-*.md`).

## To-do

Vertical slices; review after each. Every slice leaves `make vet test` green.

| # | Slice | Step | Done |
| --- | --- | --- | --- |
| 1 | **Slice 1: save a file, see it in Drive** | Refactor the worker in `internal/sync/sync.go` so it can serve both modes: it takes `lookup func(rel string) (Entry, bool)` (one-shot `Sync`: the scan map; `Run`: `Lstat` of the file, dropped when it is gone, not a regular file, or ignored) and `idle func(ctx) bool` (one-shot: `nil`, stop when the queue is empty; `Run`: wait for a wake-up or a 1 s poll tick and carry on). All `Sync` tests pass unchanged. New `Run(ctx, root, repo, upload, report, workers, settle)` in `internal/sync/run.go`: reset running jobs, start the `Watcher` and `Debouncer`, a feeder goroutine calls `EnqueueJobs` for each ready path and wakes the workers, a worker pool drains the queue, including jobs left from an earlier run. On cancel it stops the feeder, debouncer and watcher, lets the workers return (in-flight jobs go back to pending) and returns nil. New `syncd run [--root DIR] [--workers N] [--settle D]`: prints `watching N directories under ROOT (Ctrl+C to stop)` on stderr and the same `uploaded`/`updated`/`failed` lines as `sync`; exit 0 on Ctrl+C. Tests with a fake uploader: a file created after start is uploaded once; a burst of writes is one upload; an editor-style save is one upload; a file in a brand-new folder is uploaded; leftover jobs from an earlier run are processed at start; a vanished file's job is dropped; Ctrl+C (cancel) returns promptly, an upload in flight is requeued, and nothing is uploaded after it. **Review point (demo: `syncd run`, save a file in the root, see `uploaded <path>` in about 3 s and the file in Drive).** | [x] |
| 2 | **Slice 2: failures, sign-in loss and watcher problems** | While running, jobs left `failed` are queued again every 5 minutes (`Repo.RetryFailedJobs`, one statement), so "will retry" in `syncd errors` is true for a long-lived process; the interval is a field of the run options so tests can shorten it. A lost sign-in (`ErrNotSignedIn`, `ErrSessionExpired`) stops the run: in-flight jobs are requeued, `syncd run` prints the same hint as `sync` and exits 1. An error from the watcher's `Errors()` channel (queue overflow, a folder that could not be watched) is logged at warn and printed on stderr, and the run goes on. Tests: a failing upload is retried after the (short) interval and succeeds, clearing its error row; sign-in loss stops all workers and leaves the job pending; a watcher error does not stop the run; a second event for a file that is uploading is handled as a new job (not lost) once the first finishes. **Review point (demo: block the network, save a file, see `failed` and `syncd errors`; restore it and watch the retry upload it).** | [x] |
| 3 | **Slice 3: docs** | Wiki new "Running" section (the pipeline, wake-ups, graceful stop, retry of failed jobs, what is not covered: no startup scan yet, renames and deletes, single process), README note, `internal/sync/doc.go`, `=> Done` for ticket 21 in `requirements.md`. **Review point.** | [x] |

## Context

All the parts of live sync now exist but are not joined: the watcher reports file events (ticket 19), the debouncer turns them into one path per settled save (20), the persistent queue and worker pool upload with retries and error rows (16 to 18). Today a user still has to run `syncd sync` by hand. `syncd run` is the foreground version of the daemon: it connects them in one process, in the terminal, until Ctrl+C. Packaging it as a service (34) and controlling it from a UI (33) come later and reuse this loop. It does not scan the whole root at start; that is ticket 22, so until then a file changed while `run` was not running is picked up by `syncd sync`.

## Decisions

1. **The workers are the ones `Sync` already has.** `worker()` becomes the one place that claims a job, calls `process` and records the outcome (`CompleteJob`, `FailJob`, `RequeueJob`, error rows). Only two things differ between the modes, and each is a function parameter: how the file is found (`lookup`) and what to do when the queue is empty (`idle`). No second worker implementation.
2. **`lookup` in run mode is a stat at claim time.** There is no scan, so the job handler stats the file itself. The size and mtime it records are taken just before the upload starts, so they are never newer than the uploaded content (the property the one-shot code relies on); if the file changes during the upload the debouncer will queue it again. A job for a file that is gone, is not a regular file, or is now ignored is dropped (the same rule as a path missing from a scan).
3. **Waiting for work.** When the queue is empty a worker waits on a wake channel or a 1 s poll tick, whichever comes first, then claims again. The feeder sends up to `workers` non-blocking wake-ups after each enqueue; the poll tick covers a lost wake-up and jobs that become pending from retries. No cond variables, no per-job channels.
4. **Feeder: ready path to job.** One goroutine reads `Debouncer.Ready()`, calls `EnqueueJobs(ctx, []string{p})` (idempotent per path: a path already pending stays one job, a running one is left alone and is picked up again when it is re-saved) and wakes the workers. If the same file is saved again while its job is running, `EnqueueJobs` leaves the running job alone and the new ready path is not lost: it re-queues once the running job finishes. To make that true the enqueue upsert marks a path that was `running` as dirty and `CompleteJob` re-queues instead of deleting when the flag is set (slice 1 includes this small change and its test).
5. **Start: resume first.** `Run` resets `running` jobs left by a dead process and the workers drain whatever is pending before and while the watcher feeds new ones. There is no full scan (ticket 22).
6. **Graceful shutdown.** On Ctrl+C the context is cancelled: the feeder, debouncer and watcher stop; each worker's current upload is cancelled by the context, its job goes back to `pending` (`RequeueJob` with `WithoutCancel`, as today), and `Run` returns nil once all workers have exited. Nothing is lost: the next `run` or `sync` resumes the job. No grace period for in-flight uploads (they resume, and resumable chunk sessions are not persisted, ticket 15 note); a second Ctrl+C is not special-cased.
7. **Failed jobs are retried while running.** A job whose upload failed stays `failed`; every 5 minutes `RetryFailedJobs` flips them to `pending` and wakes the workers. That makes "will retry" in `syncd errors` true for a long-lived process. Fixed interval, no per-job backoff (ticket 16 already backs off inside one attempt). A file that fails forever is retried every 5 minutes forever, which is cheap and visible in `syncd errors`.
8. **A lost sign-in stops the run.** Same rule as one-shot `Sync`: the workers cannot make progress, so the first worker to see it cancels the others, its job is requeued, and `syncd run` exits 1 with the existing hint (`run syncd login`). A daemon that keeps polling Drive with a dead token would only flood the log.
9. **Watcher errors do not stop the run.** An overflow (`ErrEventOverflow`) means events may have been missed; it is logged at warn and printed to stderr. Recovering missed events is the startup scan (22); a periodic rescan is deliberately not added here.
10. **Output.** The same `uploaded`, `updated` and `failed` lines as `syncd sync` (one `report` callback, serialized), plus the start line on stderr. Logs go through slog as everywhere (path, job, drive_file_id). No summary on exit (it is a daemon, not a batch).
11. **Known limits, stated in the docs.** Only one `syncd` process may use the database at a time (`run` and `sync` at once would reset each other's running jobs): not enforced here, a lock is tracked with ticket 23. Renames and deletes are not propagated yet: a rename uploads the new name as a new file and leaves the old one in Drive (ticket 25), a delete leaves the Drive file (ticket 26).
12. **No new dependency.** Everything needed is in the standard library and the existing packages.

## Shape

```go
// internal/sync/run.go
type RunOptions struct {
    Workers     int           // default 3
    Settle      time.Duration // debounce settle period, default 1s
    RetryEvery  time.Duration // failed jobs back to pending, default 5m
    PollEvery   time.Duration // idle worker poll, default 1s
}

// Run watches root and uploads what changes until ctx is cancelled (returns nil) or a
// fatal error stops it (sign-in lost, database failure; the error is returned).
func Run(ctx context.Context, root string, repo *store.Repo, upload UploadFunc, report func(Event), opts RunOptions) error
```

```
watcher.Events() -> Debouncer -> Ready(path) -> feeder: EnqueueJobs([path]); wake workers
                                                          |
failed jobs --(every RetryEvery: RetryFailedJobs)--> pending
                                                          v
workers x N:  ClaimJob -> lookup (Lstat) -> process (upload) -> Complete | Fail | Requeue
              ErrNoJob -> idle: wait for wake or PollEvery tick
ctx cancelled: stop feeder, debouncer, watcher; in-flight jobs requeued; return nil
```

```
$ syncd run --root ~/MySync
watching 14 directories under /home/me/MySync (Ctrl+C to stop)
uploaded notes/todo.txt
updated notes/todo.txt
failed big/video.mov: network problem
^C
```

## Files to create/modify

| File | Change |
| --- | --- |
| `internal/sync/sync.go` (+ `sync_test.go`) | worker takes `lookup` and `idle`; `Sync` passes the scan map and `nil` |
| `internal/sync/run.go` (+ `run_test.go`) | `Run`, feeder, retry ticker, wake-ups, shutdown |
| `internal/store/jobs.go` (+ `jobs_test.go`) | dirty flag so a re-save during an upload is not lost; `RetryFailedJobs` |
| `internal/store/migrations/00006_jobs_dirty.sql` | `dirty` column on `jobs` |
| `cmd/syncd/main.go` (+ `main_test.go`) | `run` command, flags, exit codes |
| `README.md`, `docs/wiki/index.md`, `internal/sync/doc.go`, `requirements.md` | Docs and status |

## Testing plan

- All existing `Sync` tests pass untouched (the refactor is behaviour-preserving). `go test -race -count=3` on `internal/sync`.
- `Run` tests use a real `Watcher` and `Debouncer` on a temp dir, a fake `UploadFunc`, a real `Repo`, a settle of 30 to 50 ms and a short poll, and an `eventually` helper with a 3 s limit.
- Slice 1: the cases in the to-do row, plus: a re-save while the first upload of the file is still in flight leads to a second upload after the first (the dirty flag), and the file is not uploaded a third time.
- Slice 2: retry with a short interval, sign-in loss, watcher error.
- `syncd run`: usage errors (exit 2), missing root (exit 1), the exit codes for sign-in loss; the loop itself is covered in `internal/sync`.

## Verification

1. `make vet test` under `-race`.
2. Manual (the ticket checkpoint): `syncd login`, then `syncd run` on a tiny root; create, edit and move files and folders; each save shows `uploaded` or `updated` within a few seconds and the file is in Drive. Ctrl+C returns promptly with exit 0.
3. Kill test: `kill -9` while a large file uploads, run again: it resumes (`resuming N queued jobs` is for `sync`; `run` logs the count at info).
4. Offline test: disconnect the network, save a file, see `failed`, reconnect, within 5 minutes it uploads and `syncd errors` is empty.

## Out of scope (say if you want any)

- A full scan at start to catch changes made while stopped: ticket 22.
- Panic recovery, a clear `max_user_watches` message, a lock against two processes: ticket 23.
- Propagating renames and deletes: tickets 25 to 27.
- Status, pause and resume from outside: tickets 33 and 35. Installing as a service: 34.
- A per-job retry backoff or a cap on retries of a permanently failing file.
