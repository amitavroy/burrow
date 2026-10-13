# Ticket 37: Release builds (GH-37)

Source: `requirements.md` section 14, ticket 37 (M7 Daemon and UI), and section 11 (packaging). Depends on 34 (done). GitHub issue: #26.
Done when: **a binary downloaded from a GitHub release runs.**
Final location: `docs/plans/028-release-builds.md` (same format as `docs/plans/archive/027-*.md`).

## To-do

Vertical slices; review after each. Every slice leaves `make vet test` green.

| # | Slice | Step | Done |
| --- | --- | --- | --- |
| 1 | **Slice 1: a built-in client, a local snapshot build** | `internal/drive`: package vars `ClientID` and `ClientSecret` (empty by default), set with `-ldflags "-X github.com/amitavroy/burrow/internal/drive.ClientID=..."`. `LoadClient` takes the env var when set, else the baked-in value, else the existing "is not set" error. `.goreleaser.yaml` (version 2): builds `syncd` for linux, darwin, windows × amd64, arm64 with `CGO_ENABLED=0`, ldflags `-s -w -X main.Version={{.Version}}` plus the two credential vars from `BURROW_GOOGLE_CLIENT_ID` / `BURROW_GOOGLE_CLIENT_SECRET` in the environment. `make release-check` loads `.env`, runs `goreleaser check` and `goreleaser release --snapshot --clean`. Tests: `LoadClient` precedence (env wins, baked-in fallback, both empty is the old error). **Review point (demo: `make release-check`, then run `dist/syncd_linux_amd64*/syncd version` and `whoami` from a folder with no `.env`).** | [x] |
| 2 | **Slice 2: tray app in the archives** | Second build `syncui` for linux and windows, amd64 and arm64, `CGO_ENABLED=0`; the windows build adds `-ldflags -H=windowsgui`. macOS gets no `syncui` (cgo). Archives: `burrow_<version>_<os>_<arch>`, `.tar.gz` (`.zip` on windows), each with the binaries for that OS, `README.md`; one `checksums.txt`. Verify the snapshot produces 6 archives and that the windows `syncui.exe` has the GUI subsystem (`file`). **Review point (demo: unpack the linux archive, run `syncd run` and `syncui`).** | [x] |
| 3 | **Slice 3: publish a real release** | `release.github` config for `amitavroy/burrow`, changelog from commits (skip `docs:`, `test:`, `chore:`), `make release` that refuses on a dirty tree or a missing tag, then runs `goreleaser release --clean` with `GITHUB_TOKEN` from `gh auth token`. Cut the first release (`v0.0.1`). **Review point (demo: download the release asset in a clean directory, check the checksum, `syncd version` prints `0.1.0`, `syncd login` works with no `.env`).** | [x] |
| 4 | **Slice 4: docs** | README "Install from a release" (download, verify, run, service install with a release binary), wiki "Release builds" section (what ships, the ldflags credentials, the macOS tray gap), update ADR-003's consequence line and the Platforms notes, `=> Done` for ticket 37 in `requirements.md`. **Review point.** | [x] |

## Context

Today the app is built from source: `make build` and `make build-ui`, with the OAuth client ID and secret read from a `.env` beside the binary (ADR-003). A downloaded binary has no `.env`, so it cannot sign in. This ticket makes a release a self-contained download: cross-compiled binaries with the client baked in at build time, published as a GitHub release.

## Decisions

1. **GoReleaser, run locally (user choice).** No GitHub Actions workflow, consistent with ADR-002. The Makefile targets are the single definition, so a tag-triggered workflow can call them later without changes.
2. **Credentials are baked in with `-ldflags -X`.** This is what ADR-003 already planned. Google treats a desktop client secret as non-confidential, and the requirements say one client ID ships with the app and never changes (`drive.file` access is tied to it). The values come from the environment (`.env` loaded by the Makefile), never from the repo.
3. **Env still wins over the baked-in value.** `LoadClient` checks the env vars first, so development and swapping the client need no rebuild.
4. **`syncd` everywhere, `syncui` where it builds without cgo.** Verified: `syncd` builds for darwin and `syncui` for linux and windows (amd64, arm64) with `CGO_ENABLED=0`. macOS `syncui` needs cgo and a Mac toolchain, which a Linux host cannot provide, so the macOS archive ships `syncd` only and the tray stays `make build-ui` on a Mac. This is a known gap, documented.
5. **Windows `syncui` is built with `-H=windowsgui`** so no console window opens.
6. **Version comes from the git tag** (`{{.Version}}`), through the existing `main.Version`.
7. **No signing, no notarization, no package managers.** macOS Gatekeeper and Windows SmartScreen will warn on first run; the README says so. Homebrew/Scoop and signing are later work.
8. **The release step is guarded.** `make release` refuses on a dirty tree or when HEAD is not on a tag, so a published version always matches a commit.
9. **Plan number vs issue number.** This doc is 028 and the GitHub issue is #26; they are unrelated counters. `GH-37` in the title and commits follows the repo's ticket-number convention (as in `GH-36`).

## Shape

```go
// internal/drive/oauthclient.go
// Set at release build time with -ldflags "-X ...drive.ClientID=...".
var ClientID, ClientSecret string
// LoadClient: env var if set, else the baked-in value, else the existing error.
```

```yaml
# .goreleaser.yaml (sketch)
version: 2
builds:
  - id: syncd
    main: ./cmd/syncd
    binary: syncd
    env: [CGO_ENABLED=0]
    goos: [linux, darwin, windows]
    goarch: [amd64, arm64]
    ldflags:
      - -s -w -X main.Version={{.Version}}
      - -X github.com/amitavroy/burrow/internal/drive.ClientID={{.Env.BURROW_GOOGLE_CLIENT_ID}}
      - -X github.com/amitavroy/burrow/internal/drive.ClientSecret={{.Env.BURROW_GOOGLE_CLIENT_SECRET}}
  - id: syncui
    main: ./cmd/syncui
    binary: syncui
    env: [CGO_ENABLED=0]
    goos: [linux, windows]
    goarch: [amd64, arm64]
    overrides:
      - goos: windows
        ldflags: [-s -w -H=windowsgui]
```

## Files to create/modify

| File | Change |
| --- | --- |
| `.goreleaser.yaml` | new: builds, archives, checksums, changelog, GitHub release |
| `internal/drive/oauthclient.go` | `ClientID` / `ClientSecret` vars; `LoadClient` precedence |
| `internal/drive/oauthclient_test.go` | precedence tests |
| `Makefile` | `release-check` and `release` targets |
| `.gitignore` | `dist/` |
| `README.md`, `docs/wiki/index.md`, `requirements.md` | docs, ticket 37 done |

## Testing plan

- Unit: `LoadClient` env over baked-in, baked-in alone, neither.
- Snapshot build: 6 archives, expected binaries in each, `checksums.txt`.
- No test for GoReleaser itself; the demo steps are the check.

## Verification

1. `make vet test` passes.
2. `make release-check` builds the snapshot; the linux `syncd version` runs from a folder without `.env`.
3. After the real release: download the asset on a clean directory, verify the checksum, run `syncd version` and `syncd login`.
4. Open: install `goreleaser` locally first (not present on this machine); `GITHUB_TOKEN` from `gh auth token` needs `repo` scope.
