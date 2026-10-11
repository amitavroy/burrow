# Ticket 24: Inode / file ID tracking (GH-24)

Source: `requirements.md` section 14, ticket 24 (M5 Rename and delete), section 6 ("Per-file record: ... inode or file ID") and section 7 ("Rename or move: detected by matching inode and hash in the database"). Depends on 8 (files repository), done. Unblocks 25 (rename and move), which matches on what this ticket records.
Done when: **`syncd stat` shows the inode for a tracked file.**
Final location: `docs/plans/026-inode-file-id-tracking.md` (same format as `docs/plans/archive/017-*.md`).

## To-do

Vertical slices; review after each. Every slice leaves `make vet test` green.

| # | Slice | Step | Done |
| --- | --- | --- | --- |
| 1 | **Slice 1: `syncd stat` shows the inode (Unix)** | `internal/sync/fileid_unix.go` (`//go:build unix`): `fileID(path string, info fs.FileInfo) uint64` returns `info.Sys().(*syscall.Stat_t).Ino` (0 when the type is not a `Stat_t`); a stub for other systems in `fileid_other.go` returns 0 until slice 3. `Entry` gets `Inode uint64`; `Scan` fills it from the `DirEntry`'s info (no extra syscall on Unix). `process` writes `Inode` into every row it saves (new upload, update in place, refreshed row), and the `Run` `lookup` fills it from its `Lstat`. `syncd stat` prints a `Local:` block after the Drive fields when the database has a row for the file (found by `rel_path` for `--path`, by Drive ID otherwise): `Inode:`, `Size:`, `MTime:` (UTC, RFC 3339 with nanoseconds), `Synced MD5:`; no row means no block; a database that cannot open is a one-line warning, not a failure. New `--local` flag: skip Drive, read only the database (no sign-in, no network), exit 1 with `no tracked file` when there is no row. Tests: `fileID` on a real temp file equals `os.Stat`'s inode and is stable across a rename and a content edit but not across a delete-and-recreate; `Scan` entries carry inodes that match the files; a `Sync` run records the inode in the row (and in `file_revisions`' sibling `files`, not elsewhere); `Run` records it too; `stat --local --path` prints the block, `stat --local <id>` works by Drive ID, a missing file exits 1. **Review point (demo: `syncd sync`, then `syncd stat --local --path notes.txt` shows an `Inode:` that equals `ls -i notes.txt`).** | [x] |
| 2 | **Slice 2: old and changed rows catch up** | Rows written before this ticket have inode 0, and a file that was replaced (same size and mtime, new inode) has a stale one; ticket 25 would be misled by either. Add `Repo.SetInode(ctx, relPath, inode)` (one `UPDATE`, ignoring missing rows) and use it where a file is judged unchanged without an upload: in `process` on the size-and-mtime-match path (only when the scan's inode is non-zero and differs from the row), and in `Reconcile` for unchanged files (one batch call, `Repo.SetInodes`, in a single transaction so a large tree is one commit). The counts printed (`reconciled: ...`, `already synced`) do not change. `Diff` stays read-only and gains nothing. Tests: a row with inode 0 gets the real inode after `Sync` with no upload and `Synced` counted as before; the same after `Reconcile`; a file replaced by a copy with the same size and mtime gets its inode refreshed and is not uploaded; an inode of 0 from the scan (unknown) never overwrites a known one; `SetInodes` is one transaction and ignores paths without rows; `syncd scan --dry-run --diff` output is unchanged. **Review point (demo: a database from before this ticket, `syncd run`, `syncd stat --local` now shows inodes for files that were never uploaded again).** | [x] |
| 3 | **Slice 3: Windows file ID, other systems** | `internal/sync/fileid_windows.go`: `fileID` opens the path with `CreateFile` (read access, share read/write/delete, `FILE_FLAG_BACKUP_SEMANTICS` so directories also work), calls `GetFileInformationByHandle`, closes the handle, and returns `FileIndexHigh<<32 | FileIndexLow`; any failure returns 0 (unknown), never an error, so a locked file just has no ID. Systems without either (plan 9, js) keep the stub. Scan fills the ID with this path-based call on Windows only (a Unix `Lstat` already has it). Because Windows cannot be run here, the slice is verified by cross-compiling (`GOOS=windows go vet ./...` and `go build ./...`, also `GOOS=darwin`) and by a Windows-only test file that is compiled but skipped elsewhere: the ID of a file is non-zero, equal for a hard link or after a rename, different for another file. `make vet` gains the cross-compile checks for `windows` and `darwin` so they stay green. **Review point (run the Windows-only test on a Windows machine if one is at hand; otherwise the cross-compile output).** | [x] |
| 4 | **Slice 4: docs** | Wiki new "File identity" section (what is recorded, per OS, where it is written, when it is refreshed, what it is for, its limits), `Local:` block and `--local` in the `stat` documentation, README note, `internal/sync/doc.go` and `internal/store/doc.go`, `=> Done` for ticket 24 in `requirements.md`. **Review point.** | [x] |

## Context

Ticket 25 will detect a rename by noticing that a file that disappeared from one path and a file that appeared at another are the same file. The operating system cannot be asked "what was this file renamed to" in a portable way (inotify, FSEvents and `ReadDirectoryChangesW` report renames differently, and after a restart there is no event at all), so the app has to remember a stable identity per file: on Unix the inode number, on Windows the file ID (the NTFS file index). A rename within a volume keeps it; an edit keeps it; a delete-and-recreate does not. The schema already has the column (`files.inode`, nullable) and the Go field (`store.File.Inode`, with zero mapped to NULL), added in ticket 8, but nothing writes it. This ticket makes it real: a per-OS helper, the scan carrying it, every saved row storing it, old rows catching up, and `syncd stat` showing it.

## Decisions

1. **One helper, three small files.** `fileID(path, info)` has a Unix version (read it from the `FileInfo` the scan already has: free), a Windows version (open the file and ask the OS: one syscall pair per file), and a stub. The package-level signature takes both `path` and `info` so each platform uses what is cheapest; callers never branch on the OS.
2. **0 means unknown.** The stored `uint64` already maps 0 to NULL. An unreadable file, a filesystem with no stable IDs (some network and FAT volumes report 0 or reuse numbers), or an unsupported OS yields 0; nothing fails because of it. Ticket 25 treats a missing ID as "cannot match by inode, fall back to hash only".
3. **Only the inode, not the device.** Inode numbers are unique per filesystem, not globally. A sync root normally lives on one filesystem, and a collision across a mount point would need the same inode, the same hash and a removed path, so ticket 25's hash check covers it. Storing the device would need a schema change and a second column for little gain; revisit if bind mounts inside a root turn out to matter. On Windows only the 64-bit file index is stored, not the volume serial (same reasoning; ReFS 128-bit IDs are truncated to what `GetFileInformationByHandle` returns).
4. **Written wherever a row is written.** The scan entry carries the ID; `process` copies it into the row on upload, update and "content unchanged" refresh; `Run`'s `lookup` (which stats the file at claim time) fills it too. The ID read at scan time is never newer than the content uploaded, the same property the size and mtime rely on.
5. **Old rows catch up without uploads.** Rows from before this ticket have inode 0, and a replaced file (same size and mtime, different inode) has a stale one. Both are fixed whenever the code already judges a file unchanged: in a job's size-and-mtime shortcut and in `Reconcile`. It is a plain `UPDATE`, never an upload, never counted differently. A scan that cannot determine an ID (0) never erases a known one.
6. **`Diff` stays read-only.** The refresh lives in `Reconcile` and in `process`, as the other writes do.
7. **`syncd stat` shows what the database knows.** The existing command asks Drive. A `Local:` block follows from the database row for the same file, found by `rel_path` for `--path` and by Drive ID otherwise. `--local` skips Drive: it needs no sign-in and no network, it is how the checkpoint is shown, and it is the natural offline view. A missing row prints nothing in the combined mode and `no tracked file` (exit 1) in `--local` mode.
8. **Output format.** Same style as the Drive fields (label padded to 10): `Inode:`, `Size:`, `MTime:` as UTC RFC 3339 with nanoseconds, `Synced MD5:`. An unknown inode prints `Inode:    unknown`.
9. **Cross-compile checks instead of a Windows machine.** `GOOS=windows go vet ./...` and `GOOS=darwin go vet ./...` run in `make vet`. The Windows-only behaviour is covered by a test file with a `windows` build tag that is compiled everywhere the tag matches and skipped otherwise.
10. **No dependency.** `syscall` is enough on both systems (`golang.org/x/sys` is in the module graph, but `syscall.CreateFile` and `syscall.GetFileInformationByHandle` exist in the standard library).

## Shape

```go
// internal/sync/fileid_unix.go   (//go:build unix)
func fileID(path string, info fs.FileInfo) uint64 {
    if st, ok := info.Sys().(*syscall.Stat_t); ok {
        return uint64(st.Ino)
    }
    return 0
}
```

```go
// internal/sync/fileid_windows.go
func fileID(path string, info fs.FileInfo) uint64 // CreateFile + GetFileInformationByHandle; 0 on any failure
```

```go
// internal/sync/scan.go
type Entry struct {
    RelPath string
    Size    int64
    MTime   int64
    Inode   uint64 // inode (Unix) or file ID (Windows); 0 when unknown
}

// internal/store/repo.go
func (r *Repo) SetInode(ctx context.Context, relPath string, inode uint64) error
func (r *Repo) SetInodes(ctx context.Context, inodes map[string]uint64) error // one transaction
```

```
$ syncd sync
$ syncd stat --local --path notes/todo.txt
Local:
Inode:      1048713
Size:       2048
MTime:      2026-10-11T10:02:11.123456789Z
Synced MD5: 9dd4e461268c8034f5c8564e155c67a6
$ ls -i ~/MySync/notes/todo.txt
1048713 /home/me/MySync/notes/todo.txt
```

## Files to create/modify

| File | Change |
| --- | --- |
| `internal/sync/fileid_unix.go`, `fileid_windows.go`, `fileid_other.go` (+ tests) | per-OS helper |
| `internal/sync/scan.go`, `sync.go`, `run.go`, `reconcile.go` (+ tests) | carry the ID, write it, refresh old rows |
| `internal/store/repo.go` (+ `repo_test.go`) | `SetInode`, `SetInodes` |
| `cmd/syncd/main.go` (+ `main_test.go`) | `Local:` block, `--local` |
| `Makefile` | cross-compile vet for `windows` and `darwin` |
| `README.md`, `docs/wiki/index.md`, `internal/sync/doc.go`, `internal/store/doc.go`, `requirements.md` | Docs and status |

## Testing plan

- `fileID` on Unix compared with `os.Stat`'s `Sys().(*syscall.Stat_t).Ino`; stable across rename and edit; differs after delete-and-recreate (the inode may be reused by the filesystem, so the test creates a second file first to keep the old inode occupied); zero for a type that is not a `Stat_t`.
- `Scan` entries match; `Sync` and `Run` rows carry the inode; `Run`'s `lookup` path is covered by the existing fake-uploader tests extended with an inode check.
- Slice 2: row with inode 0, row with a stale inode, scan inode 0, and the batch transaction; check that no upload happens and that counts are unchanged.
- `stat`: `--local` by path and by Drive ID, missing file, combined mode prints the block after the Drive fields (Drive part via the existing fake where one exists, otherwise `--local` only), unreadable database warns once.
- Windows: `GOOS=windows go vet ./...`, `GOOS=darwin go vet ./...`, and the tagged test file.
- `go test -race ./...`.

## Verification

1. `make vet test` under `-race`, including the cross-compile vet lines.
2. Manual (the ticket checkpoint): `syncd sync` on a small root, `ls -i` a file, `syncd stat --local --path <file>` shows the same inode; rename the file and `syncd stat --local --path <newname>` after the next `run` or `sync` shows the same inode on the new row (the old row is ticket 25's job).
3. Backfill: use a database from before this ticket (or `sqlite3 ... "UPDATE files SET inode = NULL"`), run `syncd run` once, confirm `syncd stat --local` shows inodes again and nothing was uploaded.

## Out of scope (say if you want any)

- Using the ID to detect renames and moves (ticket 25), deletes (26) and folder moves (27).
- Device numbers, volume serials, ReFS 128-bit IDs.
- Hard links and symlinks (the scan already skips symlinks; two hard links share an inode, which ticket 25 must tolerate).
- Storing the ID of directories.
