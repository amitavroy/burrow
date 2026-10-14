package autostart

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type rig struct {
	env  Env
	cmds []string
	out  string // what a command returns
}

func newRig(t *testing.T, goos string) *rig {
	t.Helper()
	r := &rig{}
	cfg := t.TempDir()
	r.env = Env{
		GOOS: goos, ConfigDir: func() (string, error) { return cfg, nil }, HomeDir: func() (string, error) { return cfg, nil },
		SystemDir: t.TempDir(), UID: 1000,
		Run: func(name string, args ...string) (string, error) {
			r.cmds = append(r.cmds, name+" "+strings.Join(args, " "))
			if r.out == "fail" {
				return "", errors.New("exit status 1")
			}
			return r.out, nil
		},
	}
	return r
}

func (r *rig) userEntry() string {
	dir, _ := r.env.ConfigDir()
	return filepath.Join(dir, "autostart", DesktopFile)
}

func TestEnsureWritesTheLinuxEntryOnceAndLeavesItAlone(t *testing.T) {
	r := newRig(t, "linux")
	changed, err := r.env.Ensure("/opt/burrow/syncui")
	if err != nil || !changed {
		t.Fatalf("first Ensure = %v, %v", changed, err)
	}
	b, _ := os.ReadFile(r.userEntry())
	if !strings.Contains(string(b), `Exec="/opt/burrow/syncui"`) {
		t.Errorf("entry = %q", b)
	}
	if changed, err = r.env.Ensure("/opt/burrow/syncui"); err != nil || changed {
		t.Errorf("second Ensure = %v, %v; want no change", changed, err)
	}
	// The binary moved: the entry follows it.
	if changed, err = r.env.Ensure("/home/me/bin/syncui"); err != nil || !changed {
		t.Errorf("moved Ensure = %v, %v", changed, err)
	}
	if b, _ = os.ReadFile(r.userEntry()); !strings.Contains(string(b), "/home/me/bin/syncui") {
		t.Errorf("entry after move = %q", b)
	}
}

// The .deb's own entry already starts /usr/bin/syncui; a same-named user file would only shadow it.
func TestEnsureLeavesThePackageEntryAlone(t *testing.T) {
	r := newRig(t, "linux")
	if err := os.WriteFile(filepath.Join(r.env.SystemDir, DesktopFile), []byte("[Desktop Entry]\nExec=/usr/bin/syncui\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if changed, err := r.env.Ensure("/usr/bin/syncui"); err != nil || changed {
		t.Fatalf("Ensure = %v, %v", changed, err)
	}
	if _, err := os.Stat(r.userEntry()); !errors.Is(err, os.ErrNotExist) {
		t.Error("a user entry shadows the package entry")
	}
	// A different binary than the package's gets its own entry.
	if changed, _ := r.env.Ensure("/home/me/bin/syncui"); !changed {
		t.Error("a tray the package does not start was not registered")
	}
}

func TestEnsureRespectsHiddenTrue(t *testing.T) {
	r := newRig(t, "linux")
	if err := os.MkdirAll(filepath.Dir(r.userEntry()), 0o755); err != nil {
		t.Fatal(err)
	}
	mine := "[Desktop Entry]\nExec=/usr/bin/syncui\nHidden=true\n"
	if err := os.WriteFile(r.userEntry(), []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}
	if changed, err := r.env.Ensure("/usr/bin/syncui"); err != nil || changed {
		t.Fatalf("Ensure = %v, %v", changed, err)
	}
	if b, _ := os.ReadFile(r.userEntry()); string(b) != mine {
		t.Errorf("opt-out overwritten: %q", b)
	}
}

func TestUninstallRemovesTheLinuxEntryAndIsRepeatable(t *testing.T) {
	r := newRig(t, "linux")
	if _, err := r.env.Install("/opt/syncui"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := r.env.Uninstall(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(r.userEntry()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("entry still there: %v", err)
	}
}

func TestEnsureOnMacWritesThePlistWithoutStartingASecondTray(t *testing.T) {
	r := newRig(t, "darwin")
	if changed, err := r.env.Ensure("/Apps/syncui"); err != nil || !changed {
		t.Fatalf("Ensure = %v, %v", changed, err)
	}
	if len(r.cmds) != 0 {
		t.Errorf("commands = %v; the tray is already running", r.cmds)
	}
	if changed, _ := r.env.Ensure("/Apps/syncui"); changed {
		t.Error("second Ensure rewrote the plist")
	}
}

func TestEnsureOnWindowsChecksTheRunValue(t *testing.T) {
	r := newRig(t, "windows")
	r.out = `    syncui    REG_SZ    "C:\Apps\syncui.exe"`
	if changed, err := r.env.Ensure(`C:\Apps\syncui.exe`); err != nil || changed {
		t.Fatalf("registered Ensure = %v, %v", changed, err)
	}
	r.out, r.cmds = "fail", nil // value missing
	if changed, _ := r.env.Ensure(`C:\Apps\syncui.exe`); changed {
		// the query failed and so did reg add: nothing was written
		t.Error("reported a change though reg add failed")
	}
	if len(r.cmds) != 2 || !strings.HasPrefix(r.cmds[1], "reg add") {
		t.Errorf("commands = %v", r.cmds)
	}
}
