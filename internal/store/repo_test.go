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
