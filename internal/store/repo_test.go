package store

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
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
