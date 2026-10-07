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
	"github.com/amitavroy/burrow/internal/store"
	burrowsync "github.com/amitavroy/burrow/internal/sync"
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
func persistToken(store drive.KeyringStore, tok *oauth2.Token) error {
	if tok.RefreshToken == "" {
		return errors.New("Google returned no refresh token; try signing in again")
	}
	return store.Save(tok.RefreshToken)
}

// reportDriveErr prints the user-facing message for a failed command that
// talks to Drive and returns the exit code (always 1). A missing or expired
// sign-in gets a hint to run login; anything else names the command.
func reportDriveErr(stderr io.Writer, cmd string, err error) int {
	switch {
	case errors.Is(err, drive.ErrNotSignedIn):
		fmt.Fprintln(stderr, "syncd: not signed in; run `syncd login`")
	case errors.Is(err, drive.ErrSessionExpired):
		fmt.Fprintln(stderr, "syncd: session expired; run `syncd login` to sign in again")
	default:
		fmt.Fprintf(stderr, "syncd: %s failed: %v\n", cmd, err)
	}
	return 1
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
	if err != nil {
		return reportDriveErr(stderr, "whoami", err)
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
	if err != nil {
		return reportDriveErr(stderr, "root", err)
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

// put uploads one file into MySync, tagged with its relative path. Putting
// the same rel_path again updates the existing Drive file.
func put(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("put", flag.ContinueOnError)
	fs.SetOutput(stderr)
	rootDir := fs.String("root", "", "directory the file's rel_path is relative to (default: the file's basename)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: syncd put [--root DIR] <file>")
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

	info, err := drive.Upload(ctx, client, drive.KeyringStore{}, drive.FileStore{}, file, rel)
	if err != nil {
		return reportDriveErr(stderr, "put", err)
	}
	fmt.Fprintf(stdout, "Uploaded %s\n", info.RelPath)
	fmt.Fprintf(stdout, "File ID: %s\n", info.ID)
	if info.WebLink != "" {
		fmt.Fprintln(stdout, info.WebLink)
	}
	return 0
}

// stat prints what Drive holds for one file, found by Drive ID or, with
// --path, by its rel_path tag.
func stat(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("stat", flag.ContinueOnError)
	fs.SetOutput(stderr)
	byPath := fs.Bool("path", false, "look the file up by its rel_path instead of its Drive ID")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: syncd stat <file-id> | syncd stat --path <rel_path>")
		return 2
	}

	client, err := drive.LoadClient()
	if err != nil {
		fmt.Fprintf(stderr, "syncd: %v\n", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var info drive.FileInfo
	if *byPath {
		info, err = drive.FindByTags(ctx, client, drive.KeyringStore{}, drive.FileStore{}, fs.Arg(0))
	} else {
		info, err = drive.Stat(ctx, client, drive.KeyringStore{}, fs.Arg(0))
	}
	if err != nil {
		return reportDriveErr(stderr, "stat", err)
	}
	fmt.Fprintf(stdout, "ID:       %s\n", info.ID)
	fmt.Fprintf(stdout, "Name:     %s\n", info.Name)
	fmt.Fprintf(stdout, "Size:     %d\n", info.Size)
	fmt.Fprintf(stdout, "MD5:      %s\n", info.MD5)
	fmt.Fprintf(stdout, "Revision: %s\n", info.RevisionID)
	fmt.Fprintf(stdout, "rel_path: %s\n", info.RelPath)
	return 0
}

// dbPath locates the state database. Tests replace it so they never touch the
// real data dir.
var dbPath = store.DefaultPath

// db groups the state database commands. `path` only prints the location;
// `status` opens (creating and migrating if needed) and reports on the database.
func db(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 {
		switch args[0] {
		case "path":
			fmt.Fprintln(stdout, dbPath())
			return 0
		case "status":
			return dbStatus(stdout, stderr)
		}
	}
	fmt.Fprintln(stderr, "usage: syncd db path | syncd db status")
	return 2
}

func dbStatus(stdout, stderr io.Writer) int {
	path := dbPath()
	conn, err := store.Open(path)
	if err != nil {
		fmt.Fprintf(stderr, "syncd: db status failed: %v\n", err)
		return 1
	}
	defer conn.Close()

	info, err := store.Status(conn)
	if err != nil {
		fmt.Fprintf(stderr, "syncd: db status failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Path:    %s\n", path)
	fmt.Fprintf(stdout, "Version: %d\n", info.Version)
	fmt.Fprintf(stdout, "Tables:  %s\n", strings.Join(info.Tables, ", "))
	return 0
}

// homeDir locates the user's home directory. Tests replace it.
var homeDir = os.UserHomeDir

// scan lists the files under the sync root that a sync would upload. It only
// reads; --dry-run is required so the command is never mistaken for a sync.
func scan(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dryRun := fs.Bool("dry-run", false, "list the files that would upload, without uploading")
	rootDir := fs.String("root", "", "sync root to scan (default ~/MySync)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !*dryRun || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: syncd scan --dry-run [--root DIR]")
		return 2
	}
	root := *rootDir
	if root == "" {
		home, err := homeDir()
		if err != nil {
			fmt.Fprintf(stderr, "syncd: scan failed: %v\n", err)
			return 1
		}
		root = filepath.Join(home, "MySync")
	}

	res, err := burrowsync.Scan(root)
	if err != nil {
		fmt.Fprintf(stderr, "syncd: scan failed: %v\n", err)
		return 1
	}
	for _, e := range res.Entries {
		fmt.Fprintln(stdout, e.RelPath)
	}
	for _, e := range res.Errors {
		fmt.Fprintf(stderr, "syncd: skipped: %v\n", e)
	}
	fmt.Fprintf(stderr, "%d files, %d ignored\n", len(res.Entries), res.Ignored)
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
	case "stat":
		return stat(args[1:], stdout, stderr)
	case "db":
		return db(args[1:], stdout, stderr)
	case "scan":
		return scan(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "syncd: unknown command %q\n", args[0])
		return 2
	}
}
