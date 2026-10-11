# Ticket 19: Recursive watcher (GH-19)

Source: `requirements.md` section 14, ticket 19 (M4 Live watching), and section 7 "Watching". Depends on 1, done. Unblocks 20 (debounce and stabilize), which consumes this event stream, and through it 21 (foreground daemon).
Done when: **`syncd watch` logs create, write, rename and remove events.**
Final location: `docs/plans/021-recursive-watcher.md` (same format as `docs/plans/archive/017-*.md`).

## To-do

Vertical slices; review after each. Every slice leaves `make vet test` green.

| # | Slice | Step | Done |
| --- | --- | --- | --- |
| 1 | **Slice 1: `syncd watch` shows events in the existing tree** | Add `github.com/fsnotify/fsnotify` to `go.mod`. `internal/sync/watch.go`: `NewWatcher(root)` walks the root with `filepath.WalkDir`, skips ignored directories with the same `Matcher` as `Scan`, and adds a watch for every directory; one goroutine reads fsnotify and sends `WatchEvent{RelPath, Op, IsDir}` (ops `create`, `write`, `rename`, `remove`; chmod dropped; ignored paths dropped) on `Events()`; raw watcher errors go to `Errors()`; `Close()` stops the goroutine and closes both channels. New `syncd watch [--root DIR]`: prints `create docs/a.txt` style lines (a trailing `/` marks a directory), logs the number of directories watched at start, runs until Ctrl+C, exit 0. Tests with real temp dirs: create, write, rename and remove of a file in the root and in an existing nested folder; an ignored file (`x.tmp`, `node_modules/…`, `.git/…`) gives no event; `Watching()` counts the root and subfolders but not ignored ones; `Close` ends the channels and leaks no goroutine. **Review point (demo: `syncd watch`, then `touch`, edit, `mv`, `rm` a file in a nested folder and see the lines).** | [x] |
| 2 | **Slice 2: folders created, moved or removed while watching** | A created directory is watched at once, and the walk of its contents reports the files and folders already inside it as `create` events (so `mkdir -p a/b/c && touch a/b/c/f` loses nothing to the race between the mkdir and the new watch). A removed or renamed directory drops its watches (and its subfolders') from the set. A directory moved in from outside the root arrives as a create and is walked the same way. Vanished-directory errors while adding (`ENOENT`) are ignored, not reported. Tests: mkdir then file inside it is seen; `os.MkdirAll` of a deep path plus an immediate file is seen; remove a folder then recreate it and write inside; rename a folder inside the root, then write in it under the new name (events carry the new path); `Watching()` follows all of these; a burst of 200 files created in a new folder is all seen. **Review point (demo: `syncd watch`, `mkdir -p x/y/z; echo hi > x/y/z/f`, rename `x` to `w`, edit `w/y/z/f`).** | [x] |
| 3 | **Slice 3: docs** | Wiki new "Watching" section (event table, recursion, ignore rules, the new-folder race, what is not handled), README note, `internal/sync/doc.go`, `=> Done` for ticket 19 in `requirements.md`. **Review point.** | [x] |

## Context

Everything so far is pull: the user runs `syncd sync` and the tree is scanned. The goal of M4 is push: save a file and it reaches Drive within a minute. That needs a stream of "something changed here" events. The Go library for it, `fsnotify`, uses the OS notification API (inotify on Linux, FSEvents/kqueue on macOS, ReadDirectoryChangesW on Windows) but only watches the directories you give it, not their subfolders. This ticket turns it into a recursive watcher with the same ignore rules as the scan, and a command that prints what it sees so the behaviour can be judged by hand before the debounce (20) and the queue (21) are built on top. The watcher does no uploading and touches no database.

## Decisions

1. **`fsnotify` is the one new dependency** (it is the library named in the requirements stack table). Pure Go, no cgo, so `syncd` stays easy to cross-compile (ADR-001).
2. **One watch per directory, set up by walking.** `NewWatcher` walks the root with `filepath.WalkDir` and calls `Add` on each directory. Ignored directories (`node_modules`, `.git`, anything in `.syncignore`) are never walked or watched, which also saves inotify watches. Symlinks are not followed (a symlinked directory is not a directory to `WalkDir`), matching the scan.
3. **Same ignore rules as `Scan`.** The watcher builds a `Matcher` once at start (`NewMatcher(root)`), and drops events whose path is ignored. A change to `.syncignore` while running is not picked up until restart; ticket 28 (shared ignore rules) and 22 can revisit that.
4. **A small event type of our own.** `WatchEvent{RelPath string, Op WatchOp, IsDir bool}` with ops `create`, `write`, `rename`, `remove`. `RelPath` is slash-form relative to the root, like everywhere else. `fsnotify.Chmod` is dropped: nothing downstream cares about a mode change alone, and editors fire it a lot. If an event carries several ops, one `WatchEvent` is sent per op, in the order create, write, remove, rename. Raw event names do not leak out of the package, so the OS differences stay in this file.
5. **`IsDir` comes from our own set of watched directories** for remove and rename (the path no longer exists to `Stat`), and from `Lstat` for create and write. A directory we never watched (for example an ignored one that was filtered earlier) cannot appear.
6. **New directories are watched, then walked.** On a create event for a directory, the watcher adds a watch and then walks its contents, adding watches for subfolders and emitting `create` events for the files and folders found. Between a directory being created and the watch being added the OS reports nothing, so a file created in that gap would be lost; the walk finds it. Duplicate `create` events for the same path (the walk plus a late real event) are expected and harmless: ticket 20's debounce collapses them. This is the one subtle part of the ticket and gets its own tests.
7. **Removed and renamed directories drop their watches.** On a rename or remove of a known directory, the watcher removes its watch and the watches of everything below it from its set (`Remove`, ignoring "not watched" errors). This matters because on Linux the inotify watch follows the directory itself, so after `mv a b` events for `b/f` would otherwise still carry the old name `a/f`. The new name arrives as a `create`, is walked and re-watched (decision 6), and events then carry the new path.
8. **One goroutine owns the watch set.** The set of watched directories and all `Add`/`Remove` calls happen in the single reader goroutine, so there is no lock around it. `Watching()` reads it through an atomic counter. `Close` closes the fsnotify watcher and waits for the goroutine, then closes `Events()` and `Errors()`.
9. **Back-pressure and overflow.** `Events()` is unbuffered-friendly but the reader goroutine must not block the OS queue for long, so it uses a modest buffered channel (256). If the consumer falls behind and the OS queue overflows, fsnotify reports `ErrEventOverflow`; it is passed on through `Errors()` and logged at warn. Missed events are exactly what the startup reconciliation scan (ticket 22) is for; this ticket does not try to recover them.
10. **`syncd watch` is a development command**, like `scan --dry-run`: it prints events to stdout (`create docs/a.txt`, `write docs/a.txt`, `rename old.txt`, `remove old.txt`, a trailing `/` on directories), logs a start line with the number of directories watched through slog, and runs until Ctrl+C (exit 0). It needs no database and no sign-in. `--root` defaults to `~/MySync`, as for `sync`.
11. **Out of this ticket.** `max_user_watches` exhaustion is surfaced as a clear message in ticket 23 (until then `Add` returns the OS error wrapped with the directory it failed on). Panic recovery for the goroutine is also ticket 23. Rename detection (matching a `remove` and a `create` as one move by inode and hash) is ticket 25; this ticket reports raw rename and create events.

## Shape

```go
// internal/sync/watch.go
type WatchOp string

const (
    OpCreate WatchOp = "create"
    OpWrite  WatchOp = "write"
    OpRename WatchOp = "rename"
    OpRemove WatchOp = "remove"
)

type WatchEvent struct {
    RelPath string  // slash form, relative to the root
    Op      WatchOp
    IsDir   bool
}

type Watcher struct { /* fsnotify watcher, root, matcher, watched set, channels */ }

func NewWatcher(root string) (*Watcher, error)  // walks and watches the tree; error names the directory that failed
func (w *Watcher) Events() <-chan WatchEvent
func (w *Watcher) Errors() <-chan error
func (w *Watcher) Watching() int                // directories currently watched
func (w *Watcher) Close() error                 // idempotent; closes both channels
```

```
$ syncd watch --root ~/MySync
watching 14 directories under /home/me/MySync (Ctrl+C to stop)
create notes/todo.txt
write notes/todo.txt
rename notes/todo.txt
create notes/done.txt
remove old/
```

## Files to create/modify

| File | Change |
| --- | --- |
| `go.mod`, `go.sum` | add `github.com/fsnotify/fsnotify` |
| `internal/sync/watch.go` (+ `watch_test.go`) | `Watcher`, `WatchEvent`, new-folder handling |
| `cmd/syncd/main.go` (+ `main_test.go`) | `watch` command, usage and dispatch |
| `README.md`, `docs/wiki/index.md`, `internal/sync/doc.go`, `requirements.md` | Docs and status |

## Testing plan

- Real temp directories and real fsnotify; no mocks of the OS. A test helper collects events with a timeout (2 s) and asserts "eventually seen" rather than exact sequences, since the OS may send extra `write` events or merge some.
- Slice 1: the four ops for a file in the root and in a nested folder; ignored paths give no events (a `.tmp` file, a file under `node_modules`, a file under `.git`, and `.syncignore` itself); `Watching()` counts; `Close` is idempotent and closes the channels; an unreadable or missing root returns an error.
- Slice 2: the new-folder cases listed above, including the race (`MkdirAll` then immediate file), 200 files in a new folder, remove and recreate, rename a folder and write under the new name. Run with `-race -count=5` to shake out ordering problems.
- `syncd watch`: usage error on extra arguments (exit 2), a missing root is exit 1 with a clear message, the output format of a line is covered at the formatting function, not by running the loop.
- Platform: written for Linux (CI-free, local runs); the same code uses fsnotify's other backends elsewhere, and any test that depends on a Linux-only behaviour (for example the order of rename events) is guarded.

## Verification

1. `make vet test` under `-race`.
2. Manual: `syncd watch` in one terminal; in another `touch a.txt`, edit it, `mv a.txt b.txt`, `rm b.txt`, `mkdir -p x/y/z && echo hi > x/y/z/f`, `mv x w`, edit `w/y/z/f`. Every step prints a line, and the last edit carries the new path.
3. `ls /proc/$(pgrep syncd)/fd | wc -l` stays flat after creating and deleting a few hundred folders (watches are released).

## Out of scope (say if you want any)

- Debouncing, stabilizing, and queueing: tickets 20 and 21.
- Startup reconciliation after missed events or an overflow: ticket 22.
- A clear `max_user_watches` error and panic recovery: ticket 23.
- Pairing a remove and a create into a rename: ticket 25.
- Reloading `.syncignore` while running, and watching symlinked folders.
