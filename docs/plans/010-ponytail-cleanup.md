# Ticket: Simplify code flagged by the ponytail audit (GH-9)

Source: `/ponytail-audit` run after ticket 8. Not a numbered ticket in `requirements.md` section 14; it is a cleanup between tickets 8 and 9.
Done when: **`make vet test` passes under `-race` after every slice, and no CLI output or Drive behaviour has changed.**
Final location after approval: `docs/plans/010-ponytail-cleanup.md` (same format as `docs/plans/009-*.md`).

## To-do

Vertical slices, one area each. Every slice leaves the build green, so review after each. Pure refactor: if a test has to change its expectations (not just its setup), stop and ask.

| # | Slice | Step | Done |
| --- | --- | --- | --- |
| 1 | **Slice 1: one CLI error reporter** (audit #2) | `cmd/syncd/main.go`: add `reportDriveErr(stderr, cmd, err) int` covering `ErrNotSignedIn`, `ErrSessionExpired` and the generic `"<cmd> failed"` case. Use it in `whoami`, `root`, `put`, `stat`. Existing `main_test.go` output checks must pass unchanged. **Review point.** | [x] |
| 2 | **Slice 2: one function per Drive operation** (audit #1, #9) | `internal/drive`: delete the unexported twins `uploadFile`, `stat`, `findFileByTags`, `ensureRoot`, `resume`, `email` and keep one exported function each. Tests reach the fake OAuth endpoint through an optional `Client.Endpoint *oauth2.Endpoint` (zero means Google), mirroring `LoginOptions.Endpoint`. Add `oldestMatch(ctx, svc, q, fields)` shared by `findOrCreateRoot` and `findByTags`. Update the tests' setup only. **Review point.** | [x] |
| 3 | **Slice 3: drop single-impl interfaces** (audit #3, #6) | Remove `TokenStore` and `RootStore`; callers take `KeyringStore` / `FileStore` directly. `FileStore.Save` becomes `os.WriteFile` (0o600) since `Load` already treats a damaged file as empty. Remove the temp-file test if it only covered the rename. **Review point.** | [ ] |
| 4 | **Slice 4: tighter `Repo`** (audit #4, #5, #7) | `internal/store/repo.go`: generic `do[T any]` replaces `any` and the `v.(File)` assertions; `sql.Null[T]` shortens `scanFile` and the null helpers (the NULL mapping and the `Inode` bit reinterpretation stay the same). `store.go`: inline `fsSub()`. All `repo_test.go` tests unchanged and green, including `-count=20`. **Review point.** | [ ] |
| 5 | **Slice 5: docs** | `docs/wiki/index.md` only where it names removed things (the `TokenStore`/`RootStore` interfaces, the `Repo` shape, the temp-file-and-rename wording for `state.json`); `internal/store/doc.go` / `internal/drive/doc.go` if they mention them. **Review point.** | [ ] |

## Context

The audit found about 80 lines that can go with no behaviour change. Tickets 9 to 11 add a scanner, a folder cache and the one-shot sync on top of the Drive and store helpers, so cleaning first keeps those tickets smaller. Drive stays the source of truth and the DB a disposable cache; nothing here touches either contract.

## Decisions

1. **Behaviour is frozen.** CLI output, exit codes, Drive queries, the schema and the `Repo` API (`Upsert`, `Get`, `GetByDriveID`, `List`, `Delete`, `Close`) do not change. Only internals and the exported-wrapper surface shrink.
2. **Test endpoint via `Client.Endpoint`.** The twin functions existed only so tests could pass a custom `oauth2.Config`. An optional `Endpoint *oauth2.Endpoint` on `Client` (nil means `google.Endpoint`) lets `OAuthConfig` serve both. Tests that used `extra ...option.ClientOption` for the Drive endpoint keep that.
3. **Interfaces go until a second implementation exists.** `TokenStore` and `RootStore` have one implementation and no fakes. Phase 2 can reintroduce an interface at the point of use if it needs one.
4. **Simpler cache write.** `state.json` is disposable and a damaged file already reads as empty, so the atomic temp-file write buys nothing. (Downloads in tickets 30 and 31 still use temp-file-then-rename; CLAUDE.md requires that for user data.)
5. **Kept on purpose:** the `Repo` owner goroutine (CLAUDE.md), goose, go-keyring, adrg/xdg, `state.json` as a separate file, and `GetByDriveID` / `List` (tickets 11 and 25 use them).
6. **Ponytail comments.** If a shortcut is left on purpose, mark it `ponytail:` so `/ponytail-debt` can list it.

## Files to create/modify

| File | Change |
| --- | --- |
| `cmd/syncd/main.go` (+ `main_test.go` if needed) | `reportDriveErr`, four call sites |
| `internal/drive/{upload,root,resume,about,auth}.go` | Merge twins, `Client.Endpoint`, `oldestMatch` |
| `internal/drive/{tokenstore,rootstore}.go` | Remove interfaces, simplify `Save` |
| `internal/drive/*_test.go` | Setup only |
| `internal/store/{repo,store}.go` | Generic `do`, `sql.Null`, inline `fsSub` |
| `docs/wiki/index.md`, `internal/*/doc.go` | Wording only |

## Testing plan

- No new behaviour, so no new tests; the existing suite is the safety net.
- Slice 1: the existing `main_test.go` output and exit-code checks pass unchanged.
- Slice 2: all `internal/drive` tests pass with only their setup changed.
- Slice 4: `go test -race -count=20 ./internal/store/` is stable.
- Each slice: `go vet ./...` clean.

## Verification

1. `make vet test` passes after every slice.
2. `git diff --stat` shows a net reduction of roughly 80 lines and no dependency removed from `go.mod`.
3. Smoke test against a signed-in dev account: `bin/syncd whoami`, `root`, `put <file>` and `stat` print the same output as before; with the token cleared they print the same "not signed in" hint and exit 1.
4. `/ponytail-audit` again lists none of findings 1 to 7 and 9.
