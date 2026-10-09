# Ticket 12: MD5 skip and updates (GH-13)

Source: `requirements.md` section 14, ticket 12 (M2 One-shot sync). Depends on 11, done apart from its docs slice. Unblocks 13 (revision tracking) and 15 to 17.
Done when (**checkpoint**): **a second run uploads 0 files; editing one file uploads only that file.**
Final location: `docs/plans/014-md5-skip-and-updates.md` (same format as `docs/plans/013-*.md`).

## To-do

Vertical slices; review after each. Every slice leaves `make vet test` green.

| # | Slice | Step | Done |
| --- | --- | --- | --- |
| 1 | **Slice 1: unchanged files are skipped by MD5** | `fileMD5(path)` in `internal/sync` (streamed `io.Copy` into `md5.New()`). In `Sync`, replace "row has a `DriveFileID`, so skip" with a change check: size and mtime equal to the row means unchanged without hashing; otherwise hash, and a hash equal to `SyncedMD5` means unchanged (refresh the row's `Size`, `MTime`, `LocalMD5`). A differing hash is still not uploaded in this slice. Tests: `fileMD5` against a known digest, size/mtime hit does not read the file, touched-but-identical file refreshes the row, second run still uploads 0. **Review point (demo: `touch` every file, rerun, `0 uploaded, N already synced`, row mtimes refreshed).** | [x] |
| 2 | **Slice 2: changed files are updated in place** | A differing hash calls `upload` (`Uploader.Upload` already does `files.update` when the `rel_path` tag matches). Upsert the row with new `Size`, `MTime`, `LocalMD5`, `SyncedMD5`. `Summary.Updated`, `Event` distinguishes uploaded from updated, `sync` prints `updated <rel_path>` lines and `N uploaded, M updated, K already synced, J failed`. Optional only if small: `Uploader.Update(ctx, driveID, ...)` to skip the tag query. Tests: only the edited file is uploaded and counted as updated, failed update keeps the old row, Drive file ID unchanged after update, CLI output. **Review point (checkpoint demo: edit one file, rerun, `0 uploaded, 1 updated`; file ID in the Drive web UI unchanged, content new).** | [x] |
| 3 | **Slice 3: docs** | Wiki "One-shot sync" section (change detection, replaces the "limits until ticket 12" text), README "Syncing" section, `internal/sync/doc.go`, `internal/drive/doc.go`, stale comments in `sync.go`. Also closes out plan 013 slice 3 (ticket 11 docs, if still open). `=> Done` for tickets 11 and 12 in `requirements.md`. **Review point.** | [x] |

## Context

After ticket 11, `syncd sync` uploads every new file and records a row (`rel_path`, `drive_file_id`, `synced_md5`, `size`, `mtime`). It never compares: any file with a row is skipped, so an edit is silently left behind. This ticket adds change detection so that a rerun uploads exactly the files whose content changed.

Drive stays the source of truth. `local_md5` is a cache of the last hash taken, never trusted over the file itself. `base_md5` and `base_revision_id` stay empty until ticket 13.

## Decisions

1. **Two-step change check.** Same size and mtime as the row: unchanged, no read. Otherwise stream-hash the file and compare to `synced_md5`. This keeps a rerun over a large tree cheap (stat only) but is not fooled by a touched file or a restore that resets mtimes.
2. **Compare against `synced_md5`, not `local_md5`.** `synced_md5` is what Drive holds (its `md5Checksum`). Equal means Drive already has this content, whatever the mtime says. `local_md5` records the last local hash for later tickets.
3. **Streamed MD5.** `io.Copy` into `md5.New()`; files are never loaded whole (CLAUDE.md safety rule).
4. **Update through the existing path.** `Uploader.Upload` finds the file by its `rel_path` tag and calls `files.update`, so the Drive file ID is kept. The engine does not need a new Drive call. Using the row's `DriveFileID` directly (skipping the tag query) is an optional optimisation, taken only if it stays small.
5. **Rows written only on success, after the upload**, with `context.WithoutCancel` as today, so Ctrl+C never loses a row. A failed update leaves the old row, so the next run sees the file as changed again.
6. **Row without a `synced_md5`** (older row, or Drive returned none) is treated as changed and re-uploaded once; that repairs the row.
7. **Failure policy unchanged** from ticket 11: per-file errors continue and exit 1, sign-in errors and cancelled contexts abort. A file that cannot be read for hashing is a per-file failure.
8. **Summary and output.** `Summary` gains `Updated`; "uploaded" now means new files only. Stdout: `uploaded <rel>` or `updated <rel>`. Stderr: `N uploaded, M updated, K already synced, J failed`.
9. **Sequential, no retries, no revision data.** Concurrency and retries are tickets 16 and 17; `file_revisions` and `base_*` are ticket 13.
10. **Never log tokens or file contents.**

## Shape

```
for each entry from Scan(root), sorted by rel_path:
  row := repo.Get(rel)
  none                              -> upload   (new)       -> upsert row, report "uploaded"
  size && mtime equal row           -> already synced, skip
  else md5 := fileMD5(path)
       md5 == row.SyncedMD5         -> already synced; upsert row (Size, MTime, LocalMD5)
       else                         -> upload   (update)    -> upsert row, report "updated"
  sign-in error / ctx               -> abort
  other error                       -> report "failed", failed++
```

```go
// internal/sync/md5.go
func fileMD5(path string) (string, error) // hex, streamed

// internal/sync/sync.go
type Event struct {
	RelPath string
	Updated bool  // true when an existing Drive file was replaced
	Err     error // nil means uploaded or updated
}

type Summary struct{ Uploaded, Updated, Synced, Failed, Ignored, Unreadable int }
```

## Files to create/modify

| File | Change |
| --- | --- |
| `internal/sync/md5.go` (+ `md5_test.go`) | `fileMD5` |
| `internal/sync/sync.go` (+ `sync_test.go`) | change check, `Updated`, `Event.Updated` |
| `internal/drive/upload.go` (+ `upload_test.go`) | only if the optional `Uploader.Update` is added |
| `cmd/syncd/main.go` (+ `main_test.go`) | `updated` lines and the new summary |
| `internal/sync/doc.go`, `internal/drive/doc.go` | Package comments |
| `README.md`, `docs/wiki/index.md`, `requirements.md` | Docs and status |

Reused: `Scan`/`Entry`, `store.Repo.Get`/`Upsert`, `Uploader.Upload`, the `syncTree`, `openRepo`, `collect` and fake `uploads` test helpers.

## Testing plan

Table-driven, `go test -race ./...`. Risky cases: touched-but-identical file, same size and mtime but edited content (accepted limit of the pre-check, documented), a file edited while syncing, a failed update, a row with no `synced_md5`, a file that vanishes between scan and hash.

- `fileMD5`: known digest, empty file, missing file error.
- Engine: second run uploads nothing and reads no file when size/mtime match; touched file refreshes the row without upload; edited file is the only one uploaded, counted as Updated, Drive ID unchanged; failed update keeps the old row and exits 1; missing `synced_md5` re-uploads once.
- CLI: `updated` lines and the summary line.

## Verification

1. `make vet test` passes under `-race`.
2. `bin/syncd sync --root tree` twice: the second run says `0 uploaded, 0 updated, N already synced`.
3. `touch` every file and rerun: still 0 uploaded and 0 updated.
4. Edit one file and rerun: `0 uploaded, 1 updated`; the Drive file ID is unchanged and the content is new.
5. Delete `burrow.db` and rerun: files are found by tag and updated in place, no duplicates.
