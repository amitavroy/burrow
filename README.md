# Burrow

A Go daemon that watches one local folder and mirrors it to Google Drive. See `requirements.md` for scope and architecture.

## Google Cloud setup

Burrow signs in as your own Google account with an OAuth desktop client. You create that client once, in your own Google Cloud project.

1. **Create a project.** Open <https://console.cloud.google.com/projectcreate>, name it `burrow` and create it. Make sure it is selected in the top bar.
2. **Enable the Drive API.** Open <https://console.cloud.google.com/apis/library/drive.googleapis.com> and click **Enable**.
3. **Configure the consent screen.** Open <https://console.cloud.google.com/auth/overview> and click **Get started**.
   - App name `Burrow`, plus your support email.
   - Audience: **External**.
   - Contact information: your email.
4. **Add the scope.** Under **Data Access**, add only `https://www.googleapis.com/auth/drive.file`. Burrow can then see only the files it creates, and Google does not need to verify the app.
5. **Publish the app.** Under **Audience**, click **Publish app** so the status reads **In production**. In "Testing" mode refresh tokens expire after 7 days, which looks like random sign-in failures.
6. **Create the OAuth client.** Open <https://console.cloud.google.com/auth/clients>, click **Create client**, choose **Desktop app** and name it `burrow-desktop`. Copy the client ID and client secret.

An "unverified app" warning at sign-in is normal for `drive.file`.

## Local configuration

Copy the example file and fill in the two values from step 6:

```
cp .env.example .env
```

```
BURROW_GOOGLE_CLIENT_ID=<client ID>
BURROW_GOOGLE_CLIENT_SECRET=<client secret>
```

`.env` is git-ignored. Never commit it. Variables already set in your shell take precedence over `.env`.

The client ID must never change once files have been uploaded. `drive.file` access is tied to the client ID, so a different client cannot see files created by the first one.

## Sign in

```
make build
bin/syncd login
```

Your browser opens to Google's consent screen (scope `drive.file` only). When you approve, the terminal prints `Signed in as <email>`, and the refresh token is saved in your OS keychain (never in a file or the database).

```
bin/syncd whoami   # signs in silently from the saved token and prints the email
bin/syncd logout   # clears the saved token (does not revoke it at Google)
```

On Linux the keychain is the Secret Service (GNOME Keyring or KWallet), so a desktop session is needed. Without one, `login` and `whoami` fail with a keychain error rather than storing the token elsewhere.

## Drive root folder

```
bin/syncd root   # finds or creates MySync/ in My Drive, prints its ID and URL
```

Burrow creates and owns `MySync/` itself. With the `drive.file` scope it cannot see a folder you made by hand, so a hand-made `MySync/` is ignored. Running `root` again prints the same ID and creates nothing.

The folder ID is cached in `state.json` in the app data dir (`~/.local/share/burrow/` on Linux). The cache is disposable: delete it and the next run finds the existing folder again. If you trash `MySync/` in the web UI, the next run notices and creates a new one.

## Uploading a file

```
bin/syncd put [--root DIR] <file>                # uploads into MySync/ (nested folders mirrored), prints the file ID and link
bin/syncd stat <file-id>                         # prints ID, name, size, MD5, revision and tags
bin/syncd stat --path <rel_path>                 # same, found by its rel_path tag instead of ID
```

Every upload is tagged in Drive with its `rel_path`. Without `--root`, `rel_path` is the file's name; with it, the path relative to `--root` (a file outside the root is rejected). Putting a file with the same `rel_path` again updates the existing Drive file instead of making a second one. A file with `rel_path` `a/b/c.txt` is uploaded into `MySync/a/b/`; missing folders are created on the way, and existing ones are reused, never duplicated. To mirror a whole tree for now:

```
find tree -type f -exec bin/syncd put --root tree {} \;
```

Folder IDs are cached in the state database (see below) so later files in the same folder cost no lookups. The cache is disposable: delete `burrow.db` and the next `put` finds the existing folders again. `stat --path` only reads; it creates nothing in Drive.

## Scanning

```
bin/syncd scan --dry-run [--root DIR]   # lists the files a sync would upload; changes nothing
```

`--dry-run` is required. The root defaults to `~/MySync`. Output is one slash-form path per line on stdout (so it pipes cleanly); skipped entries and a `N files, M ignored` summary go to stderr. Symlinks and other special files are skipped, and an unreadable file or folder is reported and skipped without stopping the scan. A missing root, or a file given as the root, exits 1.

Ignored by default: `.git`, `node_modules`, `*.tmp` and `~$*` (editor temp files). To add your own rules, put a `.syncignore` file in the root, in gitignore syntax (`*.log`, `build/`, `**/gen/*.go`, `!keep.log`). A rule can re-include a default such as `*.tmp`, but never `.git`, and `.syncignore` itself is never listed.

## Syncing a folder

```
bin/syncd sync [--root DIR]   # uploads every file under the root that is not recorded yet
```

The root defaults to `~/MySync`. Each file is uploaded into the Drive folder that mirrors its path, in `rel_path` order, and `sync` prints `uploaded <rel_path>` for each one. The same ignore rules as `scan` apply (see above), so run `scan --dry-run` first to see what would go. Failed files are listed on stderr with a summary (`N uploaded, M already synced, K failed, I ignored`), and the exit code is 1 if any failed.

Every uploaded file gets a row in the state database (see below). A file that already has a row is skipped, so a second run uploads nothing and an interrupted run (Ctrl+C) simply continues where it stopped. For now an edited file is not re-uploaded; that arrives with the MD5 check. The database is disposable: delete `burrow.db` and the next run finds each file in Drive by its path and updates it in place, without duplicates. `sync` needs the database, so it stops with an error if it cannot open.

## State database

```
bin/syncd db path     # prints where the database lives; creates nothing
bin/syncd db status   # opens and migrates it, then prints path, migration version and tables
```

The database is a local SQLite cache at `burrow.db` in the app data dir (`~/.local/share/burrow/` on Linux), next to `state.json`. Drive is the source of truth, so the file is disposable: delete it and the next `db status` recreates it. It is never synced and never inside the sync root.

## Development

```
make build   # builds bin/syncd
make vet     # go vet ./...
make test    # go test -race ./...
```
