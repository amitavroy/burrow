package sync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/amitavroy/burrow/internal/store"
)

// synced records a row for the file as it is now, as a finished upload would.
func synced(t *testing.T, repo *store.Repo, root, rel string) {
	t.Helper()
	st, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	err = repo.Upsert(context.Background(), store.File{
		RelPath: rel, DriveFileID: "id-" + rel, Size: st.Size(), MTime: st.ModTime().UnixNano(), SyncedMD5: "m",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func queued(t *testing.T, repo *store.Repo) []string {
	t.Helper()
	var got []string
	for {
		j, err := repo.ClaimJob(context.Background())
		if errors.Is(err, store.ErrNoJob) {
			return got
		}
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, j.RelPath)
	}
}

func TestDiffClassifiesFiles(t *testing.T) {
	root := t.TempDir()
	repo := openRepo(t)
	write(t, filepath.Join(root, "same.txt"), "same")
	write(t, filepath.Join(root, "size.txt"), "one")
	write(t, filepath.Join(root, "mtime.txt"), "mtime")
	write(t, filepath.Join(root, "gone.txt"), "gone")
	write(t, filepath.Join(root, "noid.txt"), "never finished")
	for _, rel := range []string{"same.txt", "size.txt", "mtime.txt", "gone.txt"} {
		synced(t, repo, root, rel)
	}
	_ = repo.Upsert(context.Background(), store.File{RelPath: "noid.txt", Size: 14}) // a row without a Drive ID
	write(t, filepath.Join(root, "size.txt"), "one, now longer")
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(root, "mtime.txt"), later, later); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "b/new.txt"), "n")
	write(t, filepath.Join(root, "a/new.txt"), "n")
	write(t, filepath.Join(root, "skip.tmp"), "ignored")

	plan, err := Diff(root, repo)

	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a/new.txt", "b/new.txt", "noid.txt"}; !reflect.DeepEqual(plan.New, want) {
		t.Errorf("New = %v, want %v", plan.New, want)
	}
	if want := []string{"mtime.txt", "size.txt"}; !reflect.DeepEqual(plan.Changed, want) {
		t.Errorf("Changed = %v, want %v", plan.Changed, want)
	}
	if want := []string{"gone.txt"}; !reflect.DeepEqual(plan.Missing, want) {
		t.Errorf("Missing = %v, want %v", plan.Missing, want)
	}
	if plan.Unchanged != 1 || plan.Ignored != 1 || plan.Queued() != 5 {
		t.Errorf("plan = %+v", plan)
	}
	if n, _ := repo.CountJobs(context.Background()); n != 0 {
		t.Errorf("Diff queued %d jobs, it must write nothing", n)
	}
}

func TestDiffEmptyDatabaseEverythingIsNew(t *testing.T) {
	root := syncTree(t)
	plan, err := Diff(root, openRepo(t))
	if err != nil || !reflect.DeepEqual(plan.New, []string{"a.txt", "a/b.txt", "z.txt"}) || plan.Unchanged != 0 {
		t.Fatalf("plan = %+v, err = %v", plan, err)
	}
}

func TestDiffBadRoot(t *testing.T) {
	if _, err := Diff(filepath.Join(t.TempDir(), "nope"), openRepo(t)); err == nil {
		t.Error("want an error for a missing root")
	}
}

func TestReconcileQueuesOnlyNewAndChanged(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repo := openRepo(t)
	write(t, filepath.Join(root, "same.txt"), "same")
	write(t, filepath.Join(root, "edit.txt"), "v1")
	synced(t, repo, root, "same.txt")
	synced(t, repo, root, "edit.txt")
	write(t, filepath.Join(root, "edit.txt"), "v2, longer")
	write(t, filepath.Join(root, "fresh.txt"), "new")

	var plan Plan
	var err error
	for range 3 { // again and again is still one job per path
		if plan, err = Reconcile(ctx, root, repo); err != nil {
			t.Fatal(err)
		}
	}
	got := queued(t, repo)
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"edit.txt", "fresh.txt"}) {
		t.Errorf("queued %v, want edit.txt and fresh.txt only", got)
	}
	if plan.Unchanged != 1 {
		t.Errorf("plan = %+v", plan)
	}
}

func TestReconcileUnchangedTreeQueuesNothing(t *testing.T) {
	root := syncTree(t)
	repo := openRepo(t)
	for _, rel := range []string{"a.txt", "a/b.txt", "z.txt"} {
		synced(t, repo, root, rel)
	}
	plan, err := Reconcile(context.Background(), root, repo)
	if err != nil || plan.Queued() != 0 || plan.Unchanged != 3 {
		t.Fatalf("plan = %+v, err = %v", plan, err)
	}
	if n, _ := repo.CountJobs(context.Background()); n != 0 {
		t.Errorf("%d jobs queued", n)
	}
}

func TestReconcileClearsStaleErrorRows(t *testing.T) {
	ctx := context.Background()
	root := syncTree(t)
	repo := openRepo(t)
	_ = repo.RecordError(ctx, "deleted.txt", "network problem", "")
	if _, err := Reconcile(ctx, root, repo); err != nil {
		t.Fatal(err)
	}
	if got := errorsOf(t, repo); len(got) != 0 {
		t.Errorf("errors = %+v", got)
	}
}
