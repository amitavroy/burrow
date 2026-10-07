package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func openTemp(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nested", "burrow.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db, path
}

func TestOpenCreatesFile(t *testing.T) {
	_, path := openTemp(t)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database file missing: %v", err)
	}
	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("dir mode = %o, want 700", perm)
	}
}

func TestOpenPragmas(t *testing.T) {
	db, _ := openTemp(t)
	for pragma, want := range map[string]string{
		"journal_mode": "wal",
		"foreign_keys": "1",
		"busy_timeout": "5000",
		"synchronous":  "1", // NORMAL
	} {
		var got string
		if err := db.QueryRow("PRAGMA " + pragma).Scan(&got); err != nil {
			t.Fatalf("PRAGMA %s: %v", pragma, err)
		}
		if got != want {
			t.Errorf("PRAGMA %s = %q, want %q", pragma, got, want)
		}
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "burrow.db")
	for i := 0; i < 2; i++ {
		db, err := Open(path)
		if err != nil {
			t.Fatalf("Open #%d: %v", i+1, err)
		}
		var version int
		if err := db.QueryRow("SELECT MAX(version_id) FROM goose_db_version").Scan(&version); err != nil {
			t.Fatalf("read version: %v", err)
		}
		if version != 2 {
			t.Errorf("open #%d: version = %d, want 2", i+1, version)
		}
		db.Close()
	}
}

func TestFilesTableColumns(t *testing.T) {
	db, _ := openTemp(t)
	rows, err := db.Query("SELECT name FROM pragma_table_info('files') ORDER BY cid")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		got = append(got, name)
	}
	want := []string{"id", "rel_path", "drive_file_id", "size", "mtime", "inode",
		"local_md5", "synced_md5", "base_md5", "base_revision_id"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("files columns = %v, want %v", got, want)
	}
}

func TestFoldersTableColumns(t *testing.T) {
	db, _ := openTemp(t)
	rows, err := db.Query("SELECT name, pk FROM pragma_table_info('folders') ORDER BY cid")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var name string
		var pk int
		if err := rows.Scan(&name, &pk); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s:%d", name, pk))
	}
	want := []string{"rel_dir:1", "drive_folder_id:0"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("folders columns (name:pk) = %v, want %v", got, want)
	}
}

func TestFilesRelPathIsUnique(t *testing.T) {
	db, _ := openTemp(t)
	if _, err := db.Exec("INSERT INTO files (rel_path) VALUES ('a/b.txt')"); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	_, err := db.Exec("INSERT INTO files (rel_path) VALUES ('a/b.txt')")
	if err == nil || !strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("duplicate rel_path: err = %v, want a UNIQUE violation", err)
	}
}

func TestStatus(t *testing.T) {
	db, _ := openTemp(t)
	info, err := Status(db)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if info.Version != 2 {
		t.Errorf("Version = %d, want 2", info.Version)
	}
	want := []string{"files", "folders", "goose_db_version"}
	if !reflect.DeepEqual(info.Tables, want) {
		t.Errorf("Tables = %v, want %v", info.Tables, want)
	}
}
