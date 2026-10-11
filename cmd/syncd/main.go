package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strconv"
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
	code := guarded(os.Stderr, func() int { return run(os.Args[1:], os.Stdout, os.Stderr) })
	closeLog()
	os.Exit(code)
}

// guarded runs fn with a last-resort recover: a panic that reaches the main
// goroutine is logged (with its stack) and the command ends with exit 1.
// Goroutines started by the commands recover their own panics (see
// internal/sync), so this is only for bugs in the command code itself.
func guarded(stderr io.Writer, fn func() int) (code int) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("panic in main goroutine", "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
			fmt.Fprintf(stderr, "syncd: internal error: %v (see the log)\n", r)
			code = 1
		}
	}()
	return fn()
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
	local := fs.Bool("local", false, "show only what the state database knows (no sign-in, no Drive call)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: syncd stat [--local] <file-id> | syncd stat [--local] --path <rel_path>")
		return 2
	}
	if *local {
		row, err := localRow(fs.Arg(0), *byPath)
		if errors.Is(err, store.ErrNotFound) {
			fmt.Fprintf(stderr, "syncd: no tracked file %q\n", fs.Arg(0))
			return 1
		}
		if err != nil {
			fmt.Fprintf(stderr, "syncd: stat failed: %v\n", err)
			return 1
		}
		printLocal(stdout, row)
		return 0
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
	// What the state database knows about the same file, if anything.
	if row, err := localRow(info.ID, false); err == nil {
		printLocal(stdout, row)
	} else if !errors.Is(err, store.ErrNotFound) {
		fmt.Fprintf(stderr, "syncd: local details unavailable: %v\n", err)
	}
	return 0
}

// localRow finds the tracked file in the state database, by rel_path or by
// Drive file ID. store.ErrNotFound means it is not tracked.
func localRow(key string, byPath bool) (store.File, error) {
	repo, err := store.OpenRepo(dbPath())
	if err != nil {
		return store.File{}, err
	}
	defer repo.Close()
	if byPath {
		return repo.Get(context.Background(), key)
	}
	return repo.GetByDriveID(context.Background(), key)
}

// printLocal prints the database row of a tracked file. The inode is the
// inode on Unix and the file ID on Windows; unknown when it was never recorded.
func printLocal(w io.Writer, f store.File) {
	inode := "unknown"
	if f.Inode != 0 {
		inode = strconv.FormatUint(f.Inode, 10)
	}
	fmt.Fprintln(w, "Local:")
	fmt.Fprintf(w, "  Inode:      %s\n", inode)
	fmt.Fprintf(w, "  Size:       %d\n", f.Size)
	fmt.Fprintf(w, "  MTime:      %s\n", time.Unix(0, f.MTime).UTC().Format(time.RFC3339Nano))
	fmt.Fprintf(w, "  Synced MD5: %s\n", f.SyncedMD5)
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
	diff := fs.Bool("diff", false, "compare with the state database: list new, changed and missing files")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !*dryRun || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: syncd scan --dry-run [--diff] [--root DIR]")
		return 2
	}
	root, err := resolveRoot(*rootDir)
	if err != nil {
		fmt.Fprintf(stderr, "syncd: scan failed: %v\n", err)
		return 1
	}
	if *diff {
		return scanDiff(root, stdout, stderr)
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

// lockDatabase takes the single-instance lock that run and sync need, so two
// of them never work on the same database at once. On failure it has already
// printed why and the caller exits 1.
func lockDatabase(cmd string, stderr io.Writer) (*store.FileLock, bool) {
	l, err := store.Lock(dbPath())
	switch {
	case errors.Is(err, store.ErrLocked):
		fmt.Fprintln(stderr, "syncd: another syncd is using this database (run or sync); stop it first")
		return nil, false
	case err != nil:
		fmt.Fprintf(stderr, "syncd: %s failed: %v\n", cmd, err)
		return nil, false
	}
	return l, true
}

// reportEvent prints one upload outcome: uploaded and updated lines on stdout,
// failures on stderr.
func reportEvent(stdout, stderr io.Writer) func(burrowsync.Event) {
	return func(e burrowsync.Event) {
		if e.Err != nil {
			fmt.Fprintf(stderr, "failed %s: %v\n", e.RelPath, e.Err)
			return
		}
		if e.Trashed {
			fmt.Fprintf(stdout, "trashed %s\n", e.RelPath)
			return
		}
		if e.MovedFrom != "" {
			fmt.Fprintf(stdout, "moved %s -> %s\n", e.MovedFrom, e.RelPath)
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
	lock, ok := lockDatabase("run", stderr)
	if !ok {
		return 1
	}
	defer lock.Unlock()
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
	if err := loadSharedConfig(ctx, uploader, stderr); err != nil {
		return reportDriveErr(stderr, "run", err)
	}
	err = burrowsync.Run(ctx, root, repo, uploader.Upload, reportEvent(stdout, stderr), burrowsync.RunOptions{
		Workers: *workers,
		Settle:  *settle,
		Move:    uploader.Move,
		Trash:   uploader.Trash,
		MoveDir: uploader.MoveFolder,
		OnStart: func(dirs int) {
			fmt.Fprintf(stderr, "watching %d directories under %s (Ctrl+C to stop)\n", dirs, root)
		},
		OnWatchError: func(err error) { fmt.Fprintf(stderr, "syncd: watch error: %v\n", err) },
		OnReconcile:  func(p burrowsync.Plan) { reportReconcile(stderr, p) },
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

// scanDiff prints what `syncd run` would queue on its next start: files that
// are new or changed since the last sync, and files that are gone. It reads
// the state database and changes nothing.
func scanDiff(root string, stdout, stderr io.Writer) int {
	repo, err := store.OpenRepo(dbPath())
	if err != nil {
		fmt.Fprintf(stderr, "syncd: scan failed: %v\n", err)
		return 1
	}
	defer repo.Close()
	plan, err := burrowsync.Diff(root, repo)
	if err != nil {
		fmt.Fprintf(stderr, "syncd: scan failed: %v\n", err)
		return 1
	}
	for _, p := range plan.New {
		fmt.Fprintln(stdout, "new", p)
	}
	for _, p := range plan.Changed {
		fmt.Fprintln(stdout, "changed", p)
	}
	for _, m := range plan.Moved {
		fmt.Fprintf(stdout, "moved %s -> %s\n", m.From, m.To)
	}
	for _, p := range plan.Missing {
		fmt.Fprintln(stdout, "missing", p)
	}
	for _, e := range plan.Unreadable {
		fmt.Fprintf(stderr, "syncd: skipped: %v\n", e)
	}
	fmt.Fprintf(stderr, "%d new, %d changed, %d moved, %d unchanged, %d missing, %d ignored\n",
		len(plan.New), len(plan.Changed), len(plan.Moved), plan.Unchanged, len(plan.Missing), plan.Ignored)
	return 0
}

// reportReconcile prints the outcome of the startup scan of `syncd run`: the
// counts, then up to 10 files that are gone locally.
func reportReconcile(stderr io.Writer, p burrowsync.Plan) {
	fmt.Fprintf(stderr, "reconciled: %d new, %d changed, %d moved, %d unchanged, %d missing locally\n",
		len(p.New), len(p.Changed), len(p.Moved), p.Unchanged, len(p.Missing))
	for i, m := range p.Missing {
		if i == 10 {
			fmt.Fprintf(stderr, "  ...and %d more\n", len(p.Missing)-10)
			break
		}
		fmt.Fprintf(stderr, "  missing: %s\n", m)
	}
	for _, e := range p.Unreadable {
		fmt.Fprintf(stderr, "syncd: skipped: %v\n", e)
	}
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
	lock, ok := lockDatabase("sync", stderr)
	if !ok {
		return 1
	}
	defer lock.Unlock()
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
	// A bad root fails before any Drive call, as it did before the shared config.
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		if err == nil {
			err = fmt.Errorf("%s is not a directory", root)
		}
		fmt.Fprintf(stderr, "syncd: sync failed: scan root: %v\n", err)
		return 1
	}
	if err := loadSharedConfig(ctx, uploader, stderr); err != nil {
		return reportDriveErr(stderr, "sync", err)
	}
	sum, err := burrowsync.SyncWorkers(ctx, root, repo, uploader.Upload, reportEvent(stdout, stderr), *workers, burrowsync.WithMove(uploader.Move), burrowsync.WithTrash(uploader.Trash), burrowsync.WithMoveDir(uploader.MoveFolder))
	for _, e := range sum.Unreadable {
		fmt.Fprintf(stderr, "syncd: skipped: %v\n", e)
	}
	fmt.Fprintf(stderr, "%d uploaded, %d updated, %d moved, %d trashed, %d already synced, %d failed, %d ignored\n", sum.Uploaded, sum.Updated, sum.Moved, sum.Trashed, sum.Synced, sum.Failed, sum.Ignored)
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

// loadSharedConfig reads config.json from Drive and applies its ignore rules
// to every scan and watch of this process. It fails closed: if the settings
// cannot be read, the run must not upload files they may exclude.
func loadSharedConfig(ctx context.Context, u *drive.Uploader, stderr io.Writer) error {
	cfg, err := u.ReadConfig(ctx)
	if err != nil {
		return err
	}
	burrowsync.SetRemoteIgnores(cfg.Ignore)
	if n := len(cfg.Ignore); n > 0 {
		fmt.Fprintf(stderr, "using %d ignore rules from the shared config.json\n", n)
	}
	return nil
}

// editIgnore returns cfg with pattern added (op "add") or removed (op "rm").
// changed is false when there was nothing to do.
func editIgnore(cfg drive.Config, op, pattern string) (out drive.Config, changed bool, err error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" || strings.HasPrefix(pattern, "#") || strings.ContainsAny(pattern, "\r\n") {
		return cfg, false, fmt.Errorf("invalid ignore rule %q", pattern)
	}
	i := slices.Index(cfg.Ignore, pattern)
	out = cfg
	out.Ignore = slices.Clone(cfg.Ignore)
	switch op {
	case "add":
		if i >= 0 {
			return out, false, nil
		}
		out.Ignore = append(out.Ignore, pattern)
	case "rm":
		if i < 0 {
			return out, false, fmt.Errorf("%q is not in the shared ignore rules", pattern)
		}
		out.Ignore = slices.Delete(out.Ignore, i, i+1)
	}
	return out, true, nil
}

// configCmd shows and edits the settings file shared through Drive
// (config.json under MySync). The next `sync` or `run` of any machine applies
// the ignore rules.
func configCmd(args []string, stdout, stderr io.Writer) int {
	const usage = "usage: syncd config show | syncd config ignore add|rm <pattern>"
	switch {
	case len(args) == 1 && args[0] == "show":
	case len(args) == 3 && args[0] == "ignore" && (args[1] == "add" || args[1] == "rm"):
	default:
		fmt.Fprintln(stderr, usage)
		return 2
	}
	client, err := drive.LoadClient()
	if err != nil {
		fmt.Fprintf(stderr, "syncd: %v\n", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	u := drive.NewUploader(client, drive.KeyringStore{}, drive.FileStore{}, nil)
	cfg, err := u.ReadConfig(ctx)
	if err != nil {
		return reportDriveErr(stderr, "config", err)
	}
	if args[0] == "ignore" {
		next, changed, err := editIgnore(cfg, args[1], args[2])
		if err != nil {
			fmt.Fprintf(stderr, "syncd: config: %v\n", err)
			return 1
		}
		if !changed {
			fmt.Fprintf(stderr, "syncd: %q is already in the shared ignore rules\n", args[2])
			return 0
		}
		if err := u.WriteConfig(ctx, next); err != nil {
			return reportDriveErr(stderr, "config", err)
		}
		cfg = next
	}
	b, _ := json.MarshalIndent(cfg, "", "  ")
	fmt.Fprintln(stdout, string(b))
	return 0
}

// formatRemote is one `remote ls` line: rel_path, size, MD5, Drive file ID.
// A file without a rel_path tag is marked, since it was not put there by syncd.
func formatRemote(f drive.RemoteFile) string {
	line := fmt.Sprintf("%s  %d  %s  %s", f.RelPath, f.Size, f.MD5, f.ID)
	if !f.Tagged {
		line += "  (no rel_path tag)"
	}
	return line
}

// remoteCmd works with what Drive holds under MySync: `ls` lists it, `get`
// downloads one file by its rel_path.
func remoteCmd(args []string, stdout, stderr io.Writer) int {
	const usage = "usage: syncd remote ls | syncd remote get <rel_path> [<local path>]"
	switch {
	case len(args) == 1 && args[0] == "ls":
	case (len(args) == 2 || len(args) == 3) && args[0] == "get":
	default:
		fmt.Fprintln(stderr, usage)
		return 2
	}
	client, err := drive.LoadClient()
	if err != nil {
		fmt.Fprintf(stderr, "syncd: %v\n", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	u := drive.NewUploader(client, drive.KeyringStore{}, drive.FileStore{}, nil)
	if args[0] == "get" {
		return remoteGet(ctx, client, u, args[1:], stdout, stderr)
	}
	files, err := u.ListTree(ctx)
	if err != nil {
		return reportDriveErr(stderr, "remote ls", err)
	}
	for _, f := range files {
		fmt.Fprintln(stdout, formatRemote(f))
	}
	fmt.Fprintf(stderr, "%d files\n", len(files))
	return 0
}

// remoteGet downloads the Drive file tagged rel to dest (default: its base
// name in the current directory), verified against Drive's MD5.
func remoteGet(ctx context.Context, client drive.Client, u *drive.Uploader, args []string, stdout, stderr io.Writer) int {
	rel, dest := args[0], path.Base(args[0])
	if len(args) == 2 {
		dest = args[1]
	}
	info, err := drive.FindByTags(ctx, client, drive.KeyringStore{}, rel)
	if errors.Is(err, drive.ErrFileNotFound) {
		fmt.Fprintf(stderr, "syncd: no file %q in Drive\n", rel)
		return 1
	}
	if err != nil {
		return reportDriveErr(stderr, "remote get", err)
	}
	if err := u.Download(ctx, info.ID, info.MD5, dest); err != nil {
		return reportDriveErr(stderr, "remote get", err)
	}
	fmt.Fprintf(stdout, "Downloaded %s to %s (%d bytes, md5 %s)\n", rel, dest, info.Size, info.MD5)
	return 0
}

// reportRestore prints one restore outcome: progress on stdout, failures on stderr.
func reportRestore(stdout, stderr io.Writer) func(burrowsync.RestoreEvent) {
	return func(e burrowsync.RestoreEvent) {
		switch {
		case e.Err != nil:
			fmt.Fprintf(stderr, "failed %s: %v\n", e.RelPath, e.Err)
		case e.Action == burrowsync.RestoreConflict:
			fmt.Fprintf(stdout, "conflict %s (your version kept as %s)\n", e.RelPath, e.ConflictCopy)
		default:
			fmt.Fprintf(stdout, "%s %s\n", e.Action, e.RelPath)
		}
	}
}

// restoreCmd fills the sync root from Drive on a new machine: files that are
// missing are downloaded, files that already match are only recorded, and
// files with other content are kept as conflicted copies. Run `syncd run`
// afterwards.
func restoreCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	fs.SetOutput(stderr)
	rootDir := fs.String("root", "", "sync root to fill from Drive (default ~/MySync; created if missing)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: syncd restore [--root DIR]")
		return 2
	}
	root, err := resolveRoot(*rootDir)
	if err != nil {
		fmt.Fprintf(stderr, "syncd: restore failed: %v\n", err)
		return 1
	}
	client, err := drive.LoadClient()
	if err != nil {
		fmt.Fprintf(stderr, "syncd: %v\n", err)
		return 1
	}
	lock, ok := lockDatabase("restore", stderr)
	if !ok {
		return 1
	}
	defer lock.Unlock()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	repo, err := store.OpenRepo(dbPath())
	if err != nil {
		fmt.Fprintf(stderr, "syncd: restore failed: %v\n", err)
		return 1
	}
	defer repo.Close()

	uploader := drive.NewUploader(client, drive.KeyringStore{}, drive.FileStore{}, repo)
	if err := loadSharedConfig(ctx, uploader, stderr); err != nil {
		return reportDriveErr(stderr, "restore", err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		fmt.Fprintf(stderr, "syncd: restore failed: %v\n", err)
		return 1
	}
	remote, err := uploader.ListTree(ctx)
	if err != nil {
		return reportDriveErr(stderr, "restore", err)
	}
	host, _ := os.Hostname()
	sum, err := burrowsync.Restore(ctx, root, repo, remote, uploader.Download, reportRestore(stdout, stderr), burrowsync.RestoreOptions{Host: host})
	fmt.Fprintf(stderr, "%d downloaded, %d already there, %d kept as conflicted copies, %d failed\n", sum.Downloaded, sum.Matched, sum.Conflicts, sum.Failed)
	switch {
	case errors.Is(err, context.Canceled):
		fmt.Fprintln(stderr, "syncd: restore interrupted; run it again to continue")
		return 1
	case err != nil:
		return reportDriveErr(stderr, "restore", err)
	case sum.Failed > 0:
		return 1
	}
	fmt.Fprintln(stderr, "restored; run `syncd run` to keep syncing")
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
	case "config":
		return configCmd(args[1:], stdout, stderr)
	case "remote":
		return remoteCmd(args[1:], stdout, stderr)
	case "restore":
		return restoreCmd(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "syncd: unknown command %q\n", args[0])
		return 2
	}
}
