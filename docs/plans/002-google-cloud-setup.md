# Google Cloud Setup and OAuth Client Config (GH-2)

Source: `requirements.md` section 14, ticket 2 (M1 Hello Drive). Issue: https://github.com/amitavroy/burrow/issues/2

## To-do

Slices are vertical: review and confirm after each one.

| # | Slice | Step | Done |
| --- | --- | --- | --- |
| 1 | **Slice 1: Cloud project ready (manual)** | Create the Google Cloud project and enable the Drive API | [x] |
| 2 | | Configure the OAuth consent screen: External, app name "Burrow", support email, scope `drive.file`, publish to "In production" | [x] |
| 3 | | Create an OAuth client of type "Desktop app"; copy the client ID and secret into a local `.env` | [x] |
| 4 | | Review point: consent screen status reads "In production". **Review point.** | [x] |
| 5 | **Slice 2: Credentials loaded from `.env`** | Add `.env.example` (placeholders) and add `.env` to `.gitignore` | [x] |
| 6 | | Add `internal/drive/oauthclient.go`: `LoadClient()` and the `drive.file` scope const | [x] |
| 7 | | Add `internal/drive/oauthclient_test.go` (table-driven, `t.Setenv`) | [x] |
| 8 | | Add `godotenv` and load `.env` best-effort at startup in `cmd/syncd` | [ ] |
| 9 | | Demo: `make vet test` is clean. **Review point.** | [ ] |
| 10 | **Slice 3: README and docs** | Add `README.md` with the Google Cloud setup steps and the `.env` copy step | [ ] |
| 11 | | Add `godotenv` to the stack table in `requirements.md` section 3; add a `.env` line to the wiki Development section | [ ] |
| 12 | | Demo: a fresh reader can follow the README to a working `.env`. **Review point.** | [ ] |

## Context

Ticket 3 (`syncd login`) needs the OAuth client that ships with the app. This ticket creates the Google Cloud project and Desktop OAuth client, and wires the credentials into the repo through `.env`. Most of the work is manual in the Google Cloud console. No login flow, keychain or Drive calls yet.

Constraints from `CLAUDE.md`: user OAuth (loopback and PKCE), scope `drive.file` only, one client ID that never changes (access is tied to it), consent screen "In production" (in "Testing" mode refresh tokens expire after 7 days). No user tokens in the repo.

## Decisions

1. **Credentials come from `.env`, not Go constants.** Google treats a desktop client secret as non-confidential, but keeping it out of source is the preferred hygiene. `.env` is git-ignored; `.env.example` is committed with placeholders.
2. **Variable names:** `BURROW_GOOGLE_CLIENT_ID` and `BURROW_GOOGLE_CLIENT_SECRET`.
3. **`LoadClient()` in `internal/drive`** reads them from the environment and returns an error naming the missing variable. It does not read files itself, which keeps it trivially testable.
4. **The `drive.file` scope is a Go const**, not configuration. It is not a credential and must never vary.
5. **`github.com/joho/godotenv` loads `.env` in `cmd/syncd`**, best-effort (a missing file is fine) and real environment variables win. It is the first third-party dependency and is not in the requirements stack table, so the table is updated.
6. **Release builds (ticket 37) cannot rely on a `.env` beside the binary.** Flagged for ticket 37: inject the values with `-ldflags -X` at build time, with `.env` as the dev-time override. Not built here; the "one client ID ships with the app" invariant stays true.
7. **README.md is created** (none exists) and holds only this setup for now.

## Layout

`.env.example`:

```
# Copy to .env and fill in from the Google Cloud console (see README).
BURROW_GOOGLE_CLIENT_ID=
BURROW_GOOGLE_CLIENT_SECRET=
```

`internal/drive/oauthclient.go` (shape):

```go
package drive

import (
	"fmt"
	"os"
)

// ScopeDriveFile is the only scope the app requests.
const ScopeDriveFile = "https://www.googleapis.com/auth/drive.file"

// Client holds the OAuth client credentials that ship with the app.
type Client struct {
	ID     string
	Secret string
}

// LoadClient reads the OAuth client credentials from the environment.
func LoadClient() (Client, error) {
	id := os.Getenv("BURROW_GOOGLE_CLIENT_ID")
	if id == "" {
		return Client{}, fmt.Errorf("BURROW_GOOGLE_CLIENT_ID is not set")
	}
	secret := os.Getenv("BURROW_GOOGLE_CLIENT_SECRET")
	if secret == "" {
		return Client{}, fmt.Errorf("BURROW_GOOGLE_CLIENT_SECRET is not set")
	}
	return Client{ID: id, Secret: secret}, nil
}
```

`cmd/syncd/main.go`: call `_ = godotenv.Load()` at the top of `main()` before `run`, so `run` stays pure and testable.

## Files to create/modify

| File | Change |
| --- | --- |
| `.env.example` | Create: placeholder variables |
| `.gitignore` | Add `.env` |
| `internal/drive/oauthclient.go` | Create: `Client`, `LoadClient`, `ScopeDriveFile` |
| `internal/drive/oauthclient_test.go` | Create: table-driven tests |
| `cmd/syncd/main.go` | Load `.env` via godotenv in `main()` |
| `go.mod` / `go.sum` | Add `github.com/joho/godotenv` |
| `README.md` | Create: Google Cloud setup steps |
| `requirements.md` | Add `godotenv` to the section 3 stack table |
| `docs/wiki/index.md` | Add a `.env` line to the Development section |

## Testing plan

Table-driven test of `LoadClient` using `t.Setenv`:

- Both variables set: returns the client, no error.
- ID missing: error mentions `BURROW_GOOGLE_CLIENT_ID`.
- Secret missing: error mentions `BURROW_GOOGLE_CLIENT_SECRET`.
- `ScopeDriveFile` equals `https://www.googleapis.com/auth/drive.file` (guards against scope creep).

## Verification

1. `make vet test` passes with the race detector.
2. With a real `.env` created, `git status` does not list it; `git grep` finds no real client secret in tracked files.
3. Google Cloud console shows: Drive API enabled, consent screen "In production", one Desktop client.
4. A fresh reader can follow the README from clone to a filled-in `.env`. The full sign-in is verified in ticket 3.
