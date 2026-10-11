# Ticket 22: Startup reconciliation (GH-22)

Source: `requirements.md` section 14, ticket 22 (M4 Live watching, a demo checkpoint), and section 7 "Startup reconciliation". Depends on 21 (`syncd run`), done. Unblocks 23 (hardening), 25 and 26 (rename and delete, which also act on what a scan finds).
Done when: **files changed while stopped sync on next start.**
Final location: `docs/plans/024-startup-reconciliation.md` (same format as `docs/plans/archive/017-*.md`).

## To-do

Vertical slices; review after each. Every slice leaves `make vet test` green.

| # | Slice | Step | Done |
| --- | --- | --- | --- |
| 1 | **Slice 1: `syncd run` catches up on start** | `internal/sync/reconcile.go`: `Diff(root, repo) (Plan, error)` scans the root (same ignore rules as `scan`), loads the `files` rows, and sorts every scanned file into **new** (no row, or a row without a Drive ID), **changed** (size or mtime differ from the row) or **unchanged**; rows whose file is not in the scan are **missing**. `Reconcile(ctx, root, repo) (Plan, error)` = `Diff`, then records unreadable entries and prunes stale error rows (the existing `noteScanErrors`), then `EnqueueJobs` for new and changed paths only (unchanged files are not queued). `Run` starts the watcher and debouncer first, then reconciles, then starts the workers; `RunOptions.OnReconcile(Plan)` reports the outcome. `syncd run` prints `reconciled: 3 new, 2 changed, 117 unchanged, 1 missing locally` on stderr. Missing files are counted and logged at info, never acted on (Drive trash is ticket 26). Tests: `Diff` classifies new, changed (size only, mtime only), unchanged, missing, a row with no Drive ID, ignored files, and sorts paths; `Reconcile` queues exactly the new and changed paths, records an unreadable folder, queues nothing for an unchanged tree, and is idempotent when run twice; `Run` on a tree where one file was added and one edited while stopped uploads those two and not the rest (**the ticket checkpoint**), and leaves a failed job from the last run retried because its file is still new or changed. **Review point (demo: `syncd run`, Ctrl+C, edit one file and add one, `syncd run` again: both upload within seconds, nothing else does).** | [x] |
| 2 | **Slice 2: preview it, and close the gap while scanning** | `syncd scan --dry-run --diff [--root DIR]` prints the plan from `Diff` against the state database without queueing anything: `new docs/a.txt`, `changed docs/b.txt`, `missing old.txt`, then the summary line; it needs no sign-in. Order is new, changed, missing, each sorted. Without `--diff`, `scan --dry-run` is unchanged. Test, in `internal/sync`, that a file created *during* the reconcile walk (a hook between the watcher start and the enqueue) is uploaded exactly once: the watcher saw it, the debounce queued it, and the reconcile either saw it too or not, with one job either way. CLI tests for the flag, the output lines and the exit codes. **Review point (demo: the same edit-and-add scenario with `scan --dry-run --diff` before restarting `run`).** | [x] |
| 3 | **Slice 3: docs** | Wiki "Running" section: the start sequence (reset, watcher, reconcile, workers), what counts as new, changed and missing, and the `scan --diff` preview; update the Watching/Running text that says there is no startup scan; README note; `internal/sync/doc.go`; `=> Done` for ticket 22 in `requirements.md`. **Review point.** | [x] |

## Context

`syncd run` (ticket 21) only reacts to what the watcher sees while it is running. Anything that happened while the process was stopped, the machine slept, or the watcher's queue overflowed is invisible to it, so until now such files needed a manual `syncd sync`. Requirements section 7 says the fix: on launch, walk the root, diff against the database, and enqueue the differences. This is also what makes the "changes made while the app was closed are picked up on next launch" success criterion true. `syncd sync` already does a form of this (it queues every file and lets each job skip itself); the reconciliation is the cheaper, explicit version for a long-running process: it compares first and queues only what differs, so a start on a large unchanged tree queues nothing.

## Decisions

1. **Compare, then queue only the differences.** The comparison uses what the files table already holds: size and mtime (nanoseconds) per `rel_path`. A file is **unchanged** when both match its row, **changed** when either differs, **new** when it has no row or its row has no Drive ID (an upload that never finished). The job handler still does the expensive check (streamed MD5 against the last synced MD5) before uploading, so a file that was only touched costs one read and no upload, exactly as in `syncd sync`.
2. **Diff is read-only, Reconcile applies it.** `Diff` returns a `Plan` (sorted `New`, `Changed`, `Missing`, counts of unchanged and ignored, unreadable errors) and writes nothing. That makes it testable without a queue and lets the `scan --diff` preview (slice 2) share the code. `Reconcile` is `Diff` plus the writes: error rows for unreadable entries (the existing `noteScanErrors`) and `EnqueueJobs`.
3. **Missing locally is reported, not acted on.** A row whose file is gone could mean a delete, a rename (new name appears as a new file), or a temporarily unmounted folder. Trashing in Drive is ticket 26 and rename detection is ticket 25, both with inode tracking (24). Until then these are counted in the summary line and logged at info with their paths; nothing is queued. Because a scan of an unmounted root would show every file as missing, the count is also the early warning.
4. **Watch first, then scan.** `Run` starts the watcher and debouncer, then reconciles. A change made during the walk is therefore seen by the watcher (and queued by the debouncer a moment later) even if the walk missed it; a change the walk saw and the watcher also saw is one job (one job per path). The opposite order would leave a gap. Duplicates are the cost, and they are free.
5. **Workers start after the reconcile.** Simple and ordered: leftover jobs from the previous run and the new differences are all in the queue before any worker starts. For a very large tree the first upload starts after the walk (seconds for tens of thousands of files); starting workers earlier is a later optimisation, not needed for the checkpoint.
6. **Failed files come back for free.** A file whose last upload failed has no row (new) or a stale row (changed), so the reconcile queues it again; no separate retry-on-start step is needed.
7. **Output.** One stderr line from `syncd run`: `reconciled: N new, M changed, K unchanged, J missing locally`, followed by the list of missing paths at most 10 long (`...and 5 more` after that) so a surprising number is visible. The existing `resuming N queued jobs` line stays.
8. **`syncd scan --dry-run --diff`** is the user-visible preview: the same `Diff`, printed as `new`, `changed` and `missing` lines and the summary. It opens the state database (like `history` and `errors`) and needs no sign-in or network.
9. **No periodic rescans.** The reconcile runs once per start. Recovering from a watcher queue overflow with a rescan, or rescanning every N hours as a safety net, is a separate decision (it can be added with the same `Reconcile` call); it is out of scope here.
10. **No new dependency, no schema change.** Everything uses `files` and `jobs` as they are.

## Shape

```go
// internal/sync/reconcile.go
type Plan struct {
    New, Changed, Missing []string // rel_paths, sorted
    Unchanged, Ignored    int
    Unreadable            []error
}

func Diff(root string, repo *store.Repo) (Plan, error)                       // read-only
func Reconcile(ctx context.Context, root string, repo *store.Repo) (Plan, error) // Diff + error rows + EnqueueJobs(New+Changed)
```

```go
// RunOptions gains:
OnReconcile func(Plan) // called once, after the reconcile and before the workers start
```

```
syncd run start:
  ResetRunningJobs
  Watcher + Debouncer up                      (nothing is missed from here on)
  OnStart(dirs)
  Reconcile: Scan -> compare with files -> EnqueueJobs(new + changed)
  OnReconcile(plan)
  workers, feeder, retry ticker               (as in ticket 21)
```

```
$ syncd run
resuming 2 queued jobs
watching 14 directories under /home/me/MySync (Ctrl+C to stop)
reconciled: 3 new, 2 changed, 117 unchanged, 1 missing locally
  missing: old/notes.txt
uploaded docs/new.md
updated docs/plan.md
...
$ syncd scan --dry-run --diff
new docs/new.md
changed docs/plan.md
missing old/notes.txt
3 new, 2 changed, 117 unchanged, 1 missing, 4 ignored
```

## Files to create/modify

| File | Change |
| --- | --- |
| `internal/sync/reconcile.go` (+ `reconcile_test.go`) | `Plan`, `Diff`, `Reconcile` |
| `internal/sync/run.go` (+ `run_test.go`) | reconcile after the watcher starts; `OnReconcile`; checkpoint and ordering tests |
| `cmd/syncd/main.go` (+ `main_test.go`) | `reconciled:` line in `run`; `scan --dry-run --diff` |
| `README.md`, `docs/wiki/index.md`, `internal/sync/doc.go`, `requirements.md` | Docs and status |

## Testing plan

- `Diff` is tested with a real temp tree and a real `Repo` (rows written with `Upsert`): each classification alone and mixed, mtime-only and size-only changes, ignored files and folders, `.syncignore`, unreadable folder (non-root, unix), sorted output, empty tree, empty database (everything new), and a row without a Drive ID.
- `Reconcile`: the queue holds exactly new and changed afterwards (`ClaimJob` until empty), unchanged is not queued, calling it twice leaves one job per path, an unreadable entry gets an error row.
- `Run` (fake uploader, short settle): the checkpoint scenario (sync a tree, stop, change one file and add one, start again: exactly those two are uploaded); a restart with no changes uploads nothing; the order test (a file created between watcher start and enqueue is uploaded once).
- CLI: `scan --dry-run --diff` output order and counts, `--diff` without `--dry-run` is a usage error, the flag does not change plain `scan --dry-run`.
- `go test -race -count=3` on `internal/sync`.

## Verification

1. `make vet test` under `-race`.
2. Manual (the ticket checkpoint): `syncd run` on a small root until it is quiet, Ctrl+C, edit one file and add another and delete a third, `syncd scan --dry-run --diff` shows `changed`, `new` and `missing`, then `syncd run`: the edit and the new file upload within seconds, the delete is only reported as missing, nothing else uploads.
3. Large tree: `syncd run` on a root of several thousand unchanged files prints `reconciled: 0 new, 0 changed, N unchanged` and queues no jobs.

## Out of scope (say if you want any)

- Trashing files in Drive that are missing locally (ticket 26) and moving renamed files (ticket 25).
- Rescanning periodically or after a watcher overflow.
- Hashing every file at start (the size and mtime comparison is the point; the job's MD5 check handles touched files).
- Panic recovery and the process lock (ticket 23).
