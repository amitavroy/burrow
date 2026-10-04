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

## Development

```
make build   # builds bin/syncd
make vet     # go vet ./...
make test    # go test -race ./...
```
