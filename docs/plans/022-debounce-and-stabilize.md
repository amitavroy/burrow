# Ticket 20: Debounce and stabilize (GH-20)

Source: `requirements.md` section 14, ticket 20 (M4 Live watching), and section 7 "Debounce and stabilize". Depends on 19 (recursive watcher), done. Unblocks 21 (foreground daemon), which connects this to the job queue.
Done when: **one editor save produces one queued job.**
Final location: `docs/plans/022-debounce-and-stabilize.md` (same format as `docs/plans/archive/017-*.md`).

## To-do

Vertical slices; review after each. Every slice leaves `make vet test` green.

| # | Slice | Step | Done |
| --- | --- | --- | --- |
| 1 | **Slice 1: `syncd watch` prints one `ready` line per save** | `internal/sync/debounce.go`: `NewDebouncer(in <-chan WatchEvent, root string, settle time.Duration)`, `Ready() <-chan string`, `Close()`. One goroutine owns a map of per-path timers. A `create` or `write` of a file starts or restarts that path's timer; `rename` and `remove` cancel it; directory events are ignored. When a timer fires the file is `Lstat`ed: not a regular file any more means the path is dropped silently; otherwise its rel_path is sent on `Ready()`. Input closing stops all timers and closes `Ready()`. `syncd watch` now prints `ready <path>` for debounced files and takes `--settle DURATION` (default `1s`) and `--raw` (the raw event lines of ticket 19, no debounce). Tests with a short settle (30 ms): a burst of 20 writes gives one ready; two saves a settle apart give two; a rename or remove before the timer cancels it; a directory gives none; closing the input closes `Ready()`; an editor-style save (write `doc.txt.tmp`, rename over `doc.txt`) through the real `Watcher` gives exactly one ready for `doc.txt`. **Review point (demo: `syncd watch`, save a file in an editor; one `ready` line per save, not a stream of events).** | [x] |
| 2 | **Slice 2: wait until size and mtime stop changing** | A fired timer no longer reports at once: it records a sample of (size, mtime) and re-arms for another settle period; only when the next sample equals the last one is the path reported. Any new event for the path discards its sample and restarts the cycle, and a changed sample re-arms and keeps waiting, so a file still being written or copied is reported once, after it stops growing. Tests: a file appended to every 10 ms for 300 ms (settle 100 ms) is reported once, after the last append; a file deleted between the two samples is dropped; a file renamed away between samples is dropped and its new name is reported; a touch (mtime only) restarts the wait; a 5 MB file written in chunks gives one ready. The ticket checkpoint test lives in `internal/sync`: real `Watcher` + `Debouncer` + `repo.EnqueueJobs` on a temp dir; an editor-style save followed by `CountJobs` shows exactly one queued job. **Review point (demo: `cp` a large file into the root while `syncd watch` runs; one `ready` after the copy finishes).** | [x] |
| 3 | **Slice 3: docs** | Wiki "Watching" section extended with the debounce rules (what resets, what cancels, the two-sample check, defaults, flags), README note on `--settle` and `--raw`, `internal/sync/doc.go`, `=> Done` for ticket 20 in `requirements.md`. **Review point.** | [x] |

## Context

The watcher of ticket 19 reports what the OS reports. One save in an editor is many events: a typical editor writes a temp file, writes it in several chunks, renames it over the original, and may chmod or touch it. Even a plain `cp` of a big file is a create followed by a stream of writes. Uploading on each event would send the same file many times and could upload it half written. Requirements section 7 asks for a debounce per path with timers, then an upload only once size and mtime are stable. This ticket adds that layer between the watcher and the queue. It does not connect them: `syncd run` (ticket 21) does that. Until then `syncd watch` shows the effect.

## Decisions

1. **A separate component, fed by a channel.** `Debouncer` reads `<-chan WatchEvent` and writes `<-chan string`; it knows nothing about fsnotify, the database or Drive. That keeps it testable with synthetic events and lets ticket 21 simply connect `Watcher.Events()` to it and `Ready()` to `EnqueueJobs`.
2. **Output is a rel_path, once per settled change.** The queue is one job per `rel_path` (ticket 17), so a path is all the queue needs; the job handler stats the file itself.
3. **What starts, resets and cancels a timer.** `create` and `write` of a file (re)start the path's timer. `rename` and `remove` cancel it: the path no longer holds the content that changed (a rename's new name arrives as its own `create`). Directory events are ignored, because jobs are per file; the watcher already reports the files inside a new directory as `create` events.
4. **One goroutine owns the timer map.** Timers are `time.AfterFunc` that only send the path and a generation number on an internal channel; the loop does everything else. The generation lets the loop ignore a fire that was superseded by a newer event (the classic race when a timer fires while a reset is arriving). No locks.
5. **Stable means two equal samples.** When a timer fires the loop `Lstat`s the file. The first fire records (size, mtime) and re-arms for another settle period; the next fire compares. Equal: the path is ready. Different: record the new sample and wait again. A new event for the path throws the sample away and restarts. So a file is reported at the earliest two settle periods after its last event, and a growing file is reported once, after it stops. Slice 1 reports on the first fire (one sample); slice 2 adds the second sample, so each slice is demoable.
6. **A file that is gone is dropped silently.** If `Lstat` fails or the entry is not a regular file (deleted, turned into a directory, a symlink), the path is forgotten with no event. The remove or rename event that caused this has been or will be seen; deleting in Drive is ticket 26.
7. **Default settle 1 s, configurable.** 1 s keeps the end-to-end latency (about 2 s) far below the "within about a minute" goal and long enough for typical editors. `syncd watch --settle 500ms` changes it for the demo; tests use 10 to 100 ms. No adaptive logic and no maximum wait: a file that never stops changing is not uploaded, which is the right outcome for something half written.
8. **No dependency on mtime resolution.** Equal samples are compared as (size, mtime in nanoseconds). Where the filesystem has coarse mtimes (some network or FAT volumes), size plus the two-sample wait still covers a file being appended to.
9. **Shutdown.** When the input channel closes (the watcher was closed) the loop stops all timers and closes `Ready()`; pending, not yet settled paths are dropped on purpose. Ticket 22 (startup reconciliation) finds anything missed on the next start. `Close()` does the same without waiting for the input.
10. **A slow consumer.** `Ready()` has a small buffer (64) and the loop sends with a `select` on the done channel, so a consumer that stops reading blocks only the debouncer, not the watcher's OS queue, which is already buffered (ticket 19 decision 9).
11. **`syncd watch` keeps its role as a dev aid.** Default output is `ready <path>`; `--raw` restores the ticket 19 output. Both stay free of the database and sign-in.

## Shape

```go
// internal/sync/debounce.go
type Debouncer struct { /* in, root, settle, ready, done, pending map[string]*pendingFile */ }

func NewDebouncer(in <-chan WatchEvent, root string, settle time.Duration) *Debouncer
func (d *Debouncer) Ready() <-chan string // rel_path of a file whose size and mtime have stopped changing
func (d *Debouncer) Close()               // idempotent; stops timers, closes Ready
```

```
pending[path]: {gen int, timer *time.Timer, sample *fileSample}   // fileSample{size int64, mtime int64}

event create/write (file)  -> gen++, sample = nil, (re)arm timer(path, gen)
event rename/remove        -> stop timer, delete pending[path]
timer fires (path, gen)    -> stale gen: ignore
                              Lstat; gone or not regular: delete, drop
                              no sample or sample differs: sample = now, re-arm (slice 2)
                              sample equal: delete, send path on ready
```

```
$ syncd watch --root ~/MySync          # editor saves notes/todo.txt
watching 14 directories under /home/me/MySync (Ctrl+C to stop)
ready notes/todo.txt
$ syncd watch --raw --root ~/MySync    # the ticket 19 stream
create notes/todo.txt.tmp
write notes/todo.txt.tmp
...
```

## Files to create/modify

| File | Change |
| --- | --- |
| `internal/sync/debounce.go` (+ `debounce_test.go`) | `Debouncer`, per-path timers, two-sample stability |
| `internal/sync/debounce_queue_test.go` | watcher + debouncer + `EnqueueJobs`: one save, one job |
| `cmd/syncd/main.go` (+ `main_test.go`) | `watch` uses the debouncer; `--settle`, `--raw` |
| `README.md`, `docs/wiki/index.md`, `internal/sync/doc.go`, `requirements.md` | Docs and status |

## Testing plan

- Synthetic events for the unit tests, a 30 ms settle, and an `eventually` helper with a generous timeout so the tests are stable under `-race` and a loaded machine. "Exactly one" is checked by waiting for the one ready, then asserting nothing else arrives for 4 settle periods.
- Slice 1: burst of 20 writes, two separate saves, cancel by rename and by remove, directory ignored, input closed closes `Ready()`, `Close` idempotent, stale timer fire ignored (reset while a fire is in flight).
- Slice 2: growing file reported once after the last append; deleted between samples; renamed between samples; touch restarts; chunked large file; the end-to-end editor-style save with the real `Watcher`; the queue test (`CountJobs` is 1 after one save and still 1 after the same file is saved again before the job is claimed).
- `syncd watch`: flag parsing for `--settle` (bad value is exit 2) and `--raw`; the ready-line format at the formatting function.
- `go test -race -count=5` on the `internal/sync` watch and debounce tests.

## Verification

1. `make vet test` under `-race`.
2. Manual: `syncd watch`, save a file in an editor several times: one `ready` line per save, about 2 s after the save. `cp` a multi-hundred-MB file into the root: one `ready` after the copy finishes, none while it grows. Delete a file right after saving it: no `ready`.
3. Checkpoint (ticket text): one editor save gives one queued job (the queue test).

## Out of scope (say if you want any)

- Connecting the debouncer to the queue and worker (`syncd run`, ticket 21).
- Startup reconciliation, panic recovery and the `max_user_watches` message (22, 23).
- Detecting a rename by inode and hash (25).
- A maximum wait for a file that never settles, or per-extension settle times.
