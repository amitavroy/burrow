# Drive Sync App — High-Level Requirements

Oct 2, 2026 · @Amitav Roy

## 1. Overview and goals

A lightweight desktop app written in Go that watches a local sync folder and mirrors every change to Google Drive, working as a Dropbox replacement with Drive as the storage backend.

**Goals**

- Automatically upload new, changed, renamed and deleted files from a local root folder to Drive.
- Stay light on memory and simple to maintain: a headless sync daemon built as one Go binary, at roughly 10 to 25 MB idle, with the UI opened only when needed.
- Treat Drive as the source of truth, so a new machine can be restored from Drive alone.
- Never lose user data: deletes go to trash, conflicts keep both versions.

**Success criteria**

- Saving a file in the sync root appears in Drive within about a minute, without manual action.
- Changes made while the app was closed or the machine was asleep are picked up on next launch.
- Deleting the local database loses nothing; it is rebuilt from Drive and the local folder.
- The daemon survives crashes and restarts without losing queued work.

## 2. Scope

Version 1 is one-way sync (local to Drive) plus a restore path for new machines. Two-way sync and conflict handling follow as a second phase.

| Area | Phase 1 (v1) | Phase 2 | Later / optional |
| --- | --- | --- | --- |
| Direction | Local to Drive | Two-way via Drive `changes` API |  |
| Files watched | One sync root, e.g. `~/MySync` |  | Arbitrary folders via a per-machine mapping |
| New machine | One-time restore (pull) from Drive | Live sync on several machines |  |
| Conflicts | Not applicable | Keep-both copies | Ask-the-user UI |
| UI | CLI, then tray | Settings window, status and error log | Bandwidth throttling, versioning |

**Included in v1: revision tracking.** The app records the Drive revision ID of every upload from the start. Browsing and restoring old versions is not part of v1, but the data it needs is.

**Out of scope for v1:** merging file contents, sharing with other Google accounts, and syncing the same Drive folder from two live machines.

## 3. Architecture and stack

&#91;embedded content: architecture · 5-stage sync pipeline, state DB, optional UI\]

File events are debounced, queued and uploaded by the sync worker, with the state database tracking every file. A startup scan feeds the same queue, and the UI is a separate process that talks to the daemon.

| Concern | Go package |
| --- | --- |
| Concurrency | Goroutines and channels (standard library) |
| File watching | `github.com/fsnotify/fsnotify`, with a debounce layer written in the app |
| Directory walking | `filepath.WalkDir` (standard library) |
| Ignore rules | A gitignore-style matcher such as `go-gitignore` |
| Drive API | Official `google.golang.org/api/drive/v3` client, which includes resumable uploads |
| OAuth (loopback, PKCE) | `golang.org/x/oauth2` plus a small local HTTP listener |
| Keychain | `github.com/zalando/go-keyring` |
| Local config (`.env`) | `github.com/joho/godotenv`, loads the OAuth client ID and secret in development |
| State database | `modernc.org/sqlite` (pure Go, no C compiler needed) |
| Migrations | `goose` or `golang-migrate` |
| Hashing | `crypto/md5`, streamed with `io.Copy` |
| Data directories | `github.com/adrg/xdg` |
| Logging | `log/slog` (standard library) with `lumberjack` for rotation |
| Tray UI | `fyne.io/systray` |

## 4. Authentication and Drive access

The app signs in as the Google account that owns the storage, using the OAuth 2.0 desktop flow. Sharing a folder with a second account (a service account) is not used, because service accounts have no storage quota on a personal Drive and uploads fail with `storageQuotaExceeded`.

- **Flow:** loopback redirect with PKCE, via `golang.org/x/oauth2` and a small local HTTP listener for the redirect.
- **Scope:** `drive.file` only. The app sees just the files it created, so it is least-privilege and needs no Google verification.
- **Root folder:** the app creates its own `MySync/` folder in Drive on first run. With `drive.file` it cannot see a folder created by hand.
- **Consent screen:** set it to "In production". In "Testing" mode refresh tokens expire after 7 days.
- **Token storage:** refresh token in the OS keychain (`go-keyring`), never in the database or a config file.
- **One OAuth client ID** ships with the app and never changes. `drive.file` access is tied to the client ID, so a different one on another machine cannot see the files.

## 5. Sync root and Drive layout

Each machine has one local setting, the sync root path, and Drive mirrors the folder tree under it, the way Dropbox works.

- **Local:** one root per machine (`~/MySync` on Linux, `D:\MySync` on Windows). The root path lives only in local config, never in Drive.
- **Drive:** a `MySync/` folder whose tree mirrors the local tree. A `watch_id` (a top-level subfolder per synced local folder) is deferred with the multi-folder feature below; v1 has one root and does not write it.
- **Relative path:** every file's `rel_path` is its path under the root, with no machine-specific parts.
- **Limitation:** files must live inside the root. Watching an existing folder in place (for example `~/Documents`) is a later feature, via a per-machine mapping of `watch_id` to local path. Symlinks are avoided because they behave inconsistently across operating systems.

**Metadata written on every upload**

Each Drive file gets private `appProperties`, so the database can be rebuilt from Drive alone:

```json
{ "rel_path": "reports/q3.pdf" }
```

The folder tree already implies `rel_path`. The tags are a safety net that survives a renamed Drive folder and makes matching unambiguous. Files are always tracked by Drive file ID, never by name, because Drive allows duplicate names in one folder.

**`config.json` in Drive**

A small shared settings file, not a database: ignore rules and a settings version. It holds no absolute paths. With the single-root model it may be nearly empty.

## 6. Local state and database

The SQLite database is a rebuildable cache, never the source of truth. Drive holds the truth, so the database is never copied or synced between machines.

| OS | Database and logs location (via the `adrg/xdg` package) |
| --- | --- |
| Linux | `~/.local/share/<app>/` |
| macOS | `~/Library/Application Support/<app>/` |
| Windows | `%LOCALAPPDATA%\<app>\` (Local, not Roaming) |

- **Never inside the sync root**, or the app would sync its own state file.
- **WAL mode**, one goroutine owns the connection, others talk to it over channels.
- **Migrations from day one** (`goose` or `golang-migrate`).
- **Per-file record:** relative path, Drive file ID, size, mtime, inode or file ID, local MD5, last-synced MD5.
- **Base state (written from v1):** `base_md5` and `base_revision_id`, the MD5 and Drive revision ID at the last successful sync. Phase 2 conflict detection reads them as they are.
- **Revision history (v1):** a `file_revisions` table with one row per successful upload or download: file ID, Drive revision ID, MD5, size, time and source.
- **Other tables:** persistent job queue, `sync_errors` / events table for the UI, Drive `changes` page token (phase 2).
- **Keychain:** the OAuth refresh token stays out of the database, so deleting the database loses only cache.

## 7. Sync engine (phase 1, one-way)

The engine turns file system events into reliable Drive operations. It is a separate library so it can run headless.

| Function | Requirement |
| --- | --- |
| Watching | `fsnotify` in its own goroutine, sending events over a channel. The app adds a watch for every subfolder, including new ones |
| Debounce and stabilize | Debounce per path with timers, then upload only once size and mtime are stable. Editors write temp files and fire several events per save |
| Ignore rules | Gitignore-style patterns: `.git`, `node_modules`, `*.tmp`, `~$*` |
| Startup reconciliation | On launch, walk the sync root, diff against the database and enqueue differences. The watcher misses everything that happens while the app is closed or asleep |
| New file | `files.create` with `appProperties`; folder hierarchy created lazily with a path to folder-ID cache |
| Changed file | `files.update` with the new content; skip when local MD5 matches Drive's `md5Checksum` |
| Large files | Resumable upload through the Go client in 8 MB chunks (multiples of 256 KB), streamed from disk so files never sit fully in RAM; MD5 computed by streaming |
| Rename or move | Detected by matching inode and hash in the database, then `files.update` with `addParents` / `removeParents` instead of a re-upload |
| Delete | Move to trash (`trashed: true`), never hard delete |
| Retries | Exponential backoff with jitter on 403, 429 and 5xx |
| Durability | Persistent queue, so a crash or restart loses no pending work |

**Revisions (v1):** after every successful create or update, the worker reads `headRevisionId` from the response and writes it to `base_revision_id` and to a new `file_revisions` row. Drive prunes old revisions of regular files on its own schedule, and revisions count toward storage, so pinning them (`keepRevisionForever`) is an optional per-watch setting, off by default. Listing or restoring previous versions can come later and needs no new data.

Platform notes: `fsnotify` reports renames differently on each OS, and on Linux large trees can hit `fs.inotify.max_user_watches`, which must be surfaced as a clear error. `fsnotify` does not watch subfolders recursively, so the app registers each directory itself.

## 8. New machine restore

On a new machine the database is empty, so the app rebuilds it from Drive and the local folder. This needs a one-time download path, which is much smaller than full two-way sync.

1. Sign in with the same Google account and the same OAuth client ID.
2. Fetch `config.json` from Drive.
3. The user chooses the local sync root path for this machine.
4. List all Drive files under `MySync/`, reading `appProperties`, `md5Checksum`, the Drive file ID and headRevisionId.
5. For each file, check `<sync root>/<rel_path>`:
   - Exists and the hash matches: insert a database row, no transfer.
   - Missing: download to a temp file, rename into place atomically, insert a database row.
   - Exists with a different hash: flag a conflict and keep both copies.
6. Start the watcher and normal sync.

**Rule:** until two-way sync exists, a second machine is restore-only, or each device uses its own root (`MySync/<device-name>/`). Two live machines pointed at the same Drive folder with one-way sync would overwrite each other.

## 9. Phase 2: two-way sync and conflict resolution

Conflicts are detected by comparing three versions: local, Drive, and the base recorded at the last successful sync. Timestamps alone cannot tell "I edited it" from "the other machine edited it".

| Local vs base | Drive vs base | Action |
| --- | --- | --- |
| Unchanged | Unchanged | Nothing |
| Changed | Unchanged | Upload |
| Unchanged | Changed | Download |
| Changed | Changed, same MD5 | Nothing, update base |
| Changed | Changed, different MD5 | Conflict |

**Detecting remote changes:** store a `startPageToken` and poll the Drive `changes.list` API instead of re-listing everything.

**Resolution: keep both (the Dropbox approach)**

1. The version already on Drive keeps the original name.
2. The machine that detects the conflict renames its local file to something like `q3 (conflicted copy, laptop, 2026-10-02).pdf`.
3. It downloads the Drive version to the original path.
4. The conflicted copy syncs up as a normal new file, so every machine ends with both.

Rejected alternatives: last-writer-wins by mtime (clock drift, lost edits), auto-merge (needs base content, text only), and prompting the user (a possible later UI, but the daemon must never block).

**Edge cases**

- **Edit vs delete:** the edit wins and the file is restored.
- **Rename vs edit:** tracked by Drive file ID, so they merge cleanly.
- **Both created the same path:** treated as a conflict, since no base exists and hashes differ.
- **Echo loops:** after a download, record the expected MD5 and ignore the watcher's matching event.
- **Safe downloads:** write to a temp file, confirm the local file still matches base, then rename atomically.
- **Race window:** check `headRevisionId` just before uploading and again via the changes feed afterward. Drive v3 does not reliably offer conditional content updates, so Drive revision history is only a secondary safety net.

## 10. Logging and diagnostics

Logging uses `log/slog` from the standard library, with structured attributes that tag each log line with the file path, job ID and Drive file ID.

|  | Development | Production |
| --- | --- | --- |
| Output | stderr, human-readable text handler | Rotating log files via `lumberjack` |
| Level | Environment variable, e.g. `SYNC_LOG=debug` | `info` by default, with a "debug logging" toggle in settings |
| Retention | n/a | Size-based rotation, 7 to 14 backups |
| Format | Plain text | Plain text (JSON only if logs are shipped elsewhere) |

- Recover from panics in every top-level goroutine and log them with `slog`, so a daemon never crashes silently.
- Close and flush the log file cleanly on shutdown.
- Never log tokens, auth headers or request bodies.
- **UI-facing errors come from an `events` / `sync_errors` table in SQLite**, not from parsing log files. Example: "report.pdf failed: quota exceeded, will retry".
- The UI offers an "Open logs folder" action.

## 11. UI and packaging

The sync daemon is always running and headless. The UI is a separate, optional process, because the UI is what drives memory use, not the sync engine.

- **Daemon:** one Go binary, run as a launchd agent (macOS), systemd user service (Linux) or a Windows startup entry. Always on, roughly 10 to 25 MB idle.
- **UI:** a small tray app (`fyne.io/systray`) with a simple menu first. A fuller settings window is added only if needed. Built last.
- **Daemon and UI talk** over a local socket or localhost HTTP.
- **UI features:** sync status, pause and resume, add or change the sync root, error log from the `sync_errors` table, open logs folder, debug logging toggle.
- **One Go module, split by package from day one:**

```
cmd/syncd/      # main package: runs the daemon headless
cmd/syncui/     # tray UI (optional, added later)
internal/sync/  # watcher, debounce, queue, uploader
internal/store/ # SQLite state DB and migrations
internal/drive/ # Drive client and auth
```

The whole engine can then be built and tested through a CLI before any UI exists.

**Decision (ADR-001, 2026-10-02): keep the daemon and the UI as two processes**, even though Go is lighter than the Rust design this was first written for. The UI toolkit drives memory use, not the sync engine. `fyne.io/systray` needs cgo, and keeping it out of `syncd` leaves the daemon pure Go and easy to cross-compile. A UI crash also must not stop syncing. The control API is needed anyway for `syncd ctl status`. See `docs/wiki/index.md`, ADR section.

## 12. Non-functional requirements, risks and open questions

**Non-functional requirements**

- **Memory:** daemon idle at roughly 10 to 25 MB; large files streamed, never loaded whole.
- **Reliability:** persistent queue, startup reconciliation, retries with backoff, atomic file writes.
- **Safety:** deletes go to trash, conflicts keep both versions, no secrets in logs or the database.
- **Portability:** Linux, macOS and Windows from one codebase, using OS-standard data directories and keychains.

**Risks**

| Risk | Mitigation |
| --- | --- |
| Unreliable rename events across OSes | Track inode or file ID plus hash in the database |
| `fsnotify` is not recursive | Register a watch for every subfolder, including new ones, and rescan on startup |
| Drive rate limits on many small files | Backoff with jitter, bounded concurrency |
| `drive.file` scope cannot see pre-existing folders | App creates and owns its Drive root |
| Goroutine races and leaks | One goroutine owns the SQLite connection; run tests with `go test -race` |
| No conditional content updates in Drive v3 | Check `headRevisionId` before upload and via the changes feed |
| Google Drive duplicates and quotas | Track by Drive file ID; surface quota errors in the UI |

**Open questions**

- [ ] Which UI first: a minimal tray menu or a fuller settings window?
- [ ] Should a per-machine mapping for arbitrary folders be a v1.x feature? (This is where `watch_id` comes back, likely as a per-watch Drive folder plus a local-path mapping.)
- [ ] Per-device Drive roots, or restore-only second machines, until phase 2 ships?
- [ ] Rely on rclone for the first working version, then replace it?

## 13. Build order

The work runs in seven steps for phase 1, CLI first and UI last, then phase 2. Each item is a candidate task.

**Phase 1: one-way sync and restore**

- [ ] **1. Auth and first upload:** Go module skeleton, OAuth loopback with PKCE, token in keychain, upload one file with `appProperties`
- [ ] **2. State DB and one-shot sync:** SQLite schema and migrations, database in the app data directory, scan and sync one folder, MD5 skip, record the Drive revision ID per upload
- [ ] **3. Robust uploads:** resumable chunked upload, retries with backoff, persistent queue, `slog` logging with rotating files
- [ ] **4. Live watching:** `fsnotify` watcher with per-subfolder watches, debounce and stabilization, ignore rules, startup reconciliation
- [ ] **5. Rename, move and delete:** inode and hash matching, `addParents` / `removeParents`, trash on delete
- [ ] **6. Restore flow:** `config.json`, sync root selection, list Drive, hash local files, download missing files
- [ ] **7. Daemon and UI:** launchd, systemd and Windows startup packaging, then a tray UI with status, pause and error log

**Phase 2: two-way sync**

- [ ] Use the `base_md5` and `base_revision_id` values recorded since v1
- [ ] Drive `changes` poller with stored page token
- [ ] Three-way compare decision function, with unit tests for every table row
- [ ] Conflict copies, echo-loop suppression, safe atomic downloads
- [ ] Edge cases: edit vs delete, rename vs edit, both created

**Optional later:** per-machine folder mapping, bandwidth throttling, file versioning, ask-the-user conflict UI.

## 14. Task sequence (ticket breakdown)

The build splits into 41 tickets of roughly half a day to a day each, ordered so something works end to end as early as ticket 6. Demo checkpoints, where you can stop and judge the result, fall after tickets 6, 12, 22, 26, 32 and 36. Each row is meant to become one GitHub issue, with the "Visible output" column as its acceptance criterion.

| # | Milestone | Ticket | Scope | Visible output (done when) | Depends on |
| --- | --- | --- | --- | --- | --- |
| 1 | M1 Hello Drive | Project skeleton | Go module, `cmd/syncd`, `internal/{sync,store,drive}`, Makefile with `go vet` and `go test -race` targets (no CI workflow) | `syncd version` prints a version; `make vet test` passes locally | – | => Done
| 2 | M1 Hello Drive | Google Cloud setup | Cloud project, Drive API on, desktop OAuth client, `drive.file` scope, consent screen "In production" | Client ID committed to config; setup steps in README | – | => Done
| 3 | M1 Hello Drive | OAuth login command | `syncd login`: loopback redirect with PKCE via `x/oauth2` and a local listener | Browser opens, terminal prints the signed-in email | 1, 2 | => Done
| 4 | M1 Hello Drive | Keychain token storage | Save the refresh token with `go-keyring`; `syncd logout` clears it | `syncd whoami` works after a restart without signing in again | 3 | => Done
| 5 | M1 Hello Drive | Drive root folder | Create or find the app-owned `MySync/` folder; cache its ID locally | `MySync/` appears in the Drive web UI | 4 | => Done
| 6 | M1 Hello Drive | Single-file upload | `syncd put <file>` with `watch_id` and `rel_path` in `appProperties` | **Checkpoint:** file shows in Drive; `syncd stat` prints its ID and tags | 5 | => Done
| 7 | M2 One-shot sync | State DB setup | `modernc.org/sqlite`, WAL mode, `goose` migrations, path via `adrg/xdg` | `syncd db path` prints the location; tables exist | 1 | => Done
| 8 | M2 One-shot sync | Files repository | `files` table and CRUD layer owned by one goroutine; unit tests | Tests pass under `-race` | 7 | => Done
| 9 | M2 One-shot sync | Scan with ignore rules | `filepath.WalkDir` over the sync root, gitignore-style matcher, default ignores | `syncd scan --dry-run` lists files that would upload | 1 | => Done
| 10 | M2 One-shot sync | Folder hierarchy in Drive | Lazy folder creation with a path to folder-ID cache | A nested local tree is mirrored in Drive | 6 | => Done
| 11 | M2 One-shot sync | One-shot sync command | `syncd sync`: upload new files, store Drive file IDs in the DB | The whole sync root appears in Drive | 8, 9, 10 | => Done
| 12 | M2 One-shot sync | MD5 skip and updates | Streamed MD5; skip when it matches; `files.update` for changed files | **Checkpoint:** a second run uploads 0 files; editing one file uploads only that file | 11 | => Done
| 13 | M2 One-shot sync | Revision tracking | Write `headRevisionId` to `base_revision_id`, `base_md5` and `file_revisions` | `syncd history <path>` lists revisions | 12 | => Done
| 14 | M3 Robust uploads | Logging | `log/slog` text handler to stderr, `SYNC_LOG` level, `lumberjack` rotation | Log lines carry path, job ID and Drive file ID | 1 |
| 15 | M3 Robust uploads | Resumable uploads | 8 MB chunked resumable uploads streamed from disk | A 1 GB file uploads with flat memory use | 12 |
| 16 | M3 Robust uploads | Retries with backoff | Exponential backoff with jitter on 403, 429 and 5xx | Tests against a fake server that returns 429 then 200 | 12 |
| 17 | M3 Robust uploads | Persistent job queue | Jobs table in SQLite; a worker drains it with bounded concurrency | Kill the process mid-sync; on restart it resumes | 8, 12 |
| 18 | M3 Robust uploads | Error events table | `sync_errors` / `events` table; `syncd errors` command | A failed upload shows as a readable line, e.g. "quota exceeded, will retry" | 17 |
| 19 | M4 Live watching | Recursive watcher | `fsnotify` goroutine; watch every subfolder, including new ones | `syncd watch` logs create, write, rename and remove events | 1 |
| 20 | M4 Live watching | Debounce and stabilize | Per-path timers; enqueue only when size and mtime are stable | One editor save produces one queued job | 19 |
| 21 | M4 Live watching | Foreground daemon | `syncd run`: watcher to queue to worker, graceful shutdown on Ctrl+C | A saved file appears in Drive within a minute | 17, 20 |
| 22 | M4 Live watching | Startup reconciliation | On launch, walk the root, diff against the DB, enqueue differences | **Checkpoint:** files changed while stopped sync on next start | 21 |
| 23 | M4 Live watching | Hardening | Panic recovery in every goroutine; clear error on `max_user_watches` | A forced panic is logged and the daemon keeps running | 21 |
| 24 | M5 Rename and delete | Inode / file ID tracking | Per-OS helper for inode (Unix) or file ID (Windows); store it in the DB | `syncd stat` shows the inode for a tracked file | 8 |
| 25 | M5 Rename and delete | Rename and move | Match inode and hash; `files.update` with `addParents` / `removeParents` | A renamed file keeps its Drive file ID; nothing is re-uploaded | 21, 24 |
| 26 | M5 Rename and delete | Delete to trash | Local delete sets `trashed: true` in Drive | **Checkpoint:** a deleted file sits in the Drive trash | 21 |
| 27 | M5 Rename and delete | Folder rename and delete | Apply moves and trashes to whole folders and their cache entries | Renaming a folder moves it in Drive with its children intact | 25, 26 |
| 28 | M6 Restore | config.json | Read and write the shared settings file in Drive (ignore rules, version) | Ignore rules edited on one run apply on the next | 5 |
| 29 | M6 Restore | List Drive tree | List all files under `MySync/` with `appProperties`, `md5Checksum`, `headRevisionId` | `syncd remote ls` prints the remote tree | 5 |
| 30 | M6 Restore | Safe download | Download to a temp file, then atomic rename into place | Downloaded file matches the Drive MD5 | 29 |
| 31 | M6 Restore | Restore command | `syncd restore --root <path>`: match, download missing, keep both on mismatch | A fresh folder fills from Drive with DB rows created | 28, 30 |
| 32 | M6 Restore | Rebuild-from-Drive test | End-to-end test: delete the DB, restore, sync again | **Checkpoint:** nothing is lost or re-uploaded | 31 |
| 33 | M7 Daemon and UI | Control API | Local socket or localhost HTTP: status, pause, resume | `syncd ctl status` prints queue length and last sync | 21 |
| 34 | M7 Daemon and UI | OS service install | systemd user unit, launchd agent, Windows startup entry | Daemon starts on login and survives a reboot | 21 |
| 35 | M7 Daemon and UI | Tray app: status | `cmd/syncui` with `fyne.io/systray`: status, pause and resume | Tray icon shows the state; pause stops uploads | 33 |
| 36 | M7 Daemon and UI | Tray app: errors and settings | Error log from `sync_errors`, open logs folder, debug toggle, change root | **Checkpoint:** v1 usable day to day without a terminal | 18, 35 |
| 37 | M7 Daemon and UI | Release builds | Cross-compile for Linux, macOS, Windows (e.g. GoReleaser) | Download a binary from a GitHub release and run it | 34 |
| 38 | P2 Two-way sync | Changes poller | Store `startPageToken`; poll `changes.list` | `syncd changes` prints edits made in the Drive web UI | 29 |
| 39 | P2 Two-way sync | Three-way decision | Pure function over local, Drive and base; a test per table row | All decision-table tests pass | 13 |
| 40 | P2 Two-way sync | Downloads and echo suppression | Apply remote changes; ignore the watcher event for the expected MD5 | A Drive-side edit lands locally with no re-upload loop | 30, 38, 39 |
| 41 | P2 Two-way sync | Conflict copies and edge cases | Keep-both naming; edit vs delete, rename vs edit, both created | Editing on two machines leaves both versions everywhere | 40 |
