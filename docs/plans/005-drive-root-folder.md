# Ticket 5: Drive root folder (`syncd root`) (GH-TBD)

Source: `requirements.md` section 14, ticket 5 (M1 Hello Drive). Depends on 4 (done).
Done when: **`MySync/` appears in the Drive web UI.**
Final location after approval: `docs/plans/005-drive-root-folder.md` (same format as `docs/plans/archive/004-*.md`). A GitHub issue is created first via `do-create-ticket` (title/body shown before creating); the doc title then carries its `(GH-n)`.

## Context

Tickets 3-4 gave us a persisted sign-in. Ticket 6 (`syncd put`) and later tickets need a parent folder to upload into. This ticket creates or finds the app-owned `MySync/` folder in Drive and caches its ID locally. With the `drive.file` scope the app only sees files it created, so a hand-made `MySync/` is invisible; the app must create and own it.

## Decisions

1. **Find, then create.** Query `name = 'MySync' and mimeType = 'application/vnd.google-apps.folder' and 'root' in parents and trashed = false`, ordered by `createdTime`. Because of `drive.file`, results can only be folders this client created. If none, `files.create` the folder under My Drive root.
2. **Duplicates:** Drive allows same-name folders. If several match, use the oldest (`createdTime asc`) deterministically; do not delete or merge.
3. **Cache = small JSON file** `state.json` in the `adrg/xdg` data dir (`xdg.DataFile("burrow/state.json")`, Local not Roaming on Windows). Holds `{"root_folder_id": "..."}`. Disposable per CLAUDE.md: deleting it only costs one lookup. Never in the sync root. Ticket 7 can fold it into SQLite.
4. **Validate the cached ID before trusting it:** `files.get(id, fields=id,trashed)`. On 404 or `trashed: true`, fall back to find/create and rewrite the cache. A cache hit costs one cheap call, so a user who trashes `MySync/` in the web UI doesn't get uploads into the trash.
5. **Idempotent:** running `syncd root` twice prints the same ID and creates nothing the second time.
6. **`RootStore` interface** (`Load() (string, error)`, `Save(id string) error`) with `FileStore` in `internal/drive`, mirroring `TokenStore`; `ErrNoRoot` sentinel for a missing cache. Keeps tests off the real filesystem and the function testable with `t.TempDir()`.
7. **Output:** `syncd root` prints `MySync folder: <id>` and `https://drive.google.com/drive/folders/<id>`. Errors reuse the `whoami` hints (not signed in, session expired).
8. **Auth reuse:** build the Drive service from `TokenFromRefresh` + `OAuthConfig` as `Resume` does. Factor a small unexported `newService(ctx, cfg, tok, extra...)` out of `email()` in `about.go` rather than duplicating the client setup.
9. Never log tokens or request bodies; the folder ID is not a secret.

## To-do

Vertical slices; review after each.

| # | Slice | Step | Done |
| --- | --- | --- | --- |
| 1 | **Slice 1: `syncd root` creates/finds `MySync/` (no cache)** | `go get github.com/adrg/xdg` (used in slice 2; skip here if unused) and factor `newService` out of `about.go` | [x] |
| 2 | | `internal/drive/root.go`: `EnsureRoot(ctx, client, store, extra...)` find/create logic, oldest-wins on duplicates | [x] |
| 3 | | `cmd/syncd/main.go`: `root` subcommand with not-signed-in / expired hints; prints ID and URL. **Review point (demo: folder shows in Drive web UI).** | [x] |
| 4 | **Slice 2: local cache** | `internal/drive/rootstore.go`: `RootStore`, `FileStore` (xdg path, atomic write via temp + rename), `ErrNoRoot` | [x] |
| 5 | | `EnsureRoot` reads cache, validates via `files.get`, falls back and rewrites on 404/trashed | [x] |
| 6 | | Tests: fake Drive server (`httptest`, `option.WithEndpoint`): creates when absent, reuses when present, oldest of duplicates, cache hit skips list, stale/trashed cache recovers, store round trip and corrupt file treated as empty. **Review point.** | [ ] |
| 7 | **Slice 3: docs** | README + `docs/wiki/index.md` (command list, cache location, decisions); mark ticket 5 done in `requirements.md` | [ ] |
| 8 | | Demo: `syncd root` twice (same ID, one folder in Drive); trash it in the web UI, run again (new folder, cache updated); delete `state.json` (re-finds same folder). **Review point.** | [ ] |

## Files to create/modify

| File | Change |
| --- | --- |
| `internal/drive/root.go` (+ `_test.go`) | Create: `EnsureRoot`, constants for name and folder MIME type |
| `internal/drive/rootstore.go` (+ `_test.go`) | Create: `RootStore`, `FileStore`, `ErrNoRoot` |
| `internal/drive/about.go` | Extract `newService` helper |
| `cmd/syncd/main.go` | Add `root` subcommand and dispatch case |
| `cmd/syncd/main_test.go` | Cases for `root` (not signed in, usage unaffected) |
| `go.mod` / `go.sum` | Add `github.com/adrg/xdg` |
| `README.md`, `docs/wiki/index.md`, `requirements.md` | Docs and ticket status |

Reused: `drive.LoadClient`, `Client.OAuthConfig`, `TokenFromRefresh`, `TokenStore`/`KeyringStore`, `ErrNotSignedIn`, `ErrSessionExpired`, the `run(args, stdout, stderr)` dispatch and `keyring.MockInit()` test pattern.

## Testing plan

Table-driven, no network, no real keychain or home dir. A fake Drive server answers `files.list`, `files.get`, `files.create` and a fake token endpoint answers refreshes (same pattern as `resume_test.go`). `FileStore` tests use `t.TempDir()` via an injectable path. Assert the create request body has `mimeType` folder and no parents other than root, and that nothing in output contains the token.

## Verification

1. `make vet test` passes under `-race`.
2. Manual: `bin/syncd login`, `bin/syncd root` prints an ID and URL; open the URL, `MySync/` is in My Drive.
3. Run `root` again: same ID, still one `MySync/` folder. `cat ~/.local/share/burrow/state.json` shows the ID.
4. Trash `MySync/` in the web UI, run `root`: a new folder is created and the cache updated. Delete `state.json`, run `root`: the existing folder is found, not duplicated.
5. `bin/syncd logout` then `root` exits 1 with the sign-in hint.
