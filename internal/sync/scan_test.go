package sync

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScan(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "b.txt"), "bb")
	write(t, filepath.Join(root, "a", "z.txt"), "z")
	write(t, filepath.Join(root, "a", "b", "c.txt"), "ccc")
	write(t, filepath.Join(root, "a.txt"), "")
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	mt := time.Date(2026, 10, 1, 12, 0, 0, 5, time.UTC)
	if err := os.Chtimes(filepath.Join(root, "b.txt"), mt, mt); err != nil {
		t.Fatal(err)
	}

	got, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}

	var paths []string
	for _, e := range got {
		paths = append(paths, e.RelPath)
	}
	want := []string{"a.txt", "a/b/c.txt", "a/z.txt", "b.txt"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	for _, e := range got {
		if e.RelPath == "b.txt" {
			if e.Size != 2 {
				t.Errorf("size = %d, want 2", e.Size)
			}
			if e.MTime != mt.UnixNano() {
				t.Errorf("mtime = %d, want %d", e.MTime, mt.UnixNano())
			}
		}
	}
}

func TestScanSkipsSymlinks(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "real.txt"), "x")
	if err := os.Symlink("real.txt", filepath.Join(root, "link.txt")); err != nil {
		t.Skip("symlinks not supported:", err)
	}

	got, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].RelPath != "real.txt" {
		t.Fatalf("got %v, want only real.txt", got)
	}
}

func TestScanBadRoot(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	write(t, file, "x")

	for name, root := range map[string]string{"missing": filepath.Join(dir, "nope"), "file": file} {
		if _, err := Scan(root); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestScanDefaultIgnores(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs chmod 000 to make a directory unreadable")
	}
	root := t.TempDir()
	write(t, filepath.Join(root, "keep.txt"), "x")
	write(t, filepath.Join(root, "a", "keep.md"), "x")
	write(t, filepath.Join(root, ".git", "config"), "x")
	write(t, filepath.Join(root, "a", "node_modules", "pkg", "i.js"), "x")
	write(t, filepath.Join(root, "x.tmp"), "x")
	write(t, filepath.Join(root, "a", "~$draft.docx"), "x")

	// A directory that cannot be read fails the walk if it is entered, so this
	// proves an ignored directory is skipped whole rather than walked.
	locked := filepath.Join(root, "node_modules")
	if err := os.Mkdir(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })

	got, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, e := range got {
		paths = append(paths, e.RelPath)
	}
	want := []string{"a/keep.md", "keep.txt"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
}

func TestScanSyncignore(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, ".syncignore"), "*.log\n!keep.log\n")
	write(t, filepath.Join(root, "a.log"), "x")
	write(t, filepath.Join(root, "keep.log"), "x")
	write(t, filepath.Join(root, "d", "b.log"), "x")
	write(t, filepath.Join(root, "d", "c.txt"), "x")

	got, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, e := range got {
		paths = append(paths, e.RelPath)
	}
	want := []string{"d/c.txt", "keep.log"} // .syncignore itself is not listed
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
}
