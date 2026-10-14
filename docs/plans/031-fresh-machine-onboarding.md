# Fresh-machine onboarding: restore, auto-start sync, tray autostart (GH-29)

Source: conversation; found while installing the `.deb` on a new machine. Builds on GH-28 (apt repo, plan 030) and GH-37 (release builds, plan 028). GitHub issue: #29.
Done when: **on a clean machine, installing the package, logging out and in, and signing in from the tray is all it takes: the tray is already there, syncing starts by itself, and the files already in Drive appear in the sync folder.**
Final location: `docs/plans/031-fresh-machine-onboarding.md` (same format as `docs/plans/030-apt-repository.md`).

## To-do

Vertical slices; review after each. Every slice leaves `make vet test` green.

| # | Slice | Step | Done |
| --- | --- | --- | --- |
| 1 | **Slice 1: first run pulls what is in Drive** | In `internal/sync/run.go`, before the workers and the changes poller start, add an initial restore step: when `repo.GetMeta(MetaChangesToken)` is empty, call a new `RunOptions.Restore func(ctx) error` hook. `cmd/syncd/main.go` wires it to `uploader.ListTree` + `burrowsync.Restore` (the same calls as `restoreCmd`, with `Host` from the hostname). Take the changes baseline (`src.Start`) **before** `ListTree`, so an edit made while the restore runs is not missed (Pull is idempotent). Per-file failures go to `sync_errors` through the existing error path and do not stop `Run`; a sign-in error stops it as today. Progress is logged and printed like `syncd restore` (`downloaded <path>`). The restore does not run again once the token exists, and restarting mid-restore resumes it: `Restore` skips files already matching, and the token is saved only after a complete pass. **Review point (demo: `syncd uninstall --purge`, empty `~/MySync`, `syncd login`, `syncd run`; the Drive files appear and `syncd ctl status` shows the queue).** | [x] |
| 2 | **Slice 2: the tray shows the restore** | Add `Restoring` and `RestoreDone/RestoreTotal` to `control.Status` (`internal/control/control.go`) and fill them from a counter in `sync.Control` that the restore hook updates. `cmd/syncui/view.go` shows `Downloading 12 of 340…` with the blue syncing icon while it runs, and failed files show in the existing errors submenu. **Review point (demo: repeat slice 1 with the tray open and watch it count).** | [x] |
| 3 | **Slice 3: signing in starts syncing** | Extract the body of `serviceInstall` into a function `login` can call. After a successful `syncd login` (token saved and email cached), run it with the default root unless the service is already installed (`service status`), and print `syncing ~/MySync`. The tray's "Sign in…" already runs `syncd login`, so it gets this for free: remove the "Start syncing" menu item and its `kindStopped` special case in `cmd/syncui/main.go`, but keep a small "Start syncing" fallback shown only when signed in and the daemon is down, in case the install failed (its error already shows through `note`). A `--no-service` flag on `login` keeps the old behaviour for scripts and tests. **Review point (demo: fresh machine, tray "Sign in…", browser, then the folder fills with no further click).** | [x] |
| 4 | **Slice 4: the tray starts at login** | New `syncui autostart` logic in `cmd/syncui` (or a shared `internal/autostart` package that both binaries use, moving `trayDesktopEntry`, `trayPlist` and the Run-key code out of `cmd/syncd/service.go`): on every launch, ensure the per-user autostart entry exists and points at the running executable; skip it when `/etc/xdg/autostart/burrow-syncui.desktop` is present and no user file exists yet (the package already handles that, a user file of the same name would override it) unless the exe path differs. `syncd login` calls the same function when a `syncui` sits next to `syncd`, so a tarball install also gets it at sign-in. `syncd uninstall` keeps removing it. A tray the user quits from the menu stays quit until the next login. **Launcher and single instance (Linux):** the `.deb` also installs `packaging/burrow.desktop` as `/usr/share/applications/burrow.desktop` (`Name=Burrow`, `Exec=/usr/bin/syncui`, `Icon=burrow`, `Categories=Network;FileTransfer;`, no autostart keys) and the icon from `cmd/syncui/burrow.png` as `/usr/share/icons/hicolor/256x256/apps/burrow.png`, via new `nfpms` `contents` entries in `.goreleaser.yaml`, so typing "Burrow" in the Ubuntu app search starts the tray with no relogin. `syncui` today has no single-instance guard (`systray.Run` only), so launching it from search while autostart already ran would add a second icon: take a non-blocking lock on `syncui.lock` in the data dir (`gofrs/flock`, as `internal/store/lock.go` does) at startup and exit quietly if it is held. **Review point (demo: remove `~/.config/autostart/burrow-syncui.desktop`, run `syncui &`, the file reappears; log out and in, the tray is there).** | [x] |
| 5 | **Slice 5: docs and GNOME note** | README Install and Quick start rewritten around the new flow (install, sign in from the tray, done); drop the manual `service install` step but keep it documented as the way to change `--root`. Add a "No tray icon on GNOME" note: stock GNOME needs the AppIndicator extension (`gnome-shell-extension-appindicator` on Debian and Ubuntu, preinstalled on Ubuntu). Wiki: first-run restore (when it runs, why the token is the marker, how it differs from `syncd restore`). **Review point.** | [x] |

### As built

- Slice 1 took `RunOptions.Remote` (the Drive listing) and `OnRestore` instead of a `Restore` hook, so `Run` owns the restore and can record errors and progress. It runs **before the watcher starts**, which settles the echo question: downloads are never seen as local edits. A test checks no upload is made.
- Slice 4 moved the tray autostart code from `cmd/syncd/service.go` into `internal/autostart` (`Install`, `Uninstall`, `Ensure`) with the existing service tests unchanged. `Ensure` also honours a user `Hidden=true` entry (the README's documented opt-out) and skips the user file when the package's entry already starts the same binary.
- Slice 4's launcher icon is a 256 px generated square (`packaging/burrow.png`: the tray arch in white on blue), since the tray's own `burrow.png` is a 64 px black alpha mask that vanishes on dark launchers.
- Not verified on a real desktop or a built `.deb` (no `goreleaser` here): `desktop-file-validate` passes and the `.goreleaser.yaml` parses; run `make release-check` and `dpkg -c` before releasing.

## Context

Installing on a new machine today needs: a relogin for the tray to appear (the `.deb` only adds `/etc/xdg/autostart/burrow-syncui.desktop`, read at the next login; tarball installs have no autostart until `syncd service install` runs), a click on "Start syncing" after sign-in (`syncd login` only stores the token; the tray's start item runs `syncd service install`), and a manual `syncd restore` to download existing Drive files. `syncd run` never downloads on its own: its changes poller (`pollChanges` in `internal/sync/pull.go`) takes a "from now" baseline on its first poll and only applies later edits, so an empty folder stays empty.

Drive is the source of truth and the DB is a disposable cache (CLAUDE.md), so a machine with no DB must rebuild from Drive: this is the same rule, applied automatically.

## Decisions

1. **The marker for "first run" is the missing changes token, not an empty folder.** A folder can be empty because the user deleted everything; a missing token means this DB has never seen Drive. Deleting the DB (allowed by the disposable-cache rule) therefore re-runs the restore, which is safe because `Restore` records matching files and keeps both copies of differing ones.
2. **Baseline first, then list, then download.** Taking the changes start token before `ListTree` means anything edited during the restore shows up in the first poll. The token is stored after the restore pass completes without a fatal error; per-file failures do not block it, they sit in `sync_errors` and the user can run `syncd restore` to retry (documented).
3. **The restore runs inside `Run`, not as a separate step in the service.** One process, one lock, one control socket, and the tray can show progress. `syncd restore` stays as the manual command (and for a machine used restore-only).
4. **Default root `~/MySync`; per-device roots stay an open question** (requirements.md section 12). We do not add a folder picker or per-device config here. `--root` on `service install` keeps working.
5. **Sign-in installs the service.** The click on "Start syncing" is a step with no decision in it. A `--no-service` flag keeps scripted use and tests simple. The fallback tray item stays for the failure case only.
6. **The tray manages its own autostart entry.** The package file covers only the next login of a package install; the tray knows its own path. Writing the user entry only when needed avoids shadowing the package file (same name, user wins).
7. **No new auth or storage.** The token stays in the keychain, the DB stays in the xdg data dir. No tokens in logs or in `Status`.
8. **Autostart and a searchable launcher, both.** Autostart alone gives users who do not want a tray at login no way to start it later, and it does nothing right after install (a package script runs as root and cannot start a program in the user's session). The launcher lets the user start the tray at once from the app search. The launcher has no autostart keys; the autostart file stays separate. macOS (an `.app` bundle in `/Applications`, since the release has no `syncui`) and Windows (Start menu shortcut) are follow-up tickets.
8a. **A second launch must not make a second icon.** Launcher plus autostart makes this common, hence the lock in slice 4.
9. **GNOME's missing tray support is documented, not worked around.** The extension is the user's desktop choice.
10. **Out of scope:** macOS `.app` bundle, Windows Start menu shortcut, a settings window or folder picker, per-device roots, restoring into a non-empty folder with a different layout beyond the existing conflicted-copy rule, two live machines on one folder (phase 2), a CI workflow (ADR-002).
11. **Plan number vs issue number.** This doc is 031 and the GitHub issue is #29; they are unrelated counters.

## Shape

```go
// internal/sync/run.go (sketch)
type RunOptions struct {
    // ...
    // Restore fills the root from Drive; Run calls it once, when no changes
    // token is stored yet, before the workers and the poller start.
    Restore func(ctx context.Context) error
}

// in Run, after Reconcile and before the workers:
if opts.Restore != nil && opts.Changes != nil {
    tok, _, err := repo.GetMeta(ctx, store.MetaChangesToken)
    if err != nil { return err }
    if tok == "" {
        if err := baseline(); err != nil { return err } // src.Start, SetMeta
        if err := opts.Restore(ctx); err != nil { return err } // sign-in or db errors only
    }
}
```

```go
// cmd/syncd/main.go (sketch)
Restore: func(ctx context.Context) error {
    remote, err := uploader.ListTree(ctx)
    if err != nil { return err }
    _, err = burrowsync.Restore(ctx, root, repo, remote, uploader.Download, reportRestore(stdout, stderr), burrowsync.RestoreOptions{Host: host})
    return err
},
```

The reconcile that runs before the restore would see an empty tree and queue nothing; after the restore the downloaded files have `files` rows with matching size and mtime, so a later restart queues nothing either. The `expected` echo-suppression map used by `Pull` covers the watcher seeing the downloads; the restore must set it too (or run before the watcher starts), otherwise every downloaded file is uploaded back as a new job. Slice 1 decides which and tests it.

## Files to create/modify

| File | Change |
| --- | --- |
| `internal/sync/run.go` | `RunOptions.Restore`; first-run restore step; progress counters on `Control` |
| `internal/sync/control.go` | restore counters and `POST`-free status fields |
| `internal/control/control.go` | `Status.Restoring`, `RestoreDone`, `RestoreTotal` |
| `cmd/syncd/main.go` | wire `Restore` in `runCmd`; `login` installs the service unless `--no-service` |
| `cmd/syncd/service.go` | extract install into a reusable function; move tray autostart code to the shared package |
| `internal/autostart/` | new: tray autostart entry for Linux, macOS and Windows (moved from `service.go`) |
| `cmd/syncui/main.go`, `view.go` | restore progress line; "Start syncing" only as the fallback; ensure autostart on launch; single-instance lock |
| `packaging/burrow.desktop` | new: app-menu launcher (searchable "Burrow"), no autostart |
| `.goreleaser.yaml` | `nfpms` contents: launcher to `/usr/share/applications/`, icon to `/usr/share/icons/hicolor/256x256/apps/` |
| `README.md`, `docs/wiki/index.md` | new flow, GNOME AppIndicator note, first-run restore |

## Testing plan

- Slice 1: a `Run` test with an empty DB and a fake remote tree: files appear in the root, rows are recorded with `Source: download`, **no upload jobs are queued for them** (echo check), and the changes token is set. A second `Run` with the token set does not call `Restore`. A restore that fails on one file still sets the token and leaves a `sync_errors` row. A sign-in error aborts `Run` without setting the token.
- Slice 2: `view_test.go` cases for the `Downloading N of M…` line and the icon; a control status test for the new fields.
- Slice 3: `main_test.go` for `login` calling the install function (fake service manager as in `service_test.go`), the already-installed case, and `--no-service`.
- Slice 4: autostart tests per OS with the existing `goos` seam: writes when missing, rewrites when the exe path changed, leaves alone when the package file exists and no user file does, removed by uninstall.
- Single instance: a second `syncui` with the lock held exits 0 without creating an icon (test the lock helper; the exit path is thin).
- Launcher: `desktop-file-validate packaging/burrow.desktop` is clean, and `dpkg -c` on the built `.deb` lists the launcher and icon at the paths above.
- `go vet ./...` and `go test -race ./...` after every slice.

## Verification

- On a clean VM or container with a desktop: install the `.deb`, log out and in, confirm the tray is present (with the AppIndicator extension on GNOME), click "Sign in…", and watch `Downloading N of M…` fill `~/MySync` with no other click.
- Delete `~/.local/share/burrow` and restart the service: the folder is matched again with nothing re-uploaded (check `syncd history` shows no new upload revisions and Drive shows no duplicates).
- Edit a file in Drive during the restore and confirm the first poll applies it.
- After installing the `.deb` (no relogin), search "Burrow" in the app grid: the tray starts; launching it again leaves one icon.
- `syncd service status` active after login; `syncd uninstall` removes the service and the tray entry.
