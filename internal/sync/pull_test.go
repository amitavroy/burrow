package sync

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/amitavroy/burrow/internal/drive"
	"github.com/amitavroy/burrow/internal/store"
)

func TestPull(t *testing.T) {
	ctx := context.Background()
	root, repo := t.TempDir(), openRepo(t)
	for rel, c := range map[string]string{"drive-edit.txt": "a", "local-edit.txt": "a", "both.txt": "a", "echo.txt": "a"} {
		write(t, filepath.Join(root, rel), c)
		if err := repo.Upsert(ctx, store.File{RelPath: rel, DriveFileID: "id-" + rel, LocalMD5: md5Hex("a"), SyncedMD5: md5Hex("a"), BaseMD5: md5Hex("a"), BaseRevisionID: "r1"}); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(root, "local-edit.txt"), "mine")
	write(t, filepath.Join(root, "both.txt"), "mine")

	fr := newFakeRemote()
	ch := func(rel, content, rev string) drive.Change {
		fr.content["id-"+rel] = content
		return drive.Change{FileID: "id-" + rel, RelPath: rel, Tagged: true, MD5: md5Hex(content), RevisionID: rev}
	}
	changes := []drive.Change{
		ch("drive-edit.txt", "b", "r2"),
		ch("local-edit.txt", "a", "r1"), // unchanged on Drive
		ch("both.txt", "theirs", "r2"),
		ch("echo.txt", "a", "r1"), // our own revision
		ch("new.txt", "fresh", "r1"),
	}
	if _, err := Pull(ctx, root, repo, changes, fr.download, &expected{}, pullOpts()); err != nil {
		t.Fatal(err)
	}

	for rel, want := range map[string]string{"drive-edit.txt": "b", "local-edit.txt": "mine", "both.txt": "theirs", "new.txt": "fresh"} {
		if got := readFile(t, filepath.Join(root, rel)); got != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
	if len(fr.got) != 3 {
		t.Errorf("downloaded %v, want drive-edit.txt, both.txt and new.txt only", fr.got)
	}
	// both edited: the local version is kept as a conflicted copy and queued
	cp := "both (conflicted copy, laptop, 2026-10-12).txt"
	if got := readFile(t, filepath.Join(root, cp)); got != "mine" {
		t.Errorf("conflicted copy = %q, want %q", got, "mine")
	}
	if n, _ := repo.CountJobs(ctx); n != 1 {
		t.Errorf("%d jobs queued, want 1 (the copy)", n)
	}
	row, err := repo.Get(ctx, "drive-edit.txt")
	if err != nil || row.BaseMD5 != md5Hex("b") || row.BaseRevisionID != "r2" || row.SyncedMD5 != md5Hex("b") {
		t.Errorf("row after pull = %+v, %v", row, err)
	}
	if _, err := repo.Get(ctx, "new.txt"); err != nil {
		t.Errorf("new.txt has no row: %v", err)
	}
}

// A download is not uploaded back: the job the watcher queues for the renamed
// file is dropped while its MD5 is the expected one, even before the row is written.
func TestPullEchoNotUploaded(t *testing.T) {
	ctx := context.Background()
	root, repo := t.TempDir(), openRepo(t)
	write(t, filepath.Join(root, "f.txt"), "a")
	if err := repo.Upsert(ctx, store.File{RelPath: "f.txt", DriveFileID: "id", LocalMD5: md5Hex("a"), SyncedMD5: md5Hex("a"), BaseMD5: md5Hex("a"), BaseRevisionID: "r1"}); err != nil {
		t.Fatal(err)
	}
	exp := &expected{}
	run := &runState{expect: exp}
	upload := func(context.Context, string, string) (drive.FileInfo, error) {
		t.Error("uploaded a downloaded file")
		return drive.FileInfo{}, nil
	}
	fr := newFakeRemote()
	fr.content["id"] = "b"
	download := func(ctx context.Context, id, md5, dst string) error {
		if err := fr.download(ctx, id, md5, dst); err != nil {
			return err
		}
		st, _ := os.Lstat(dst) // the watcher's event, before Pull writes the row
		if _, fatal := process(ctx, slog.Default(), root, repo, upload, run, Entry{RelPath: "f.txt", Size: st.Size(), MTime: st.ModTime().UnixNano()}); fatal != nil {
			t.Error(fatal)
		}
		return nil
	}
	c := drive.Change{FileID: "id", RelPath: "f.txt", Tagged: true, MD5: md5Hex("b"), RevisionID: "r2"}
	if _, err := Pull(ctx, root, repo, []drive.Change{c}, download, exp, pullOpts()); err != nil {
		t.Fatal(err)
	}
	if _, ok := exp.get("f.txt"); ok {
		t.Error("expectation not cleared")
	}
}

func pullOpts() RestoreOptions {
	return RestoreOptions{Host: "laptop", Now: time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)}
}

// Edit vs delete: the edit wins, whichever side made it.
func TestPullEditVsDelete(t *testing.T) {
	ctx := context.Background()
	root, repo := t.TempDir(), openRepo(t)
	for _, rel := range []string{"del-local.txt", "del-drive.txt"} {
		write(t, filepath.Join(root, rel), "a")
		if err := repo.Upsert(ctx, store.File{RelPath: rel, DriveFileID: "id-" + rel, LocalMD5: md5Hex("a"), SyncedMD5: md5Hex("a"), BaseMD5: md5Hex("a"), BaseRevisionID: "r1"}); err != nil {
			t.Fatal(err)
		}
	}
	// deleted here, edited on Drive: restored from Drive
	if err := os.Remove(filepath.Join(root, "del-local.txt")); err != nil {
		t.Fatal(err)
	}
	// edited here, trashed on Drive: uploaded again as a new file
	write(t, filepath.Join(root, "del-drive.txt"), "mine")

	fr := newFakeRemote()
	fr.content["id-del-local.txt"] = "theirs"
	changes := []drive.Change{
		{FileID: "id-del-local.txt", RelPath: "del-local.txt", Tagged: true, MD5: md5Hex("theirs"), RevisionID: "r2"},
		{FileID: "id-del-drive.txt", RelPath: "del-drive.txt", Tagged: true, Trashed: true},
	}
	if _, err := Pull(ctx, root, repo, changes, fr.download, &expected{}, pullOpts()); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(root, "del-local.txt")); got != "theirs" {
		t.Errorf("del-local.txt = %q, want the Drive edit", got)
	}
	if _, err := repo.Get(ctx, "del-drive.txt"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("trashed file still has a row: %v", err)
	}
	if got := readFile(t, filepath.Join(root, "del-drive.txt")); got != "mine" {
		t.Errorf("local edit lost: %q", got)
	}
	if n, _ := repo.CountJobs(ctx); n != 1 {
		t.Errorf("%d jobs, want 1 (re-upload of del-drive.txt)", n)
	}
}

// Both machines created the same path: no base, different content, keep both.
func TestPullBothCreated(t *testing.T) {
	ctx := context.Background()
	root, repo := t.TempDir(), openRepo(t)
	write(t, filepath.Join(root, "n.txt"), "mine")
	fr := newFakeRemote()
	fr.content["id"] = "theirs"
	c := drive.Change{FileID: "id", RelPath: "n.txt", Tagged: true, MD5: md5Hex("theirs"), RevisionID: "r1"}
	if _, err := Pull(ctx, root, repo, []drive.Change{c}, fr.download, &expected{}, pullOpts()); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(root, "n.txt")); got != "theirs" {
		t.Errorf("n.txt = %q", got)
	}
	if got := readFile(t, filepath.Join(root, "n (conflicted copy, laptop, 2026-10-12).txt")); got != "mine" {
		t.Errorf("copy = %q", got)
	}
}

// A failed download of a conflicting file puts the local file back.
func TestPullConflictDownloadFails(t *testing.T) {
	ctx := context.Background()
	root, repo := t.TempDir(), openRepo(t)
	write(t, filepath.Join(root, "n.txt"), "mine")
	fr := newFakeRemote()
	fr.failID = "id"
	c := drive.Change{FileID: "id", RelPath: "n.txt", Tagged: true, MD5: md5Hex("theirs"), RevisionID: "r1"}
	if _, err := Pull(ctx, root, repo, []drive.Change{c}, fr.download, &expected{}, pullOpts()); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(root, "n.txt")); got != "mine" {
		t.Errorf("n.txt = %q, want the local file back", got)
	}
}

// Rename vs edit: renamed here and edited on Drive. While the move is not
// pushed the change is deferred (no duplicate at the old path); once the row
// follows the rename, the same change lands on the new path.
func TestPullRenameVsEdit(t *testing.T) {
	ctx := context.Background()
	root, repo := t.TempDir(), openRepo(t)
	oldP, newP := filepath.Join(root, "old.txt"), filepath.Join(root, "new.txt")
	write(t, oldP, "a")
	st, _ := os.Lstat(oldP)
	row := store.File{RelPath: "old.txt", DriveFileID: "id", Size: st.Size(), MTime: st.ModTime().UnixNano(), Inode: fileID(oldP, st),
		LocalMD5: md5Hex("a"), SyncedMD5: md5Hex("a"), BaseMD5: md5Hex("a"), BaseRevisionID: "r1"}
	if err := repo.Upsert(ctx, row); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(oldP, newP); err != nil {
		t.Fatal(err)
	}
	fr := newFakeRemote()
	fr.content["id"] = "b"
	c := drive.Change{FileID: "id", RelPath: "old.txt", Tagged: true, MD5: md5Hex("b"), RevisionID: "r2"}

	deferred, err := Pull(ctx, root, repo, []drive.Change{c}, fr.download, &expected{}, pullOpts())
	if err != nil || !deferred {
		t.Fatalf("Pull = deferred %v, %v; want deferred", deferred, err)
	}
	if _, err := os.Lstat(oldP); err == nil {
		t.Error("old path restored while the rename is pending")
	}

	if err := repo.RenameFile(ctx, "old.txt", "new.txt", store.File{RelPath: "new.txt", DriveFileID: "id", Size: row.Size, MTime: row.MTime, Inode: row.Inode,
		LocalMD5: row.LocalMD5, SyncedMD5: row.SyncedMD5, BaseMD5: row.BaseMD5, BaseRevisionID: row.BaseRevisionID}); err != nil {
		t.Fatal(err)
	}
	if deferred, err = Pull(ctx, root, repo, []drive.Change{c}, fr.download, &expected{}, pullOpts()); err != nil || deferred {
		t.Fatalf("second Pull = deferred %v, %v", deferred, err)
	}
	if got := readFile(t, newP); got != "b" {
		t.Errorf("new.txt = %q, want the Drive edit", got)
	}
}
