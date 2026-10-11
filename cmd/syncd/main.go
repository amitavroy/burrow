package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

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
	closeLog, err := setupLogger(os.Getenv("SYNC_LOG"), os.Stderr, store.LogPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "syncd: %v\n", err)
		os.Exit(2)
	}
	code := run(os.Args[1:], os.Stdout, os.Stderr)
	closeLog()
	os.Exit(code)
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

	// The state database only caches folder IDs, so put carries on without it.
	repo, err := store.OpenRepo(dbPath())
	if err != nil {
		fmt.Fprintf(stderr, "syncd: warning: state database unavailable, continuing without the folder cache: %v\n", err)
		repo = nil
	} else {
		defer repo.Close()
	}

	info, err := drive.Upload(ctx, client, drive.KeyringStore{}, drive.FileStore{}, repo, file, rel)
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
		info, err = drive.FindByTags(ctx, client, drive.KeyringStore{}, fs.Arg(0))
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
	root, err := resolveRoot(*rootDir)
	if err != nil {
		fmt.Fprintf(stderr, "syncd: scan failed: %v\n", err)
		return 1
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

// resolveRoot returns dir, or ~/MySync when dir is empty.
func resolveRoot(dir string) (string, error) {
	if dir != "" {
		return dir, nil
	}
	home, err := homeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "MySync"), nil
}

// reportEvent prints one upload outcome: uploaded and updated lines on stdout,
// failures on stderr.
func reportEvent(stdout, stderr io.Writer) func(burrowsync.Event) {
	return func(e burrowsync.Event) {
		if e.Err != nil {
			fmt.Fprintf(stderr, "failed %s: %v\n", e.RelPath, e.Err)
			return
		}
		verb := "uploaded"
		if e.Updated {
			verb = "updated"
		}
		fmt.Fprintf(stdout, "%s %s\n", verb, e.RelPath)
	}
}

// runCmd is the foreground daemon: it watches the sync root and uploads what
// changes until Ctrl+C. Jobs left by an earlier run are processed first. A
// lost sign-in stops it with exit 1.
func runCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	rootDir := fs.String("root", "", "sync root to watch and upload (default ~/MySync)")
	workers := fs.Int("workers", 3, "files to upload at once (1 to 16)")
	settle := fs.Duration("settle", time.Second, "how long a file must stay unchanged before it is uploaded")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 || *workers < 1 || *workers > 16 || *settle <= 0 {
		fmt.Fprintln(stderr, "usage: syncd run [--root DIR] [--workers N] [--settle DURATION]  (N is 1 to 16)")
		return 2
	}
	root, err := resolveRoot(*rootDir)
	if err != nil {
		fmt.Fprintf(stderr, "syncd: run failed: %v\n", err)
		return 1
	}
	client, err := drive.LoadClient()
	if err != nil {
		fmt.Fprintf(stderr, "syncd: %v\n", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	repo, err := store.OpenRepo(dbPath())
	if err != nil {
		fmt.Fprintf(stderr, "syncd: run failed: %v\n", err)
		return 1
	}
	defer repo.Close()

	if n, err := repo.CountJobs(ctx); err == nil && n > 0 {
		fmt.Fprintf(stderr, "resuming %d queued jobs\n", n)
	}
	uploader := drive.NewUploader(client, drive.KeyringStore{}, drive.FileStore{}, repo)
	err = burrowsync.Run(ctx, root, repo, uploader.Upload, reportEvent(stdout, stderr), burrowsync.RunOptions{
		Workers: *workers,
		Settle:  *settle,
		OnStart: func(dirs int) {
			fmt.Fprintf(stderr, "watching %d directories under %s (Ctrl+C to stop)\n", dirs, root)
		},
		OnWatchError: func(err error) { fmt.Fprintf(stderr, "syncd: watch error: %v\n", err) },
	})
	if err != nil {
		if errors.Is(err, drive.ErrNotSignedIn) || errors.Is(err, drive.ErrSessionExpired) {
			return reportDriveErr(stderr, "run", err)
		}
		fmt.Fprintf(stderr, "syncd: run failed: %v\n", err)
		return 1
	}
	fmt.Fprintln(stderr, "syncd: stopped; unfinished uploads stay queued")
	return 0
}

// syncCmd uploads every file under the sync root to Drive, several at a time.
// Files already recorded in the state database are skipped. Per-file failures
// are reported and make the exit code 1; a lost sign-in or Ctrl+C stops the run.
func syncCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	fs.SetOutput(stderr)
	rootDir := fs.String("root", "", "sync root to upload (default ~/MySync)")
	workers := fs.Int("workers", 3, "files to upload at once (1 to 16)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 || *workers < 1 || *workers > 16 {
		fmt.Fprintln(stderr, "usage: syncd sync [--root DIR] [--workers N]  (N is 1 to 16)")
		return 2
	}
	root, err := resolveRoot(*rootDir)
	if err != nil {
		fmt.Fprintf(stderr, "syncd: sync failed: %v\n", err)
		return 1
	}
	client, err := drive.LoadClient()
	if err != nil {
		fmt.Fprintf(stderr, "syncd: %v\n", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// Unlike put, sync needs the state database: it records every file's Drive ID.
	repo, err := store.OpenRepo(dbPath())
	if err != nil {
		fmt.Fprintf(stderr, "syncd: sync failed: %v\n", err)
		return 1
	}
	defer repo.Close()

	if n, err := repo.CountJobs(ctx); err == nil && n > 0 {
		fmt.Fprintf(stderr, "resuming %d queued jobs\n", n)
	}
	uploader := drive.NewUploader(client, drive.KeyringStore{}, drive.FileStore{}, repo)
	sum, err := burrowsync.SyncWorkers(ctx, root, repo, uploader.Upload, reportEvent(stdout, stderr), *workers)
	for _, e := range sum.Unreadable {
		fmt.Fprintf(stderr, "syncd: skipped: %v\n", e)
	}
	fmt.Fprintf(stderr, "%d uploaded, %d updated, %d already synced, %d failed, %d ignored\n", sum.Uploaded, sum.Updated, sum.Synced, sum.Failed, sum.Ignored)
	if sum.Failed > 0 || len(sum.Unreadable) > 0 {
		fmt.Fprintln(stderr, "see 'syncd errors' for details")
	}
	switch {
	case errors.Is(err, context.Canceled):
		fmt.Fprintln(stderr, "syncd: sync interrupted")
		return 1
	case err != nil:
		return reportDriveErr(stderr, "sync", err)
	case sum.Failed > 0:
		return 1
	}
	return 0
}

// history lists the revisions recorded for one rel_path, newest first. It
// reads the state database only; no Drive call or sign-in is needed.
func history(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("history", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: syncd history <rel_path>")
		return 2
	}
	rel := fs.Arg(0)
	repo, err := store.OpenRepo(dbPath())
	if err != nil {
		fmt.Fprintf(stderr, "syncd: history failed: %v\n", err)
		return 1
	}
	defer repo.Close()
	revs, err := repo.Revisions(context.Background(), rel)
	if err != nil {
		fmt.Fprintf(stderr, "syncd: history failed: %v\n", err)
		return 1
	}
	if len(revs) == 0 {
		fmt.Fprintf(stderr, "syncd: no revisions recorded for %q\n", rel)
		return 1
	}
	for _, r := range revs {
		fmt.Fprintf(stdout, "%s  %s  %s  %d  %s\n", time.Unix(0, r.Time).UTC().Format(time.RFC3339), r.RevisionID, r.MD5, r.Size, r.Source)
	}
	return 0
}

// formatWatchEvent is one line of `syncd watch` output: the operation, then the
// path, with a trailing slash on a directory.
func formatWatchEvent(e burrowsync.WatchEvent) string {
	p := e.RelPath
	if e.IsDir {
		p += "/"
	}
	return string(e.Op) + " " + p
}

// watchCmd prints the files that have settled under the sync root (or, with
// --raw, every event) until Ctrl+C. It is a development aid: it needs no database and no sign-in.
func watchCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	rootDir := fs.String("root", "", "sync root to watch (default ~/MySync)")
	settle := fs.Duration("settle", time.Second, "how long a file must stay unchanged before it is ready")
	raw := fs.Bool("raw", false, "print every raw event instead of debounced ready lines")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 || *settle <= 0 {
		fmt.Fprintln(stderr, "usage: syncd watch [--root DIR] [--settle DURATION] [--raw]")
		return 2
	}
	root, err := resolveRoot(*rootDir)
	if err != nil {
		fmt.Fprintf(stderr, "syncd: watch failed: %v\n", err)
		return 1
	}
	w, err := burrowsync.NewWatcher(root)
	if err != nil {
		fmt.Fprintf(stderr, "syncd: watch failed: %v\n", err)
		return 1
	}
	defer w.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	slog.Info("watching", "root", root, "directories", w.Watching())
	fmt.Fprintf(stderr, "watching %d directories under %s (Ctrl+C to stop)\n", w.Watching(), root)
	if *raw {
		for {
			select {
			case <-ctx.Done():
				return 0
			case e, ok := <-w.Events():
				if !ok {
					return 0
				}
				fmt.Fprintln(stdout, formatWatchEvent(e))
			case err, ok := <-w.Errors():
				if ok {
					fmt.Fprintf(stderr, "syncd: watch error: %v\n", err)
				}
			}
		}
	}
	d := burrowsync.NewDebouncer(w.Events(), root, *settle)
	defer d.Close()
	for {
		select {
		case <-ctx.Done():
			return 0
		case p, ok := <-d.Ready():
			if !ok {
				return 0
			}
			fmt.Fprintf(stdout, "ready %s\n", p)
		case err, ok := <-w.Errors():
			if ok {
				fmt.Fprintf(stderr, "syncd: watch error: %v\n", err)
			}
		}
	}
}

// errorsCmd lists the files that are failing to sync, newest first. It reads
// the state database only; no Drive call or sign-in is needed.
func errorsCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("errors", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: syncd errors")
		return 2
	}
	repo, err := store.OpenRepo(dbPath())
	if err != nil {
		fmt.Fprintf(stderr, "syncd: errors failed: %v\n", err)
		return 1
	}
	defer repo.Close()
	errs, err := repo.ListErrors(context.Background())
	if err != nil {
		fmt.Fprintf(stderr, "syncd: errors failed: %v\n", err)
		return 1
	}
	if len(errs) == 0 {
		fmt.Fprintln(stdout, "no errors")
		return 0
	}
	for _, e := range errs {
		fmt.Fprintf(stdout, "%s  %s  %s, will retry\n", time.Unix(0, e.LastSeen).UTC().Format(time.RFC3339), e.RelPath, e.Message)
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
	case "stat":
		return stat(args[1:], stdout, stderr)
	case "db":
		return db(args[1:], stdout, stderr)
	case "scan":
		return scan(args[1:], stdout, stderr)
	case "sync":
		return syncCmd(args[1:], stdout, stderr)
	case "history":
		return history(args[1:], stdout, stderr)
	case "watch":
		return watchCmd(args[1:], stdout, stderr)
	case "run":
		return runCmd(args[1:], stdout, stderr)
	case "errors":
		return errorsCmd(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "syncd: unknown command %q\n", args[0])
		return 2
	}
}
