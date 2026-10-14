package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/amitavroy/burrow/internal/autostart"
)

func TestSecondTrayDoesNotStart(t *testing.T) {
	base := filepath.Join(t.TempDir(), "syncui")
	first, ok := acquire(base)
	if !ok || first == nil {
		t.Fatalf("first acquire = %v, %v", first, ok)
	}
	if _, ok := acquire(base); ok {
		t.Error("a second tray got the lock")
	}
	first.Unlock()
	again, ok := acquire(base)
	if !ok {
		t.Fatal("lock not free after the first tray quit")
	}
	again.Unlock()
}

func TestRegisterAutostartSkipsATempFolderBinary(t *testing.T) {
	cfg := t.TempDir()
	env := autostart.Env{GOOS: "linux", ConfigDir: func() (string, error) { return cfg, nil }, SystemDir: t.TempDir()}
	tmp := t.TempDir()

	registerAutostart(env, filepath.Join(tmp, "syncui"), tmp)
	if _, err := os.Stat(filepath.Join(cfg, "autostart")); err == nil {
		t.Error("registered a binary in a temp folder")
	}
	registerAutostart(env, "/opt/burrow/syncui", tmp)
	if _, err := os.Stat(filepath.Join(cfg, "autostart", autostart.DesktopFile)); err != nil {
		t.Errorf("not registered: %v", err)
	}
}
