# Ticket 3: OAuth login command (`syncd login`)

Source: `requirements.md` section 14, ticket 3 (M1 Hello Drive). Depends on 1 and 2, both done.
Done when: **the browser opens and the terminal prints the signed-in email.**
Final location after approval: `docs/plans/003-oauth-login-command.md` (same format as `docs/plans/archive/002-*.md`).

## Context

Tickets 1-2 gave us the skeleton, `drive.LoadClient()` (client ID/secret from `.env`) and `ScopeDriveFile`. Ticket 3 adds the sign-in itself: loopback redirect plus PKCE via `golang.org/x/oauth2`. Persisting the refresh token is ticket 4, so here the token lives in memory only and `login` just proves it works by printing the account email. No keychain, no DB, no Drive folder.

## Decisions

1. **Email comes from Drive `about.get(fields=user(emailAddress))`, not the userinfo endpoint.** userinfo needs the `openid email` scope, which would break the `drive.file`-only rule in CLAUDE.md. `about.get` works with `drive.file`. This adds `google.golang.org/api/drive/v3`, which tickets 5-6 need anyway.
2. **Request a refresh token now** (`access_type=offline`, `prompt=consent`) so ticket 4 can store it without changing the flow. Without `prompt=consent`, Google omits the refresh token on repeat sign-ins.
3. **Loopback listener on `127.0.0.1:0`** (random free port), redirect URI `http://127.0.0.1:<port>/callback`. Handler validates `state` (random, constant-time compare) and returns the one-shot code over a channel; then the server shuts down. Overall timeout of 2 minutes; also honours `ctx` cancel (Ctrl+C).
4. **PKCE:** `oauth2.GenerateVerifier()`, `oauth2.S256ChallengeOption`, `oauth2.VerifierOption` (needs the x/oauth2 version that has these; `go get` latest).
5. **Browser opening is injectable** (`func(url string) error`), default uses `xdg-open` / `open` / `rundll32 url.dll,FileProtocolHandler`. The auth URL is always printed too, so a headless box can paste it. On failure to open, print a note and continue waiting.
6. **Never log tokens, auth headers, or the auth code.** Print only the URL (which has no secret beyond the PKCE challenge) and the email.
7. **Package split:** flow in `internal/drive/auth.go` (library, testable); `cmd/syncd` only wires the `login` subcommand. `Login` returns an `*oauth2.Token` so ticket 4 can plug in storage.

## To-do

Vertical slices; review after each.

| # | Slice | Step | Done |
| --- | --- | --- | --- |
| 1 | **Slice 1: Auth flow library** | `go get golang.org/x/oauth2` and `google.golang.org/api` | [x] |
| 2 | | `internal/drive/auth.go`: `Login(ctx, Client, opts) (*oauth2.Token, error)` with loopback listener, state check, PKCE, timeout | [x] |
| 3 | | `internal/drive/auth_test.go`: fake token endpoint + fake "browser" that hits the redirect; cases for success, state mismatch, `error=access_denied`, timeout, PKCE verifier sent | [x] |
| 4 | | Review point: `make vet test` clean under `-race`. **Review point.** | [x] |
| 5 | **Slice 2: Email lookup** | `internal/drive/about.go`: `Email(ctx, token, Client) (string, error)` using `drive.NewService` with `oauth2.Config.Client`; test with `httptest` server via `option.WithEndpoint` | [x] |
| 6 | **Slice 3: `syncd login`** | `cmd/syncd/main.go`: `login` case calls `LoadClient`, `Login`, `Email`, prints `Signed in as <email>`; usage text lists `login` | [x] |
| 7 | | `cmd/syncd/main_test.go`: `login` with missing env returns exit 1 and names the variable (flow itself is covered in the drive package) | [x] |
| 8 | | README: short "Sign in" section (`make build && bin/syncd login`); note that the token is not saved until ticket 4 | [x] |
| 9 | | Demo: real run opens the browser and prints the email. **Review point.** | [x] |

## Files to create/modify

| File | Change |
| --- | --- |
| `internal/drive/auth.go` | Create: `Login`, options (open-browser func, timeout, output writer), loopback handler |
| `internal/drive/auth_test.go` | Create |
| `internal/drive/about.go` | Create: `Email` |
| `internal/drive/about_test.go` | Create |
| `cmd/syncd/main.go` | Add `login` subcommand; keep `run(args, stdout, stderr)` signature so tests stay pure |
| `cmd/syncd/main_test.go` | Add missing-env case |
| `go.mod` / `go.sum` | Add `golang.org/x/oauth2`, `google.golang.org/api` |
| `README.md` | Sign-in section |
| `requirements.md` | Mark ticket 3 done (matches how 1-2 were marked) |

Reused: `drive.LoadClient`, `drive.ScopeDriveFile` (`internal/drive/oauthclient.go`), the `run()` dispatch in `cmd/syncd/main.go`.

## Testing plan

Table-driven, no network. A fake OAuth server (`httptest`) stands in for `oauth2.Endpoint`; the injected "browser" func parses the auth URL, asserts `code_challenge_method=S256`, `access_type=offline`, `scope=drive.file`, the loopback `redirect_uri`, then GETs the redirect with `code`/`state`. The fake token endpoint asserts `code_verifier` is present. Error cases: wrong state, provider error param, context timeout (short timeout in opts). Email test checks the `fields` query and parses the response.

## Verification

1. `make vet test` passes (race detector on).
2. Manual: with real `.env`, `make build && bin/syncd login` opens the browser, consent screen shows only "See, edit, create, and delete only the specific Google Drive files you use with this app", terminal prints `Signed in as you@example.com`.
3. Manual: deny consent, or wait past the timeout, and confirm a clear error and exit code 1.
4. `git grep` shows no token/code logging; the process exits cleanly with the port released.
