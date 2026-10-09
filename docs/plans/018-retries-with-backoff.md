# Ticket 16: Retries with backoff (GH-16)

Source: `requirements.md` section 14, ticket 16 (M3 Robust uploads), and section 7 "Retries". Depends on 12, done. Unblocks 17 (job queue), which will reuse the same retry classification.
Done when: **tests against a fake server that returns 429 then 200 pass.**
Final location: `docs/plans/018-retries-with-backoff.md` (same format as `docs/plans/archive/017-*.md`).

## To-do

Vertical slices; review after each. Every slice leaves `make vet test` green.

| # | Slice | Step | Done |
| --- | --- | --- | --- |
| 1 | **Slice 1: retry helper** | `internal/drive/retry.go`: `withRetry(ctx, op func() error) error` and `retryable(err) (delay time.Duration, ok bool)`. Retry on `*googleapi.Error` with code 429 or 5xx, code 403 whose `Errors[].Reason` is a rate-limit reason (`rateLimitExceeded`, `userRateLimitExceeded`, `sharingRateLimitExceeded`), and transient network errors (timeouts, connection reset or refused, unexpected EOF). Never retry other 403 (permission, `storageQuotaExceeded`), 404, 401/`invalid_grant` sign-in errors, or a cancelled context. Backoff: exponential from 1 s, doubling, capped at 30 s, full jitter, honouring `Retry-After` when it asks for longer; 5 attempts in all. The sleep and the jitter source are package vars so tests run instantly and deterministically; waiting stops at once when the context is cancelled. Logs each retry at warn with `attempt`, `delay`, `err` (never a request body). Tests (table-driven, no HTTP): classification for each status and reason, delay growth and cap, jitter bounds, `Retry-After`, attempts exhausted returns the last error, cancel during the wait. **Review point.** | [ ] |
| 2 | **Slice 2: wire it into `Uploader.Upload`** | Wrap the Drive part of `Upload` (connect, tag lookup, folder creation, create or update) in `withRetry`, seeking the open file back to the start before each attempt. Retrying the whole attempt is safe because the tag lookup makes it idempotent: if a create succeeded but its response was lost, the retry finds the tagged file and updates it in place instead of duplicating. Tests against the fake Drive in `upload_test.go`: **429 then 200 uploads once (the ticket checkpoint)**, 503 on the tag lookup then success, 403 `rateLimitExceeded` retried, 403 permission denied not retried, persistent 500 gives up after 5 attempts and returns the error, small (multipart) and large (chunked) files both retried, cancel during backoff returns `context.Canceled` promptly, lost create response does not duplicate. In `Sync`, a file that still fails after its retries is a normal per-file failure (existing test coverage; add one case). **Review point (demo: fake server that fails twice, `syncd sync` succeeds and logs two retries).** | [ ] |
| 3 | **Slice 3: docs** | Wiki new "Retries" section (what is retried, what is not, backoff numbers, idempotency via the tag), README note, `internal/drive/doc.go`, `=> Done` for ticket 16 in `requirements.md`. **Review point.** | [ ] |

## Context

The Google Drive client already retries a failed chunk of a resumable upload (5xx, 429, 408, network errors) with its own backoff, but only for chunks of files over 8 MB and only within a 32 s deadline per chunk. It does not retry the metadata calls that every upload makes (tag lookup, folder lookup and creation, root check), the single-request upload of small files, or the 403 rate-limit replies Drive uses for "too many requests". So a rate-limited tree of small files currently produces one `failed` line per throttled call. This ticket adds one retry layer around the whole per-file Drive work.

## Decisions

1. **Retry the whole attempt, not each HTTP call.** One wrapper in `Uploader.Upload` instead of a custom `http.RoundTripper`. A transport-level retry cannot replay the streamed upload body; retrying the attempt re-opens nothing, it just seeks the file to 0.
2. **Idempotent by construction.** Every attempt starts with the `rel_path` tag lookup (CLAUDE.md: track by tag and ID), so a retry after a lost response updates the file that was created rather than making a duplicate.
3. **Which errors.** 429, 5xx, 403 with a rate-limit reason, and transient network errors. A plain 403 (permission, `storageQuotaExceeded`) is not retried: waiting will not fix it, and the user should see it (ticket 18 will surface it).
4. **Backoff.** Exponential with full jitter (random delay between 0 and the current ceiling), ceiling 1 s doubling to 30 s, 5 attempts, `Retry-After` honoured. Worst case is a file that waits about a minute before failing.
5. **Layering with the client's own chunk retry.** The two stack; the worst case is longer, never wrong. Not tuned in this ticket.
6. **Per-file scope only.** `Sync` still continues past a file that fails after its retries; a sign-in error still aborts at once. The bounded-concurrency worker and run-wide throttling arrive with ticket 17.
7. **No new dependency.** `math/rand/v2` and `time` are enough; `gax` is already in the module graph but not needed.
8. **Logging.** Each retry is one warn line with `path`, `attempt`, `delay` and the error text (ticket 14's attributes).

## Out of scope (say if you want any)

- Retrying `Stat`, `FindByTags` and `EnsureRoot` when called on their own by `stat`, `put --path` and `root`: users can rerun a one-shot command. Easy to add with the same helper.
- A circuit breaker or shared rate limiter across files (ticket 17).
- Persisting retry state across restarts (ticket 17's queue).

## Files to create/modify

| File | Change |
| --- | --- |
| `internal/drive/retry.go` (+ `retry_test.go`) | classification, backoff, `withRetry` |
| `internal/drive/upload.go` (+ `upload_test.go`) | wrap `Upload`, seek per attempt, fake-server tests |
| `internal/sync/sync_test.go` | one case for a file that fails after retries |
| `README.md`, `docs/wiki/index.md`, `internal/drive/doc.go`, `requirements.md` | Docs and status |

## Verification

1. `make vet test` under `-race`.
2. The 429-then-200 test passes (the ticket's checkpoint).
3. Manual: point `syncd sync` at a tiny local fake that fails twice with 503; the run succeeds, stderr shows two retries with `SYNC_LOG=debug`, and Drive holds one file, not two.
