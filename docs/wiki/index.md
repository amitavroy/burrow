# Burrow wiki

## ADR

Architecture decision records. Newest last. Each record states the context, the decision and its consequences.

### ADR-001: Separate headless daemon and optional tray UI

- **Status:** Accepted
- **Date:** 2026-10-02

**Context**

The first design was written for Rust, where a separate daemon and UI was an obvious split. Go is lighter, so the question was whether one process (daemon with the tray inside) would be enough.

The reason for the split is not the language. The UI toolkit drives memory use, not the sync engine. `fyne.io/systray` also needs cgo on macOS and system libraries on Linux, while the rest of the stack is pure Go (`modernc.org/sqlite`, no C compiler needed).

**Decision**

Keep two processes:

- `cmd/syncd`: the headless sync daemon, always running (launchd, systemd user service or Windows startup entry). It contains the whole sync engine and is built and tested through the CLI.
- `cmd/syncui`: an optional tray app. It is a separate process that talks to the daemon over a local socket or localhost HTTP (the control API, ticket 33). It is built last (tickets 35 and 36).

**Consequences**

- `syncd` stays pure Go and cross-compiles without cgo.
- A UI crash or quit does not stop syncing, and sync runs with no desktop session.
- The control API is needed anyway for `syncd ctl status`, so the split adds little extra work.
- Tickets 1 to 32 are daemon and CLI only and do not depend on this decision.
- If a single binary is wanted later, only tickets 33 to 36 change. The cost would be cgo in the main binary and a UI crash taking sync down.

### ADR-002: No CI workflow; run checks locally

- **Status:** Accepted
- **Date:** 2026-10-03

**Context**

Ticket 1 originally included a GitHub Actions workflow running `go vet ./...` and `go test -race ./...` on every push and PR, with "CI is green" as part of its acceptance criterion.

This is a single-developer project and the checks are fast, so a hosted CI workflow adds setup and upkeep without adding much safety.

**Decision**

Do not add a CI workflow. The same checks run locally through the Makefile:

- `make vet` runs `go vet ./...`.
- `make test` runs `go test -race ./...`. The race detector stays required because of the goroutine-ownership design.

Ticket 1 is done when `syncd version` prints a version and `make vet test` passes locally.

**Consequences**

- Nothing enforces the checks on push, so they must be run before committing or merging.
- A workflow can be added later without touching the code. The Makefile targets are the single definition of the checks, so a workflow would only call them.
- `requirements.md` (ticket 1 row), `CLAUDE.md` and the GH-1 plan doc were updated to match.
- The GitHub issue for ticket 1 still mentions CI and needs the same update.
- Ticket 37 (release builds) is unaffected. It still uses a GitHub release as the distribution point.

### ADR-003: OAuth client credentials come from .env, not source

Date: 2026-10-03. Ticket: GH-2.

- Decision: the client ID and secret are read from `BURROW_GOOGLE_CLIENT_ID` and `BURROW_GOOGLE_CLIENT_SECRET`. `.env` is git-ignored and `.env.example` is committed.
- Why: Google treats a desktop client secret as non-confidential, but keeping it out of source is the safer default and lets the client be swapped without a code change.
- `drive.LoadClient()` reads the variables. `syncd` loads `.env` at startup with `godotenv`, and real environment variables win.
- The `drive.file` scope is a Go const, not configuration.
- Consequence: a release binary has no `.env` beside it. Ticket 37 must inject the values with `-ldflags -X` at build time, with `.env` as the dev-time override.

### ADR-004: goose for migrations, embedded in the binary

Date: 2026-10-05. Ticket: GH-6.

- Decision: schema changes are plain `.sql` files in `internal/store/migrations/`, embedded with `//go:embed` and applied by `goose.NewProvider` when the database is opened.
- Why: goose supports embedded SQL files and a provider with no global state, so there are no migration files to ship beside the binary. This chose it over golang-migrate.
- Consequence: each later ticket adds its own numbered migration (`file_revisions` in 13, jobs in 17, `sync_errors` in 18) instead of guessing future schemas now.

### ADR-005: go-git's gitignore matcher for ignore rules

Date: 2026-10-07. Ticket: GH-8.

- Decision: ignore rules use `github.com/go-git/go-git/v5/plumbing/format/gitignore`.
- Why: both candidates were run against the same 19-case table (names, globs, `**`, anchored `/foo`, `dir/`, negation, the four defaults). `sabhiram/go-gitignore` failed the `~$*` default; go-git passed every case. A hand-written matcher was rejected because gitignore semantics are easy to get subtly wrong.
- Consequence: a few small extra modules in `go.mod` (`go-git`, `go-context`, `warnings`, and `go-billy` and `gcfg` indirectly). Only the `gitignore` package is linked, not the whole of go-git.

## Sign-in

`syncd login` signs the user in with Google. Rule: user OAuth with loopback redirect and PKCE, scope `drive.file` only.

```
syncd login
  -> listen on 127.0.0.1:<random port>
  -> open browser at the auth URL (S256 challenge, random state, access_type=offline, prompt=consent)
  -> Google redirects to /callback?code&state
  -> check state, exchange code with the PKCE verifier
  -> save the refresh token in the OS keychain
  -> Drive about.get (fields=user(emailAddress)) -> print "Signed in as <email>"
```

- The email comes from Drive `about.get`, not the userinfo endpoint, because userinfo needs a scope beyond `drive.file`.
- `prompt=consent` makes Google return a refresh token on every sign-in, which is what gets stored.
- Times out after 2 minutes or on Ctrl+C. The auth URL is always printed too, so it works on a box with no browser.
- Tokens and the auth code are never logged.
- Code: `drive.Login` (`internal/drive/auth.go`), `drive.Email` (`internal/drive/about.go`).

### Token storage

Rule: the refresh token lives only in the OS keychain (`go-keyring`), never in the DB, config or logs.

- Only the refresh token string is stored (service `burrow`, user `google-refresh-token`). Access tokens are re-derived by refreshing.
- `drive.KeyringStore` is the keychain store (`internal/drive/tokenstore.go`); callers use it directly, with no interface in between. A missing entry is `ErrNotSignedIn`, and deleting a missing entry is not an error.
- `syncd login` saves the token right after the code exchange, before the email lookup, so a failed `about.get` does not waste the sign-in.
- `syncd whoami` calls `drive.Resume` (`internal/drive/resume.go`): load the token, refresh it, ask Drive for the email. The rebuilt token has an expiry in the past (`TokenFromRefresh`), because oauth2 treats a zero expiry as "never expires" and would never refresh.
- Google answering `invalid_grant` (revoked or expired) becomes `ErrSessionExpired`, and `whoami` tells the user to run `syncd login`.
- `syncd logout` only clears the local entry. It does not revoke the token at Google.
- No file fallback when the keychain is unavailable (for example Linux without a Secret Service): the command fails with a keychain error, because secrets must stay out of files.

## Drive root folder

`syncd root` finds or creates the app-owned `MySync/` folder in My Drive and prints `MySync folder: <id>` plus its web URL. Later tickets upload into it.

- With `drive.file` the app only sees folders it created, so a hand-made `MySync/` is invisible. The app creates and owns it.
- Lookup: `files.list` with `name = 'MySync'`, folder MIME type, `'root' in parents`, `trashed = false`, ordered by `createdTime`. If several match (Drive allows duplicate names) the oldest wins; nothing is deleted or merged. If none, `files.create` under `root`.
- Cache: `{"root_folder_id": "..."}` in `state.json` in the `adrg/xdg` data dir (`burrow/state.json`; Local, not Roaming, on Windows). It is never inside the sync root. Disposable: deleting it costs one lookup. It stays a separate file for now; the State database section covers the SQLite cache.
- The cached ID is checked with `files.get(fields=id,trashed)` before use. On 404 or `trashed: true` it falls back to find/create and rewrites the cache, so uploads never go into the trash.
- Idempotent: a second run prints the same ID and creates nothing.
- Code: `drive.EnsureRoot` (`internal/drive/root.go`); `drive.FileStore`, `ErrNoRoot` (`internal/drive/rootstore.go`). `FileStore` writes the file in place; a corrupt or half-written cache is treated as empty, so the next run just looks the folder up again. The Drive service is built by `newService` in `about.go`, shared with `Email`.
- Same exit-1 hints as `whoami` when not signed in or the session expired.

## Uploading a file

`syncd put` uploads one local file into `MySync/`, in the folder that mirrors its `rel_path`. Rule: files are tracked by Drive file ID, and every upload carries the `appProperties` tag `rel_path`, because Drive allows duplicate names.

```
syncd put [--root DIR] <file>
  -> rel_path = file's basename, or its slash-form path relative to --root
     (outside --root is rejected, exit 2)
  -> rel_path validated: not empty, no leading "/", no "", "." or ".." component (else error, no Drive call)
  -> EnsureRoot -> MySync folder ID
  -> files.list: appProperties rel_path, not a folder, trashed = false (any parent)
       match    -> files.update (content + tags), same ID, no folder work
       no match -> ensureDir(dir of rel_path) -> files.create (parent = that folder, tags)
  -> print file ID and web link

ensureDir(relDir)                  (relDir "." is MySync itself)
  cached + usable      -> cached ID          (files.get id,trashed)
  otherwise            -> parent := ensureDir(parent of relDir)
                          id := oldest folder with this name under parent, or create it
                          cache relDir -> id
```

Layout: `rel_path` `a/b/c.txt` lands in `MySync/a/b/` with the name `c.txt`. Directories sit directly under `MySync/`; there is no per-watch folder.

- `rel_path` never has `..`, a leading `/` or machine-specific parts.
- If several files carry the same `rel_path`, the oldest wins.
- There is no `watch_id`: v1 has one sync root, so a file is identified by `rel_path` alone. Files uploaded by earlier versions also carry `watch_id: default`; the lookup ignores it, so they are still found and updated. A `watch_id` returns only with multi-folder support (requirements section 12), most likely as a per-watch Drive folder plus a local-path mapping.
- Tag values and folder names are escaped (`\` and `'`) in queries.
- File lookup does not depend on the parent folder, so a file is found wherever it sits (this also keeps later renames and moves working).
- Folder cache: `syncd put` opens the state database and keeps `rel_dir -> Drive folder ID` in its `folders` table (see State database). A cached ID is trusted only after `files.get` shows it exists and is not trashed; only the deepest folder is checked, because trashing a folder trashes its contents. A stale entry heals itself by looking the folder up again. The cache is best effort: if the database cannot be opened, `put` prints a warning and works without it; cache read and write errors never fail an upload. With no cache, or after deleting `burrow.db`, existing folders are found again, not duplicated.
- Two concurrent uploads could create the same folder twice; duplicates are tolerated (oldest wins). Workers of one `Uploader` share a per-folder lock, so a new folder is created once (see Job queue).
- Files that fit in one 8 MB chunk go as one multipart request; larger ones use a resumable upload in 8 MB chunks. The file is streamed from disk, never read whole: the Drive client holds one chunk (8 MB) at a time, so memory use does not grow with file size. A test uploads a 128 MB sparse file to the fake Drive and asserts the live heap grows by less than three chunks (it measures about 8.5 MB, and the same for 1 GB). An exact multiple of the chunk size ends with an empty `bytes */total` request, which finalizes the upload.
- "Resumable" holds within one run: the client retries a chunk on its session URI. The session is not saved, so a killed process starts the file again from byte 0 on the next run; no `files` row is written until an upload succeeds, so nothing is lost. Cancelling (Ctrl+C) stops the upload between chunks and returns the context error, which `syncd sync` treats as an abort, not a failed file.
- Folders are found by name, parent and mimeType (`trashed = false`), and the oldest wins if Drive holds duplicates. They carry no `appProperties`, because the tree already implies the path. Names are escaped like tag values. Folder rename and delete come with ticket 27, and the MD5 skip with ticket 12 (retries: see Retries).
- Requested fields: `id,name,md5Checksum,size,headRevisionId,appProperties,webViewLink`.
- Same exit-1 hints as `whoami` when not signed in or the session expired. Usage errors exit 2.
- Code: `drive.Upload`, `drive.Stat`, `drive.FindByTags`, `drive.FileInfo`, `ErrFileNotFound`, `ensureDir` (`internal/drive/upload.go`); the cached-folder check is `folderUsable` (`root.go`). Service setup is shared through `serviceFromStore` (`root.go`), and the oldest-match list query behind `FindByTags` and the root lookup is `oldestMatch` (`upload.go`). Each operation is one exported function that takes a `drive.Client`; tests point its optional `Endpoint` at a fake OAuth server. `drive.Upload` is `NewUploader(...).Upload(...)` for one call; `drive.Uploader` keeps the Drive service and `MySync` ID so many files share them (see "One-shot sync").

### Stat

`syncd stat <file-id>` uses `files.get`. `syncd stat --path <rel_path>` uses the same tag query as `put` and creates nothing in Drive. Both print ID, name, size, MD5, revision and `rel_path`. A file this app cannot see is reported as not found, because `drive.file` returns 404 for it. After the Drive fields, if the state database has a row for the same file, a `Local:` block follows (inode, size, mtime in UTC, synced MD5; see File identity). `--local` skips Drive entirely and prints only that block: no sign-in, no network; a file that is not tracked exits 1 with `no tracked file`.

## State database

`syncd db path` prints the database location and creates nothing. `syncd db status` opens the database (creating and migrating it if needed) and prints `Path:`, `Version:` and `Tables:`. Both exit 2 with usage on a missing or unknown subcommand.

- Location: `burrow/burrow.db` in the `adrg/xdg` data dir (Local, not Roaming, on Windows), next to `state.json`. `store.DefaultPath()` only computes the path, because `xdg.DataFile` would create directories. It is never inside the sync root.
- Disposable: Drive is the source of truth. Deleting the file loses only cache and the next open recreates it. It is never copied or synced.
- Driver: `modernc.org/sqlite`, pure Go, no cgo.
- `store.Open(path)` creates the directory (0700), applies the pragmas through the DSN (`journal_mode(WAL)`, `busy_timeout(5000)`, `foreign_keys(on)`, `synchronous(NORMAL)`), caps the pool at one connection and runs pending migrations. The caller must keep the `*sql.DB` in one owner goroutine; `Repo` (below) is that owner for the `files` table. SQL arguments are never logged.
- First migration `00001_init.sql` creates `files`: `rel_path` (unique), `drive_file_id`, `size`, `mtime`, `inode`, `local_md5`, `synced_md5`, `base_md5`, `base_revision_id`. See ADR-004 for how later tables arrive. A unique partial index on `drive_file_id` ignores NULLs, so unuploaded rows do not collide.
- Second migration `00002_folders.sql` creates `folders(rel_dir PRIMARY KEY, drive_folder_id)`: the folder-ID cache for nested uploads. `rel_dir` is slash-form and relative to the root, like `files.rel_path`. `Repo.GetFolder` returns `ErrNotFound` when absent, and `Repo.PutFolder` is an upsert. Like everything here, it is disposable cache.
- Third migration `00003_file_revisions.sql` creates `file_revisions(id, drive_file_id, revision_id, md5, size, time, source)`, unique on `(drive_file_id, revision_id)`. It is keyed by Drive file ID, so history follows a file through renames. `time` is Unix nanoseconds; `source` is `upload` (`download` arrives with restore). `Repo.RecordUpload` writes the `files` row and the revision in one transaction (a revision already recorded is ignored), and `Repo.Revisions(relPath)` lists them newest first. Rows exist only for uploads made since this migration; older uploads are not backfilled, and the next upload starts the history.
- Repository: `store.OpenRepo(path)` calls `Open` and starts one goroutine that owns the `*sql.DB`. `Upsert` (keyed by `rel_path`), `Get`, `GetByDriveID`, `List` (ordered by `rel_path`) and `Delete` send a request over a channel and wait for the reply, so callers never see SQL or the connection. Every method takes a `context.Context`; a cancelled context returns early, but the owner still finishes the statement it started.
- `Repo` errors: `ErrNotFound` (missing row on `Get`, `GetByDriveID`, `Delete`) and `ErrClosed` (any call after `Close`). A panic inside a request is recovered and returned as an error, and the owner keeps serving. `Close` is safe to call twice.
- `syncd sync` is what writes `files` rows today (see "One-shot sync"); `local_md5` is set when a run hashes a file; `base_md5` and `base_revision_id` are set on every successful upload or update (the state after the last sync with Drive, which phase 2 conflict detection reads) and are left alone when a file is only refreshed.
- NULL mapping: an empty string or zero `Inode` is stored as NULL and read back as the zero value, so "no Drive ID yet" is a real NULL and the unique `drive_file_id` index ignores it. `File.MTime` is Unix nanoseconds.
- `store.Status(db)` returns the migration version and the user table names (`file_revisions`, `files`, `folders` and goose's `goose_db_version`).
- `state.json` stays for now; folding the root folder ID into the database is a later cleanup.
- Code: `internal/store/store.go`, `path.go`, `file.go`, `repo.go` (including the folder cache methods); the `db` command in `cmd/syncd/main.go`. Tests replace the `dbPath` var so they never touch the real data dir.

## Scanning

`syncd scan --dry-run` lists the files a sync would upload. It only reads the local folder; no Drive or database is involved, so the same function (`sync.Scan`) feeds the one-shot sync (see "One-shot sync") and startup reconciliation (see Running). `syncd scan --dry-run --diff [--root DIR]` additionally opens the state database and prints what `syncd run` would queue on its next start: `new <path>`, `changed <path>` and `missing <path>` lines (sorted, in that order) and a count line on stderr. It queues nothing and needs no sign-in.

```
syncd scan --dry-run [--root DIR]        (--dry-run required, else usage and exit 2)
  -> NewMatcher(root): defaults + <root>/.syncignore
  -> filepath.WalkDir(root):
       ignored dir  -> fs.SkipDir (never walked), counted once
       ignored file -> counted, skipped
       symlink / non-regular -> skipped
       unreadable below the root -> recorded, skipped, scan continues
       otherwise    -> Entry{rel_path, size, mtime}
  -> stdout: rel_path per line, sorted;  stderr: skipped lines and "N files, M ignored"
```

- Root: `--root DIR`, default `~/MySync` (home dir through the `homeDir` var, which tests replace). A missing root, or a file given as the root, exits 1; the root is never created. Only a problem with the root itself or an unreadable `.syncignore` fails the scan.
- `Scan(root)` returns `Result{Entries, Ignored, Errors}`. `Entry` has `RelPath` (slash form, relative to the root, no `..`), `Size` and `MTime` (Unix nanoseconds, same unit as `store.File`). Directories are not listed, because Drive folders are created lazily from `rel_path` (ticket 10).
- Rules use gitignore syntax (ADR-005). Defaults, applied first: `node_modules`, `*.tmp`, `~$*`. Then the root's `.syncignore`, if present (blank lines, `#` comments and CRLF are fine). Later rules win, so a `.syncignore` negation like `!keep.tmp` re-includes a default.
- Two rules cannot be overridden: any path with a `.git` component is ignored, and `.syncignore` at the root is never listed or uploaded. Whether `.syncignore` should sync to a new machine belongs to ticket 28 (`config.json`).
- Matching runs on slash-form paths on every OS. Case sensitivity follows the library; Windows case-insensitivity is not handled.
- Code: `internal/sync/scan.go`, `ignore.go`; the `scan` command in `cmd/syncd/main.go`.

## One-shot sync

`syncd sync` uploads every file under the sync root to Drive and records each file's Drive ID in the state database. Rule: Drive stays the source of truth; a row only says "this `rel_path` is on Drive as this ID", so losing the database costs nothing but time.

```
syncd sync [--root DIR]                 (root defaults to ~/MySync; usage error exits 2)
  -> store.OpenRepo(dbPath())            (required: if it cannot open, exit 1)
  -> drive.NewUploader(...)              (Drive service, token source and MySync ID built once, on the first upload)
  -> sync.Sync(ctx, root, repo, uploader.Upload, report):
       res := Scan(root)                 (ignore rules and skipped entries as in "Scanning")
       for each entry, in rel_path order:
         row with a Drive ID:
           size and mtime equal the row -> already synced, skip (no read)
           else stream-hash the file; MD5 equals synced_md5 -> already synced, refresh the row
           else (changed, or row has no synced_md5)      -> upload below, as an update
         upload(ctx, root/rel_path, rel_path)
           ok                 -> write the row, report "uploaded rel_path" (new) or "updated rel_path"
           sign-in lost       -> abort
           cancelled (Ctrl+C) -> abort
           other error        -> report "failed rel_path: reason", carry on
  -> stderr: "N uploaded, M updated, X moved, K already synced, J failed, I ignored"; exit 0, or 1 if any file failed or the run aborted
```

- Output: `uploaded <rel_path>` and `updated <rel_path>` lines on stdout; `failed <rel_path>: <reason>`, `skipped: <unreadable entry>` and the summary on stderr. Exit 1 for a failed file or an abort (sign-in lost, interrupted, root unreadable, database failure), with the same login hints as the other commands.
- A file is uploaded as new when it has no row, or a row with no Drive ID. For a file with a row, change detection is two steps: same size and mtime means unchanged with no read; otherwise the file is stream-hashed (MD5, never loaded whole) and compared to `synced_md5`, which is what Drive holds. An equal hash only refreshes the row (`size`, `mtime`, `local_md5`), so a `touch` or a restore that resets mtimes uploads nothing. A different hash goes through `Uploader.Upload`, which finds the file by its `rel_path` tag and calls `files.update`, so the Drive file ID is kept. A row without `synced_md5` counts as changed and is re-uploaded once. Accepted limit: an edit that keeps both size and mtime is not noticed.
- The row holds `drive_file_id`, `synced_md5` (Drive's `md5Checksum`), and the `size` and `mtime` seen by the scan before the upload. If the file changes while it uploads, the stored values are older than the file, which errs toward a re-upload on the next run. `local_md5` is a cache of the last hash taken; `base_md5` and `base_revision_id` stay empty until ticket 13.
- The row is written right after each upload, ignoring cancellation, so Ctrl+C or a crash loses at most the file in flight and a rerun resumes. A file uploaded but not recorded is found again by its `rel_path` tag and updated in place, never duplicated. A failed upload writes no row, so the next run retries it.
- Uploads run through the job queue, `--workers` at a time (see Job queue). A run that hits a repeating problem (for example a dead network or an `invalid_client` error) reports each file as failed instead of aborting; only a lost sign-in aborts. Transient errors are retried (see Retries).
- After each successful upload or update the row also records `base_md5` and `base_revision_id` (Drive's `md5Checksum` and `headRevisionId`) and adds a `file_revisions` entry, in one transaction. A Drive response without a head revision still records the row, with no revision entry. A skipped or refreshed file changes neither.
- `syncd history <rel_path>` prints the recorded revisions newest first, one per line: time (UTC), revision ID, MD5, size, source. It reads the database only (no sign-in), and exits 1 with a message when nothing is recorded for the path. Drive prunes old revisions on its own schedule, so the local rows are the durable record.
- The same state database also holds the folder-ID cache (see "Uploading a file"), so the folders of a tree are resolved once.
- Code: `sync.Sync`, `UploadFunc`, `Event`, `Summary` (`internal/sync/sync.go`); `drive.Uploader` and `NewUploader` (`internal/drive/upload.go`); the `sync` command in `cmd/syncd/main.go`. `Sync` takes the upload as a function, so the engine is tested with a fake and no HTTP.

## Job queue

`syncd sync` does not upload straight from the scan. It queues a job per file in the `jobs` table of the state database and drains the queue with a small pool of workers (`internal/store/jobs.go`, `internal/sync/sync.go`).

```
syncd sync [--root DIR] [--workers N]
  -> ResetRunningJobs      jobs a dead process left "running" go back to pending
  -> Scan the root         (ignore rules as for scan)
  -> EnqueueJobs           one job per rel_path, in one transaction; idempotent
  -> N workers loop:       ClaimJob -> process -> CompleteJob | FailJob | RequeueJob
```

- **Table:** `jobs(id, rel_path UNIQUE, status, attempts, last_error, created_at, updated_at)`. Status is `pending`, `running` or `failed`. A finished job is deleted: `files` and `file_revisions` already record what happened.
- **One job per path.** Queueing a path again does not duplicate it: pending and failed jobs go to pending; a running one keeps running and is marked `dirty`, so it is queued again when it finishes (see Running).
- **Claim** is a single `UPDATE ... RETURNING` run by the repo's owner goroutine, so two workers never get the same job. Oldest (lowest id) first.
- **Resume:** kill the process (even `kill -9`) mid-sync and run `syncd sync` again. It prints `resuming N queued jobs`, goes through what was left, and uploads nothing twice, because each file's `files` row is written before its job is deleted and the skip check (size and mtime, then MD5) makes a repeated job cheap.
- **Cancel or lost sign-in:** the job in flight goes back to pending, and every worker stops. Other per-file errors mark the job `failed` (with `last_error`, never a request body) and the run goes on; the next run queues that file again. Each failure also gets a readable row in `sync_errors` (see Errors).
- **A job whose file is no longer in the scan** (deleted or newly ignored) is dropped, not failed. Deleting in Drive is ticket 26.
- **Workers:** `--workers N`, 1 to 16, default 3. `drive.Uploader` is safe to share: the Drive service is built once, and a folder is looked up or created by one worker at a time, so files going into a new folder create it once. The summary and `uploaded`/`updated`/`failed` lines are never interleaved. With more than one worker the order of the lines is not `rel_path` order.
- **Job ID** in log lines is the row id of the job, so it stays the same across a resumed run.
- Only one `syncd` may use the database at a time; the reset of `running` jobs relies on it.
- Not here: the watcher feeding the queue (tickets 19 to 21), delete and rename jobs (25 to 27), run-wide rate limiting.

## Watching

`syncd watch [--root DIR] [--settle DURATION] [--raw]` prints the files that have settled under the sync root until Ctrl+C: one `ready <path>` line per save. With `--raw` it prints every event instead. It is the first piece of live sync: it only reports, and uploads nothing, touches no database and needs no sign-in. Feeding the ready paths to the job queue is ticket 21.

```
$ syncd watch --root ~/MySync          # an editor saves notes/todo.txt
watching 14 directories under /home/me/MySync (Ctrl+C to stop)
ready notes/todo.txt
$ syncd watch --raw --root ~/MySync    # every event, as reported by the OS
create notes/todo.txt.tmp
write notes/todo.txt.tmp
rename notes/todo.txt.tmp
create notes/todo.txt
```

- **Code:** `sync.Watcher` (`internal/sync/watch.go`) on `github.com/fsnotify/fsnotify`. `NewWatcher(root)`, `Events()`, `Errors()`, `Watching()`, `Close()`. One goroutine reads fsnotify and owns the set of watched directories.
- **Events:** `WatchEvent{RelPath, Op, IsDir}` with ops `create`, `write`, `rename`, `remove`. `RelPath` is slash-form relative to the root. A trailing `/` in the command output marks a directory. Chmod is dropped. A rename is the old path as `rename` plus the new path as `create`; pairing them is ticket 25.
- **Recursive:** fsnotify watches one directory only, so the watcher walks the root at start and adds every directory, and adds each new directory when its create event arrives.
- **Ignore rules:** the same `Matcher` as `scan` (defaults plus `.syncignore`, never `.git`). Ignored paths give no events and ignored directories are not watched, which also saves inotify watches. `.syncignore` is read once at start; edit it and restart.
- **Debounce and stabilize** (`internal/sync/debounce.go`): `Debouncer` reads the watcher's events and delivers each settled file's rel_path on `Ready()`. One goroutine owns a timer per path. A `create` or `write` of a file starts or restarts its timer; `rename` and `remove` cancel it (a rename's new name arrives as its own `create`); directory events are ignored, since jobs are per file. When a timer fires, the file is `Lstat`ed: gone or not a regular file means the path is dropped silently. Otherwise (size, mtime) is sampled and the timer re-armed; only when the next sample, one settle period later, is equal is the path reported. A new event throws the sample away, and a changed sample waits again, so a file that is still being written or copied is reported once, after it stops growing. A file that never stops changing is never reported. Default settle is 1 s (`--settle`), so a save is reported about 2 s later. A timer that fires after a newer event is ignored by a generation number. Closing the watcher closes `Ready()`; paths still waiting are dropped, and the startup reconciliation (see Running) finds them. Tests show one editor-style save (temp file renamed over the original) gives one ready path and one queued job.
- **The new-folder race:** nothing is reported for a file created after its folder appears but before the folder is watched. So when a directory is created, its contents are walked and reported as `create` events (`mkdir -p a/b/c && touch a/b/c/f` loses nothing). A path can therefore be reported twice; the debounce collapses that. A write that happened before the watch existed is reported only as the create.
- **Removed and renamed folders** drop their watches and those of everything below. On Linux the kernel watch follows the directory, so without this events inside a renamed folder would keep carrying the old name. The new name is walked and watched when its create arrives.
- **Overflow:** if the OS queue overflows (`ErrEventOverflow`), the error comes out of `Errors()` and is logged at warn. Missed events are recovered by the startup reconciliation on the next start (see Running); there is no rescan while running.
- **Not here:** a clear `max_user_watches` message and panic recovery (ticket 23; until then the OS error names the directory it failed on), rename detection by inode and hash (25), symlinked folders (not followed, as in `scan`).
- Linux inotify has a per-user watch limit (`fs.inotify.max_user_watches`, often 8192 to 65536); a very large tree can hit it.

## Running

`syncd run [--root DIR] [--workers N] [--settle D]` is the foreground daemon: it watches the sync root and uploads what changes until Ctrl+C. It joins the pieces described above (Watching, Job queue, Retries, Errors) in one process; installing it as a service and controlling it from a UI come later.

```
watcher.Events() -> Debouncer -> Ready(path) -> feeder: EnqueueJobs([path]); wake workers
                                                          |
failed jobs --(every 5 min: RetryFailedJobs)--> pending
                                                          v
workers x N:  ClaimJob -> Lstat the file -> process (upload) -> Complete | Fail | Requeue
              queue empty -> wait for a wake-up or a 1 s poll tick
Ctrl+C: stop feeder, debouncer, watcher; cancel uploads in flight; their jobs go back to pending; exit 0
```

```
$ syncd run --root ~/MySync
watching 14 directories under /home/me/MySync (Ctrl+C to stop)
uploaded notes/todo.txt
updated notes/todo.txt
failed big/video.mov: network problem
^C
syncd: stopped; unfinished uploads stay queued
```

- **Code:** `sync.Run` (`internal/sync/run.go`). The workers are the ones `Sync` uses (`worker()` in `sync.go`); the two modes differ only in how a job's file is found (`Sync`: the scan; `Run`: an `Lstat` at claim time) and what happens when the queue is empty (`Sync`: stop; `Run`: wait).
- **Latency:** a save is uploaded about two settle periods after the last write (default `--settle 1s`) plus the upload itself; well inside "within about a minute".
- **Start** (`Run`, in this order): reset jobs a dead process left `running`; start the watcher and debouncer; **reconcile** the root with the database (below); start the workers, which first drain the jobs left by the earlier run (`resuming N queued jobs` on stderr) and the ones the reconcile queued. The watcher is up before the scan, so a change made during the walk is seen by one or the other, or both, which is still one job.
- **Reconcile** (`internal/sync/reconcile.go`): `Diff` scans the root (same ignore rules as `scan`) and compares each file with its `files` row: **new** = no row, or a row without a Drive ID (an upload that never finished); **changed** = size or mtime differ from the row; **unchanged** = both match; **missing** = a row whose file is not in the scan. `Reconcile` is `Diff` plus its writes: error rows for entries the scan could not read (and stale error rows dropped), and one job for each new and changed path. Unchanged files are not queued, so a start on an unchanged tree queues nothing. A touched-but-identical file is queued and then skipped by the job's MD5 check, as in `sync`. A file whose last upload failed is new or changed again, so it is retried on the next start. Missing files are **not acted on**: they are counted, listed (first 10) and logged, because a missing file may be a delete, a rename or an unmounted folder (trashing in Drive is ticket 26, rename detection 25); rows under a folder the scan could not read are not counted as missing. Output: `reconciled: 3 new, 2 changed, 117 unchanged, 1 missing locally`. Reconcile runs once per start; there is no periodic rescan.
- **A file saved again while it is uploading** is not lost: `EnqueueJobs` marks the running job `dirty`, and when that job finishes it goes back to pending instead of being deleted, so the file is uploaded once more with its newest content (and not a third time). `FailJob` does the same for a dirty job.
- **Failures:** a failed upload leaves the job `failed` and a row in `syncd errors`. While running, `RetryFailedJobs` puts failed jobs back to pending every 5 minutes, so "will retry" is true; a file that keeps failing is retried every 5 minutes forever. Within one attempt the Retries layer already backs off.
- **Lost sign-in** stops the run (the workers cannot make progress): in-flight jobs are requeued and `syncd run` exits 1 with the same hint as `sync`. Other errors from the database also stop it.
- **Watcher problems** (a queue overflow, a folder that could not be watched) are logged and printed on stderr, and the run goes on. Missed events are recovered by the next start's reconcile.
- **Ctrl+C** cancels the uploads in flight; they resume from the start next time (resumable sessions are not persisted). Exit code 0.
- **Limits:** only one `syncd` may run jobs against a database at a time; this is enforced by a lock (see Hardening). Renames and moves are handled (see Renames and moves). Deletes are not propagated: a deleted file stays in Drive (ticket 26).

## Hardening

Three things keep `syncd run` up and safe for days.

**Panics are recovered and logged** (`internal/sync/safe.go`). `safely(name, fn)` runs one unit of work and recovers a panic in it, logging `panic recovered` at error level with the goroutine name (`worker`, `watcher.event`, `debouncer.event`, `debouncer.fire`, `run.feeder`, `run.retry`, `run.watch-errors`, `report`), the panic value, the stack, and the `path` and `job` where known. Every long-lived loop wraps one iteration in it, so a bug costs that event, tick or job and not the daemon.

| Where the panic happens | What happens |
| --- | --- |
| a worker, while processing or recording a job (an upload bug, a `report` callback) | the job is marked `failed`, an error row `internal error (see log)` appears in `syncd errors`, a `failed <path>: internal error` line is printed, the worker takes the next job; the 5-minute retry tries it again |
| the watcher or debouncer loop | that one event or timer fire is lost; the loop carries on (a lost debounce entry is dropped and starts again on the next event) |
| the feeder, retry ticker or watcher-error pump in `Run` | that one iteration is lost |
| the main goroutine (`syncd` itself) | logged with the stack, `syncd: internal error: ... (see the log)`, exit 1; nothing is left to keep running |

The log never contains file contents (stack traces show function names and addresses). A file that panics every time costs one log entry per retry; there is no retry cap. Tests force panics through a `testPanic` seam that is nil in production, not through a debug flag.

**A clear message when inotify runs out** (`internal/sync/watchlimit.go`). On Linux, adding a watch fails with `ENOSPC` once `fs.inotify.max_user_watches` is used up (`EMFILE` for `max_user_instances`). That becomes `ErrWatchLimit` with the directory, how many directories were watched before it, the current limit read from `/proc`, and the fix:

```
syncd: watch failed: inotify watch limit reached: cannot watch /data/vendor/x after 8192 directories (fs.inotify.max_user_watches=8192)
  raise it with:  sudo sysctl fs.inotify.max_user_watches=524288
  and keep it after a reboot with:  echo 'fs.inotify.max_user_watches=524288' | sudo tee /etc/sysctl.d/60-burrow.conf
```

At start the watcher fails with it (a half-watched tree would silently miss changes), so `syncd watch` and `syncd run` exit 1. While running, a new folder that cannot be watched is reported once (stderr and the log) and the run goes on; the next start's reconcile finds the files in it. On other systems `ENOSPC` means a full disk and is left alone.

**One syncd at a time** (`internal/store/lock.go`). `syncd run` and `syncd sync` take an exclusive non-blocking file lock on `<database>.lock` (in the data directory, never the sync root; `github.com/gofrs/flock`, Unix `flock` and Windows `LockFileEx`) before opening the database. If another holds it: `syncd: another syncd is using this database (run or sync); stop it first`, exit 1. The OS releases the lock when the process exits or crashes, so it is never stale. Read-only commands (`errors`, `history`, `scan`, `watch`, `db`) do not lock and work while a daemon runs; `put` only touches the folder cache and is not locked either.

## File identity

Rename detection (ticket 25) needs to know that the file that vanished from one path is the file that appeared at another. The OS cannot answer that portably after the fact, so each tracked file's identity is stored in `files.inode`:

| OS | Identity | How it is read (`fileID`, `internal/sync/fileid_*.go`) |
| --- | --- | --- |
| Linux, macOS, other Unix | inode number | `syscall.Stat_t.Ino` from the `Lstat` the scan already did: free |
| Windows | NTFS file index (64 bit) | `CreateFile` + `GetFileInformationByHandle`, one syscall pair per file; 0 if the file cannot be opened |
| anything else | unknown (0) | stub |

- **0 means unknown** and is stored as NULL. Nothing fails because an ID is missing; ticket 25 falls back to the hash alone for such files.
- **What keeps it:** a rename or move within the filesystem, and an edit. **What changes it:** delete and recreate, a copy, a restore from backup.
- **Where it is written:** the scan puts it in each `Entry`; `sync` and `run` store it in the row on every upload, update and "content unchanged" refresh (`run` reads it with its `Lstat` at claim time). An unknown ID never erases a known one.
- **Old and stale rows catch up without any upload:** rows from before this feature have no inode, and a file replaced by a copy with the same size and mtime has a stale one. Whenever a file is judged unchanged (a job's size-and-mtime shortcut, and `Reconcile` at `run` start) a different non-zero inode is written with `SetInode` / `SetInodes` (one transaction). `Diff` (`scan --dry-run --diff`) stays read-only.
- **Limits:** the inode is unique per filesystem, not globally (device and volume are not stored), so it is only a hint and ticket 25 always confirms with the hash. Two hard links share one inode. ReFS 128-bit IDs are not supported. Directories are not tracked.
- **See it:** `syncd stat --local --path notes/todo.txt` prints `Inode:` and should equal `ls -i`. `make vet` also vets for `windows` and `darwin`, since the Windows helper cannot run on the development machine.

## Renames and moves

A renamed or moved file keeps its Drive file ID, its content and its revision history: the app moves the Drive file instead of uploading it again. It works in `syncd sync`, in `syncd run` (live) and after a restart, because the check is made by the job for the new path (`tryMove` in `internal/sync/sync.go`), not by the watcher: OS rename events differ and cannot be paired across a restart, but by the time the job runs the old path is known to be gone.

**The match.** A file with no usable row (no row, or one without a Drive ID) and a known inode is a *move* of a tracked file only when all of these hold; otherwise it is uploaded as new, exactly as before:

| Condition | Why |
| --- | --- |
| a tracked row (with a Drive ID and a synced MD5) has the same non-zero inode (see File identity) | the file system says it is the same file |
| that row's local file no longer exists | a copy or hard link leaves the original in place |
| **exactly one** such row | two vanished hard links are ambiguous |
| the new file's streamed MD5 equals that row's synced MD5 | the inode alone can be reused by an unrelated file after a delete |

A rename **plus an edit** in one step fails the hash check and is uploaded as new; the old Drive file stays until deletes are handled (ticket 26). That is the conservative choice: matching on the inode alone could attach a stranger's content to an old Drive file.

**The Drive call** (`Uploader.Move`, `internal/drive/move.go`, under the retry layer): `files.get` for the file's parents, ensure the folder that mirrors the new directory (created lazily like uploads), then one `files.update` with the new `name`, `addParents` the new folder, `removeParents` the old ones and the new `rel_path` appProperty. No content is sent. When the file is already in the right folder the parent arguments are left out, so repeating a move after a lost reply changes nothing. A missing or trashed Drive file is `ErrFileNotFound`; the sync then uploads the file as new and drops the stale row.

**The database.** After Drive says yes, `Repo.RenameFile` renames the row in one transaction: the Drive ID, base MD5, base revision and synced MD5 stay, and `file_revisions` (keyed by Drive ID) follows; size, mtime, inode and local MD5 come from the new file; a leftover row without a Drive ID at the target is dropped. Error rows for the old path are cleared. The move happens before the rename, so a crash in between just repeats the (idempotent) move on the next run. A transient Drive error fails the job (it is retried) and leaves the old row alone.

**Output.** `moved old -> new` on stdout (next to `uploaded` and `updated`), `N moved` in the `syncd sync` summary. `syncd scan --dry-run --diff` and the `reconciled:` line of `run` preview likely moves, found without hashing (a new file and a missing row with the same inode and size, one each); the job confirms them by hash. `Plan.Moved` pairs leave the new and missing lists.

**Folders.** Renaming a folder shows up as every file below it appearing at a new path, so each file is moved by its own job: the end state is right, at one Drive call per file, and the emptied old folder stays in Drive. Moving a folder as one operation is ticket 27.

**Limits.** Matching is by inode, which is unique per filesystem and not stored with a device number; the hash makes a false match require an identical file. No match across filesystems or volumes. The code has no OS branch (Windows file IDs are used as inodes).

## Errors

A file that fails to sync leaves a row in the `sync_errors` table, so the user (and later the tray UI) can read what is wrong without parsing logs. `syncd errors` prints it.

```
$ syncd errors
2026-10-11T10:02:11Z  docs/a.pdf    Drive storage quota exceeded, will retry
2026-10-11T10:02:09Z  img/big.png   network problem, will retry
```

- **Table:** `sync_errors(rel_path UNIQUE, message, detail, attempts, first_seen, last_seen)`. One row per file, the latest error. A repeat updates the row and counts another attempt. `message` is readable; `detail` is the raw error text, for debugging (no tokens or request bodies, same rule as the logs).
- **Written** by the sync worker when a file fails (`internal/sync/sync.go`), and for entries the scan could not read (permission denied on a file or folder).
- **Cleared** when the file next syncs (uploaded, updated, or skipped because Drive already has the content), when its job is dropped because the file is gone, and for a failing file that has been deleted. So the list is always what is wrong now; `no errors` means every file is fine.
- **Messages** come from `drive.Describe` (`internal/drive/describe.go`): `Drive storage quota exceeded`, `Drive rate limit hit` (429 and the rate-limit 403s), `permission denied by Drive`, `file or folder not found in Drive`, `Drive is unavailable (HTTP 503)`, `network problem`, `not signed in`, `sign-in expired`, `cannot read file: permission denied`, `file is gone`. Anything else is the first line of the error text, cut to 200 characters.
- **"will retry"** is on every line because the next `syncd sync` queues every failed file again (see Job queue). If something later stops being retried, a flag column is added then.
- `syncd errors` reads the database only: no sign-in, no network. Newest first, UTC. Exit 0 even when there are errors. `syncd sync` ends with `see 'syncd errors' for details` when a file failed or could not be read.
- Not recorded: run-level errors with no file, such as a lost sign-in (reported by the exit code) or `max_user_watches` (tickets 23, 33 to 36). A failed job for a file that was deleted stays `failed` in the jobs table; it is harmless and not shown.

## Retries

Every file's Drive work (connect, tag lookup, folder creation, create or update) runs inside `withRetry` (`internal/drive/retry.go`), called from `Uploader.Upload`.

- **Retried:** HTTP 429, any 5xx, 403 whose reason is `rateLimitExceeded`, `userRateLimitExceeded` or `sharingRateLimitExceeded`, and transient network errors (timeout, connection reset or refused, unexpected EOF).
- **Not retried:** any other 403 (permission, `storageQuotaExceeded`), 404, 401, `invalid_grant` sign-in errors, and a cancelled context. Waiting would not fix these, and the user should see them.
- **Backoff:** full jitter under a ceiling of 1 s that doubles to a 30 s cap; `Retry-After` is honoured when it asks for longer. 5 attempts in all, so a file waits roughly a minute at worst before it fails. Cancelling (Ctrl+C) ends the wait at once.
- **Idempotent:** each attempt starts with the `rel_path` tag lookup and seeks the file back to the start. If a create succeeded but its reply was lost, the retry finds the tagged file and updates it, so there is no duplicate.
- **Logging:** each retry is one warn line with `attempt`, `delay` and `err`.
- A file that still fails after its retries is an ordinary per-file failure in `syncd sync`; a sign-in error still aborts the run.
- The Drive client's own chunk retry for large files stacks with this one (longer worst case, never wrong). `Stat`, `FindByTags` and `EnsureRoot` called alone are not retried. Run-wide throttling comes with ticket 17.

## Logging

Rule: diagnostics go through `log/slog`, installed once in `main`; user-facing results stay on stdout and stderr, and user-facing errors come from the `sync_errors` table (see Errors), never from parsing logs.

```
main -> setupLogger(os.Getenv("SYNC_LOG"), stderr, store.LogPath())
          SYNC_LOG set    -> text handler on stderr at that level (debug, info, warn, error); anything else exits 2
          SYNC_LOG unset  -> text handler at info into a rotating file; stderr stays free for command output
        run(...) ; closeLog() flushes and closes the file before exit
```

- File: `burrow/logs/syncd.log` in the `adrg/xdg` data dir (Local on Windows), next to the database; the directory is created with mode 0700. `lumberjack` rotates at 10 MB and keeps 10 compressed backups. `store.LogPath()` only computes the path.
- Packages log through `slog.Default()` rather than taking a logger, so `Sync` and `Uploader` signatures are unchanged. Tests install their own handler; `run` never touches the logger.
- Attributes on `internal/sync` lines: `path` (the `rel_path`), `job` and `drive_file_id`. `job` is the file's 1-based position within a run, a stand-in until the job queue (ticket 17) provides real job IDs under the same name. `uploaded`, `updated` and `refreshed row` are info; `unchanged` and the Drive call (`drive upload done`, in `internal/drive`) are debug; a failed hash or upload is warn; a failed row write or lost sign-in is error. A new file that fails has an empty `drive_file_id`, since Drive never assigned one.
- Never logged: tokens, auth headers, request bodies, SQL arguments, file contents. Only paths, IDs, sizes and error text.
- Not yet: panic recovery logging (ticket 23) and the settings toggle for debug logging (ticket 36).
- Code: `setupLogger`, `parseLevel` (`cmd/syncd/log.go`), `store.LogPath` (`internal/store/path.go`); log calls in `sync.Sync` and `Uploader.Upload`.

## Development

How to build and check the project locally. There is no CI (see ADR-002), so run the checks before committing or merging.

| Command | What it does |
| --- | --- |
| `make build` | Builds `bin/syncd` with `Version` set from `git describe --tags --always --dirty` |
| `make vet` | `go vet ./...`, plus the same for `GOOS=windows` and `GOOS=darwin` |
| `make test` | `go test -race ./...` (race detector required) |

```
syncd <command>
  version  -> prints Version (a git describe string, or "dev" with a plain go build)
  login    -> browser sign-in (loopback + PKCE), saves the refresh token in the keychain, prints "Signed in as <email>"
  whoami   -> silent sign-in from the saved token, prints "Signed in as <email>"; exit 1 with a hint if not signed in or expired
  logout   -> clears the saved token, prints "Signed out" (safe to repeat)
  root     -> finds or creates MySync/ in Drive, prints its ID and URL; caches the ID in state.json
  put      -> [--root DIR] <file>: uploads into MySync/ with a rel_path tag; the same rel_path updates the same file
  stat     -> <file-id> or --path <rel_path>: prints ID, name, size, MD5, revision and rel_path
  scan     -> --dry-run [--root DIR]: lists the files a sync would upload, after ignore rules
  sync     -> [--root DIR]: uploads every file under the root not yet recorded in the state database; records Drive IDs
  (none)   -> usage on stderr, exit 2
  unknown  -> error on stderr, exit 2
```

Config: the OAuth client credentials come from `BURROW_GOOGLE_CLIENT_ID` and `BURROW_GOOGLE_CLIENT_SECRET`. Copy `.env.example` to `.env` (git-ignored); `syncd` loads it at startup with `godotenv`, and real environment variables win. `drive.LoadClient()` reads them. See the README for the Google Cloud setup. Release builds (ticket 37) will inject the values with `-ldflags` instead.

Layout: `cmd/syncd` (CLI, plain `os.Args` switch, no framework), `internal/sync`, `internal/store` and `internal/drive` (package stubs for now). `run(args, stdout, stderr) int` holds the logic so it can be tested without spawning a process.
