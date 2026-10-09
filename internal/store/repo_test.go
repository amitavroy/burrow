package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func openTempRepo(t *testing.T) (*Repo, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "burrow.db")
	r, err := OpenRepo(path)
	if err != nil {
		t.Fatalf("OpenRepo: %v", err)
	}
	t.Cleanup(func() { r.Close() })
	return r, path
}

func fullFile() File {
	return File{
		RelPath:        "docs/a.txt",
		DriveFileID:    "drive-1",
		Size:           42,
		MTime:          1_700_000_000_123_456_789,
		Inode:          9876543210,
		LocalMD5:       "aaa",
		SyncedMD5:      "bbb",
		BaseMD5:        "ccc",
		BaseRevisionID: "rev-1",
	}
}

func TestRepoRoundTrip(t *testing.T) {
	r, _ := openTempRepo(t)
	ctx := context.Background()
	want := fullFile()
	if err := r.Upsert(ctx, want); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := r.Get(ctx, want.RelPath)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Get = %+v, want %+v", got, want)
	}
}

func TestRepoUpsertUpdatesSameRow(t *testing.T) {
	r, path := openTempRepo(t)
	ctx := context.Background()
	f := fullFile()
	if err := r.Upsert(ctx, f); err != nil {
		t.Fatal(err)
	}
	f.Size = 99
	f.SyncedMD5 = "new"
	if err := r.Upsert(ctx, f); err != nil {
		t.Fatal(err)
	}
	got, err := r.Get(ctx, f.RelPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.Size != 99 || got.SyncedMD5 != "new" {
		t.Errorf("Get = %+v, want updated values", got)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, path); n != 1 {
		t.Errorf("rows = %d, want 1", n)
	}
}

func TestRepoGetMissing(t *testing.T) {
	r, _ := openTempRepo(t)
	_, err := r.Get(context.Background(), "nope")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Get missing = %v, want ErrNotFound", err)
	}
}

func TestRepoNullMapping(t *testing.T) {
	r, path := openTempRepo(t)
	ctx := context.Background()
	want := File{RelPath: "plain.txt", Size: 1, MTime: 2}
	if err := r.Upsert(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := r.Get(ctx, "plain.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Get = %+v, want %+v", got, want)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var nulls int
	err = db.QueryRow(`SELECT (drive_file_id IS NULL) + (inode IS NULL) + (local_md5 IS NULL) +
		(synced_md5 IS NULL) + (base_md5 IS NULL) + (base_revision_id IS NULL) FROM files`).Scan(&nulls)
	if err != nil {
		t.Fatal(err)
	}
	if nulls != 6 {
		t.Errorf("NULL columns = %d, want 6", nulls)
	}
}

func TestRepoUseAfterClose(t *testing.T) {
	r, _ := openTempRepo(t)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.Upsert(context.Background(), fullFile()); !errors.Is(err, ErrClosed) {
		t.Errorf("Upsert after Close = %v, want ErrClosed", err)
	}
	if _, err := r.Get(context.Background(), "x"); !errors.Is(err, ErrClosed) {
		t.Errorf("Get after Close = %v, want ErrClosed", err)
	}
}

func countRows(t *testing.T, path string) int {
	t.Helper()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM files").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRepoGetByDriveID(t *testing.T) {
	r, _ := openTempRepo(t)
	ctx := context.Background()
	want := fullFile()
	if err := r.Upsert(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := r.GetByDriveID(ctx, want.DriveFileID)
	if err != nil {
		t.Fatalf("GetByDriveID: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("GetByDriveID = %+v, want %+v", got, want)
	}
	if _, err := r.GetByDriveID(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetByDriveID missing = %v, want ErrNotFound", err)
	}
}

func TestRepoListOrderedByRelPath(t *testing.T) {
	r, _ := openTempRepo(t)
	ctx := context.Background()
	got, err := r.List(ctx)
	if err != nil || len(got) != 0 {
		t.Fatalf("List empty = %v, %v; want no rows", got, err)
	}
	for _, p := range []string{"b.txt", "a/z.txt", "a.txt"} {
		if err := r.Upsert(ctx, File{RelPath: p}); err != nil {
			t.Fatal(err)
		}
	}
	got, err = r.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, f := range got {
		paths = append(paths, f.RelPath)
	}
	if want := []string{"a.txt", "a/z.txt", "b.txt"}; !reflect.DeepEqual(paths, want) {
		t.Errorf("List order = %v, want %v", paths, want)
	}
}

func TestRepoDriveIDUnique(t *testing.T) {
	r, _ := openTempRepo(t)
	ctx := context.Background()
	// Any number of rows without a Drive ID is fine.
	for _, p := range []string{"a", "b", "c"} {
		if err := r.Upsert(ctx, File{RelPath: p}); err != nil {
			t.Fatalf("Upsert without drive id: %v", err)
		}
	}
	if err := r.Upsert(ctx, File{RelPath: "a", DriveFileID: "d1"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Upsert(ctx, File{RelPath: "b", DriveFileID: "d1"}); err == nil {
		t.Error("duplicate drive_file_id accepted, want error")
	}
}

func TestRepoDelete(t *testing.T) {
	r, _ := openTempRepo(t)
	ctx := context.Background()
	for _, p := range []string{"a", "b"} {
		if err := r.Upsert(ctx, File{RelPath: p}); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Delete(ctx, "a"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := r.Get(ctx, "a"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after Delete = %v, want ErrNotFound", err)
	}
	files, err := r.List(ctx)
	if err != nil || len(files) != 1 || files[0].RelPath != "b" {
		t.Errorf("List after Delete = %v, %v; want only b", files, err)
	}
	if err := r.Delete(ctx, "a"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second Delete = %v, want ErrNotFound", err)
	}
}

func TestRepoConcurrentUpsertGet(t *testing.T) {
	r, _ := openTempRepo(t)
	ctx := context.Background()
	const n = 50
	var wg sync.WaitGroup
	errs := make(chan error, n*2)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := fmt.Sprintf("dir/file-%02d.txt", i)
			if err := r.Upsert(ctx, File{RelPath: p, Size: int64(i)}); err != nil {
				errs <- err
				return
			}
			got, err := r.Get(ctx, p)
			if err != nil {
				errs <- err
				return
			}
			if got.Size != int64(i) {
				errs <- fmt.Errorf("%s: size = %d, want %d", p, got.Size, i)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	files, err := r.List(ctx)
	if err != nil || len(files) != n {
		t.Errorf("List = %d rows, %v; want %d", len(files), err, n)
	}
}

// block occupies the owner goroutine until release is closed and returns once
// the owner is running it.
func block(t *testing.T, r *Repo) (release func()) {
	t.Helper()
	started := make(chan struct{})
	gate := make(chan struct{})
	go r.do(context.Background(), func(*sql.DB) (any, error) {
		close(started)
		<-gate
		return nil, nil
	})
	<-started
	return func() { close(gate) }
}

func TestRepoCancelledContextWhileSending(t *testing.T) {
	r, _ := openTempRepo(t)
	release := block(t, r)
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() { done <- r.Upsert(ctx, fullFile()) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Upsert = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Upsert with cancelled context hung")
	}
}

func TestRepoCancelledContextWhileWaiting(t *testing.T) {
	r, _ := openTempRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	gate := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := r.do(ctx, func(*sql.DB) (any, error) {
			close(started)
			<-gate
			return nil, nil
		})
		done <- err
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("do = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("call hung after cancel")
	}
	close(gate) // owner finishes its request and keeps serving
	if err := r.Upsert(context.Background(), fullFile()); err != nil {
		t.Errorf("Upsert after cancelled call: %v", err)
	}
}

func TestRepoPanicIsReturnedAndOwnerSurvives(t *testing.T) {
	r, _ := openTempRepo(t)
	ctx := context.Background()
	_, err := r.do(ctx, func(*sql.DB) (any, error) { panic("boom") })
	if err == nil {
		t.Fatal("panic in request returned nil error")
	}
	if strings.Contains(err.Error(), "boom") {
		t.Errorf("error leaks panic value: %v", err)
	}
	if err := r.Upsert(ctx, fullFile()); err != nil {
		t.Errorf("Upsert after panic: %v", err)
	}
	if _, err := r.Get(ctx, fullFile().RelPath); err != nil {
		t.Errorf("Get after panic: %v", err)
	}
}

func TestRepoCloseTwice(t *testing.T) {
	r, _ := openTempRepo(t)
	if err := r.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestRepoFolderRoundTrip(t *testing.T) {
	r, _ := openTempRepo(t)
	ctx := context.Background()

	if err := r.PutFolder(ctx, "a/b", "folder-1"); err != nil {
		t.Fatalf("PutFolder: %v", err)
	}
	got, err := r.GetFolder(ctx, "a/b")
	if err != nil || got != "folder-1" {
		t.Fatalf("GetFolder = %q, %v; want folder-1", got, err)
	}
}

func TestRepoPutFolderOverwrites(t *testing.T) {
	r, path := openTempRepo(t)
	ctx := context.Background()
	for _, id := range []string{"old", "new"} {
		if err := r.PutFolder(ctx, "a", id); err != nil {
			t.Fatalf("PutFolder(%s): %v", id, err)
		}
	}
	if got, err := r.GetFolder(ctx, "a"); err != nil || got != "new" {
		t.Fatalf("GetFolder = %q, %v; want new", got, err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM folders`).Scan(&n); err != nil || n != 1 {
		t.Errorf("folders rows = %d, %v; want 1", n, err)
	}
}

func TestRepoGetFolderMissing(t *testing.T) {
	r, _ := openTempRepo(t)
	if _, err := r.GetFolder(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestRepoFolderUseAfterClose(t *testing.T) {
	r, _ := openTempRepo(t)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := r.PutFolder(ctx, "a", "x"); !errors.Is(err, ErrClosed) {
		t.Errorf("PutFolder after Close: err = %v, want ErrClosed", err)
	}
	if _, err := r.GetFolder(ctx, "a"); !errors.Is(err, ErrClosed) {
		t.Errorf("GetFolder after Close: err = %v, want ErrClosed", err)
	}
}

func TestRepoConcurrentFolders(t *testing.T) {
	r, _ := openTempRepo(t)
	ctx := context.Background()
	const n = 50
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dir := fmt.Sprintf("dir/sub-%02d", i)
			id := fmt.Sprintf("folder-%02d", i)
			if err := r.PutFolder(ctx, dir, id); err != nil {
				errs <- err
				return
			}
			got, err := r.GetFolder(ctx, dir)
			if err != nil {
				errs <- err
				return
			}
			if got != id {
				errs <- fmt.Errorf("%s: id = %s, want %s", dir, got, id)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestRepoRecordUploadAndRevisions(t *testing.T) {
	ctx := context.Background()
	r, _ := openTempRepo(t)
	f := fullFile()
	rev := func(id string, at int64) Revision {
		return Revision{DriveFileID: f.DriveFileID, RevisionID: id, MD5: "m-" + id, Size: 10, Time: at, Source: "upload"}
	}

	if err := r.RecordUpload(ctx, f, rev("r1", 100)); err != nil {
		t.Fatalf("RecordUpload r1: %v", err)
	}
	f.BaseRevisionID = "r2"
	if err := r.RecordUpload(ctx, f, rev("r2", 200)); err != nil {
		t.Fatalf("RecordUpload r2: %v", err)
	}
	// Same revision again: ignored, row still updated.
	if err := r.RecordUpload(ctx, f, rev("r2", 300)); err != nil {
		t.Fatalf("RecordUpload dup: %v", err)
	}

	got, err := r.Revisions(ctx, f.RelPath)
	if err != nil {
		t.Fatalf("Revisions: %v", err)
	}
	want := []Revision{rev("r2", 200), rev("r1", 100)}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Revisions = %+v, want %+v", got, want)
	}
	row, _ := r.Get(ctx, f.RelPath)
	if row.BaseRevisionID != "r2" {
		t.Errorf("BaseRevisionID = %q, want r2", row.BaseRevisionID)
	}

	// Renaming the row keeps the history: lookup follows the Drive ID.
	moved := f
	moved.RelPath = "docs/b.txt"
	if err := r.Delete(ctx, f.RelPath); err != nil {
		t.Fatal(err)
	}
	if err := r.Upsert(ctx, moved); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.Revisions(ctx, "docs/b.txt"); len(got) != 2 {
		t.Errorf("after rename got %d revisions, want 2", len(got))
	}
	if got, _ := r.Revisions(ctx, "nope"); len(got) != 0 {
		t.Errorf("unknown path got %d revisions, want 0", len(got))
	}
}

func TestRepoRecordUploadIsAtomic(t *testing.T) {
	ctx := context.Background()
	r, _ := openTempRepo(t)
	// failing revision insert: drop the table.
	if _, err := r.do(ctx, func(db *sql.DB) (any, error) { return db.Exec(`DROP TABLE file_revisions`) }); err != nil {
		t.Fatal(err)
	}
	f := fullFile()
	if err := r.RecordUpload(ctx, f, Revision{DriveFileID: f.DriveFileID, RevisionID: "r1", Source: "upload"}); err == nil {
		t.Fatal("RecordUpload succeeded without file_revisions")
	}
	if _, err := r.Get(ctx, f.RelPath); !errors.Is(err, ErrNotFound) {
		t.Errorf("files row written despite failed revision insert: %v", err)
	}
}
