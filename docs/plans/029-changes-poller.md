# Ticket 38: Changes poller (GH-38)

Source: `requirements.md` section 14, ticket 38 (P2 Two-way sync), and section 9 "Detecting remote changes: store a `startPageToken` and poll the Drive `changes.list` API instead of re-listing everything". Depends on 29 (`ListTree`), done. Unblocks 40 (downloads and echo suppression). GitHub issue: #27.
Done when: **`syncd changes` prints edits made in the Drive web UI.**
Final location: `docs/plans/029-changes-poller.md` (same format as `docs/plans/028-release-builds.md`).

## To-do

Vertical slices; review after each. Every slice leaves `make vet test` green.

| # | Slice | Step | Done |
| --- | --- | --- | --- |
| 1 | **Slice 1: `syncd changes` baseline and a first change** | Store: migration `00007_meta.sql` (`meta(key TEXT PRIMARY KEY, value TEXT NOT NULL)`), `Repo.GetMeta(ctx, key) (string, bool, error)` and `Repo.SetMeta(ctx, key, value)` through the owning goroutine. Drive: `Uploader.StartPageToken(ctx) (string, error)` (`changes.getStartPageToken`) and `Uploader.Changes(ctx, token) ([]Change, string, error)` (`changes.list`, all pages, fields `nextPageToken, newStartPageToken, changes(fileId, removed, file(id,name,mimeType,trashed,md5Checksum,headRevisionId,modifiedTime,appProperties))`, `includeRemoved`, page size 1000), both inside `withRetry` and `connect`. `type Change struct { FileID, Name, RelPath string; Tagged, Removed, Trashed bool; MD5, RevisionID string; Time time.Time }`; `RelPath` is the `rel_path` tag, else the name. CLI: `syncd changes` takes the database lock; with no stored token it saves the baseline and prints `watching Drive changes from now`; otherwise it lists changes since the token as `modified <rel_path>`, `trashed <rel_path>` or `removed <file id>` (one per line, oldest first) and saves the new token after printing. Tests: store (missing key, set, overwrite, survives reopen); Drive against a fake changes endpoint (baseline, one page, the token sent on the request); CLI output. **Review point (demo: `syncd changes`, edit a file in the Drive web UI, `syncd changes` prints `modified notes/a.txt`, a third run prints nothing).** | [x] |
| 2 | **Slice 2: only what matters** | Paging (follow `nextPageToken` until `newStartPageToken`); skip folders and the shared `config.json` (`role` tag); collapse several changes to one file into the last. Hide the daemon's own echoes: a change whose `RevisionID` equals the DB row's `base_revision_id` (or whose trashed state matches a row already gone) is skipped unless `--all`. `--reset` drops the stored token and takes a new baseline. Tests: paged fake (3 pages), folder and config skipped, two edits to one file print once, own upload hidden and shown with `--all`, `--reset`. **Review point (demo: let `syncd run` upload a file and edit another in the web UI; only the web edit prints).** | [x] |
| 3 | **Slice 3: failure handling** | An expired or invalid token (`410 Gone` or `400 invalid page token`) is not an error loop: print `page token expired; changes before now are not reported (run syncd sync / restore to catch up)`, store a fresh baseline, exit 0. A failure mid-read leaves the stored token untouched, so the next run repeats the read (at-least-once). Sign-in errors reuse `reportDriveErr`. The fake gains a lost reply, a 410 and a 429-then-200. Tests for each. **Review point.** | [x] |
| 4 | **Slice 4: docs** | Wiki "Changes poller" section (token in `meta`, baseline semantics, echo hiding, the 410 reset, what is not done), README "Drive changes", `internal/drive/doc.go`, `internal/store/doc.go`, usage text, `=> Done` for ticket 38 in `requirements.md`. **Review point.** | [x] |

## Context

Phase 2 needs to learn about edits made on Drive (the web UI, or another machine) without listing the whole tree each time. Drive's `changes` API gives exactly that: a token marks a point in time, and `changes.list` returns every change since it, plus a new token. This ticket builds the reader and a CLI to look at it. It deliberately does nothing with the changes (no downloads, no decisions): ticket 39 decides what a change means and ticket 40 applies it. Seeing the real feed first, including what `drive.file` includes and how our own uploads echo back, is how those later tickets avoid guessing.

## Decisions

1. **The token lives in the state DB (`meta` table).** Section 6 lists the changes page token among the DB tables. The DB is a disposable cache (CLAUDE.md): if it is deleted the token is gone and the next run takes a new baseline. Nothing is lost, because a fresh machine or DB is caught up by `restore` / `sync`, not by the feed. A generic key/value `meta` table is used instead of a one-row table so later settings have a home without another migration; only one key (`changes_page_token`) is used now.
2. **First run is a baseline, not a backfill.** With no token the command calls `getStartPageToken`, stores it, and prints that it is watching from now. Reporting history is not what the feed is for (the token only reaches back a limited time anyway); catching up is `restore` / `sync`.
3. **The token moves only after the work is done.** Output is printed, then the new token is saved. A crash in between re-reports the same changes on the next run (at-least-once), which is safe because nothing is applied yet and ticket 40's application must be idempotent anyway.
4. **The CLI takes the database lock.** It writes the token, and CLAUDE.md wants one owner of the DB. So `syncd changes` cannot run while `syncd run` does, and says so like `sync` does. A read-only peek that does not save was considered and left out: until ticket 40 puts the poller inside the daemon, stopping the daemon for a look is acceptable. Revisit if it hurts.
5. **Our own uploads come back through the feed.** Every upload by the daemon is a change. They are hidden by default by comparing `headRevisionId` with the DB row's `base_revision_id` (written since ticket 13); `--all` shows them for debugging. This is the same signal ticket 40 will use for echo suppression, so slice 2 also proves it works on real data. A change that differs from the DB (a web edit, a restore from trash) is shown.
6. **What is shown.** Files only. Folders are skipped (their renames matter for paths, but they are handled when a file inside changes; ticket 40 revisits this), and so is `config.json` (`role` tag, as `ListTree` does). With `drive.file` the feed only holds files the app created, so a change without a `rel_path` tag is shown under its file name, as `ListTree` does for a stray file (no parent lookup). Removed (`removed: true`, no file metadata) prints the file ID, since the name is gone; trashed prints the path.
7. **Several changes to one file collapse to the last**, because only the final state matters. Order is the feed's order (oldest first) after collapsing.
8. **An expired token resets, it does not loop.** Drive answers 410 (or 400 for a malformed token) once a token is too old. The command says changes before now are not reported, stores a fresh baseline and exits 0. Exit 1 here would make a scheduled run fail forever.
9. **Retries and sign-in are reused.** Calls go through `withRetry` (ticket 16) and `connect`; sign-in errors print through `reportDriveErr`. No new dependency.
10. **No polling loop yet.** `syncd changes` is one-shot. A `--watch` interval flag was left out because the loop belongs inside the daemon (ticket 40), where it shares the lock, the control API and the queue.
11. **Plan number vs issue number.** This doc is 029 and the GitHub issue is #27; they are unrelated counters. `GH-38` follows the repo's ticket-number convention in titles and commits.

## Shape

```go
// internal/store
func (r *Repo) GetMeta(ctx context.Context, key string) (value string, ok bool, err error)
func (r *Repo) SetMeta(ctx context.Context, key, value string) error

// internal/drive
type Change struct {
    FileID, Name, RelPath string
    Tagged, Removed, Trashed bool
    MD5, RevisionID string
    Time time.Time
}
func (u *Uploader) StartPageToken(ctx context.Context) (string, error)
// Changes returns every change after token (all pages) and the token to save next.
func (u *Uploader) Changes(ctx context.Context, token string) ([]Change, string, error)
```

```
syncd changes [--all] [--reset]
  no token  -> save baseline, "watching Drive changes from now"
  token     -> modified <rel_path> | trashed <rel_path> | removed <file id>
               own uploads hidden unless --all
  410       -> "page token expired; ...", new baseline, exit 0
```

## Files to create/modify

| File | Change |
| --- | --- |
| `internal/store/migrations/00007_meta.sql` | new: `meta` table |
| `internal/store/meta.go`, `meta_test.go` | new: `GetMeta` / `SetMeta` |
| `internal/drive/changes.go`, `changes_test.go` | new: `StartPageToken`, `Changes`, fake changes endpoint |
| `cmd/syncd/main.go`, `main_test.go` | `changes` command, usage, lock |
| `README.md`, `docs/wiki/index.md`, `requirements.md`, package docs | docs, ticket 38 done |

## Testing plan

- Store: `meta` get/set/overwrite/reopen, under `-race`.
- Drive, against a fake: baseline, one page, three pages, token on the request, folder and config skipped, removed and trashed, 410, 429 then 200, lost reply.
- CLI: first run, later run, nothing new, `--all`, `--reset`, lock held by another syncd.
- Own-echo hiding with a DB row whose `base_revision_id` matches.

## Verification

1. `make vet test` passes.
2. Real Drive: `syncd changes` (baseline), edit a synced file in the web UI, `syncd changes` prints `modified <path>`, run it again and nothing prints.
3. Upload a file with `syncd sync`, then `syncd changes` hides it and `syncd changes --all` shows it.
4. Open: confirm against real Drive what the `drive.file` feed includes (our uploads, `config.json`, folders) before slice 2 is signed off.
