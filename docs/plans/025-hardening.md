# Ticket 23: Hardening (GH-23)

Source: `requirements.md` section 14, ticket 23 (M4 Live watching), section 7 platform notes (`max_user_watches`) and section 10 ("recover from panics in every top-level goroutine and log them with `slog`"). Depends on 21 (`syncd run`), done. The lock against two `syncd` processes was deferred to this ticket by plans 019 and 023.
Done when: **a forced panic is logged and the daemon keeps running.**
Final location: `docs/plans/025-hardening.md` (same format as `docs/plans/archive/017-*.md`).

## To-do

Vertical slices; review after each. Every slice leaves `make vet test` green.

| # | Slice | Step | Done |
| --- | --- | --- | --- |
| 1 | **Slice 1: a panic is logged and the daemon keeps running** | `internal/sync/safe.go`: `safely(name string, fn func()) (panicked bool)` recovers, logs `slog.Error("panic recovered", "goroutine", name, "panic", value, "stack", ...)` and returns. Apply it per iteration in every long-lived loop: `Watcher.loop` (one event), `Debouncer.loop` (one event or one timer fire), the `Run` feeder, retry ticker and watcher-error pump (one iteration each), and per job in the worker (`process` plus the outcome recording). A job whose processing panics is a normal per-file failure: `FailJob` with `internal error`, an error row `internal error (see log)`, a `failed <path>: internal error` line via `report`, and the worker takes the next job. The main goroutine gets a top-level recover that logs the panic and exits 1 (nothing left to keep running). A package-level `testPanic func(where string)` seam, nil in production, is called at the start of each guarded iteration so tests can force a panic in each place. Tests: a panicking upload function in `Run` fails that file only, logs the panic with a stack and the file's job ID, the next file still uploads, `syncd errors` shows `internal error (see log)`, and `Run` still stops cleanly on cancel; same in one-shot `Sync` (that file counts as failed, the others upload); a panic forced in the watcher loop, the debouncer loop, the feeder, the retry ticker and the error pump each leaves `Run` working (a file saved afterwards is uploaded); a panicking `report` callback does not kill a worker; the log never contains file contents; `safely` returns true on panic and false otherwise and re-panics nothing. **Review point (demo: `go test -run Panic -v`, and a fake-uploader run where one file panics and the next uploads).** | [x] |
| 2 | **Slice 2: a clear error when inotify runs out of watches** | `internal/sync/watchlimit.go`: on Linux, an `ENOSPC` from adding a watch becomes `ErrWatchLimit` wrapping a message that names the directory it failed on, how many directories were watched before it, the current `fs.inotify.max_user_watches` (read from `/proc/sys/fs/inotify/max_user_watches`) and the fix (`sudo sysctl fs.inotify.max_user_watches=524288`, plus the line for `/etc/sysctl.conf` to keep it); `EMFILE` becomes the same kind of message for `fs.inotify.max_user_instances`. Other systems and other errors pass through unchanged. `NewWatcher` stops with that error (a half-watched tree is worse than no watch), so `syncd watch` and `syncd run` print it and exit 1. A new folder that cannot be watched while running is reported once through `Errors()` / `OnWatchError` (not once per file) and the run goes on. Tests, using an injected `add` function on the `Watcher` (the real limit cannot be hit in a test): ENOSPC at start gives `ErrWatchLimit` with the directory, count and fix in the text; ENOSPC on a new folder while running reports one error and keeps watching the rest; unrelated errors are unchanged; the message when `/proc` cannot be read still reads well. CLI test that `watch` and `run` exit 1 with the message. **Review point (demo: `ulimit`-free: the test with the injected limit, and the printed message).** | [x] |
| 3 | **Slice 3: one `syncd` at a time** | `internal/store/lock.go`: `Lock(path string) (*FileLock, error)` takes a non-blocking exclusive lock on `<db path>.lock` (new dependency `github.com/gofrs/flock`, pure Go, handles Windows and Unix), returns `ErrLocked` when another process holds it, and `Unlock()` releases it. `syncd run` and `syncd sync` take the lock before opening the repo and release it on exit; if it is held they print `another syncd is using this database (run or sync); stop it first` and exit 1. Read-only commands (`errors`, `history`, `scan`, `db`, `watch`) do not lock. Tests: a second lock on the same path fails with `ErrLocked` while the first is held and succeeds after `Unlock`; unlocking twice is fine; the lock file sits beside the database, never in the sync root; `run` and `sync` exit 1 with the message when the lock is held (tests hold it themselves, the Drive client is never reached). The OS releases the lock if the process dies, so a crash leaves nothing stale. **Review point (demo: start `syncd run`, then `syncd sync` in another terminal and see the refusal).** | [x] |
| 4 | **Slice 4: docs** | Wiki new "Hardening" section (what is recovered and where, how a panic shows up, the watch limit message and fix, the lock), update the Running section's "only one process" limit, README note, `internal/sync/doc.go`, `=> Done` for ticket 23 in `requirements.md`. **Review point.** | [x] |

## Context

`syncd run` is the process that is supposed to stay up for days. Three things can take it down or corrupt it today. First, a panic in any goroutine ends the process: a nil dereference in the debouncer, or a bug triggered by an odd file name in an upload path, and every other file stops syncing. Requirements section 10 says every top-level goroutine must recover and log. Second, the Linux watch limit: `fs.inotify.max_user_watches` is commonly 8192 to 65536, a large tree exceeds it, and the OS error ("no space left on device") says nothing useful; section 7 says it must be a clear error. Third, two `syncd` processes on one database (`run` plus a manual `sync`, or two `run`s) reset each other's running jobs and double-upload; plans 019 and 023 left the guard for here.

## Decisions

1. **Recover per iteration, not per goroutine.** Wrapping a whole goroutine in `defer recover()` only logs the death of the loop. The loops here handle independent items (an event, a timer fire, a job), so recovery wraps one iteration: the panic costs that item, and the loop carries on. One helper, `safely(name, fn)`, does the recover, the log line and the return value, so there is one place that decides what a recovered panic looks like.
2. **What a recovered panic logs.** `slog.Error` with `goroutine` (where it was), `panic` (the value printed with `%v`) and `stack` (`debug.Stack()`), plus the usual `path` and `job` attributes where the caller has them. Never request bodies or tokens: the panic value is whatever the runtime or the code gave (a nil dereference message, an index error); Go stack traces show function names and addresses, not string contents. Tests assert that file contents do not appear in the log.
3. **A job that panics is a failed file, not a lost one.** The worker marks the job failed (`internal error`), records an error row (`internal error (see log)`, so `syncd errors` makes the failure visible), reports a `failed` line, and takes the next job. The failed job is retried by the 5-minute ticker or the next start like any other. A bug that always panics for one file therefore costs one log entry per retry, not the daemon. No retry cap and no quarantine in this ticket.
4. **A panic in the middle of recording an outcome.** The worker's guarded section covers `process` and the follow-up `CompleteJob` / `FailJob` / `ClearError` calls, so a job is never left `running` without a worker (the `ResetRunningJobs` at the next start would fix it, but not during a long run). If recovery itself finds the job still running, the worker marks it failed.
5. **The main goroutine.** A panic outside any worker (in `main`, in flag handling, in the setup of a command) is logged with the same fields and ends the process with exit 1; there is nothing to keep running. Top-level recovery in `main` also makes sure the log file is flushed (ticket 14's `closeLog`).
6. **A test seam, not a debug flag.** `var testPanic func(where string)` is called at the start of each guarded iteration; production leaves it nil. Tests set it to panic once at a named place. That avoids shipping an env var or command that crashes the daemon on purpose.
7. **`ENOSPC` means the watch limit, on Linux only.** On Linux, fsnotify's `Add` returns `ENOSPC` when `max_user_watches` is reached; on other systems `ENOSPC` would mean a full disk and is left alone. `EMFILE` on Linux means `max_user_instances` (inotify instances per user) and gets its own message. The message states the facts the user needs: which directory failed, how many were watched before it, the current limit, and the exact `sysctl` command with a persistent form. The current limit is read from `/proc/sys/fs/inotify/max_user_watches` and omitted (not guessed) when unreadable.
8. **A half-watched tree fails at start.** If the limit is hit during `NewWatcher`, it returns the error and `watch` / `run` exit 1: running with some folders unwatched would silently miss changes. If the limit is hit later for a new folder, the folder is not watched, one error is reported through the existing channel (and not repeated for every file inside), and the run continues; the next start's reconcile finds the files. The seam for tests is an `add` function field on the `Watcher` that defaults to fsnotify's `Add`.
9. **A lock file beside the database.** `Lock` takes an exclusive, non-blocking `flock` (Unix) or `LockFileEx` (Windows) on `<db path>.lock`. The OS releases it when the process exits or crashes, so no stale locks and no PID file logic. `github.com/gofrs/flock` is pure Go and covers both systems; hand-rolling two platform implementations for ~40 lines is more code and more risk than the dependency (`golang.org/x/sys` is already in the module graph). The lock lives in the data directory, never in the sync root.
10. **Who locks.** Only the commands that run jobs: `run` and `sync`. Read-only commands (`errors`, `history`, `scan`, `watch`, `db`, `whoami`) never touch the job state and stay usable while a daemon runs. `put` writes only a folder cache and is left alone. A refusal exits 1 with `another syncd is using this database (run or sync); stop it first`.
11. **No supervision here.** Restarting a process that did die is the service manager's job (ticket 34). This ticket makes the process die less often and fail loudly when it must.

## Shape

```go
// internal/sync/safe.go
var testPanic func(where string) // test seam; nil in production

// safely runs fn and recovers a panic, logging it. It reports whether fn panicked.
func safely(name string, fn func(), attrs ...any) (panicked bool)
```

```go
// internal/sync/watchlimit.go
var ErrWatchLimit = errors.New("inotify watch limit reached")

// watchLimitError turns an Add error into ErrWatchLimit (Linux ENOSPC) or a max_user_instances message (EMFILE); other errors pass through.
func watchLimitError(err error, dir string, watched int) error
```

```go
// internal/store/lock.go
var ErrLocked = errors.New("store: database is in use by another syncd")

func Lock(dbPath string) (*FileLock, error) // non-blocking; <dbPath>.lock
func (l *FileLock) Unlock() error           // idempotent
```

```
$ syncd run --root ~/BigTree
syncd: cannot watch /home/me/BigTree/vendor/x/y: inotify watch limit reached (8192 watches in use of fs.inotify.max_user_watches=8192, 8192 directories watched).
  raise it with:  sudo sysctl fs.inotify.max_user_watches=524288
  and keep it after a reboot with:  echo 'fs.inotify.max_user_watches=524288' | sudo tee /etc/sysctl.d/60-burrow.conf
$ syncd run        # while another run is active
syncd: another syncd is using this database (run or sync); stop it first
```

## Files to create/modify

| File | Change |
| --- | --- |
| `internal/sync/safe.go` (+ `safe_test.go`) | `safely`, `testPanic` |
| `internal/sync/sync.go`, `run.go`, `watch.go`, `debounce.go` (+ tests) | guarded iterations, job-level recovery, `add` seam |
| `cmd/syncd/main.go` (+ `main_test.go`) | top-level recover, lock in `run` and `sync`, messages |
| `internal/sync/watchlimit.go` (+ `watchlimit_test.go`) | limit error |
| `internal/store/lock.go` (+ `lock_test.go`), `go.mod`, `go.sum` | file lock, `gofrs/flock` |
| `README.md`, `docs/wiki/index.md`, `internal/sync/doc.go`, `requirements.md` | Docs and status |

## Testing plan

- Panics are forced through fake upload functions, a panicking `report`, and `testPanic` at named places; every test checks three things: the panic was logged with a stack, the loop or worker carried on (a later file still uploads), and the process-level result is unchanged (`Run` returns nil on cancel, `Sync` returns no error).
- `captureLogs` (existing helper) checks the log text: has `panic recovered`, the goroutine name and the job; has no file contents.
- Watch limit: injected `add` errors (`syscall.ENOSPC`, `syscall.EMFILE`, `os.ErrPermission`); message content (directory, count, limit, command); behaviour at start and while running; the platform guard (`runtime.GOOS == "linux"`) is tested through a function parameter, not by skipping.
- Lock: two locks, release, double unlock, lock path; in `cmd/syncd`, `run` and `sync` refuse when the test holds the lock and do not touch the Drive client.
- `go test -race -count=3` on `internal/sync` and `internal/store`.

## Verification

1. `make vet test` under `-race`.
2. Ticket checkpoint: the forced-panic tests (`go test -run Panic -v ./internal/sync/`) show the logged panic and the daemon still uploading afterwards.
3. Manual: start `syncd run`; in another terminal `syncd sync` is refused; `syncd errors` and `syncd scan --dry-run --diff` still work. On a Linux box, `sudo sysctl fs.inotify.max_user_watches=64`, `syncd watch` on a tree of more than 64 folders prints the clear error and exits 1; restore the value.

## Out of scope (say if you want any)

- Restarting `syncd` after a crash: the service units of ticket 34.
- A cap on retries or a quarantine for a file that panics every time.
- Panic counters or alerts (the control API, ticket 33, can expose them later).
- Locking the read-only commands, or a lock per sync root.
- Watching a tree bigger than the OS allows by falling back to polling.
