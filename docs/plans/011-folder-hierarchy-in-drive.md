# Ticket 10: Folder hierarchy in Drive (nested `put`, folder-ID cache) (GH-10)

Source: `requirements.md` section 14, ticket 10 (M2 One-shot sync). Depends on 6 (done) and on plan 012 (GH-11, drop `watch_id`), which should land first. Unblocks 11 (one-shot sync needs 8, 9 and 10; 8 and 9 are done).
Done when: **a nested local tree is mirrored in Drive.**
Final location after approval: `docs/plans/011-folder-hierarchy-in-drive.md` (same format as `docs/plans/010-*.md`).

## To-do

Vertical slices; review after each. Every slice leaves `make vet test` green.

| # | Slice | Step | Done |
| --- | --- | --- | --- |
| 1 | **Slice 1: nested `put` (no cache)** | `internal/drive/upload.go`: `rel_path` validation, `ensureDir` (find or create by name and parent, no cache), create files in their folder, `findByTags` without the parent clause and with `mimeType != folder`, `FindByTags` no longer calls `ensureRootWith`. Extend the `uploadDrive` fake in `upload_test.go` to answer folder list/create. Tests: nested create, second file in the same dir reuses the folder, duplicate folder names pick the oldest, update does no folder calls, bad `rel_path`s, names with `'` and `\`. **Review point (demo: `find tree -type f -exec syncd put --root tree {} \;` mirrors the tree in the Drive web UI).** | [ ] |
| 2 | **Slice 2: folder cache table** | `internal/store/migrations/00002_folders.sql`; `Repo.GetFolder(ctx, relDir)` (returns `ErrNotFound`) and `Repo.PutFolder(ctx, relDir, id)` (upsert) in `repo.go`, using `call[T]`; update `store_test.go` (version 2, tables). Tests: round trip, upsert overwrites, missing returns `ErrNotFound`, concurrent calls under `-race`. **Review point.** | [ ] |
| 3 | **Slice 3: use the cache** | `ensureDir` reads and writes the cache, verifies the deepest cached folder with `files.get`, and self-heals on 404 or trashed. `drive.Upload` takes a `*store.Repo` (nil allowed, meaning no cache); `put` in `cmd/syncd/main.go` opens it via `dbPath()`. Tests: second file in the same dir makes zero folder list/create calls, a trashed cached folder is recreated and re-cached, a deleted DB re-finds the existing folders without duplicating them, a nil cache still works. **Review point (demo: delete `burrow.db`, `put` again, no duplicate folders in Drive).** | [ ] |
| 4 | **Slice 4: docs** | Wiki "Uploading a file" (replace "Everything is placed directly in `MySync/`. Per-watch subfolders come with ticket 10" with the nested layout and the folder rules), "State database" (the `folders` table), README put section, `internal/drive/doc.go`, ticket 10 `=> Done` in `requirements.md`. **Review point.** | [ ] |

## Context

`syncd put` uploads every file directly into `MySync/` (ticket 6), whatever its `rel_path`. Ticket 11 uploads a whole tree, so folders must be created lazily from `rel_path` and their Drive IDs cached, otherwise every file costs a lookup per directory level. Drive stays the source of truth: the folder cache is a disposable SQLite table, and a lost cache is rebuilt by looking the folders up again.

## Decisions

Agreed with you:

1. **Layout: `MySync/a/b/c.txt`.** Directories mirror `rel_path` directly under `MySync/`. There is no per-watch folder (`watch_id` is dropped by GH-11, plan 012, which lands first). Files already uploaded flat by ticket 6 are found by their `rel_path` tag and updated where they are.
2. **Folder cache in SQLite.** New table `folders(rel_dir PRIMARY KEY, drive_folder_id)` in migration `00002_folders.sql` (ADR-004: each ticket adds its own migration), reached through new `Repo` methods so the owner-goroutine rule holds. `rel_dir` is slash-form, like `files.rel_path`.

Recommended:

3. **Resolve recursively, create lazily.** `ensureDir(relDir)`: `""` or `.` is the `MySync` root; a cached ID is used only after `files.get(fields=id,trashed)` confirms it exists and is not trashed (same check as `rootUsable`); otherwise resolve the parent first, then find the child by name and parent (oldest wins, as for the root) or create it, and write the cache entry. Trashing a folder trashes its children, so verifying the deepest folder is enough; a stale entry heals itself on the next use.
4. **Folders are found by name, parent and mimeType, not by tags.** The folder tree already implies `rel_path` (requirements section 5), so folders carry no `appProperties`. Names go through the existing `escapeQuery`. The shared `oldestMatch` helper is reused.
5. **File lookup stops depending on the parent.** `findByTags` drops the `'<root>' in parents` clause and adds `mimeType != folder`, so a file is found by its tags wherever it lives. This also means `stat --watch` no longer creates `MySync/` or any folder, and later renames (ticket 25) keep working.
6. **Upload order.** Look the file up by its `rel_path` tag first. Update replaces content and tags only. Only a create resolves the directory chain, so updates cost no folder calls.
7. **`rel_path` is validated at the `drive.Upload` boundary:** non-empty, no leading `/`, no `.` or `..` component. This matches `relPathFor` in `cmd/syncd/main.go`.
8. **The cache is optional in `drive`.** `Upload` takes a `*store.Repo`; `nil` means no cache (every miss walks Drive). `syncd put` opens the repo with `store.OpenRepo(dbPath())`, so `put` now creates and migrates the state DB. The DB is a disposable cache, so this is safe.
9. **No locking for concurrent workers yet.** Two workers could create the same folder twice. "Oldest wins" tolerates duplicates, and ticket 11 is the first multi-file consumer, so any single-flight guard belongs there.
10. **Out of scope:** folder rename and delete (ticket 27), deleting cache rows (`Repo` gets only `GetFolder` and `PutFolder`), per-watch folders.
11. **Never log SQL arguments or tokens.** Paths in errors are fine.

## Shape

```
syncd put [--root DIR] <file>
  -> rel_path validated (no "", leading "/", "." or "..")
  -> EnsureRoot -> MySync folder ID
  -> files.list by tag (rel_path, not a folder, not trashed)
       match    -> files.update (content + tags)           (no folder work)
       no match -> ensureDir(path.Dir(rel_path)) -> files.create(parent = dir folder)

ensureDir(relDir)
  "" or "."            -> MySync root ID
  cached + usable      -> cached ID          (files.get id,trashed)
  otherwise            -> parent := ensureDir(path.Dir(relDir))
                          id := oldest folder named base under parent, or create it
                          cache.Put(relDir, id)
```

```go
// internal/store/repo.go
func (r *Repo) GetFolder(ctx context.Context, relDir string) (string, error) // ErrNotFound when absent
func (r *Repo) PutFolder(ctx context.Context, relDir, driveFolderID string) error
```

```sql
-- internal/store/migrations/00002_folders.sql
-- +goose Up
CREATE TABLE folders (
    rel_dir         TEXT PRIMARY KEY,
    drive_folder_id TEXT NOT NULL
);
-- +goose Down
DROP TABLE folders;
```

## Files to create/modify

| File | Change |
| --- | --- |
| `internal/drive/upload.go` | `ensureDir`, validation, tag query without parent, `Upload` takes `*store.Repo` |
| `internal/drive/root.go` | Generalise `rootUsable` into a folder check reused for cached folders |
| `internal/drive/upload_test.go` | Fake answers folder queries; new cases |
| `internal/store/migrations/00002_folders.sql` | `folders` table |
| `internal/store/repo.go` (+ `repo_test.go`), `store_test.go` | `GetFolder`, `PutFolder`; version 2 |
| `cmd/syncd/main.go` (+ `main_test.go`) | `put` opens the repo, passes it to `Upload` |
| `README.md`, `docs/wiki/index.md`, `requirements.md`, `internal/drive/doc.go` | Docs and status |

Reused: `oldestMatch`, `escapeQuery`, `rootUsable`, `ensureRootWith`, `serviceFromStore` (`internal/drive`); `Repo`, `call[T]`, `store.OpenRepo`, `dbPath` (`internal/store`, `cmd/syncd`).

## Testing plan

Table-driven against the fake Drive server (no network) and `t.TempDir()` databases, all under `go test -race ./...`. The risky cases: duplicate folder names, a folder trashed behind the cache, a lost database, and names with quotes or backslashes in the Drive query.

- Nested create makes one folder per level and puts the file in the deepest one; a second file in the same directory makes no folder list or create calls.
- Duplicate folders with the same name: the oldest is used and nothing is deleted.
- Update of an existing file (found by tags) makes no folder calls.
- Invalid `rel_path`s (`""`, `/a`, `a/../b`, `./a`) are rejected before any Drive call.
- Cache: hit costs one `files.get`; 404 or trashed re-resolves and rewrites the entry; a deleted database re-finds existing folders without duplicates; a nil cache works.
- `Repo`: round trip, upsert overwrites, `ErrNotFound`, concurrent `GetFolder`/`PutFolder` under `-race`.
- `store_test.go`: version 2 and the `folders` table appear in `Status`.

## Verification

1. `make vet test` passes under `-race`; `go test -race -count=20 ./internal/store/` is stable.
2. Build a tree (`a/x.txt`, `a/b/y.txt`, `z.txt`); run `find tree -type f -exec bin/syncd put --root tree {} \;`. `MySync/` shows `a/x.txt`, `a/b/y.txt` and `z.txt` in the Drive web UI.
3. Run it again: nothing is duplicated and the file IDs are unchanged.
4. Delete `burrow.db` and run it again: no duplicate folders.
5. Trash `MySync/a` in the web UI and `put` a file under `a/`: the folder is recreated.
6. `bin/syncd stat --path a/b/y.txt` finds the file and creates nothing in Drive.
