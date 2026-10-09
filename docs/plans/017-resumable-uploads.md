# Ticket 15: Resumable uploads (GH-15)

Source: `requirements.md` section 14, ticket 15 (M3 Robust uploads), and section 7 "Large files". Depends on 12, done. Unblocks nothing directly; ticket 16 (retries) builds on the same upload path.
Done when: **a 1 GB file uploads with flat memory use.**
Final location: `docs/plans/017-resumable-uploads.md` (same format as `docs/plans/archive/016-*.md`).

## To-do

Vertical slices; review after each. Every slice leaves `make vet test` green.

| # | Slice | Step | Done |
| --- | --- | --- | --- |
| 1 | **Slice 1: prove flat memory in a test** | Most of the ticket already exists: `Uploader.Upload` passes `googleapi.ChunkSize(8 MB)` and streams the open file, and `TestUpload` already checks the chunked protocol against the fake server (`internal/drive/upload_test.go`). What is missing is proof that memory stays flat. Add a test that uploads a large sparse file (about 128 MB, created with `Truncate`, so no disk cost) through the fake Drive server, which drains and discards each chunk and samples `runtime.ReadMemStats` `HeapInuse` per chunk request. Assert the peak stays under a fixed bound (a few chunks plus slack, far below the file size). Also assert the chunk count and the `Content-Range` sequence. Skipped under `-short`. **Review point.** | [x] |
| 2 | **Slice 2: cancellation mid-upload** | Test that cancelling the context during a multi-chunk upload returns promptly, leaves no goroutine or open file behind, and that `Sync` treats it as an abort (no row written, no `failed` count), as it does today for small files. Fix only if the test shows a real gap. **Review point.** | [x] |
| 3 | **Slice 3: real 1 GB check and docs** | Manual: create a 1 GB file in a scratch root, run `SYNC_LOG=debug syncd sync`, watch RSS (`/usr/bin/time -v` or `ps`) stay near the 8 MB chunk plus baseline; record the number in the wiki. Docs: wiki "Uploading a file" (chunk buffer behaviour, what a restart does), README note, `internal/drive/doc.go`, `=> Done` for ticket 15 in `requirements.md`. **Review point (demo: 1 GB upload, flat RSS).** | [x] |

## Context

Ticket 6 already switched uploads to the Drive client's resumable mode: files under 8 MB go as one multipart request, larger ones as 8 MB chunks with `Content-Range`, streamed from the open file. So this ticket is mainly about proving the acceptance criterion (flat memory) and pinning it with a test, not new upload code. The client library buffers one chunk at a time, so expected memory is about one chunk (8 MB) above baseline, whatever the file size.

## Decisions

1. **No new upload code unless a test fails.** The lazy path is tests plus docs.
2. **Heap sampling inside the fake server** is the memory check: deterministic, no 1 GB file in CI. The real 1 GB run is a manual verification step.
3. **"Resumable" means resumable within one run.** The client retries a failed chunk on its session URI; a killed process starts the file again from byte 0 on the next run (the row is only written after success, so nothing is lost). Persisting the session URI across restarts is out of scope.
4. **Streamed MD5 stays as is.** Drive returns `md5Checksum`, which becomes `synced_md5`; no extra hash pass over the upload.

## Out of scope (say if you want any of these)

- Persisting the resumable session URI to continue after a crash.
- Comparing a locally streamed MD5 (via `io.TeeReader` during the upload) with Drive's `md5Checksum` as an integrity check.
- Retries with backoff (ticket 16) and concurrency (ticket 17).

## Files to create/modify

| File | Change |
| --- | --- |
| `internal/drive/upload_test.go` | memory and cancellation tests |
| `internal/drive/upload.go` | only if the cancellation test finds a gap |
| `internal/sync/sync_test.go` | cancel-mid-upload case, if not already covered |
| `docs/wiki/index.md`, `README.md`, `internal/drive/doc.go`, `requirements.md` | Docs and status |

## Verification

1. `make vet test` under `-race` (the memory test is skipped with `-short`).
2. Manual: `truncate -s 1G scratch/big.bin`, `/usr/bin/time -v bin/syncd sync --root scratch`: maximum resident set stays well under 100 MB and does not grow with file size.
3. Ctrl+C during that upload exits promptly and writes no row; a rerun uploads the file again.
