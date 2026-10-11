package sync

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	stdsync "sync"
	"testing"

	"github.com/amitavroy/burrow/internal/drive"
	"github.com/amitavroy/burrow/internal/store"
)

// memDrive is a small in-memory Drive: it keeps the content of every file by
// Drive ID, and offers the upload, trash, listing and download calls the sync
// and restore code use. It is the "Drive holds the truth" side of the
// rebuild test.
type memDrive struct {
	mu        stdsync.Mutex
	byRel     map[string]string // rel_path -> Drive ID
	content   map[string]string // Drive ID -> content
	next      int
	uploads   []string
	trashed   []string
	downloads []string
}

func newMemDrive() *memDrive {
	return &memDrive{byRel: map[string]string{}, content: map[string]string{}}
}

// upload creates the file, or replaces the content of the one tagged with the
// same rel_path (as the real Uploader does), and reports Drive's MD5.
func (d *memDrive) upload(_ context.Context, localPath, rel string) (drive.FileInfo, error) {
	b, err := os.ReadFile(localPath)
	if err != nil {
		return drive.FileInfo{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	id, ok := d.byRel[rel]
	if !ok {
		d.next++
		id = "id-" + strconv.Itoa(d.next)
		d.byRel[rel] = id
	}
	d.content[id] = string(b)
	d.uploads = append(d.uploads, rel)
	return drive.FileInfo{ID: id, MD5: md5Hex(string(b)), RevisionID: "rev-" + strconv.Itoa(len(d.uploads)), RelPath: rel}, nil
}

func (d *memDrive) trash(_ context.Context, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.trashed = append(d.trashed, id)
	return nil
}

func (d *memDrive) list() []drive.RemoteFile {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []drive.RemoteFile
	for rel, id := range d.byRel {
		c := d.content[id]
		out = append(out, drive.RemoteFile{ID: id, RelPath: rel, Tagged: true, Size: int64(len(c)), MD5: md5Hex(c), RevisionID: "head-" + id})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RelPath < out[j].RelPath })
	return out
}

func (d *memDrive) download(_ context.Context, id, wantMD5, dst string) error {
	d.mu.Lock()
	c := d.content[id]
	d.downloads = append(d.downloads, id)
	d.mu.Unlock()
	if md5Hex(c) != wantMD5 {
		return drive.ErrChecksumMismatch
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, []byte(c), 0o644)
}

func (d *memDrive) syncOnce(t *testing.T, root string, repo *store.Repo) Summary {
	t.Helper()
	sum, err := SyncWorkers(context.Background(), root, repo, d.upload, func(Event) {}, 2, WithTrash(d.trash))
	if err != nil {
		t.Fatal(err)
	}
	return sum
}

func (d *memDrive) restoreOnce(t *testing.T, root string, repo *store.Repo) RestoreSummary {
	t.Helper()
	sum, err := Restore(context.Background(), root, repo, d.list(), d.download, func(RestoreEvent) {}, restoreOpts())
	if err != nil {
		t.Fatal(err)
	}
	return sum
}

var rebuildTree = map[string]string{
	"readme.txt":        "read me",
	"docs/a.txt":        "alpha",
	"docs/deep/b.txt":   "bravo",
	"photos/2026/c.jpg": "not really a jpeg",
	"empty-content.txt": "",
}

func writeTree(t *testing.T, root string, tree map[string]string) {
	t.Helper()
	for rel, c := range tree {
		write(t, filepath.Join(root, filepath.FromSlash(rel)), c)
	}
}

func checkTree(t *testing.T, root string, tree map[string]string) {
	t.Helper()
	for rel, want := range tree {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil || string(b) != want {
			t.Errorf("%s = %q, %v; want %q", rel, b, err, want)
		}
	}
}

// The ticket checkpoint: delete the database, restore, sync again: nothing is
// lost and nothing is uploaded again. Control first: without the restore, the
// same sync would upload everything.
func TestRebuildFromDriveAfterDeletingTheDatabase(t *testing.T) {
	root, d := t.TempDir(), newMemDrive()
	writeTree(t, root, rebuildTree)
	if sum := d.syncOnce(t, root, openRepo(t)); sum.Uploaded != len(rebuildTree) || sum.Failed != 0 {
		t.Fatalf("first sync: %+v", sum)
	}
	d.uploads = nil

	// "Delete the database": a brand new, empty one.
	if sum := d.syncOnce(t, root, openRepo(t)); sum.Uploaded != len(rebuildTree) {
		t.Fatalf("control: without a restore the empty database re-uploads everything, got %+v", sum)
	}
	d.uploads = nil

	repo := openRepo(t)
	rs := d.restoreOnce(t, root, repo)
	if rs != (RestoreSummary{Matched: len(rebuildTree)}) || len(d.downloads) != 0 {
		t.Fatalf("restore: %+v, downloads %v; want every file matched and nothing transferred", rs, d.downloads)
	}
	sum := d.syncOnce(t, root, repo)

	if sum.Uploaded != 0 || sum.Updated != 0 || sum.Moved != 0 || sum.Trashed != 0 || sum.Failed != 0 || sum.Synced != len(rebuildTree) {
		t.Errorf("sync after restore: %+v", sum)
	}
	if len(d.uploads) != 0 || len(d.trashed) != 0 {
		t.Errorf("Drive was touched: uploads %v, trashed %v", d.uploads, d.trashed)
	}
	checkTree(t, root, rebuildTree)
}

// Same, on a machine that lost the local folder too: Drive alone is enough.
func TestRebuildFromDriveIntoAnEmptyFolder(t *testing.T) {
	d := newMemDrive()
	src := t.TempDir()
	writeTree(t, src, rebuildTree)
	d.syncOnce(t, src, openRepo(t))
	d.uploads = nil

	fresh, repo := t.TempDir(), openRepo(t)
	rs := d.restoreOnce(t, fresh, repo)
	sum := d.syncOnce(t, fresh, repo)

	if rs != (RestoreSummary{Downloaded: len(rebuildTree)}) {
		t.Errorf("restore: %+v", rs)
	}
	if sum.Uploaded != 0 || sum.Updated != 0 || sum.Trashed != 0 || sum.Failed != 0 || len(d.uploads) != 0 || len(d.trashed) != 0 {
		t.Errorf("sync after restore: %+v, uploads %v, trashed %v", sum, d.uploads, d.trashed)
	}
	checkTree(t, fresh, rebuildTree)
}

// A file edited while the database was gone is not lost on either side: Drive's
// version takes the path, the edit lives on in a conflicted copy, and the next
// sync uploads only that copy.
func TestRebuildKeepsLocalEditsMadeWhileTheDatabaseWasGone(t *testing.T) {
	root, d := t.TempDir(), newMemDrive()
	writeTree(t, root, rebuildTree)
	d.syncOnce(t, root, openRepo(t))
	d.uploads = nil
	write(t, filepath.Join(root, "docs/a.txt"), "alpha, edited offline")

	repo := openRepo(t)
	rs := d.restoreOnce(t, root, repo)
	sum := d.syncOnce(t, root, repo)

	if rs.Conflicts != 1 || rs.Matched != len(rebuildTree)-1 {
		t.Fatalf("restore: %+v", rs)
	}
	const copyRel = "docs/a (conflicted copy, laptop, 2026-10-11).txt"
	if sum.Uploaded != 1 || sum.Updated != 0 || sum.Trashed != 0 || len(d.uploads) != 1 || d.uploads[0] != copyRel {
		t.Errorf("sync after restore: %+v, uploads %v; want only the conflicted copy uploaded", sum, d.uploads)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "docs/a.txt")); string(b) != "alpha" {
		t.Errorf("docs/a.txt = %q, want Drive's version", b)
	}
	if got := d.content[d.byRel[copyRel]]; got != "alpha, edited offline" {
		t.Errorf("the edit in Drive = %q", got)
	}
}
