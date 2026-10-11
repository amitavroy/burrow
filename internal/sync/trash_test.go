package sync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	stdsync "sync"
	"testing"

	"github.com/amitavroy/burrow/internal/drive"
	"github.com/amitavroy/burrow/internal/store"
)

// trasher records the Drive file IDs it was asked to trash.
type trasher struct {
	mu  stdsync.Mutex
	ids []string
	err error
}

func (tr *trasher) trash(_ context.Context, id string) error {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if tr.err != nil {
		return tr.err
	}
	tr.ids = append(tr.ids, id)
	return nil
}

func (tr *trasher) got() []string {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return append([]string(nil), tr.ids...)
}

func syncWithTrash(t *testing.T, root string, repo *store.Repo, up *uploads, tr *trasher) (Summary, []Event) {
	t.Helper()
	var events []Event
	opts := []SyncOption{}
	if tr != nil {
		opts = append(opts, WithTrash(tr.trash))
	}
	sum, err := SyncWorkers(context.Background(), root, repo, up.fn, collect(&events), 1, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return sum, events
}

// The ticket checkpoint: a deleted file is moved to the Drive trash, once.
func TestSyncTrashesADeletedFile(t *testing.T) {
	root := syncTree(t)
	repo, up, tr := openRepo(t), &uploads{}, &trasher{}
	syncWithTrash(t, root, repo, up, tr)
	row, _ := repo.Get(context.Background(), "a.txt")
	if err := os.Remove(filepath.Join(root, "a.txt")); err != nil {
		t.Fatal(err)
	}

	sum, events := syncWithTrash(t, root, repo, up, tr)

	if sum.Trashed != 1 || sum.Failed != 0 || !equal(tr.got(), []string{row.DriveFileID}) {
		t.Fatalf("sum = %+v, trashed = %v; want a.txt's Drive file trashed once", sum, tr.got())
	}
	if len(events) != 1 || !events[0].Trashed || events[0].RelPath != "a.txt" {
		t.Errorf("events = %+v", events)
	}
	if _, err := repo.Get(context.Background(), "a.txt"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("row after trash: %v", err)
	}
	if sum, _ := syncWithTrash(t, root, repo, up, tr); sum.Trashed != 0 || len(tr.got()) != 1 {
		t.Errorf("second run: %+v, trashed %v", sum, tr.got())
	}
}

func TestSyncWithoutATrashFuncLeavesDeletedFilesAlone(t *testing.T) {
	root := syncTree(t)
	repo, up := openRepo(t), &uploads{}
	syncWithTrash(t, root, repo, up, nil)
	_ = os.Remove(filepath.Join(root, "a.txt"))

	sum, _ := syncWithTrash(t, root, repo, up, nil)

	if sum.Trashed != 0 {
		t.Errorf("sum = %+v", sum)
	}
	if _, err := repo.Get(context.Background(), "a.txt"); err != nil {
		t.Errorf("row dropped without trashing: %v", err)
	}
}

// A failed trash keeps the row and the error, and the next run tries again.
func TestSyncTrashFailureIsRetried(t *testing.T) {
	root := syncTree(t)
	repo, up, tr := openRepo(t), &uploads{}, &trasher{}
	syncWithTrash(t, root, repo, up, tr)
	_ = os.Remove(filepath.Join(root, "a.txt"))
	tr.err = errors.New("boom")

	sum, _ := syncWithTrash(t, root, repo, up, tr)

	if sum.Failed != 1 || sum.Trashed != 0 {
		t.Fatalf("sum = %+v", sum)
	}
	if _, err := repo.Get(context.Background(), "a.txt"); err != nil {
		t.Errorf("row dropped after a failed trash: %v", err)
	}
	if _, ok := errorsOf(t, repo)["a.txt"]; !ok {
		t.Error("no error row for a.txt")
	}
	tr.err = nil
	if sum, _ := syncWithTrash(t, root, repo, up, tr); sum.Trashed != 1 {
		t.Errorf("retry: %+v", sum)
	}
	if len(errorsOf(t, repo)) != 0 {
		t.Error("error row not cleared")
	}
}

// A Drive file that is already gone is not an error.
func TestSyncTrashOfAnAlreadyGoneDriveFileIsDone(t *testing.T) {
	root := syncTree(t)
	repo, up := openRepo(t), &uploads{}
	tr := &trasher{}
	syncWithTrash(t, root, repo, up, tr)
	_ = os.Remove(filepath.Join(root, "a.txt"))
	tr.err = drive.ErrFileNotFound

	if sum, _ := syncWithTrash(t, root, repo, up, tr); sum.Failed != 0 || sum.Trashed != 1 {
		t.Errorf("sum = %+v", sum)
	}
}

// A file that is only ignored now, or only unreadable, is not "deleted".
func TestSyncDoesNotTrashWhatIsStillThere(t *testing.T) {
	root := syncTree(t)
	repo, up, tr := openRepo(t), &uploads{}, &trasher{}
	syncWithTrash(t, root, repo, up, tr)
	if err := os.WriteFile(filepath.Join(root, ".syncignore"), []byte("a.txt\n"), 0o644); err != nil {
		t.Skip(err)
	}

	syncWithTrash(t, root, repo, up, tr)

	if got := tr.got(); len(got) != 0 {
		t.Errorf("trashed %v, but a.txt is still on disk", got)
	}
}

func TestRunTrashesAFileDeletedWhileRunning(t *testing.T) {
	root := t.TempDir()
	repo := openRepo(t)
	fd, tr := &fakeDrive{realMD5: true}, &trasher{}
	startRun(t, root, repo, fd, RunOptions{Trash: tr.trash})
	write(t, filepath.Join(root, "a.txt"), "alpha")
	eventually(t, "upload", func() bool { return fd.count("a.txt") == 1 })

	if err := os.Remove(filepath.Join(root, "a.txt")); err != nil {
		t.Fatal(err)
	}

	eventually(t, "trash", func() bool { return equal(tr.got(), []string{"id-a.txt"}) })
	eventually(t, "row dropped", func() bool {
		_, err := repo.Get(context.Background(), "a.txt")
		return errors.Is(err, store.ErrNotFound)
	})
}

// Files deleted while the daemon was stopped are trashed on the next start.
func TestRunTrashesFilesDeletedWhileStopped(t *testing.T) {
	root := syncTree(t)
	repo, up, tr := openRepo(t), &uploads{}, &trasher{}
	syncWithTrash(t, root, repo, up, tr)
	_ = os.Remove(filepath.Join(root, "z.txt"))

	startRun(t, root, repo, &fakeDrive{}, RunOptions{Trash: tr.trash})

	eventually(t, "trash", func() bool { return len(tr.got()) == 1 })
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
