package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestAccountLine(t *testing.T) {
	if got := accountLine(""); got != "Not signed in" {
		t.Errorf("got %q", got)
	}
	if got := accountLine("a@b.c"); got != "Signed in as a@b.c" {
		t.Errorf("got %q", got)
	}
}

func TestSyncdPath(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "syncui")
	if got := syncdPath(exe); got != "syncd" {
		t.Errorf("no sibling: got %q, want PATH lookup", got)
	}
	name := "syncd"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	sibling := filepath.Join(dir, name)
	if err := os.WriteFile(sibling, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := syncdPath(exe); got != sibling {
		t.Errorf("got %q, want %q", got, sibling)
	}
}
