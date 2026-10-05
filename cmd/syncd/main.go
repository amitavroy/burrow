package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/amitavroy/burrow/internal/drive"
	"github.com/joho/godotenv"
	"golang.org/x/oauth2"
)

// Version is overridden at build time via -ldflags "-X main.Version=...".
var Version = "dev"

func main() {
	// Best-effort: a missing .env is fine, and real environment variables win.
	_ = godotenv.Load()
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// persistToken stores the refresh token from a fresh sign-in. Only the refresh
// token is kept; access tokens are re-derived by refreshing.
func persistToken(store drive.TokenStore, tok *oauth2.Token) error {
	if tok.RefreshToken == "" {
		return errors.New("Google returned no refresh token; try signing in again")
	}
	return store.Save(tok.RefreshToken)
}

// login signs in through the browser, saves the refresh token to the keychain
// and prints the account email.
func login(stdout, stderr io.Writer) int {
	client, err := drive.LoadClient()
	if err != nil {
		fmt.Fprintf(stderr, "syncd: %v\n", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	tok, err := drive.Login(ctx, client, drive.LoginOptions{Out: stderr})
	if err != nil {
		fmt.Fprintf(stderr, "syncd: login failed: %v\n", err)
		return 1
	}
	// Save before the email lookup so a flaky about.get doesn't waste the sign-in.
	if err := persistToken(drive.KeyringStore{}, tok); err != nil {
		fmt.Fprintf(stderr, "syncd: %v\n", err)
		return 1
	}
	email, err := drive.Email(ctx, client, tok)
	if err != nil {
		fmt.Fprintf(stderr, "syncd: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Signed in as %s\n", email)
	return 0
}

// whoami signs in from the stored refresh token, without a browser, and
// prints the account email.
func whoami(stdout, stderr io.Writer) int {
	client, err := drive.LoadClient()
	if err != nil {
		fmt.Fprintf(stderr, "syncd: %v\n", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	email, err := drive.Resume(ctx, client, drive.KeyringStore{})
	switch {
	case errors.Is(err, drive.ErrNotSignedIn):
		fmt.Fprintln(stderr, "syncd: not signed in; run `syncd login`")
		return 1
	case errors.Is(err, drive.ErrSessionExpired):
		fmt.Fprintln(stderr, "syncd: session expired; run `syncd login` to sign in again")
		return 1
	case err != nil:
		// Keychain errors arrive already labelled ("read keychain: ...").
		fmt.Fprintf(stderr, "syncd: whoami failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Signed in as %s\n", email)
	return 0
}

// logout clears the stored refresh token. It is local only: the token is not
// revoked at Google. Running it while signed out is fine.
func logout(stdout, stderr io.Writer) int {
	if err := (drive.KeyringStore{}).Delete(); err != nil {
		fmt.Fprintf(stderr, "syncd: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "Signed out")
	return 0
}

// root finds or creates the app-owned MySync folder in Drive and prints its ID
// and web URL. Running it again returns the same folder.
func root(stdout, stderr io.Writer) int {
	client, err := drive.LoadClient()
	if err != nil {
		fmt.Fprintf(stderr, "syncd: %v\n", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	id, err := drive.EnsureRoot(ctx, client, drive.KeyringStore{}, drive.FileStore{})
	switch {
	case errors.Is(err, drive.ErrNotSignedIn):
		fmt.Fprintln(stderr, "syncd: not signed in; run `syncd login`")
		return 1
	case errors.Is(err, drive.ErrSessionExpired):
		fmt.Fprintln(stderr, "syncd: session expired; run `syncd login` to sign in again")
		return 1
	case err != nil:
		fmt.Fprintf(stderr, "syncd: root failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s folder: %s\n", drive.RootFolderName, id)
	fmt.Fprintf(stdout, "https://drive.google.com/drive/folders/%s\n", id)
	return 0
}

// relPathFor returns the slash-form path tagged on the Drive file. Without a
// root it is the file's basename. With one it is the path relative to the root,
// and a file outside the root is rejected so no machine-specific parts leak.
func relPathFor(file, root string) (string, error) {
	if root == "" {
		return filepath.Base(file), nil
	}
	absFile, err := filepath.Abs(file)
	if err != nil {
		return "", err
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(absRoot, absFile)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("%s is outside the root %s", file, root)
	}
	return filepath.ToSlash(rel), nil
}

// put uploads one file into MySync, tagged with its watch ID and relative path.
// Putting the same tags again updates the existing Drive file.
func put(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("put", flag.ContinueOnError)
	fs.SetOutput(stderr)
	watch := fs.String("watch", "default", "watch ID tagged on the file")
	rootDir := fs.String("root", "", "directory the file's rel_path is relative to (default: the file's basename)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: syncd put [--watch ID] [--root DIR] <file>")
		return 2
	}
	file := fs.Arg(0)
	rel, err := relPathFor(file, *rootDir)
	if err != nil {
		fmt.Fprintf(stderr, "syncd: %v\n", err)
		return 2
	}

	client, err := drive.LoadClient()
	if err != nil {
		fmt.Fprintf(stderr, "syncd: %v\n", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	info, err := drive.Upload(ctx, client, drive.KeyringStore{}, drive.FileStore{}, file, *watch, rel)
	switch {
	case errors.Is(err, drive.ErrNotSignedIn):
		fmt.Fprintln(stderr, "syncd: not signed in; run `syncd login`")
		return 1
	case errors.Is(err, drive.ErrSessionExpired):
		fmt.Fprintln(stderr, "syncd: session expired; run `syncd login` to sign in again")
		return 1
	case err != nil:
		fmt.Fprintf(stderr, "syncd: put failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Uploaded %s\n", info.RelPath)
	fmt.Fprintf(stdout, "File ID: %s\n", info.ID)
	if info.WebLink != "" {
		fmt.Fprintln(stdout, info.WebLink)
	}
	return 0
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: syncd <command>")
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Fprintln(stdout, Version)
		return 0
	case "login":
		return login(stdout, stderr)
	case "whoami":
		return whoami(stdout, stderr)
	case "logout":
		return logout(stdout, stderr)
	case "root":
		return root(stdout, stderr)
	case "put":
		return put(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "syncd: unknown command %q\n", args[0])
		return 2
	}
}
