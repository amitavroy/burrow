# Ticket 11: One-shot sync command (`syncd sync`) (GH-12)

Source: `requirements.md` section 14, ticket 11 (M2 One-shot sync). Depends on 8, 9 and 10, all done. Unblocks 12 (MD5 skip and updates, the next demo checkpoint).
Done when: **the whole sync root appears in Drive.**
Final location after approval: `docs/plans/013-one-shot-sync.md` (same format as `docs/plans/011-*.md`).

## To-do

Vertical slices; review after each. Every slice leaves `make vet test` green.

| # | Slice | Step | Done |
| --- | --- | --- | --- |
| 1 | **Slice 1: `syncd sync` uploads the whole tree** | `drive.Uploader` (`NewUploader`, `Upload` method; package `Upload` wraps it; `put` unchanged in behaviour). `sync.Sync` with `UploadFunc`, no DB yet: scan, upload every file in order, report per file, keep going past per-file errors, abort on sign-in errors or a cancelled context. `sync` command with `--root`, summary and exit codes. Tests: engine with a fake `UploadFunc` (order, error continues, abort on sign-in error, cancel), `Uploader` against the fake Drive (service built once, one token request for many files), CLI usage and exit codes. **Review point (demo: `syncd sync --root tree` mirrors the tree in the Drive web UI; a second run updates files in place, no duplicates).** | [ ] |
| 2 | **Slice 2: record rows, skip what is synced** | `sync.Sync` takes the `*store.Repo`: skip entries with a row, upsert `rel_path`, `drive_file_id`, `synced_md5`, `size`, `mtime` after each success. `sync` opens the DB (required). Summary adds `already synced`. Tests: skip, row contents, interrupted run resumes, row written only on success, lost DB re-finds files without duplicating. **Review point (demo: second run reports `0 uploaded, N already synced`; `Ctrl+C` midway then rerun finishes).** | [ ] |
| 3 | **Slice 3: docs** | Wiki new "One-shot sync" section (flow, rules above, limits until tickets 12 and 16), README "Syncing" section, `internal/sync/doc.go`, `internal/drive/doc.go`, ticket 11 `=> Done` in `requirements.md`. **Review point.** | [ ] |

## Context

Everything `sync` needs exists separately: `sync.Scan` lists the files (ticket 9), `drive.Upload` puts one file in its mirrored folder (tickets 6 and 10), `store.Repo` keeps the file rows (ticket 8). `put` calls `Upload` once per process, which is fine for one file but wasteful for a tree: every call builds a new Drive service and token source (a token refresh per file) and re-verifies the `MySync` root. This ticket adds the loop that ties them together, and the small refactor that makes the loop cheap.

Drive stays the source of truth. The DB row for a file only records "this `rel_path` is on Drive as this ID"; losing the DB means the next run re-finds each file by its `rel_path` tag and updates it in place, with no duplicates.

## Decisions

1. **`drive.Uploader` shared by `put` and `sync`.** New `drive.NewUploader(ctx, client, tokens, roots, folders)` builds the Drive service, token source and `MySync` root ID once; `Uploader.Upload(ctx, localPath, relPath)` does what `Upload` does now. The package-level `Upload` stays as a thin wrapper for `put`. This is the one refactor the ticket needs.
2. **Engine in `internal/sync`.** `sync.Sync(ctx, root, repo, upload UploadFunc, report func(Event)) (Summary, error)` where `UploadFunc func(ctx, localPath, relPath string) (drive.FileInfo, error)`. A function type, not an interface: it keeps the engine testable with a fake and needs no HTTP. `cmd/syncd` only parses flags, wires the real `Uploader` and prints. In slice 1 there is no `repo` yet; slice 2 adds it.
3. **What gets uploaded.** Every entry from `Scan(root)` with no row in the `files` table. A file that already has a row is skipped, not compared: change detection is ticket 12, so until then an edited file is not re-uploaded. The summary says so (`N uploaded, M already synced`).
4. **What a row records.** After each successful upload: `rel_path`, `drive_file_id`, and `synced_md5` from Drive's `md5Checksum`, plus `size` and `mtime` taken from the scan entry (read before the upload). If the file changes mid-upload, the stored size and mtime are older than the file, which errs toward a re-upload once ticket 12 compares them. `local_md5`, `base_md5` and `base_revision_id` are left empty for tickets 12 and 13.
5. **Resumable by construction.** The row is written right after each file's upload, so a Ctrl+C or crash loses at most the file in flight; the next run skips what is recorded. A file uploaded but not yet recorded is found again by its tag and updated in place, never duplicated.
6. **Failure policy.** A per-file error (file vanished, unreadable, Drive error for that file) is reported and the run carries on; the exit code is 1 if any file failed. Sign-in errors (`ErrNotSignedIn`, `ErrSessionExpired`) and a cancelled context abort at once. A scan problem with the root itself aborts.
7. **Sequential.** One file at a time. Bounded concurrency comes with the job queue in ticket 17, and retries with backoff on 403/429/5xx in ticket 16, so a rate-limited run can fail files until then; the report says which and a rerun resumes.
8. **The DB is required for `sync`.** Unlike `put`, where the DB only caches folder IDs, `sync` stores file rows. If it cannot open, `sync` exits 1 with the error.
9. **CLI.** `syncd sync [--root DIR]`, root defaulting to `~/MySync` through the existing `homeDir` var, as `scan` does. Stdout: one `uploaded <rel_path>` line per file. Stderr: `failed <rel_path>: <reason>` lines and a final `N uploaded, M already synced, K failed`. Exit 0, 1 (any failure or abort) or 2 (usage).
10. **Never log tokens or file contents.** Paths in output are fine.
11. **Out of scope:** MD5 skip and updates (12), revisions (13), retries (16), queue and concurrency (17), deletes and renames (25 to 27), logging (14).

## Shape

```
syncd sync [--root DIR]
  -> homeDir default ~/MySync; store.OpenRepo(dbPath())          (slice 2; required)
  -> drive.NewUploader(...)                                       (service, token source, MySync ID, once)
  -> sync.Sync(ctx, root, repo, uploader.Upload, report):
       res := Scan(root)                       (ignore rules, skipped entries as in scan)
       for each entry, sorted by rel_path:
         row exists            -> already synced, skip                     (slice 2)
         upload(ctx, root/rel, rel)
           ok                  -> upsert row {rel_path, drive_file_id, synced_md5, size, mtime}
                                  report "uploaded rel"
           sign-in error / ctx -> abort, return the error
           other error         -> report "failed rel: reason", failed++
  -> stderr: "N uploaded, M already synced, K failed"; exit 0, or 1 if any failed or aborted
```

```go
// internal/sync/sync.go
type UploadFunc func(ctx context.Context, localPath, relPath string) (drive.FileInfo, error)

type Event struct {
	RelPath string
	Err     error // nil means uploaded
}

type Summary struct{ Uploaded, Synced, Failed int }

func Sync(ctx context.Context, root string, repo *store.Repo, upload UploadFunc, report func(Event)) (Summary, error)

// internal/drive/upload.go
func NewUploader(ctx context.Context, client Client, tokens KeyringStore, roots FileStore, folders *store.Repo, extra ...option.ClientOption) (*Uploader, error)
func (u *Uploader) Upload(ctx context.Context, localPath, relPath string) (FileInfo, error)
```

## Files to create/modify

| File | Change |
| --- | --- |
| `internal/drive/upload.go` | `Uploader`, `NewUploader`; `Upload` becomes a wrapper |
| `internal/sync/sync.go` (+ `sync_test.go`) | `Sync`, `UploadFunc`, `Event`, `Summary` |
| `cmd/syncd/main.go` (+ `main_test.go`) | `sync` command, wiring, output |
| `internal/sync/doc.go`, `internal/drive/doc.go` | Package comments |
| `README.md`, `docs/wiki/index.md`, `requirements.md` | Docs and status |

Reused: `sync.Scan` and `Result`, `drive.Upload` logic, `ensureDir` and its folder cache, `store.Repo.Get`/`Upsert`, `homeDir` and `dbPath` vars, `reportDriveErr`.

## Testing plan

Table-driven; the engine against a fake `UploadFunc` and a temp DB, the `Uploader` against the existing fake Drive server, all under `go test -race ./...`. The risky cases: an error halfway through the tree, Ctrl+C between files, a row written for a failed upload (must not be), a file that vanishes after the scan, and a rerun after a crash between upload and row write.

- Engine: uploads in `rel_path` order; a per-file error is reported and the run carries on; a sign-in error aborts and returns it; a cancelled context stops before the next file; ignored and special files never reach `upload`.
- Rows (slice 2): a success writes one row with the expected fields; a failure writes none; entries with a row are skipped; an interrupted run followed by a second run uploads only the rest; a lost DB re-uploads as in-place updates.
- `Uploader`: many files make one token request and one root check; folder cache behaviour matches `Upload`.
- CLI: usage and exit codes (0, 1, 2), default root, signed out gives the login hint and uploads nothing.

## Verification

1. `make vet test` passes under `-race`.
2. Build a tree with a nested folder, a `.syncignore`d file and an empty directory; `bin/syncd sync --root tree` mirrors it in Drive; ignored files and the empty directory do not appear.
3. Run it again: `0 uploaded, N already synced`, nothing changes in Drive.
4. Delete `burrow.db` and run it again: files are found by tag and updated in place; no duplicate files or folders.
5. Interrupt a large run (Ctrl+C) and rerun: it finishes the rest.
6. Signed out: `syncd sync` exits 1 with the login hint and uploads nothing.
