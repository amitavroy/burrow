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
- Two concurrent uploads could create the same folder twice; duplicates are tolerated (oldest wins). Any single-flight guard belongs to ticket 11.
- Files that fit in one 8 MB chunk go as one multipart request; larger ones use a resumable upload in 8 MB chunks. The file is streamed from disk, never read whole.
- Folders are found by name, parent and mimeType (`trashed = false`), and the oldest wins if Drive holds duplicates. They carry no `appProperties`, because the tree already implies the path. Names are escaped like tag values. Folder rename and delete come with ticket 27, and the MD5 skip and retries with tickets 12 and 16.
- Requested fields: `id,name,md5Checksum,size,headRevisionId,appProperties,webViewLink`.
- Same exit-1 hints as `whoami` when not signed in or the session expired. Usage errors exit 2.
- Code: `drive.Upload`, `drive.Stat`, `drive.FindByTags`, `drive.FileInfo`, `ErrFileNotFound`, `ensureDir` (`internal/drive/upload.go`); the cached-folder check is `folderUsable` (`root.go`). Service setup is shared through `serviceFromStore` (`root.go`), and the oldest-match list query behind `FindByTags` and the root lookup is `oldestMatch` (`upload.go`). Each operation is one exported function that takes a `drive.Client`; tests point its optional `Endpoint` at a fake OAuth server.

### Stat

`syncd stat <file-id>` uses `files.get`. `syncd stat --path <rel_path>` uses the same tag query as `put` and creates nothing in Drive. Both print ID, name, size, MD5, revision and `rel_path`. A file this app cannot see is reported as not found, because `drive.file` returns 404 for it.

## State database

`syncd db path` prints the database location and creates nothing. `syncd db status` opens the database (creating and migrating it if needed) and prints `Path:`, `Version:` and `Tables:`. Both exit 2 with usage on a missing or unknown subcommand.

- Location: `burrow/burrow.db` in the `adrg/xdg` data dir (Local, not Roaming, on Windows), next to `state.json`. `store.DefaultPath()` only computes the path, because `xdg.DataFile` would create directories. It is never inside the sync root.
- Disposable: Drive is the source of truth. Deleting the file loses only cache and the next open recreates it. It is never copied or synced.
- Driver: `modernc.org/sqlite`, pure Go, no cgo.
- `store.Open(path)` creates the directory (0700), applies the pragmas through the DSN (`journal_mode(WAL)`, `busy_timeout(5000)`, `foreign_keys(on)`, `synchronous(NORMAL)`), caps the pool at one connection and runs pending migrations. The caller must keep the `*sql.DB` in one owner goroutine; `Repo` (below) is that owner for the `files` table. SQL arguments are never logged.
- First migration `00001_init.sql` creates `files`: `rel_path` (unique), `drive_file_id`, `size`, `mtime`, `inode`, `local_md5`, `synced_md5`, `base_md5`, `base_revision_id`. See ADR-004 for how later tables arrive. A unique partial index on `drive_file_id` ignores NULLs, so unuploaded rows do not collide.
- Second migration `00002_folders.sql` creates `folders(rel_dir PRIMARY KEY, drive_folder_id)`: the folder-ID cache for nested uploads. `rel_dir` is slash-form and relative to the root, like `files.rel_path`. `Repo.GetFolder` returns `ErrNotFound` when absent, and `Repo.PutFolder` is an upsert. Like everything here, it is disposable cache.
- Repository: `store.OpenRepo(path)` calls `Open` and starts one goroutine that owns the `*sql.DB`. `Upsert` (keyed by `rel_path`), `Get`, `GetByDriveID`, `List` (ordered by `rel_path`) and `Delete` send a request over a channel and wait for the reply, so callers never see SQL or the connection. Every method takes a `context.Context`; a cancelled context returns early, but the owner still finishes the statement it started.
- `Repo` errors: `ErrNotFound` (missing row on `Get`, `GetByDriveID`, `Delete`) and `ErrClosed` (any call after `Close`). A panic inside a request is recovered and returned as an error, and the owner keeps serving. `Close` is safe to call twice.
- NULL mapping: an empty string or zero `Inode` is stored as NULL and read back as the zero value, so "no Drive ID yet" is a real NULL and the unique `drive_file_id` index ignores it. `File.MTime` is Unix nanoseconds.
- `store.Status(db)` returns the migration version and the user table names (`files`, `folders` and goose's `goose_db_version`).
- `state.json` stays for now; folding the root folder ID into the database is a later cleanup.
- Code: `internal/store/store.go`, `path.go`, `file.go`, `repo.go` (including the folder cache methods); the `db` command in `cmd/syncd/main.go`. Tests replace the `dbPath` var so they never touch the real data dir.

## Scanning

`syncd scan --dry-run` lists the files a sync would upload. It only reads the local folder; no Drive or database is involved, so the same function (`sync.Scan`) will feed the one-shot sync (ticket 11) and startup reconciliation (ticket 22).

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

## Development

How to build and check the project locally. There is no CI (see ADR-002), so run the checks before committing or merging.

| Command | What it does |
| --- | --- |
| `make build` | Builds `bin/syncd` with `Version` set from `git describe --tags --always --dirty` |
| `make vet` | `go vet ./...` |
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
  (none)   -> usage on stderr, exit 2
  unknown  -> error on stderr, exit 2
```

Config: the OAuth client credentials come from `BURROW_GOOGLE_CLIENT_ID` and `BURROW_GOOGLE_CLIENT_SECRET`. Copy `.env.example` to `.env` (git-ignored); `syncd` loads it at startup with `godotenv`, and real environment variables win. `drive.LoadClient()` reads them. See the README for the Google Cloud setup. Release builds (ticket 37) will inject the values with `-ldflags` instead.

Layout: `cmd/syncd` (CLI, plain `os.Args` switch, no framework), `internal/sync`, `internal/store` and `internal/drive` (package stubs for now). `run(args, stdout, stderr) int` holds the logic so it can be tested without spawning a process.
