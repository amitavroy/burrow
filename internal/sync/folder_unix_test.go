//go:build unix

package sync

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	stdsync "sync"
	"testing"
	"time"
)

// dirMover records folder moves and rewrites the folder cache like the real one.
type dirMover struct {
	mu   stdsync.Mutex
	repo interface {
		RenameFolders(ctx context.Context, from, to string) error
	}
	calls [][2]string
}

func (m *dirMover) move(ctx context.Context, oldDir, newDir string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, [2]string{oldDir, newDir})
	return m.repo.RenameFolders(ctx, oldDir, newDir)
}

func (r *moveRig) syncDirs(m *dirMover, tr *trasher) Summary {
	r.t.Helper()
	opts := []SyncOption{WithMove(r.move), WithMoveDir(m.move)}
	if tr != nil {
		opts = append(opts, WithTrash(tr.trash))
	}
	sum, err := SyncWorkers(context.Background(), r.root, r.repo, r.upload, func(Event) {}, 3, opts...)
	if err != nil {
		r.t.Fatal(err)
	}
	return sum
}

func (r *moveRig) cache(dirs map[string]string) {
	r.t.Helper()
	for rel, id := range dirs {
		if err := r.repo.PutFolder(context.Background(), rel, id); err != nil {
			r.t.Fatal(err)
		}
	}
}

// The ticket checkpoint: a renamed folder is moved once, children intact.
func TestSyncFolderRenameMovesTheFolderOnce(t *testing.T) {
	r := newMoveRig(t, map[string]string{"old/a.txt": "alpha", "old/sub/b.txt": "bravo", "old/c.txt": "charlie", "other.txt": "o"})
	r.cache(map[string]string{"old": "F-old", "old/sub": "F-sub"})
	m := &dirMover{repo: r.repo}
	r.rename("old", "new")

	sum := r.syncDirs(m, nil)

	if sum.Moved != 3 || sum.Uploaded != 0 || len(r.ups) != 0 {
		t.Fatalf("sum = %+v, uploads = %v", sum, r.ups)
	}
	if want := [][2]string{{"old", "new"}}; !reflect.DeepEqual(m.calls, want) {
		t.Errorf("folder moves = %v, want %v", m.calls, want)
	}
	if id, err := r.repo.GetFolder(context.Background(), "new/sub"); err != nil || id != "F-sub" {
		t.Errorf("new/sub cached as %q, %v", id, err)
	}
}

// A subfolder renamed while its parent stays is moved as that subfolder.
func TestSyncSubfolderRenameMovesTheSubfolder(t *testing.T) {
	r := newMoveRig(t, map[string]string{"a/keep.txt": "k", "a/sub/b.txt": "bravo"})
	r.cache(map[string]string{"a": "F-a", "a/sub": "F-sub"})
	m := &dirMover{repo: r.repo}
	r.rename("a/sub", "c/sub")

	r.syncDirs(m, nil)

	if want := [][2]string{{"a/sub", "c/sub"}}; !reflect.DeepEqual(m.calls, want) {
		t.Errorf("folder moves = %v, want %v", m.calls, want)
	}
}

// One file moved between two folders that both stay is not a folder rename.
func TestSyncFileMovedBetweenFoldersMovesNoFolder(t *testing.T) {
	r := newMoveRig(t, map[string]string{"docs/a.txt": "alpha", "docs/b.txt": "bravo", "archive/z.txt": "z"})
	r.cache(map[string]string{"docs": "F-docs", "archive": "F-arch"})
	m := &dirMover{repo: r.repo}
	r.rename("docs/a.txt", "archive/a.txt")

	sum := r.syncDirs(m, nil)

	if len(m.calls) != 0 || sum.Moved != 1 {
		t.Errorf("folder moves = %v, sum = %+v", m.calls, sum)
	}
}

// With no folder cached for the old directory there is nothing to move.
func TestSyncFolderRenameWithoutCacheMovesFilesOneByOne(t *testing.T) {
	r := newMoveRig(t, map[string]string{"old/a.txt": "alpha"})
	m := &dirMover{repo: r.repo}
	r.rename("old", "new")

	if sum := r.syncDirs(m, nil); len(m.calls) != 0 || sum.Moved != 1 {
		t.Errorf("folder moves = %v, sum = %+v", m.calls, sum)
	}
}

// Deleting a folder trashes its files, then the folder itself, once.
func TestSyncFolderDeleteTrashesTheFolder(t *testing.T) {
	r := newMoveRig(t, map[string]string{"d/a.txt": "alpha", "d/sub/b.txt": "bravo", "keep/k.txt": "k"})
	r.cache(map[string]string{"d": "F-d", "d/sub": "F-dsub", "keep": "F-keep"})
	tr := &trasher{}
	if err := os.RemoveAll(r.path("d")); err != nil {
		t.Fatal(err)
	}

	sum := r.syncDirs(&dirMover{repo: r.repo}, tr)

	if sum.Trashed != 2 || sum.Failed != 0 {
		t.Fatalf("sum = %+v", sum)
	}
	// Which job finishes last decides whether d/sub is trashed on its own
	// first; either way d goes, once its last file is trashed, and keep stays.
	got := map[string]bool{}
	for _, id := range tr.got() {
		if id == "" || id == "F-keep" {
			t.Errorf("trashed %q", id)
		}
		got[id] = true
	}
	if !got["F-d"] || !got["id-d/a.txt"] || !got["id-d/sub/b.txt"] {
		t.Errorf("trashed %v, want both files and F-d", tr.got())
	}
	if _, err := r.repo.GetFolder(context.Background(), "d/sub"); err == nil {
		t.Error("d/sub is still cached")
	}
	if id, err := r.repo.GetFolder(context.Background(), "keep"); err != nil || id != "F-keep" {
		t.Errorf("keep cached as %q, %v", id, err)
	}
}

// Deleting one file out of several keeps its folder.
func TestSyncFileDeleteKeepsTheFolderWhileOthersRemain(t *testing.T) {
	r := newMoveRig(t, map[string]string{"d/a.txt": "alpha", "d/b.txt": "bravo"})
	r.cache(map[string]string{"d": "F-d"})
	tr := &trasher{}
	_ = os.Remove(r.path("d/a.txt"))

	r.syncDirs(&dirMover{repo: r.repo}, tr)

	if got := tr.got(); !reflect.DeepEqual(got, []string{"id-d/a.txt"}) {
		t.Errorf("trashed %v, want only the file", got)
	}
}

func TestRunMovesARenamedFolderLive(t *testing.T) {
	root := t.TempDir()
	repo := openRepo(t)
	fd, m := &fakeDrive{realMD5: true}, &liveMover{}
	dm, tr := &dirMover{repo: repo}, &trasher{}
	startRun(t, root, repo, fd, RunOptions{Move: m.move, MoveDir: dm.move, Trash: tr.trash})
	write(t, filepath.Join(root, "old", "a.txt"), "alpha")
	write(t, filepath.Join(root, "old", "b.txt"), "bravo")
	eventually(t, "uploads", func() bool { return fd.total() == 2 })
	if err := repo.PutFolder(context.Background(), "old", "F-old"); err != nil {
		t.Fatal(err)
	}

	if err := os.Rename(filepath.Join(root, "old"), filepath.Join(root, "new")); err != nil {
		t.Fatal(err)
	}

	eventually(t, "both files moved", func() bool { return m.count() == 2 })
	dm.mu.Lock()
	calls := append([][2]string(nil), dm.calls...)
	dm.mu.Unlock()
	if want := [][2]string{{"old", "new"}}; !reflect.DeepEqual(calls, want) {
		t.Errorf("folder moves = %v, want %v", calls, want)
	}
	stays(t, "nothing trashed", 500*time.Millisecond, func() bool { return len(tr.got()) == 0 && fd.total() == 2 })
}
