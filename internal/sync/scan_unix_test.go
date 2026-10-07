//go:build unix

package sync

import (
	"path/filepath"
	"syscall"
	"testing"
)

func TestScanSkipsFIFO(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "real.txt"), "x")
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0o644); err != nil {
		t.Skip("mkfifo not supported:", err)
	}

	res, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 1 || res.Entries[0].RelPath != "real.txt" {
		t.Fatalf("entries = %v, want only real.txt", res.Entries)
	}
}
