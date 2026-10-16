# Burrow

![Burrow](docs/artwork/banner.png)

Burrow is a Dropbox-style sync app that uses your own Google Drive as the storage. Pick a folder on your computer, and everything you put in it is mirrored to Drive in the background.

## What, why and how

**What.** A small background program (`syncd`) and an optional tray icon (`syncui`). It watches one folder on your machine (`~/MySync` by default) and keeps a copy of it in a `MySync` folder in your Google Drive.

**Why.** If you already pay for Google storage, that space is probably sitting mostly unused while you pay again for Dropbox. Burrow lets you use all of your Drive quota with the "drop a file in a folder and it is backed up" experience of Dropbox, without a second subscription and without handing your files to another company.

**How.**

- It watches the folder for changes. A file is uploaded a couple of seconds after you stop editing it.
- Renames and moves are moved in Drive too, so a renamed file keeps its history instead of being uploaded again.
- Deleting a file moves it to the Drive trash, never a hard delete, so you can always get it back.
- Large files are streamed in chunks, so a multi-gigabyte upload does not eat your memory. Failed uploads are retried with backoff.
- It catches up on start: anything changed while the app was closed or the machine was asleep is uploaded when it runs again.
- On a new machine, `syncd restore` (or the first run) downloads everything from Drive back into your folder.

Drive is the source of truth. Burrow keeps a small local database as a cache, and you can delete it at any time without losing anything.

**Current limits.** Sync is one-way: your computer to Drive. Changes you make in the Drive web UI are not pulled down yet, and two machines syncing live to the same Drive folder is not supported. A second machine can restore from Drive or use its own folder.

## Install (Linux)

Burrow ships as a `.deb` package for Debian, Ubuntu and derivatives, and as a plain archive for other distributions. Both come from the [GitHub releases page](https://github.com/amitavroy/burrow/releases).

### Debian, Ubuntu (`.deb`)

1. On the releases page, download `burrow_<version>_linux_<arch>.deb`. Use `amd64` for most PCs and `arm64` for ARM machines (`dpkg --print-architecture` tells you).
2. Install it:

   ```
   sudo apt install ./burrow_<version>_linux_<arch>.deb
   ```

   This puts `syncd` and `syncui` in `/usr/bin`, adds a **Burrow** entry to your app menu, and starts the tray icon for every user at their next login.
3. Check it: `syncd version`.

### Other distributions (archive)

1. Download `burrow_<version>_linux_<arch>.tar.gz` from the releases page. Optionally verify it against `checksums.txt`:

   ```
   sha256sum -c --ignore-missing checksums.txt
   ```
2. Unpack it and move `syncd` and `syncui` to a permanent folder, such as `~/.local/bin`. Keep the two next to each other. The background service remembers the path of the binary, so do not move or delete it afterwards.
3. Check it: `syncd version`.

### First run

1. Open **Burrow** from your app menu (or log out and in, or run `syncui &`). A tray icon appears.
2. Click **Sign in…** in the tray menu and approve access in your browser. Without the tray, run `syncd login` instead.
3. That is it. Signing in starts the background service, which syncs `~/MySync`. On a machine that has not synced before, it first downloads what is already in your Drive folder. To use another folder, run `syncd service install --root ~/Sync`.
4. Put a file in the folder. It shows up in Drive within about a minute. Check progress with `syncd ctl status`, and see failures with `syncd errors`.

Two Linux notes:

- **Sync before you log in:** run `loginctl enable-linger $USER`. Without it, syncing starts when you log in rather than at boot.
- **No tray icon on GNOME?** Stock GNOME hides tray icons. Install the AppIndicator extension (`sudo apt install gnome-shell-extension-appindicator`), enable it in the Extensions app, and log out and in. Ubuntu ships it enabled. Syncing works with or without the tray, and quitting the tray does not stop it.

### Uninstall

```
syncd uninstall            # remove the background service and tray autostart, quit the tray
syncd uninstall --purge    # also sign out and delete Burrow's local data
sudo apt remove burrow     # remove the program (.deb installs)
```

Run `syncd uninstall` before removing the program, since it needs the binary. For an archive install, delete `syncd` and `syncui` afterwards. Neither command touches your sync folder or anything in Drive.

## Drive access and security

**Burrow can only see what it creates.** It asks Google for a single permission, the `drive.file` scope. With that scope Burrow can read and write only the files and folders it created itself. It cannot list, open or change anything else in your Drive: not your documents, not your photos, not a folder you made by hand, not even a folder that happens to be named `MySync`.

- **One folder, owned by Burrow.** Burrow creates a `MySync` folder in My Drive and keeps everything inside it. Everything outside that folder is invisible to it.
- **No broad access.** It never asks for full Drive access, and it does not use a service account.
- **Your credentials stay with you.** Sign-in happens in your own browser on Google's page (OAuth with PKCE). Burrow never sees your password. The refresh token is stored only in your operating system's keychain (GNOME Keyring or KWallet on Linux), never in a file, the database or the logs.
- **Nothing is deleted for good.** Files you delete locally go to the Drive trash, where you can restore them.
- **Safe downloads and logs.** Downloads are written to a temporary file, checked against Drive's checksum, and only then renamed into place. Tokens, auth headers and file contents are never logged.
- **Your data, your Drive.** Files go from your machine straight to Google Drive. There is no Burrow server in between.

You can revoke Burrow's access at any time at <https://myaccount.google.com/permissions>.

Google shows an "unverified app" warning at sign-in. That is normal for apps using the `drive.file` scope.

## More

Building from source, the Google Cloud setup, the full command reference and release notes are in [docs/development.md](docs/development.md). Scope and architecture are in [requirements.md](requirements.md).
