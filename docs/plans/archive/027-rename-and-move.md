# Ticket 25: Rename and move (GH-25)

Source: `requirements.md` section 14, ticket 25 (M5 Rename and delete), and section 7 "Rename or move: detected by matching inode and hash in the database, then `files.update` with `addParents` / `removeParents` instead of a re-upload". Depends on 21 (`syncd run`) and 24 (inode tracking), both done. Unblocks 27 (folder rename and delete).
Done when: **a renamed file keeps its Drive file ID; nothing is re-uploaded.**
Final location: `docs/plans/027-rename-and-move.md` (same format as `docs/plans/archive/017-*.md`).

## To-do

Vertical slices; review after each. Every slice leaves `make vet test` green.

| # | Slice | Step | Done |
| --- | --- | --- | --- |
| 1 | **Slice 1: `syncd sync` moves a renamed file in Drive** | Store: `Repo.FindByInode(ctx, inode) ([]File, error)` (rows with that non-zero inode and a Drive ID) and `Repo.RenameFile(ctx, from, to string, f File) error` (one transaction: drop a stray row without a Drive ID at `to`, move the row `from` to `to` keeping the Drive ID, base state and revisions, set size, mtime, inode, local MD5). Drive: `Uploader.Move(ctx, driveFileID, newRelPath string) (FileInfo, error)` inside `withRetry`: `connect`, `files.get` (fields `parents`), `ensureDir` for the new directory, one `files.update` with the new `Name`, `addParents` the new directory, `removeParents` the old parents (skipped when it is already the only parent, which makes a retry after a lost reply a no-op), and the `rel_path` appProperty; a 404 returns `ErrFileNotFound`. Sync: `type MoveFunc func(ctx, driveFileID, newRel string) (drive.FileInfo, error)` and `WithMove(MoveFunc)` as an optional last argument of `SyncWorkers`. In `process`, for a file with no row (or a row without a Drive ID) and a non-zero inode: find the single tracked row with the same inode whose local file is gone, stream the new file's MD5, and when it equals that row's synced MD5, call the mover, rename the row, clear both error rows, count `Summary.Moved`, and report `Event{RelPath: new, MovedFrom: old}`. `syncd sync` passes `uploader.Move` and prints `moved old -> new` on stdout and `N moved` in the summary. Any condition that fails (no candidate, content differs, mover absent) falls through to the normal upload, unchanged. Tests: store (inode lookup ignores rows without a Drive ID or with inode 0; `RenameFile` keeps the Drive ID and revisions and is atomic); Drive against the fake (rename in the same folder, move to another folder and to a new nested folder, both parents removed, retry after a lost reply is a no-op, 404 gives `ErrFileNotFound`, the upload and create endpoints are never hit); Sync with a fake mover (rename: moved, not uploaded, row path changed, Drive ID kept, inode kept; the old path's queued job is dropped). **Review point (demo: `syncd sync`, `mv a.txt b.txt`, `syncd sync` prints `moved a.txt -> b.txt`; in Drive the file keeps its ID and its revision history).** | [x] |
| 2 | **Slice 2: `syncd run` does it live and after a restart** | `RunOptions.Move`; `Run` passes it to the workers; `syncd run` sets it to `uploader.Move`. Live rename: the debouncer drops the old path's timer, the new path's job finds the candidate and moves it. Reconcile: a rename made while stopped is handled when the new path's job runs at start (its old row is only "missing" until then; the move removes it). Tests (fake uploader and mover, real watcher): rename a file while running; rename into a new subfolder; rename while stopped then start; rename A→B→C in quick succession ends with one row at C and the Drive file moved at most once or twice but never re-uploaded; a rename of a file whose job is still pending; a rename of a file that has never been uploaded (no row, so it is a plain new file); `syncd run` prints `moved a -> b`. **Review point (demo: `syncd run`, rename and move files around in a file manager, watch `moved ...` lines and see Drive keep the IDs).** | [x] |
| 3 | **Slice 3: preview moves** | `Plan` (ticket 22) gains `Moved []Move{From, To}`: a new file whose inode equals a single missing row's and whose size equals that row's size, found without hashing (a preview, the job still verifies with the hash). Such pairs leave `New` and `Missing`. `syncd scan --dry-run --diff` prints `moved a.txt -> b.txt` lines (after `changed`, before `missing`) and counts them; `syncd run`'s `reconciled:` line says `N moved`. `Reconcile` still queues the new path (the job does the move). Tests: `Diff` pairs a renamed file, leaves a copy as `new` and the original unchanged, does not pair when sizes differ or two missing rows share the inode, and output order is stable; CLI output. **Review point.** | [x] |
| 4 | **Slice 4: edge cases that must not move** | Tests and small fixes for the cases where a move would be wrong: a copy (different inode) is uploaded as new; a hard link (same inode, the original still exists) is uploaded as new; an inode reused by an unrelated new file after a delete (hash differs) is uploaded as new, the old row stays missing; two tracked rows with the same inode (hard links) both gone is ambiguous and uploads as new; a rename plus an edit in one step uploads as new (documented limit); the candidate's Drive file is gone (`ErrFileNotFound` from the mover) falls back to uploading as new and drops the stale row; the mover fails with a transient error and the job is retried (the row is not renamed until Drive says yes); a scan inode of 0 never matches; Windows file IDs behave as inodes in the logic (the match code has no OS branch). **Review point.** | [x] |
| 5 | **Slice 5: docs** | Wiki "Renames and moves" section (the match rules, the Drive call, what is recorded, what is not handled), update the Running and Job queue text where they say renames are not propagated, README note, `internal/sync/doc.go`, `internal/drive/doc.go`, `=> Done` for ticket 25 in `requirements.md`. **Review point.** | [x] |

## Context

A rename or move is, to the file system events, a remove and a create; to a scan after a restart, a missing path and a new path. Without help the app uploads the file again under the new name and leaves the old Drive file behind, which wastes bandwidth and quota and loses the file's revision history and Drive ID. Since ticket 24 each tracked file's inode (Unix) or file ID (Windows) is stored. A rename within a filesystem keeps it, so "the new file has the inode of a tracked file whose path is now gone" identifies the file. Drive can move a file without re-sending content: `files.update` with `addParents` and `removeParents` (and a new `name`). This ticket joins the two, in the job handler, so it works the same for the one-shot sync, the live daemon and the startup reconcile.

## Decisions

1. **Detection lives in the job handler (`process`), not in the watcher.** The watcher and debouncer report paths; they do not pair a remove with a create (OS rename events differ and cannot be paired reliably across a restart). By the time the job for the new path runs, the old path is known to be gone, which is the strong signal. This is also why one code path covers `sync`, `run` and the reconcile.
2. **The match, all of it.** For a file with no usable row (no row, or one without a Drive ID) and a non-zero inode: collect the tracked rows (with a Drive ID) that have that inode; keep those whose local file no longer exists; require **exactly one**; then require the new file's streamed MD5 to equal that row's **synced MD5**. Only then is it a move. Every other outcome (no candidate, two candidates, hash differs, inode 0, no mover configured) takes the normal path and uploads the file as new. The inode is only a hint (unique per filesystem, reused after delete, shared by hard links); the hash and "the old path is gone" make a false match require an identical file at a reused inode, in which case moving is still correct.
3. **A rename plus an edit is not a move.** If the content changed as well, the hash differs and the file is uploaded as new; the old Drive file stays until the delete ticket (26) trashes it. This is the conservative choice: matching on inode alone could attach a stranger's content to an old Drive file. It is documented as a limit and can be revisited with an "update in place after move" step later.
4. **One Drive call, no content.** `Uploader.Move` does `files.get` (fields `parents`), ensures the new directory exists (the same `ensureDir` as uploads, lazily creating folders), and sends one `files.update` with the new `Name`, `addParents` the new directory, `removeParents` the old parents, and `appProperties.rel_path` set to the new path. If the file's only parent is already the target, the parent arguments are left out, so a retry after a lost reply is a harmless update. The call runs under `withRetry` (ticket 16) like uploads. A 404 becomes `ErrFileNotFound`.
5. **The row moves, it is not recreated.** `RenameFile` changes `rel_path` in place in one transaction, so the Drive ID, `base_md5`, `base_revision_id`, `synced_md5` and every `file_revisions` row (keyed by Drive ID) stay attached. Size, mtime, inode and local MD5 are refreshed from the new file. A leftover row without a Drive ID at the destination (a failed earlier attempt) is dropped first. Error rows for both paths are cleared. The old path's pending job, if any, finds no file and is dropped by the worker's existing rule.
6. **Retry safety.** The Drive move happens before the database rename. If the process dies in between, the next run sees the new path with no row and the old row still pointing at the same Drive ID; the move is repeated, finds the file already in place (no parents to change), and the row rename completes. Nothing is duplicated.
7. **`MoveFunc` is optional and injected.** The move is an optional argument to `SyncWorkers` (`WithMove`) and a field of `RunOptions`, nil in most unit tests and set by the CLI to `uploader.Move`. Existing tests and callers keep working and simply never move.
8. **Output.** `Event` gains `MovedFrom`; the CLI prints `moved old -> new` on stdout (next to `uploaded` and `updated`), and the `syncd sync` summary line gains `N moved`. `Summary.Moved` counts them. The `reconciled:` line of `run` reports likely moves found by `Diff`.
9. **Preview without hashing.** `Diff` (read-only, ticket 22) pairs a new file with a missing row by inode and size only, and reports it as `moved` in `scan --dry-run --diff` and the `reconciled:` line. It is a preview: the real decision is made by the job with the hash. Paired files stay in the queue (as new) so the job can verify.
10. **Whole folders are not special-cased here.** Renaming a directory shows up as every file below it appearing at a new path with the old inodes, so each file is moved by its own job. The end state is right; it costs one Drive call per file and leaves the old empty folder in Drive. Moving the folder as one operation (and trashing the old one) is ticket 27.
11. **No new dependency, no schema change.** `files.inode` exists since ticket 8 and is filled since ticket 24.

## Shape

```go
// internal/store
func (r *Repo) FindByInode(ctx context.Context, inode uint64) ([]File, error) // non-zero inode, Drive ID set
func (r *Repo) RenameFile(ctx context.Context, from, to string, f File) error // one transaction

// internal/drive
func (u *Uploader) Move(ctx context.Context, driveFileID, newRelPath string) (FileInfo, error)

// internal/sync
type MoveFunc func(ctx context.Context, driveFileID, newRel string) (drive.FileInfo, error)
func WithMove(m MoveFunc) SyncOption
type Event struct { RelPath string; Updated bool; MovedFrom string; Err error }
type Summary struct { ...; Moved int }
```

```
process(job for B, no usable row, inode != 0, mover != nil):
  cands := FindByInode(inode) with Drive ID, path != B, local file gone
  len(cands) == 1 and md5(B) == cands[0].SyncedMD5 ?
      yes: mover(cands[0].DriveFileID, B) ; RenameFile(A -> B) ; clear errors ; Moved++ ; report(moved A -> B)
           ErrFileNotFound -> fall through to upload as new
           other error     -> per-file failure (job failed, retried later)
      no : upload as new (as before)
```

```
$ mv ~/MySync/notes/todo.txt ~/MySync/archive/todo-2026.txt
$ syncd sync
moved notes/todo.txt -> archive/todo-2026.txt
0 uploaded, 0 updated, 0 already synced, 1 moved, 0 failed, 0 ignored
```

## Files to create/modify

| File | Change |
| --- | --- |
| `internal/store/repo.go` (+ `repo_test.go`) | `FindByInode`, `RenameFile` |
| `internal/drive/move.go` (+ `move_test.go`) | `Uploader.Move` |
| `internal/sync/sync.go` (+ `sync_test.go`) | `MoveFunc`, `WithMove`, move path in `process`, `Event.MovedFrom`, `Summary.Moved` |
| `internal/sync/run.go` (+ `run_test.go`) | `RunOptions.Move` |
| `internal/sync/reconcile.go` (+ `reconcile_test.go`) | `Plan.Moved` (preview) |
| `cmd/syncd/main.go` (+ `main_test.go`) | pass `uploader.Move`, `moved a -> b` lines, summary counts, `scan --diff` lines |
| `README.md`, `docs/wiki/index.md`, `internal/sync/doc.go`, `internal/drive/doc.go`, `requirements.md` | Docs and status |

## Testing plan

- The Drive `Move` tests use the existing fake Drive (extended with `files.get` for parents and `files.update` without media): check the request (name, `addParents`, `removeParents`, appProperties), that nothing is sent to the upload endpoints, the same-folder rename, a move to another and to a new nested folder, the idempotent retry, and the 404.
- Sync and Run tests use a fake mover that records calls and a real temp tree and `Repo`; they assert on the row (same Drive ID, new path, inode kept), on `Summary` and on the absence of an upload call.
- Run tests rename files with `os.Rename` while a real `Watcher` and `Debouncer` run (settle 40 ms) and also while stopped.
- Slice 4 is a table of the "must not move" cases, each asserting an upload happened and no mover call was made (or the documented fallback).
- `go test -race -count=3` on `internal/sync`; `make vet` (cross-compile) still passes: the match code has no OS-specific branch.

## Verification

1. `make vet test` under `-race`.
2. Manual (the ticket checkpoint): `syncd sync` on a small tree, `syncd stat --path notes/todo.txt` and note the Drive ID, `mv` the file into another folder, `syncd sync` prints `moved ...`, `syncd stat --path <new path>` shows the **same ID** and the file sits in the new folder in Drive's web UI; `syncd history <new path>` still lists the old revisions; the summary says `0 uploaded`.
3. Live: the same with `syncd run` running; the `moved` line appears within a few seconds.
4. Negative: copy a file (`cp`), and `touch` + rewrite a file under a new name: both upload as new files.

## Out of scope (say if you want any)

- Trashing the old Drive file or folder when nothing matches, and local deletes (ticket 26).
- Moving a whole folder as one operation, cleaning up emptied folders in Drive, and the folder-ID cache entries (ticket 27).
- Matching a renamed-and-edited file, or matching across devices or volumes.
- Hard links and symlinks beyond refusing to move on an ambiguous match.
