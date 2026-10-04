# Ticket 4: Keychain token storage (`syncd logout`, `syncd whoami`) (GH-4)

Source: `requirements.md` section 14, ticket 4 (M1 Hello Drive). Depends on 3 (done).
Done when: **`syncd whoami` works after a restart without signing in again.**
Final location after approval: `docs/plans/004-keychain-token-storage.md` (same format as `docs/plans/archive/003-*.md`).

## Context

Ticket 3 left `drive.Login` returning an `*oauth2.Token` that is dropped when `syncd login` exits. Ticket 4 persists the refresh token in the OS keychain with `github.com/zalando/go-keyring`, adds `syncd whoami` (re-authenticates silently from the stored token and prints the email) and `syncd logout` (clears it). Per CLAUDE.md the refresh token lives only in the keychain, never in the DB, config or logs.

## Decisions

1. **Store only the refresh token string**, not the whole token JSON. Access tokens are short-lived and re-derived by refreshing. Keychain entry: service `burrow`, user `google-refresh-token`.
2. **Zero-expiry gotcha:** `oauth2.Config.Client(ctx, &Token{RefreshToken: rt})` treats a zero `Expiry` as "never expires", so it would send an empty access token and never refresh. A helper `drive.TokenFromRefresh(rt)` returns a token with `Expiry` in the past to force a refresh on first use.
3. **`TokenStore` interface** (`Load() (string, error)`, `Save(string) error`, `Delete() error`) with a `KeyringStore` implementation in `internal/drive/tokenstore.go`. Sentinel `ErrNotSignedIn` maps from `keyring.ErrNotFound`. Delete of a missing entry is a no-op, so `logout` is idempotent.
4. **`login` saves right after the code exchange**, before the email lookup, so a flaky `about.get` doesn't waste a successful sign-in. If Google returns no refresh token, fail with a clear message (should not happen with `prompt=consent`).
5. **`whoami` refresh failures:** `invalid_grant` (revoked/expired) prints "session expired, run `syncd login`" and exits 1. Not signed in prints the same hint. Keychain unavailable (e.g. headless Linux without Secret Service) is a distinct error naming the cause. No file fallback, since CLAUDE.md forbids secrets outside the keychain.
6. **`logout` only clears the local entry**; it does not revoke the token at Google (out of scope; could be a later flag).
7. **Testability:** `keyring.MockInit()` in tests for the store and for `cmd/syncd`. `Resume`/whoami logic lives in `internal/drive` and takes an endpoint override (unexported, in-package tests) so the refresh can hit an `httptest` server.
8. Never log the token; error messages from the keychain are wrapped without including the value.

## To-do

Vertical slices; review after each.

| # | Slice | Step | Done |
| --- | --- | --- | --- |
| 1 | **Slice 1: login persists the token** | `go get github.com/zalando/go-keyring` | [x] |
| 2 | | `internal/drive/tokenstore.go`: `TokenStore`, `KeyringStore`, `ErrNotSignedIn` | [x] |
| 3 | | `cmd/syncd/main.go`: `login` saves the refresh token after exchange; test with mock keyring that it is stored. **Review point.** | [x] |
| 4 | **Slice 2: `syncd whoami`** | `internal/drive/resume.go`: `TokenFromRefresh`, `Resume(ctx, client, store)` returning email via existing `drive.Email` | [x] |
| 5 | | `whoami` subcommand, error hints for not-signed-in / revoked / keychain unavailable | [x] |
| 6 | | Tests: refresh against fake token endpoint (asserts `grant_type=refresh_token`), not signed in, `invalid_grant`. **Review point.** | [x] |
| 7 | **Slice 3: `syncd logout`** | `logout` subcommand deletes the entry, idempotent; test login-then-logout-then-whoami fails | [x] |
| 8 | | README + `docs/wiki/index.md` (Sign-in section: token now stored, new commands; add to command list); mark ticket 4 done in `requirements.md` | [x] |
| 9 | | Demo: `login`, restart shell, `whoami` prints email; `logout`, `whoami` says not signed in. **Review point.** | [x] |

## Files to create/modify

| File | Change |
| --- | --- |
| `internal/drive/tokenstore.go` (+ `_test.go`) | Create |
| `internal/drive/resume.go` (+ `_test.go`) | Create |
| `cmd/syncd/main.go` | Save token in `login`; add `whoami`, `logout`; update stale "not persisted yet" comment |
| `cmd/syncd/main_test.go` | Cases for the new commands using `keyring.MockInit()` |
| `go.mod` / `go.sum` | Add `github.com/zalando/go-keyring` |
| `README.md`, `docs/wiki/index.md`, `requirements.md` | Docs and ticket status |

Reused: `drive.Email`, `Client.OAuthConfig`, `drive.LoadClient`, the `run(args, stdout, stderr)` dispatch.

## Testing plan

Table-driven, no network, no real keychain. Store: save/load/delete round trip, missing entry gives `ErrNotSignedIn`, delete twice is fine. Resume: fake token endpoint receives `grant_type=refresh_token` and the stored refresh token; fake `about.get` returns the email; `invalid_grant` response yields the "run login" error. CLI: exit codes and messages for each command; assert no output contains the token value.

## Verification

1. `make vet test` passes under `-race`.
2. Manual (needs a desktop keychain): `bin/syncd login`, open a new shell, `bin/syncd whoami` prints the email with no browser.
3. Inspect the keychain (e.g. `secret-tool lookup service burrow`) shows the entry; `bin/syncd logout` removes it and `whoami` then exits 1 with the sign-in hint.
4. `git grep` confirms no token logging; `~/.local/share` and `.env` contain no token.
