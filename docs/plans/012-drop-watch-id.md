# Drop watch_id: identify uploads by rel_path only (GH-11)

Source: design discussion after planning ticket 10. Not a numbered ticket in `requirements.md` section 14; it is a simplification that should land **before** ticket 10 (plan `011-folder-hierarchy-in-drive.md`, GH-10), so that ticket builds on a simpler tag query.
Done when: **`put` and `stat` work with `rel_path` alone, `make vet test` passes under `-race`, and no doc still describes `watch_id` as part of v1.**
Final location after approval: `docs/plans/012-drop-watch-id.md` (same format as `docs/plans/010-*.md`).

## To-do

Vertical slices; review after each. Every slice leaves `make vet test` green.

| # | Slice | Step | Done |
| --- | --- | --- | --- |
| 1 | **Slice 1: rel_path-only tags in code and CLI** | `internal/drive/upload.go`: drop `tagWatchID`, the `watchID` parameter of `Upload` and `FindByTags`, the `watch_id` clause in `findByTags`, `FileInfo.WatchID`. Tags written are `{rel_path}`. `cmd/syncd/main.go`: `put` loses `--watch`; `stat` becomes `stat <file-id>` or `stat --path <rel_path>` (bool flag plus the argument); the `watch_id:` output line goes. Update `upload_test.go` and `main_test.go`. Tests: put twice updates the same file, lookup by `rel_path`, a Drive file that still carries a `watch_id` tag is found and updated, usage errors. **Review point (demo: `put x.txt` twice keeps one file ID; `stat --path x.txt` finds it, also for a file uploaded before this change).** | [x] |
| 2 | **Slice 2: docs** | `README.md` (put/stat usage and the tag paragraph), `docs/wiki/index.md` ("Uploading a file", "Stat", the CLI list), `CLAUDE.md` (the invariants line that names `{watch_id, rel_path}`), `requirements.md` (section 5 text and JSON example, section 12 open question) saying `watch_id` is deferred with multi-folder, `internal/drive/doc.go` if it mentions it. Edit `docs/plans/011-folder-hierarchy-in-drive.md` to match (see Decisions 6). **Review point.** | [x] |

## Context

`watch_id` was meant to name one synced folder so a machine could later sync several. Requirements section 5 describes a watch as a top-level `MySync/` subfolder named by its `watch_id`, with a per-machine mapping to a local path. None of that exists, and the multi-folder question is still open (section 12). In v1 there is one sync root, so `watch_id` is always `default`. It costs a `--watch` flag on `put` and `stat`, a `FileInfo` field, a second clause in every tag query, and text in the README, wiki and `CLAUDE.md`.

The spec does not need it elsewhere: restore (section 8) uses `rel_path` only, and two machines are handled by a per-device Drive root (`MySync/<device-name>/`), a folder path and not a tag. Dropping it now is cheap because lookups can simply stop requiring it.

## Decisions

1. **A file is identified by `rel_path` alone.** Upload writes `appProperties {rel_path}`; `findByTags` queries `rel_path` only, oldest match wins (unchanged), trashed files excluded (unchanged).
2. **No migration.** Files already in Drive carry `{watch_id: default, rel_path}`. The lookup ignores the extra key, so they are still found and updated. Drive merges `appProperties` on update, so the old `watch_id` key stays on them, harmless. Nothing rewrites tags.
3. **CLI shape.**
   - `syncd put [--root DIR] <file>`
   - `syncd stat <file-id>` or `syncd stat --path <rel_path>`
   `--path` is a bool flag and the argument is the `rel_path`. Old `--watch` usage fails with the normal flag error (exit 2); there are no users to keep compatible, since the project is pre-release.
4. **`FindByTags` keeps its name** (it still looks a file up by its tags) but takes only `relPath`. Renaming it is not worth the churn.
5. **Multi-folder later.** When it is wanted, the design is a per-watch Drive folder (`MySync/<watch_id>/...`) plus a watch-to-local-path mapping. Files with no `watch_id` tag are then treated as `default`. Nothing in this ticket blocks that.
6. **Plan 011 (GH-10) changes with this ticket** so it does not mention `watch_id`:
   - Decision 1: drop "`watch_id` stays a tag on the file"; the layout is just `MySync/a/b/c.txt`.
   - Decision 5 and the Shape block: the tag query is `rel_path` only.
   - Slice 4 / wiki wording and Verification step 6: use `stat --path a/b/y.txt`, and the `find ... -exec syncd put --root tree {}` demo is unchanged.
   - Testing plan: no `watch_id` cases.
7. **Docs say deferred, not removed.** `requirements.md` keeps the multi-folder idea but marks `watch_id` as part of that later feature, so the section 12 open question still reads correctly. Section 14's ticket 6 row is history and stays as written.

## Files to create/modify

| File | Change |
| --- | --- |
| `internal/drive/upload.go` | Drop `watch_id` tag, parameters and `FileInfo.WatchID` |
| `internal/drive/upload_test.go` | Fake and assertions use `rel_path` only; add a legacy-tag case |
| `cmd/syncd/main.go` (+ `main_test.go`) | `put` without `--watch`; `stat --path`; usage text and output |
| `README.md`, `docs/wiki/index.md`, `CLAUDE.md`, `requirements.md` | `watch_id` described as deferred |
| `docs/plans/011-folder-hierarchy-in-drive.md` | Remove `watch_id` wording (Decisions 6) |

Reused: `findByTags`, `oldestMatch`, `escapeQuery`, `fileInfo` (`internal/drive/upload.go`); the `put` and `stat` flag-set patterns in `cmd/syncd/main.go`.

## Testing plan

Table-driven against the existing fake Drive server, all under `go test -race ./...`.

- `put` creates a file tagged `{rel_path}` only, and a second `put` of the same `rel_path` updates the same Drive ID.
- The tag query contains `rel_path` and no `watch_id`.
- A Drive file carrying a legacy `watch_id` tag plus the `rel_path` is found by `FindByTags` and updated by `Upload`.
- `stat <file-id>` and `stat --path <rel_path>` print the same fields minus `watch_id:`; `stat` with neither argument, or `--path` with no argument, is a usage error (exit 2); not signed in exits 1 with the login hint.
- `put` with `--watch` is rejected by the flag parser (exit 2).

## Verification

1. `make vet test` passes under `-race`.
2. `bin/syncd put x.txt` twice: the same file ID both times, and the Drive file shows only a `rel_path` property for new uploads.
3. `bin/syncd stat --path x.txt` prints ID, name, size, MD5, revision and `rel_path`, with no `watch_id` line.
4. A file uploaded before this change (tagged `watch_id: default`) is still found by `stat --path` and updated in place by `put`.
5. `grep -rn watch_id README.md docs/wiki CLAUDE.md` finds only the "deferred with multi-folder" notes.
