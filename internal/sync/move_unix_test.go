//go:build unix

package sync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	stdsync "sync"
	"testing"
	"time"

	"github.com/amitavroy/burrow/internal/drive"
	"github.com/amitavroy/burrow/internal/store"
)

// moveRig is a synced tree plus a fake Drive that records uploads and moves.
type moveRig struct {
	t       *testing.T
	root    string
	repo    *store.Repo
	mu      stdsync.Mutex
	ups     []string
	moves   [][2]string // driveFileID, newRel
	moveErr error
}

func newMoveRig(t *testing.T, files map[string]string) *moveRig {
	t.Helper()
	r := &moveRig{t: t, root: t.TempDir(), repo: openRepo(t)}
	for rel, content := range files {
		write(t, filepath.Join(r.root, filepath.FromSlash(rel)), content)
	}
	if _, err := Sync(context.Background(), r.root, r.repo, r.upload, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	r.ups = nil // only what happens after the first sync counts
	return r
}

// upload reports the file's real MD5, as Drive's md5Checksum would.
func (r *moveRig) upload(_ context.Context, localPath, rel string) (drive.FileInfo, error) {
	md5, err := fileMD5(localPath)
	if err != nil {
		return drive.FileInfo{}, err
	}
	r.mu.Lock()
	r.ups = append(r.ups, rel)
	r.mu.Unlock()
	return drive.FileInfo{ID: "id-" + rel, MD5: md5, RevisionID: "r"}, nil
}

func (r *moveRig) move(_ context.Context, id, newRel string) (drive.FileInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.moveErr != nil {
		return drive.FileInfo{}, r.moveErr
	}
	r.moves = append(r.moves, [2]string{id, newRel})
	return drive.FileInfo{ID: id, RelPath: newRel}, nil
}

func (r *moveRig) sync() (Summary, []Event) {
	r.t.Helper()
	var events []Event
	sum, err := SyncWorkers(context.Background(), r.root, r.repo, r.upload, collect(&events), 1, WithMove(r.move))
	if err != nil {
		r.t.Fatal(err)
	}
	return sum, events
}

func (r *moveRig) path(rel string) string { return filepath.Join(r.root, filepath.FromSlash(rel)) }

func (r *moveRig) rename(from, to string) {
	r.t.Helper()
	if err := os.MkdirAll(filepath.Dir(r.path(to)), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.Rename(r.path(from), r.path(to)); err != nil {
		r.t.Fatal(err)
	}
}

func (r *moveRig) row(rel string) (store.File, error) { return r.repo.Get(context.Background(), rel) }

// The ticket checkpoint: a renamed file keeps its Drive file ID and nothing is re-uploaded.
func TestSyncMovesARenamedFile(t *testing.T) {
	r := newMoveRig(t, map[string]string{"a.txt": "alpha", "keep.txt": "keep"})
	before, _ := r.row("a.txt")
	r.rename("a.txt", "c.txt")

	sum, events := r.sync()

	if sum.Moved != 1 || sum.Uploaded != 0 || sum.Failed != 0 {
		t.Fatalf("sum = %+v, want one move and no upload", sum)
	}
	if len(r.ups) != 0 {
		t.Errorf("uploaded %v", r.ups)
	}
	if len(r.moves) != 1 || r.moves[0] != [2]string{"id-a.txt", "c.txt"} {
		t.Errorf("moves = %v", r.moves)
	}
	got, err := r.row("c.txt")
	if err != nil || got.DriveFileID != before.DriveFileID || got.Inode != before.Inode || got.SyncedMD5 != before.SyncedMD5 {
		t.Errorf("row = %+v, err = %v; want the old row's Drive ID, inode and MD5 at the new path", got, err)
	}
	if _, err := r.row("a.txt"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the old row is still there: %v", err)
	}
	var moved []Event
	for _, e := range events {
		if e.MovedFrom != "" {
			moved = append(moved, e)
		}
	}
	if len(moved) != 1 || moved[0].RelPath != "c.txt" || moved[0].MovedFrom != "a.txt" {
		t.Errorf("events = %+v", events)
	}
	if n, _ := r.repo.CountJobs(context.Background()); n != 0 {
		t.Errorf("%d jobs left", n)
	}
	// A second run finds nothing to do.
	if sum, _ := r.sync(); sum.Moved != 0 || sum.Uploaded != 0 || sum.Synced != 2 {
		t.Errorf("second run: %+v", sum)
	}
}

func TestSyncMovesIntoANewFolder(t *testing.T) {
	r := newMoveRig(t, map[string]string{"notes/todo.txt": "todo"})
	r.rename("notes/todo.txt", "archive/2026/todo.txt")

	sum, _ := r.sync()

	if sum.Moved != 1 || len(r.ups) != 0 || len(r.moves) != 1 || r.moves[0][1] != "archive/2026/todo.txt" {
		t.Fatalf("sum = %+v, moves = %v, uploads = %v", sum, r.moves, r.ups)
	}
}

// Without a mover the old behaviour stays: the new name is uploaded.
func TestSyncWithoutAMoverUploadsTheRenamedFile(t *testing.T) {
	r := newMoveRig(t, map[string]string{"a.txt": "alpha"})
	r.rename("a.txt", "c.txt")

	sum, err := Sync(context.Background(), r.root, r.repo, r.upload, func(Event) {})

	if err != nil || sum.Uploaded != 1 || sum.Moved != 0 || len(r.ups) != 1 {
		t.Fatalf("sum = %+v, err = %v, uploads = %v", sum, err, r.ups)
	}
}

// Cases that must not be treated as a move: each is uploaded as a new file.
func TestSyncDoesNotMoveWhenItShouldNot(t *testing.T) {
	cases := []struct {
		name   string
		files  map[string]string
		mutate func(r *moveRig)
	}{
		{"a copy keeps the original", map[string]string{"a.txt": "alpha"}, func(r *moveRig) {
			write(t, r.path("c.txt"), "alpha")
		}},
		{"a hard link while the original exists", map[string]string{"a.txt": "alpha"}, func(r *moveRig) {
			if err := os.Link(r.path("a.txt"), r.path("c.txt")); err != nil {
				t.Skip("no hard links:", err)
			}
		}},
		{"a rename plus an edit", map[string]string{"a.txt": "alpha"}, func(r *moveRig) {
			r.rename("a.txt", "c.txt")
			write(t, r.path("c.txt"), "alpha, edited")
		}},
		{"an inode reused by unrelated content", map[string]string{"a.txt": "alpha"}, func(r *moveRig) {
			if err := os.Remove(r.path("a.txt")); err != nil {
				t.Fatal(err)
			}
			write(t, r.path("c.txt"), "something else entirely")
			// Pretend the filesystem handed the old inode to the new file.
			_ = r.repo.SetInode(context.Background(), "a.txt", inodeOf(t, r.path("c.txt")))
		}},
		{"two vanished files with the same inode", map[string]string{"a.txt": "alpha", "b.txt": "alpha"}, func(r *moveRig) {
			r.rename("a.txt", "c.txt")
			if err := os.Remove(r.path("b.txt")); err != nil {
				t.Fatal(err)
			}
			_ = r.repo.SetInode(context.Background(), "b.txt", inodeOf(t, r.path("c.txt")))
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newMoveRig(t, c.files)
			c.mutate(r)

			sum, _ := r.sync()

			if sum.Moved != 0 || len(r.moves) != 0 {
				t.Fatalf("moved something: sum = %+v, moves = %v", sum, r.moves)
			}
			if sum.Uploaded != 1 || len(r.ups) != 1 || r.ups[0] != "c.txt" {
				t.Errorf("uploads = %v, sum = %+v; want c.txt uploaded as new", r.ups, sum)
			}
		})
	}
}

// If Drive has lost the file, the rename is uploaded as new and the stale row goes.
func TestSyncFallsBackToUploadWhenTheDriveFileIsGone(t *testing.T) {
	r := newMoveRig(t, map[string]string{"a.txt": "alpha"})
	r.moveErr = drive.ErrFileNotFound
	r.rename("a.txt", "c.txt")

	sum, _ := r.sync()

	if sum.Uploaded != 1 || sum.Moved != 0 || len(r.ups) != 1 {
		t.Fatalf("sum = %+v, uploads = %v", sum, r.ups)
	}
	if _, err := r.row("a.txt"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the stale row survived: %v", err)
	}
	if row, err := r.row("c.txt"); err != nil || row.DriveFileID != "id-c.txt" {
		t.Errorf("row = %+v, err = %v", row, err)
	}
}

// A transient Drive error fails the job without renaming the row; the next run completes the move.
func TestSyncMoveFailureIsRetried(t *testing.T) {
	r := newMoveRig(t, map[string]string{"a.txt": "alpha"})
	r.moveErr = errors.New("network problem")
	r.rename("a.txt", "c.txt")

	sum, _ := r.sync()

	if sum.Failed != 1 || sum.Moved != 0 || len(r.ups) != 0 {
		t.Fatalf("sum = %+v, uploads = %v", sum, r.ups)
	}
	if _, err := r.row("a.txt"); err != nil {
		t.Errorf("the old row must stay until Drive says yes: %v", err)
	}
	if e := errorsOf(t, r.repo)["c.txt"]; e.Message == "" {
		t.Error("no error row for the failed move")
	}

	r.moveErr = nil
	sum, _ = r.sync()
	if sum.Moved != 1 || sum.Failed != 0 {
		t.Fatalf("retry: %+v", sum)
	}
	if got := errorsOf(t, r.repo); len(got) != 0 {
		t.Errorf("errors after the retry: %+v", got)
	}
}

// An unknown inode (0) never matches anything.
func TestMoveNeedsAKnownInode(t *testing.T) {
	r := newMoveRig(t, map[string]string{"a.txt": "alpha"})
	r.rename("a.txt", "c.txt")
	run := &runState{report: func(Event) {}, move: r.move}

	fileErr, fatal := process(context.Background(), slogDiscard(), r.root, r.repo, r.upload, run, Entry{RelPath: "c.txt", Size: 5, MTime: 1})

	if fileErr != nil || fatal != nil || len(r.moves) != 0 || len(r.ups) != 1 {
		t.Fatalf("fileErr = %v, fatal = %v, moves = %v, uploads = %v", fileErr, fatal, r.moves, r.ups)
	}
}

// A sign-in loss during a move aborts the run and keeps the job.
func TestSyncMoveSignInLostAborts(t *testing.T) {
	r := newMoveRig(t, map[string]string{"a.txt": "alpha"})
	r.moveErr = drive.ErrSessionExpired
	r.rename("a.txt", "c.txt")

	_, err := SyncWorkers(context.Background(), r.root, r.repo, r.upload, func(Event) {}, 1, WithMove(r.move))

	if !errors.Is(err, drive.ErrSessionExpired) {
		t.Fatalf("err = %v", err)
	}
	if n, _ := r.repo.CountJobs(context.Background()); n != 1 {
		t.Errorf("queued = %d, want the job kept", n)
	}
}

// ---- syncd run: the same, live and after a restart ----

// liveMover records moves for Run tests.
type liveMover struct {
	mu    stdsync.Mutex
	moves [][2]string
}

func (m *liveMover) move(_ context.Context, id, newRel string) (drive.FileInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.moves = append(m.moves, [2]string{id, newRel})
	return drive.FileInfo{ID: id, RelPath: newRel}, nil
}

func (m *liveMover) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.moves)
}

func (m *liveMover) last() [2]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.moves[len(m.moves)-1]
}

func startMoveRun(t *testing.T, root string, repo *store.Repo, fd *fakeDrive, m *liveMover) *runRig {
	t.Helper()
	return startRun(t, root, repo, fd, RunOptions{Move: m.move})
}

func TestRunMovesARenamedFileLive(t *testing.T) {
	root := t.TempDir()
	repo := openRepo(t)
	fd := &fakeDrive{realMD5: true}
	m := &liveMover{}
	startMoveRun(t, root, repo, fd, m)
	write(t, filepath.Join(root, "a.txt"), "alpha")
	eventually(t, "first upload", func() bool { return fd.count("a.txt") == 1 })

	if err := os.Rename(filepath.Join(root, "a.txt"), filepath.Join(root, "c.txt")); err != nil {
		t.Fatal(err)
	}

	eventually(t, "move", func() bool { return m.count() == 1 })
	if m.last() != [2]string{"id-a.txt", "c.txt"} {
		t.Errorf("move = %v", m.last())
	}
	stays(t, "no upload of the new name", 400*time.Millisecond, func() bool { return fd.count("c.txt") == 0 && fd.total() == 1 })
	eventually(t, "row at the new path", func() bool {
		row, err := repo.Get(context.Background(), "c.txt")
		return err == nil && row.DriveFileID == "id-a.txt"
	})
	if _, err := repo.Get(context.Background(), "a.txt"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("old row: %v", err)
	}
}

func TestRunMovesIntoANewSubfolder(t *testing.T) {
	root := t.TempDir()
	repo := openRepo(t)
	fd := &fakeDrive{realMD5: true}
	m := &liveMover{}
	startMoveRun(t, root, repo, fd, m)
	write(t, filepath.Join(root, "a.txt"), "alpha")
	eventually(t, "first upload", func() bool { return fd.count("a.txt") == 1 })

	if err := os.MkdirAll(filepath.Join(root, "sub", "deeper"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "a.txt"), filepath.Join(root, "sub", "deeper", "a.txt")); err != nil {
		t.Fatal(err)
	}

	eventually(t, "move", func() bool { return m.count() == 1 })
	if m.last() != [2]string{"id-a.txt", "sub/deeper/a.txt"} || fd.total() != 1 {
		t.Errorf("move = %v, uploads = %d", m.last(), fd.total())
	}
}

// A rename made while the daemon was stopped is handled at the next start.
func TestRunMovesAFileRenamedWhileStopped(t *testing.T) {
	r := newMoveRig(t, map[string]string{"a.txt": "alpha", "keep.txt": "keep"})
	r.rename("a.txt", "c.txt")
	fd := &fakeDrive{realMD5: true}
	m := &liveMover{}

	startMoveRun(t, r.root, r.repo, fd, m)

	eventually(t, "move at start", func() bool { return m.count() == 1 })
	if m.last() != [2]string{"id-a.txt", "c.txt"} {
		t.Errorf("move = %v", m.last())
	}
	stays(t, "nothing uploaded", 400*time.Millisecond, func() bool { return fd.total() == 0 })
}

// A file that is renamed again before it settles is moved to its final name, never uploaded.
func TestRunRenameChainEndsAtTheLastName(t *testing.T) {
	root := t.TempDir()
	repo := openRepo(t)
	fd := &fakeDrive{realMD5: true}
	m := &liveMover{}
	startMoveRun(t, root, repo, fd, m)
	write(t, filepath.Join(root, "a.txt"), "alpha")
	eventually(t, "first upload", func() bool { return fd.count("a.txt") == 1 })

	for _, step := range [][2]string{{"a.txt", "b.txt"}, {"b.txt", "c.txt"}} {
		if err := os.Rename(filepath.Join(root, step[0]), filepath.Join(root, step[1])); err != nil {
			t.Fatal(err)
		}
	}

	eventually(t, "row at the last name", func() bool {
		row, err := repo.Get(context.Background(), "c.txt")
		return err == nil && row.DriveFileID == "id-a.txt"
	})
	if fd.total() != 1 {
		t.Errorf("uploads = %d, want only the first", fd.total())
	}
	if last := m.last(); last[0] != "id-a.txt" || last[1] != "c.txt" {
		t.Errorf("last move = %v", last)
	}
}

// A file renamed before it was ever uploaded is a plain new file under its final name.
func TestRunRenameBeforeFirstUploadIsJustANewFile(t *testing.T) {
	root := t.TempDir()
	repo := openRepo(t)
	fd := &fakeDrive{realMD5: true}
	m := &liveMover{}
	startMoveRun(t, root, repo, fd, m)

	write(t, filepath.Join(root, "x.txt"), "fresh")
	if err := os.Rename(filepath.Join(root, "x.txt"), filepath.Join(root, "y.txt")); err != nil {
		t.Fatal(err)
	}

	eventually(t, "upload of the final name", func() bool { return fd.count("y.txt") == 1 })
	if m.count() != 0 || fd.count("x.txt") != 0 {
		t.Errorf("moves = %d, uploads of x = %d", m.count(), fd.count("x.txt"))
	}
}

// ---- the preview in Diff ----

func TestDiffPairsARenamedFileAsMoved(t *testing.T) {
	r := newMoveRig(t, map[string]string{"a.txt": "alpha", "keep.txt": "keep", "x/b.txt": "bravo"})
	r.rename("a.txt", "docs/c.txt")
	r.rename("x/b.txt", "x/d.txt")
	write(t, r.path("fresh.txt"), "brand new")

	plan, err := Diff(r.root, r.repo)

	if err != nil {
		t.Fatal(err)
	}
	wantMoved := []Move{{"a.txt", "docs/c.txt"}, {"x/b.txt", "x/d.txt"}} // sorted by new path
	if !reflect.DeepEqual(plan.Moved, wantMoved) {
		t.Errorf("Moved = %v, want %v", plan.Moved, wantMoved)
	}
	if !reflect.DeepEqual(plan.New, []string{"fresh.txt"}) || len(plan.Missing) != 0 || plan.Unchanged != 1 {
		t.Errorf("plan = %+v; the moved files must leave New and Missing", plan)
	}
	if plan.Queued() != 3 {
		t.Errorf("Queued = %d, want 3 (a move is queued under its new path)", plan.Queued())
	}
}

func TestDiffDoesNotPairWhatIsNotClearlyAMove(t *testing.T) {
	t.Run("size differs", func(t *testing.T) {
		r := newMoveRig(t, map[string]string{"a.txt": "alpha"})
		r.rename("a.txt", "c.txt")
		write(t, r.path("c.txt"), "alpha and more")
		plan, _ := Diff(r.root, r.repo)
		if len(plan.Moved) != 0 || !reflect.DeepEqual(plan.New, []string{"c.txt"}) || !reflect.DeepEqual(plan.Missing, []string{"a.txt"}) {
			t.Errorf("plan = %+v", plan)
		}
	})
	t.Run("a copy leaves the original in place", func(t *testing.T) {
		r := newMoveRig(t, map[string]string{"a.txt": "alpha"})
		write(t, r.path("c.txt"), "alpha")
		plan, _ := Diff(r.root, r.repo)
		if len(plan.Moved) != 0 || !reflect.DeepEqual(plan.New, []string{"c.txt"}) || plan.Unchanged != 1 {
			t.Errorf("plan = %+v", plan)
		}
	})
	t.Run("two missing rows share the inode", func(t *testing.T) {
		r := newMoveRig(t, map[string]string{"a.txt": "alpha", "b.txt": "alpha"})
		r.rename("a.txt", "c.txt")
		_ = os.Remove(r.path("b.txt"))
		_ = r.repo.SetInode(context.Background(), "b.txt", inodeOf(t, r.path("c.txt")))
		plan, _ := Diff(r.root, r.repo)
		if len(plan.Moved) != 0 || len(plan.Missing) != 2 {
			t.Errorf("plan = %+v, want the ambiguous case left alone", plan)
		}
	})
}

func TestReconcileQueuesMovedFilesUnderTheirNewPath(t *testing.T) {
	r := newMoveRig(t, map[string]string{"a.txt": "alpha"})
	r.rename("a.txt", "c.txt")

	plan, err := Reconcile(context.Background(), r.root, r.repo)

	if err != nil || len(plan.Moved) != 1 {
		t.Fatalf("plan = %+v, err = %v", plan, err)
	}
	if got := queued(t, r.repo); !reflect.DeepEqual(got, []string{"c.txt"}) {
		t.Errorf("queued %v, want c.txt for the job to confirm", got)
	}
}

// Renaming a folder moves its files one by one (folder-level moves are a later ticket).
func TestSyncFolderRenameMovesEachFile(t *testing.T) {
	r := newMoveRig(t, map[string]string{"old/a.txt": "alpha", "old/sub/b.txt": "bravo", "old/c.txt": "charlie", "other.txt": "o"})
	r.rename("old", "new")

	sum, _ := r.sync()

	if sum.Moved != 3 || sum.Uploaded != 0 || len(r.ups) != 0 {
		t.Fatalf("sum = %+v, uploads = %v", sum, r.ups)
	}
	for _, rel := range []string{"new/a.txt", "new/sub/b.txt", "new/c.txt"} {
		if row, err := r.row(rel); err != nil || row.DriveFileID == "" {
			t.Errorf("%s: row = %+v, err = %v", rel, row, err)
		}
	}
}
