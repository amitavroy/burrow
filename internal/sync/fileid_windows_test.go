//go:build windows

package sync

import (
	"os"
	"path/filepath"
	"testing"
)

func winID(t *testing.T, path string) uint64 {
	t.Helper()
	st, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fileID(path, st)
}

func TestWindowsFileIDStableAndDistinct(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt")
	write(t, a, "one")
	write(t, b, "two")
	id := winID(t, a)
	if id == 0 {
		t.Fatal("no file ID for an ordinary file")
	}
	if winID(t, b) == id {
		t.Error("two files share an ID")
	}
	write(t, a, "edited")
	moved := filepath.Join(dir, "moved.txt")
	if err := os.Rename(a, moved); err != nil {
		t.Fatal(err)
	}
	if got := winID(t, moved); got != id {
		t.Errorf("after edit and rename: %d, want %d", got, id)
	}
	link := filepath.Join(dir, "link.txt")
	if err := os.Link(moved, link); err == nil && winID(t, link) != id {
		t.Error("a hard link must share the ID")
	}
}

func TestWindowsFileIDOfAMissingFileIsUnknown(t *testing.T) {
	if got := fileID(filepath.Join(t.TempDir(), "nope"), nil); got != 0 {
		t.Errorf("ID of a missing file = %d, want 0", got)
	}
}
