package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amitavroy/burrow/internal/drive"
)

var spec = serviceSpec{Exe: "/opt/my apps/syncd", Root: "/home/me/My Sync", WorkDir: "/home/me/burrow"}

func TestSystemdUnit(t *testing.T) {
	u := systemdUnit(spec)
	for _, want := range []string{
		`ExecStart="/opt/my apps/syncd" run --root "/home/me/My Sync"`,
		"WorkingDirectory=/home/me/burrow",
		"Restart=on-failure",
		"RestartSec=60",
		"WantedBy=default.target",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("unit lacks %q:\n%s", want, u)
		}
	}
	// Characters systemd would interpret are escaped.
	odd := systemdUnit(serviceSpec{Exe: "/b/syncd", Root: `/r/100%/a"b$c\d`, WorkDir: "/w/50%"})
	if !strings.Contains(odd, `--root "/r/100%%/a\"b$$c\\d"`) || !strings.Contains(odd, "WorkingDirectory=/w/50%%") {
		t.Errorf("unit = %s", odd)
	}
}

func TestLaunchdPlistIsValidXMLWithEscapedPaths(t *testing.T) {
	p := launchdPlist(serviceSpec{Exe: "/Apps/syncd", Root: "/Users/me/R&D <files>", WorkDir: "/Users/me/burrow"})
	d := xml.NewDecoder(strings.NewReader(p))
	for {
		if _, err := d.Token(); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("plist is not well-formed XML: %v\n%s", err, p)
		}
	}
	for _, want := range []string{
		"<string>dev.burrow.syncd</string>",
		"<string>/Apps/syncd</string>",
		"<string>/Users/me/R&amp;D &lt;files&gt;</string>",
		"<key>RunAtLoad</key><true/>",
		"<key>SuccessfulExit</key><false/>",
		"<key>ThrottleInterval</key><integer>60</integer>",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("plist lacks %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "R&D") {
		t.Error("unescaped ampersand")
	}
}

func TestWindowsCommand(t *testing.T) {
	got := windowsCommand(serviceSpec{Exe: `C:\Apps\syncd.exe`, Root: `D:\My Sync`, WorkDir: `C:\burrow`})
	want := `cmd /c cd /d "C:\burrow" && "C:\Apps\syncd.exe" run --root "D:\My Sync"`
	if got != want {
		t.Errorf("command = %q, want %q", got, want)
	}
}

// fakeOS points the service code at a temp config dir and records the commands
// it would have run, so no test touches a real service manager.
type fakeOS struct {
	cmds    []string
	failOn  string
	config  string
	cwd     string
	envFile bool
}

func useFakeOS(t *testing.T, goosName string) *fakeOS {
	t.Helper()
	f := &fakeOS{config: t.TempDir(), cwd: t.TempDir(), envFile: true}
	oldGOOS, oldCfg, oldWd, oldExe, oldRun, oldHome, oldTmp := goos, userConfigDir, getwd, executable, runCommand, homeDir, tempDir
	t.Cleanup(func() {
		goos, userConfigDir, getwd, executable, runCommand, homeDir, tempDir = oldGOOS, oldCfg, oldWd, oldExe, oldRun, oldHome, oldTmp
	})
	goos = goosName
	tempDir = func() string { return "/nonexistent-tmp" }
	userConfigDir = func() (string, error) { return f.config, nil }
	homeDir = func() (string, error) { return f.config, nil }
	getwd = func() (string, error) { return f.cwd, nil }
	executable = func() (string, error) { return "/opt/syncd", nil }
	runCommand = func(name string, args ...string) (string, error) {
		line := name + " " + strings.Join(args, " ")
		f.cmds = append(f.cmds, line)
		if f.failOn != "" && strings.Contains(line, f.failOn) {
			return "boom", errors.New("exit status 1")
		}
		return "ok", nil
	}
	return f
}

func (f *fakeOS) withEnv(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.cwd, ".env"), []byte("X=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestServiceInstallAndUninstallOnLinux(t *testing.T) {
	f := useFakeOS(t, "linux")
	f.withEnv(t)
	var out, errb bytes.Buffer
	root := t.TempDir()

	if code := serviceCmd([]string{"install", "--root", root}, &out, &errb); code != 0 {
		t.Fatalf("install: code %d, stderr %q", code, errb.String())
	}

	unit := filepath.Join(f.config, "systemd", "user", "syncd.service")
	b, err := os.ReadFile(unit)
	if err != nil || !strings.Contains(string(b), `ExecStart="/opt/syncd" run --root "`+root+`"`) || !strings.Contains(string(b), "WorkingDirectory="+f.cwd) {
		t.Fatalf("unit = %q, %v", b, err)
	}
	if got := strings.Join(f.cmds, "|"); got != "systemctl --user daemon-reload|systemctl --user enable --now syncd.service" {
		t.Errorf("commands = %s", got)
	}
	if !strings.Contains(out.String(), "enable-linger") {
		t.Errorf("stdout = %q, want the boot hint", out.String())
	}

	f.cmds = nil
	out.Reset()
	if code := serviceCmd([]string{"status"}, &out, &errb); code != 0 || !strings.Contains(out.String(), "enabled: ok") {
		t.Errorf("status: code %d, out %q", code, out.String())
	}

	f.cmds = nil
	if code := serviceCmd([]string{"uninstall"}, &out, &errb); code != 0 {
		t.Fatalf("uninstall: code %d, stderr %q", code, errb.String())
	}
	if _, err := os.Stat(unit); err == nil {
		t.Error("unit file still there")
	}
	if got := strings.Join(f.cmds, "|"); got != "systemctl --user disable --now syncd.service|systemctl --user daemon-reload" {
		t.Errorf("commands = %s", got)
	}
	// Uninstalling again, and status afterwards, behave.
	if code := serviceCmd([]string{"uninstall"}, &out, &errb); code != 0 {
		t.Errorf("second uninstall: code %d", code)
	}
	errb.Reset()
	if code := serviceCmd([]string{"status"}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "not installed") {
		t.Errorf("status after uninstall: code %d, stderr %q", code, errb.String())
	}
}

func TestServiceInstallCreatesAMissingRootAndKeepsAnExistingOne(t *testing.T) {
	f := useFakeOS(t, "linux")
	f.withEnv(t)
	var out, errb bytes.Buffer
	root := filepath.Join(t.TempDir(), "new", "MySync")

	if code := serviceCmd([]string{"install", "--root", root}, &out, &errb); code != 0 {
		t.Fatalf("code %d, stderr %q", code, errb.String())
	}
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		t.Errorf("root not created: %v", err)
	}
	if !strings.Contains(out.String(), "created "+root) {
		t.Errorf("stdout = %q, want a 'created' line", out.String())
	}

	// An existing folder, with content, is left alone and not announced.
	if err := os.WriteFile(filepath.Join(root, "keep.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := serviceCmd([]string{"install", "--root", root}, &out, &errb); code != 0 || strings.Contains(out.String(), "created") {
		t.Errorf("second install: code %d, stdout %q", code, out.String())
	}
	if _, err := os.Stat(filepath.Join(root, "keep.txt")); err != nil {
		t.Error("existing content touched")
	}

	// A file where the root should be is an error, and nothing is registered.
	file := filepath.Join(t.TempDir(), "afile")
	_ = os.WriteFile(file, []byte("x"), 0o644)
	f.cmds, errb = nil, bytes.Buffer{}
	if code := serviceCmd([]string{"install", "--root", filepath.Join(file, "sub")}, &out, &errb); code != 1 || len(f.cmds) != 0 {
		t.Errorf("root under a file: code %d, commands %v, stderr %q", code, f.cmds, errb.String())
	}
}

func TestServiceInstallOnMacAndWindows(t *testing.T) {
	f := useFakeOS(t, "darwin")
	f.withEnv(t)
	var out, errb bytes.Buffer
	macRoot := t.TempDir()
	if code := serviceCmd([]string{"install", "--root", macRoot}, &out, &errb); code != 0 {
		t.Fatalf("darwin install: %d %q", code, errb.String())
	}
	plist := filepath.Join(f.config, "Library", "LaunchAgents", "dev.burrow.syncd.plist")
	if b, err := os.ReadFile(plist); err != nil || !strings.Contains(string(b), macRoot) {
		t.Fatalf("plist: %q, %v", b, err)
	}
	if len(f.cmds) != 2 || !strings.HasPrefix(f.cmds[0], "launchctl bootout") || !strings.HasPrefix(f.cmds[1], "launchctl bootstrap gui/") || !strings.HasSuffix(f.cmds[1], "dev.burrow.syncd.plist") {
		t.Errorf("commands = %v", f.cmds)
	}
	f.cmds = nil
	if code := serviceCmd([]string{"uninstall"}, &out, &errb); code != 0 || len(f.cmds) != 2 { // the daemon and the tray agent
		t.Errorf("darwin uninstall: code %d, commands %v", code, f.cmds)
	}
	if _, err := os.Stat(plist); err == nil {
		t.Error("plist still there")
	}

	f = useFakeOS(t, "windows")
	f.withEnv(t)
	if code := serviceCmd([]string{"install", "--root", t.TempDir()}, &out, &errb); code != 0 {
		t.Fatalf("windows install: %d %q", code, errb.String())
	}
	if len(f.cmds) != 1 || !strings.HasPrefix(f.cmds[0], `reg add HKCU\Software\Microsoft\Windows\CurrentVersion\Run /v syncd`) || !strings.Contains(f.cmds[0], "/f") {
		t.Errorf("commands = %v", f.cmds)
	}
}

func TestServiceInstallRefusals(t *testing.T) {
	var out, errb bytes.Buffer

	f := useFakeOS(t, "linux") // no .env in the working directory
	if code := serviceCmd([]string{"install"}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "no .env") || len(f.cmds) != 0 {
		t.Errorf("no .env: code %d, stderr %q, commands %v", code, errb.String(), f.cmds)
	}

	// A release build has the client baked in, so no .env is needed.
	oldID, oldSecret := drive.ClientID, drive.ClientSecret
	drive.ClientID, drive.ClientSecret = "id", "secret"
	errb.Reset()
	if code := serviceCmd([]string{"install"}, &out, &errb); strings.Contains(errb.String(), "no .env") {
		t.Errorf("baked-in client still needs .env: code %d, stderr %q", code, errb.String())
	}
	drive.ClientID, drive.ClientSecret = oldID, oldSecret
	f.cmds = nil

	f.withEnv(t)
	f.failOn = "enable --now"
	errb.Reset()
	if code := serviceCmd([]string{"install"}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "boom") {
		t.Errorf("failing systemctl: code %d, stderr %q", code, errb.String())
	}

	useFakeOS(t, "plan9")
	errb.Reset()
	if code := serviceCmd([]string{"install"}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "not supported on plan9") {
		t.Errorf("unsupported: code %d, stderr %q", code, errb.String())
	}

	for _, args := range [][]string{{}, {"restart"}, {"install", "x"}, {"uninstall", "x"}, {"status", "x"}} {
		errb.Reset()
		if code := serviceCmd(args, &out, &errb); code != 2 || !strings.Contains(errb.String(), "usage: syncd service") {
			t.Errorf("%v: code %d, stderr %q", args, code, errb.String())
		}
	}
}

// withTray puts a syncui next to the fake syncd, as in a release archive.
func (f *fakeOS) withTray(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	name := "syncui"
	if goos == "windows" {
		name += ".exe"
	}
	tray := filepath.Join(dir, name)
	if err := os.WriteFile(tray, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	executable = func() (string, error) { return filepath.Join(dir, "syncd"), nil }
	return tray
}

func TestTrayAutostartOnLinux(t *testing.T) {
	f := useFakeOS(t, "linux")
	f.withEnv(t)
	tray := f.withTray(t)
	var out, errb bytes.Buffer

	if code := serviceCmd([]string{"install", "--root", t.TempDir()}, &out, &errb); code != 0 {
		t.Fatalf("install: code %d, stderr %q", code, errb.String())
	}
	entry := filepath.Join(f.config, "autostart", "burrow-syncui.desktop")
	b, err := os.ReadFile(entry)
	if err != nil || !strings.Contains(string(b), `Exec="`+tray+`"`) || !strings.Contains(string(b), "Type=Application") {
		t.Fatalf("autostart entry = %q, %v", b, err)
	}

	if code := serviceCmd([]string{"uninstall"}, &out, &errb); code != 0 {
		t.Fatalf("uninstall: code %d, stderr %q", code, errb.String())
	}
	if _, err := os.Stat(entry); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("autostart entry still there: %v", err)
	}
}

func TestTrayAutostartOnMacAndWindows(t *testing.T) {
	f := useFakeOS(t, "darwin")
	f.withEnv(t)
	tray := f.withTray(t)
	var out, errb bytes.Buffer
	if code := serviceCmd([]string{"install", "--root", t.TempDir()}, &out, &errb); code != 0 {
		t.Fatalf("mac install: code %d, stderr %q", code, errb.String())
	}
	plist := filepath.Join(f.config, "Library", "LaunchAgents", "dev.burrow.syncui.plist")
	b, err := os.ReadFile(plist)
	if err != nil || !strings.Contains(string(b), "<string>"+tray+"</string>") || !strings.Contains(string(b), "<key>RunAtLoad</key><true/>") {
		t.Fatalf("tray plist = %q, %v", b, err)
	}
	if !strings.Contains(strings.Join(f.cmds, "\n"), "launchctl bootstrap") {
		t.Errorf("commands = %v", f.cmds)
	}

	f = useFakeOS(t, "windows")
	f.withEnv(t)
	tray = f.withTray(t)
	out.Reset()
	if code := serviceCmd([]string{"install", "--root", t.TempDir()}, &out, &errb); code != 0 {
		t.Fatalf("windows install: code %d, stderr %q", code, errb.String())
	}
	if want := `reg add ` + windowsRunKey + ` /v syncui /t REG_SZ /d "` + tray + `" /f`; !strings.Contains(strings.Join(f.cmds, "\n"), want) {
		t.Errorf("commands = %v, want %q", f.cmds, want)
	}
	f.cmds = nil
	serviceCmd([]string{"uninstall"}, &out, &errb)
	if !strings.Contains(strings.Join(f.cmds, "\n"), "reg delete "+windowsRunKey+" /v syncui") {
		t.Errorf("uninstall commands = %v", f.cmds)
	}
}

func TestNoTrayBinaryIsAHintNotAnError(t *testing.T) {
	f := useFakeOS(t, "linux") // the fake syncd has no syncui beside it
	f.withEnv(t)
	var out, errb bytes.Buffer
	if code := serviceCmd([]string{"install", "--root", t.TempDir()}, &out, &errb); code != 0 || !strings.Contains(out.String(), "no syncui next to syncd") {
		t.Errorf("code %d, stdout %q, stderr %q", code, out.String(), errb.String())
	}
	if _, err := os.Stat(filepath.Join(f.config, "autostart")); err == nil {
		t.Error("autostart dir created without a tray binary")
	}
}

func TestServiceInstallRefusesATempBinary(t *testing.T) {
	f := useFakeOS(t, "linux")
	f.withEnv(t)
	tempDir = func() string { return "/tmp" }
	executable = func() (string, error) { return "/tmp/x/syncd", nil }
	var out, errb bytes.Buffer
	if code := serviceCmd([]string{"install"}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "temp folder") || len(f.cmds) != 0 {
		t.Errorf("code %d, stderr %q, commands %v", code, errb.String(), f.cmds)
	}
}

func TestUninstallPurge(t *testing.T) {
	f := useFakeOS(t, "linux")
	data := t.TempDir()
	if err := os.WriteFile(filepath.Join(data, "burrow.db"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldData, oldOut := dataDir, signOut
	t.Cleanup(func() { dataDir, signOut = oldData, oldOut })
	dataDir = func() string { return data }
	signedOut := false
	signOut = func(io.Writer, io.Writer) int { signedOut = true; return 0 }

	var out, errb bytes.Buffer
	if code := uninstallCmd(nil, &out, &errb); code != 0 || signedOut {
		t.Fatalf("plain uninstall: code %d, signedOut %v, stderr %q", code, signedOut, errb.String())
	}
	if _, err := os.Stat(data); err != nil {
		t.Errorf("data dir removed without --purge: %v", err)
	}
	if !strings.Contains(strings.Join(f.cmds, "\n"), "pkill -x syncui") {
		t.Errorf("tray not stopped: %v", f.cmds)
	}

	if code := uninstallCmd([]string{"--purge"}, &out, &errb); code != 0 || !signedOut {
		t.Fatalf("purge: code %d, signedOut %v, stderr %q", code, signedOut, errb.String())
	}
	if _, err := os.Stat(data); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("data dir still there: %v", err)
	}
	if code := uninstallCmd([]string{"x"}, &out, &errb); code != 2 {
		t.Errorf("extra arg: code %d", code)
	}
}
