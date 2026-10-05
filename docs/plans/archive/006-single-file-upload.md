# Ticket 6: Single-file upload (`syncd put`, `syncd stat`) (GH-5)

Source: `requirements.md` section 14, ticket 6 (M1 Hello Drive). Depends on 5 (done).
Done when: **Checkpoint: a file shows in Drive; `syncd stat` prints its ID and tags.**
Final location after approval: `docs/plans/006-single-file-upload.md` (same format as `docs/plans/005-*.md`).

## Context

Tickets 1-5 give us sign-in, keychain token storage and the cached app-owned `MySync/` folder (`drive.EnsureRoot`). Ticket 6 is the first upload and the first command that takes arguments. It proves the `appProperties` tagging (`watch_id`, `rel_path`) that every later ticket depends on. No DB exists yet (ticket 7), so Drive is the only record, which matches the "Drive is the source of truth" invariant.

## Decisions

1. **Placement:** upload directly into `MySync/`. Per-watch subfolders and nested hierarchy are ticket 10, so they are out of scope here.
2. **Tags:** `put [--watch ID] [--root DIR] <file>`. `watch_id` defaults to `default`. `rel_path` is the file path relative to `--root`, in slash form, and defaults to the file's basename when `--root` is absent. Reject a file outside `--root`, and reject `..` or absolute `rel_path`, so no machine-specific parts leak.
3. **No duplicates:** before creating, query `appProperties has { key='watch_id' and value='..' } and appProperties has { key='rel_path' and value='..' } and trashed = false and '<root>' in parents`. If it matches, call `files.update` (content plus tags). Otherwise `files.create`. Files are tracked by ID, and Drive allows duplicate names. Ticket 12 adds the MD5 skip.
4. **Streaming:** `Media(f, googleapi.ChunkSize(8<<20))` from the open file, never reading it whole. Retries and backoff are ticket 16.
5. **Fields requested:** `id,name,md5Checksum,size,headRevisionId,appProperties,webViewLink`. Printing `headRevisionId` now costs nothing, and tickets 13+ will store it.
6. **`stat`:** `syncd stat <file-id>` or `syncd stat --watch ID <rel_path>`. It prints ID, name, size, MD5, revision and the two tags. By ID uses `files.get`; by tags uses the same query as `put`.
7. **Argument parsing:** `run` passes `args[1:]` to commands, which use `flag.NewFlagSet` (stdlib, no framework). Exit codes stay 0 (ok), 1 (runtime error), 2 (usage).
8. **Never log tokens or request bodies.** Error mapping reuses the `ErrNotSignedIn` / `ErrSessionExpired` switch from `root`.
9. **Service setup:** extract an unexported `serviceFromStore` helper from `ensureRoot`'s load-token-and-build-service steps, with the same exported-wrapper plus unexported-twin pattern (`cfg *oauth2.Config`, `extra ...option.ClientOption`) for tests.

## To-do

Vertical slices; review after each.

| # | Slice | Step | Done |
| --- | --- | --- | --- |
| 1 | **Slice 1: `syncd put` uploads a file** | `internal/drive/upload.go`: extract `serviceFromStore`; add `Upload` with find-by-tags, create and update paths, plus the `uploadFile(ctx, cfg, ...)` twin | [x] |
| 2 | | `cmd/syncd/main.go`: pass `args[1:]` to commands; `put` with `flag.NewFlagSet`, `rel_path` validation, error hints; prints file ID and web link | [x] |
| 3 | | Tests: fake Drive (create vs update, tag query, `appProperties` body, 8 MB chunk, `invalid_grant`), `TestRun` cases for `put`. **Review point (demo: file shows in `MySync/`).** | [x] |
| 4 | **Slice 2: `syncd stat` shows ID and tags** | `drive.Stat` and `drive.FindByTags` (shared with `Upload`) | [x] |
| 5 | | `stat` command (by ID or by `--watch` + `rel_path`), usage and error cases, tests. **Review point (checkpoint: ID and tags printed).** | [x] |
| 6 | **Slice 3: docs** | README and `docs/wiki/index.md` (new section plus usage block); mark ticket 6 `=> Done` in `requirements.md` | [x] |
| 7 | | Demo: put, stat, edit and put again (same ID, one file), logout then put. **Review point.** | [x] |

## Files to create/modify

| File | Change |
| --- | --- |
| `internal/drive/upload.go` (+ `_test.go`) | Create: `Upload`, `Stat`, `FindByTags`, `FileInfo` |
| `internal/drive/root.go` | Extract `serviceFromStore` (behavior unchanged) |
| `cmd/syncd/main.go` | Pass args to commands; add `put` and `stat` |
| `cmd/syncd/main_test.go` | Usage and error cases for `put` and `stat` |
| `README.md`, `docs/wiki/index.md`, `requirements.md` | Docs and ticket status |

Reused: `newService`, `TokenFromRefresh`, `sessionError`, `TokenStore`/`KeyringStore`, `RootStore`/`FileStore`, `EnsureRoot`, `ErrNotSignedIn`, `ErrSessionExpired`, and the `fakeDrive` / `memStore` / `memRoots` / `keyring.MockInit()` test patterns.

## Testing plan

Table-driven, no network, no real keychain or home dir. An `httptest` fake serves `/token` and the Drive routes, including the upload endpoints. Cases:
- a new file is created with the right parent and tags;
- a second put updates the same ID instead of creating;
- `..` and out-of-root paths are rejected;
- a missing file exits 1;
- `stat` by ID and by tags;
- not signed in gives the login hint.

Assert that no output contains the token. Run with `-race`.

## Verification

1. `make vet test` passes under `-race`.
2. Manual: `bin/syncd login`, then `bin/syncd put --watch demo notes.txt` prints a file ID; the file shows in `MySync/` in the Drive web UI.
3. `bin/syncd stat <id>` prints the ID, `watch_id=demo` and `rel_path=notes.txt`.
4. Edit the file and put again: same ID, new revision, still one file in Drive.
5. `bin/syncd logout` then `put x` exits 1 with the sign-in hint.
