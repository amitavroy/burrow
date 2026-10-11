//go:build unix

package sync

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func inodeOf(t *testing.T, path string) uint64 {
	t.Helper()
	st, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Sys().(*syscall.Stat_t).Ino
}

func idOf(t *testing.T, path string) uint64 {
	t.Helper()
	st, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fileID(path, st)
}

func TestFileIDIsTheInodeAndSurvivesRenameAndEdit(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.txt")
	write(t, a, "one")
	id := idOf(t, a)
	if id == 0 || id != inodeOf(t, a) {
		t.Fatalf("fileID = %d, inode = %d", id, inodeOf(t, a))
	}

	write(t, a, "edited, longer")
	if got := idOf(t, a); got != id {
		t.Errorf("after an edit: %d, want %d", got, id)
	}
	b := filepath.Join(dir, "sub", "b.txt")
	if err := os.MkdirAll(filepath.Dir(b), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(a, b); err != nil {
		t.Fatal(err)
	}
	if got := idOf(t, b); got != id {
		t.Errorf("after a rename: %d, want %d", got, id)
	}
}

func TestFileIDChangesWhenTheFileIsReplaced(t *testing.T) {
	dir := t.TempDir()
	a, keep := filepath.Join(dir, "a.txt"), filepath.Join(dir, "keep.txt")
	write(t, a, "one")
	// Keep the old inode occupied so the filesystem cannot hand it out again.
	if err := os.Link(a, keep); err != nil {
		t.Fatal(err)
	}
	id := idOf(t, a)
	if err := os.Remove(a); err != nil {
		t.Fatal(err)
	}
	write(t, a, "one")
	if got := idOf(t, a); got == id {
		t.Errorf("a new file has the old ID %d", id)
	}
}

func TestFileIDUnknownForAnOddFileInfo(t *testing.T) {
	st, _ := os.Stat(t.TempDir())
	if fileID("x", fakeInfo{st}) != 0 {
		t.Error("want 0 when Sys() is not a Stat_t")
	}
}

type fakeInfo struct{ os.FileInfo }

func (fakeInfo) Sys() any { return nil }

func TestScanEntriesCarryInodes(t *testing.T) {
	root := syncTree(t)
	res, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range res.Entries {
		if want := inodeOf(t, filepath.Join(root, filepath.FromSlash(e.RelPath))); e.Inode != want {
			t.Errorf("%s: Inode = %d, want %d", e.RelPath, e.Inode, want)
		}
	}
}

func TestSyncRecordsTheInode(t *testing.T) {
	ctx := context.Background()
	root := syncTree(t)
	repo := openRepo(t)

	if _, err := Sync(ctx, root, repo, (&uploads{}).fn, func(Event) {}); err != nil {
		t.Fatal(err)
	}

	row, err := repo.Get(ctx, "a/b.txt")
	if err != nil || row.Inode == 0 || row.Inode != inodeOf(t, filepath.Join(root, "a", "b.txt")) {
		t.Fatalf("row = %+v, err = %v", row, err)
	}
}

func TestRunRecordsTheInode(t *testing.T) {
	root := t.TempDir()
	repo := openRepo(t)
	fd := &fakeDrive{}
	startRun(t, root, repo, fd, RunOptions{})

	write(t, filepath.Join(root, "live.txt"), "x")

	eventually(t, "upload", func() bool { return fd.count("live.txt") == 1 })
	eventually(t, "row with inode", func() bool {
		row, err := repo.Get(context.Background(), "live.txt")
		return err == nil && row.Inode == inodeOf(t, filepath.Join(root, "live.txt"))
	})
}

// Slice 2: rows that predate the inode, or hold a stale one, catch up without an upload.
func TestSyncRefreshesMissingAndStaleInodesWithoutUploading(t *testing.T) {
	ctx := context.Background()
	root := syncTree(t)
	repo := openRepo(t)
	if _, err := Sync(ctx, root, repo, (&uploads{}).fn, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	_ = repo.SetInode(ctx, "a.txt", 0)   // a row from before this ticket
	_ = repo.SetInode(ctx, "z.txt", 999) // a stale one
	u := &uploads{}

	sum, err := Sync(ctx, root, repo, u.fn, func(Event) {})

	if err != nil || sum.Synced != 3 || len(u.rels) != 0 {
		t.Fatalf("sum = %+v, err = %v, uploads = %v; want nothing uploaded", sum, err, u.rels)
	}
	for _, rel := range []string{"a.txt", "z.txt"} {
		row, _ := repo.Get(ctx, rel)
		if want := inodeOf(t, filepath.Join(root, rel)); row.Inode != want {
			t.Errorf("%s: Inode = %d, want %d", rel, row.Inode, want)
		}
	}
}

func TestReconcileRefreshesInodesAndDiffDoesNot(t *testing.T) {
	ctx := context.Background()
	root := syncTree(t)
	repo := openRepo(t)
	for _, rel := range []string{"a.txt", "a/b.txt", "z.txt"} {
		synced(t, repo, root, rel) // rows without inodes
	}

	if _, err := Diff(root, repo); err != nil {
		t.Fatal(err)
	}
	if row, _ := repo.Get(ctx, "a.txt"); row.Inode != 0 {
		t.Fatal("Diff must not write")
	}

	plan, err := Reconcile(ctx, root, repo)
	if err != nil || plan.Unchanged != 3 || plan.Queued() != 0 {
		t.Fatalf("plan = %+v, err = %v", plan, err)
	}
	for _, rel := range []string{"a.txt", "a/b.txt", "z.txt"} {
		row, _ := repo.Get(ctx, rel)
		if want := inodeOf(t, filepath.Join(root, filepath.FromSlash(rel))); row.Inode != want {
			t.Errorf("%s: Inode = %d, want %d", rel, row.Inode, want)
		}
	}
}

// An unknown ID from the scan never erases a known one.
func TestUnknownInodeNeverErasesAKnownOne(t *testing.T) {
	ctx := context.Background()
	root := syncTree(t)
	repo := openRepo(t)
	synced(t, repo, root, "a.txt")
	_ = repo.SetInode(ctx, "a.txt", 4242)
	u := &uploads{}
	e := Entry{RelPath: "a.txt", Size: 1, MTime: 1} // Inode 0: unknown
	st, _ := os.Stat(filepath.Join(root, "a.txt"))
	e.Size, e.MTime = st.Size(), st.ModTime().UnixNano()

	_, fatal := process(ctx, slogDiscard(), root, repo, u.fn, &runState{report: func(Event) {}}, e)

	if fatal != nil {
		t.Fatal(fatal)
	}
	if row, _ := repo.Get(ctx, "a.txt"); row.Inode != 4242 {
		t.Errorf("Inode = %d, want 4242 kept", row.Inode)
	}
}
