# Ticket 13: Revision tracking (GH-TBD)

Source: `requirements.md` section 14, ticket 13 (M2 One-shot sync). Depends on 12, done. Unblocks 39 (three-way decision).
Done when: **`syncd history <path>` lists revisions.**
Final location: `docs/plans/015-revision-tracking.md` (same format as `docs/plans/archive/014-*.md`).

## To-do

Vertical slices; review after each. Every slice leaves `make vet test` green.

| # | Slice | Step | Done |
| --- | --- | --- | --- |
| 1 | **Slice 1: `file_revisions` table and repo API** | Migration `00003_file_revisions.sql`: `file_revisions(id, drive_file_id, revision_id, md5, size, time, source)`, unique on `(drive_file_id, revision_id)`. `store.Revision`; `Repo.RecordUpload(ctx, File, Revision)` upserts the files row and inserts the revision (`INSERT OR IGNORE`) in one transaction; `Repo.Revisions(ctx, relPath)` newest first, joined on `files`. Tests: migration applies, insert and order, duplicate ignored, lookup follows the Drive ID, atomic write. **Review point.** | [x] |
| 2 | **Slice 2: `Sync` records base state and revisions** | On each successful upload or update set `BaseMD5 = info.MD5`, `BaseRevisionID = info.RevisionID` and call `RecordUpload` (source `upload`). Skipped or refreshed rows leave base alone. Empty `RevisionID`: write the row, skip the revision insert. Tests with the fake `UploadFunc`: new file, update moves base and adds a second revision, skip adds none, failed upload changes nothing, empty revision ID tolerated. **Review point.** | [x] |
| 3 | **Slice 3: `syncd history <path>`** | `rel_path` argument, DB only. One line per revision, newest first: time, revision ID, MD5, size, source. Unknown path or no history: stderr message, exit 1; usage error exits 2. Tests: output and exit codes. **Review point (demo: edit a file, rerun `sync`, `history` shows 2 revisions matching the Drive web UI).** | [x] |
| 4 | **Slice 4: docs** | Wiki "One-shot sync" and "State database", README, `internal/store/doc.go`, `internal/sync/doc.go`, `=> Done` for ticket 13 in `requirements.md`. **Review point.** | [x] |

## Context

`drive.FileInfo.RevisionID` already carries `headRevisionId` and the `files` table already has `base_md5` and `base_revision_id`, but `Sync` never fills them and there is no `file_revisions` table. Phase 2 conflict detection (local vs Drive vs base) needs this data recorded from v1.

## Decisions

1. **Keyed by Drive file ID**, so history survives renames.
2. **History reads the local `file_revisions` table**, not Drive's `revisions.list`: offline, and durable after Drive prunes old revisions. Uploads before this ticket have no rows; the next upload starts the history, no backfill.
3. **Base means the last successful sync with Drive.** Only upload/update success moves it; a refreshed unchanged row does not.
4. **Row and revision are written in one transaction**, still with `context.WithoutCancel`.
5. **Empty `headRevisionId` never fails a file.**
6. `source` is `upload` now; `download` arrives with restore (ticket 31).

## Verification

1. `make vet test` under `-race`.
2. `bin/syncd sync --root tree`, edit one file, rerun, `bin/syncd history <rel_path>`: 2 revisions, newest first, IDs match Drive "Manage versions".
3. `touch`-only rerun adds no revision rows; `files.base_md5` and `base_revision_id` are filled.
