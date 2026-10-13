package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/amitavroy/burrow/internal/drive"
	"github.com/amitavroy/burrow/internal/store"
)

// The names under which the daemon is registered with each OS.
const (
	systemdUnitName = "syncd.service"
	launchdLabel    = "dev.burrow.syncd"
	windowsRunValue = "syncd"

	// The tray app, started in the desktop session at login (never a system service).
	trayLaunchdLabel = "dev.burrow.syncui"
	trayDesktopFile  = "burrow-syncui.desktop"
	trayWindowsValue = "syncui"
	windowsRunKey    = `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`
)

// Replaced by tests, so they never touch the real service manager.
var (
	goos          = runtime.GOOS
	userConfigDir = os.UserConfigDir
	getwd         = os.Getwd
	executable    = func() (string, error) {
		p, err := os.Executable()
		if err != nil {
			return "", err
		}
		return filepath.EvalSymlinks(p)
	}
	tempDir    = os.TempDir
	dataDir    = func() string { return filepath.Dir(store.DefaultPath()) }
	signOut    = logout
	runCommand = func(name string, args ...string) (string, error) {
		out, err := exec.Command(name, args...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
)

// serviceSpec is what the three registrations are rendered from.
type serviceSpec struct {
	Exe     string // absolute path of this binary
	Root    string // sync root, absolute
	WorkDir string // folder holding .env (the OAuth client), which `syncd` reads from its working directory
}

// systemdQuote quotes one word for a systemd ExecStart line: backslash, quote,
// and the % and $ that systemd would otherwise expand.
func systemdQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%", "$", "$$")
	return `"` + r.Replace(s) + `"`
}

// systemdUnit is the user unit for `syncd run`. It restarts after a failure
// (a crash, a lost sign-in) at most once a minute.
func systemdUnit(s serviceSpec) string {
	return fmt.Sprintf(`[Unit]
Description=Burrow: sync a folder to Google Drive
After=network-online.target

[Service]
ExecStart=%s run --root %s
WorkingDirectory=%s
Restart=on-failure
RestartSec=60

[Install]
WantedBy=default.target
`, systemdQuote(s.Exe), systemdQuote(s.Root), strings.ReplaceAll(s.WorkDir, "%", "%%"))
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// launchdPlist is the launch agent for `syncd run`. KeepAlive restarts it only
// after a failure, throttled to once a minute; its log is the rotating file
// syncd writes itself, so the console streams are dropped.
func launchdPlist(s serviceSpec) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>run</string>
    <string>--root</string>
    <string>%s</string>
  </array>
  <key>WorkingDirectory</key><string>%s</string>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
  <key>ThrottleInterval</key><integer>60</integer>
  <key>StandardOutPath</key><string>/dev/null</string>
  <key>StandardErrorPath</key><string>/dev/null</string>
</dict>
</plist>
`, launchdLabel, xmlEscape(s.Exe), xmlEscape(s.Root), xmlEscape(s.WorkDir))
}

// windowsCommand is the Run-key command line: it changes to the .env folder
// first, since the Run key has no working directory of its own.
func windowsCommand(s serviceSpec) string {
	return fmt.Sprintf(`cmd /c cd /d "%s" && "%s" run --root "%s"`, s.WorkDir, s.Exe, s.Root)
}

// trayExe is the syncui binary that sits next to this one, or "" when the
// release or build has none (macOS releases ship syncd only).
func trayExe(exe string) string {
	name := "syncui"
	if goos == "windows" {
		name += ".exe"
	}
	p := filepath.Join(filepath.Dir(exe), name)
	if st, err := os.Stat(p); err != nil || st.IsDir() {
		return ""
	}
	return p
}

// trayDesktopEntry is the XDG autostart entry that starts the tray with the
// desktop session on Linux.
func trayDesktopEntry(exe string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", "$", `\$`)
	return fmt.Sprintf("[Desktop Entry]\nType=Application\nName=Burrow tray\nComment=Sync status for Burrow\nExec=\"%s\"\nTerminal=false\n", r.Replace(exe))
}

// trayPlist is the launch agent that starts the tray at login on macOS. It is
// not restarted: quitting the tray from its menu should stay quit.
func trayPlist(exe string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>LimitLoadToSessionType</key><string>Aqua</string>
</dict>
</plist>
`, trayLaunchdLabel, xmlEscape(exe))
}

func trayDesktopPath() (string, error) {
	dir, err := userConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "autostart", trayDesktopFile), nil
}

func trayPlistPath() (string, error) {
	home, err := homeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", trayLaunchdLabel+".plist"), nil
}

// installTray registers the tray app to start at login, when there is one next
// to syncd. It is best effort: a problem here is reported but never fails the
// service install.
func installTray(exe string, stdout, stderr io.Writer) {
	tray := trayExe(exe)
	if tray == "" {
		fmt.Fprintln(stdout, "no syncui next to syncd, so no tray icon at login (build it with `make build-ui`, then run `syncd service install` again)")
		return
	}
	var err error
	switch goos {
	case "linux":
		var path string
		if path, err = trayDesktopPath(); err == nil {
			if err = os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
				err = os.WriteFile(path, []byte(trayDesktopEntry(tray)), 0o644)
			}
		}
		if err == nil {
			fmt.Fprintf(stdout, "installed %s; the tray icon starts with your next login (start it now: %s &)\n", path, tray)
		}
	case "darwin":
		var path string
		if path, err = trayPlistPath(); err == nil {
			if err = os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
				_, _ = runCommand("launchctl", "bootout", launchdDomain()+"/"+trayLaunchdLabel)
				if err = os.WriteFile(path, []byte(trayPlist(tray)), 0o644); err == nil {
					err = run1("launchctl", "bootstrap", launchdDomain(), path)
				}
			}
		}
		if err == nil {
			fmt.Fprintf(stdout, "installed %s and started the tray icon; it starts when you log in\n", path)
		}
	case "windows":
		if err = run1("reg", "add", windowsRunKey, "/v", trayWindowsValue, "/t", "REG_SZ", "/d", `"`+tray+`"`, "/f"); err == nil {
			fmt.Fprintf(stdout, "installed %s\\%s; the tray icon starts with your next login\n", windowsRunKey, trayWindowsValue)
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "syncd: tray icon not installed: %v\n", err)
	}
}

// uninstallTray removes the tray registration; already gone is fine.
func uninstallTray() error {
	switch goos {
	case "linux":
		path, err := trayDesktopPath()
		if err == nil {
			if err = os.Remove(path); errors.Is(err, os.ErrNotExist) {
				err = nil
			}
		}
		return err
	case "darwin":
		path, err := trayPlistPath()
		if err == nil {
			_, _ = runCommand("launchctl", "bootout", launchdDomain()+"/"+trayLaunchdLabel)
			if err = os.Remove(path); errors.Is(err, os.ErrNotExist) {
				err = nil
			}
		}
		return err
	case "windows":
		_, _ = runCommand("reg", "delete", windowsRunKey, "/v", trayWindowsValue, "/f")
	}
	return nil
}

func unitPath() (string, error) {
	dir, err := userConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "systemd", "user", systemdUnitName), nil
}

func plistPath() (string, error) {
	home, err := homeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist"), nil
}

func launchdDomain() string { return fmt.Sprintf("gui/%d", os.Getuid()) }

// serviceCmd installs, removes and inspects the registration that starts
// `syncd run` when you log in: a systemd user unit on Linux, a launch agent on
// macOS, a Run-key entry on Windows.
func serviceCmd(args []string, stdout, stderr io.Writer) int {
	const usage = "usage: syncd service install [--root DIR] | syncd service uninstall | syncd service status"
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	switch args[0] {
	case "install":
		fs := flag.NewFlagSet("service install", flag.ContinueOnError)
		fs.SetOutput(stderr)
		rootDir := fs.String("root", "", "sync root the service watches (default ~/MySync)")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		if fs.NArg() != 0 {
			fmt.Fprintln(stderr, usage)
			return 2
		}
		return serviceInstall(*rootDir, stdout, stderr)
	case "uninstall", "status":
		if len(args) != 1 {
			fmt.Fprintln(stderr, usage)
			return 2
		}
		if args[0] == "uninstall" {
			return serviceUninstall(stdout, stderr)
		}
		return serviceStatus(stdout, stderr)
	}
	fmt.Fprintln(stderr, usage)
	return 2
}

func unsupported(stderr io.Writer) int {
	fmt.Fprintf(stderr, "syncd: service install is not supported on %s\n", goos)
	return 1
}

func serviceInstall(rootDir string, stdout, stderr io.Writer) int {
	if goos != "linux" && goos != "darwin" && goos != "windows" {
		return unsupported(stderr)
	}
	root, err := resolveRoot(rootDir)
	if err == nil {
		root, err = filepath.Abs(root)
	}
	if err != nil {
		fmt.Fprintf(stderr, "syncd: service install failed: %v\n", err)
		return 1
	}
	exe, err := executable()
	if err != nil {
		fmt.Fprintf(stderr, "syncd: service install failed: %v\n", err)
		return 1
	}
	work, err := getwd()
	if err != nil {
		fmt.Fprintf(stderr, "syncd: service install failed: %v\n", err)
		return 1
	}
	// The service remembers this path, and a temp dir is wiped on reboot.
	if tmp := tempDir(); strings.HasPrefix(exe, tmp+string(filepath.Separator)) {
		fmt.Fprintf(stderr, "syncd: %s is in a temp folder that is cleared on reboot; install syncd somewhere permanent (such as /usr/bin or ~/.local/bin) and run `syncd service install` from there\n", exe)
		return 1
	}
	// The daemon reads its OAuth client from .env in its working directory, so
	// the service must start where that file is: where you ran this command.
	// A release build has the client baked in and needs no .env.
	if _, err := os.Stat(filepath.Join(work, ".env")); err != nil && (drive.ClientID == "" || drive.ClientSecret == "") {
		fmt.Fprintln(stderr, "syncd: no .env in the current folder; run `syncd service install` from the folder that holds it (the OAuth client ID and secret)")
		return 1
	}
	// `run` never creates its root (a missing one may be an unmounted drive),
	// so a service for a root that does not exist yet would fail every minute.
	st, err := os.Stat(root)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := os.MkdirAll(root, 0o755); err != nil {
			fmt.Fprintf(stderr, "syncd: service install failed: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "created %s\n", root)
	case err != nil:
		fmt.Fprintf(stderr, "syncd: service install failed: %v\n", err)
		return 1
	case !st.IsDir():
		fmt.Fprintf(stderr, "syncd: service install failed: %s is not a directory\n", root)
		return 1
	}
	spec := serviceSpec{Exe: exe, Root: root, WorkDir: work}

	switch goos {
	case "linux":
		err = installSystemd(spec, stdout)
	case "darwin":
		err = installLaunchd(spec, stdout)
	case "windows":
		err = installWindows(spec, stdout)
	}
	if err != nil {
		fmt.Fprintf(stderr, "syncd: service install failed: %v\n", err)
		return 1
	}
	installTray(exe, stdout, stderr)
	return 0
}

// run1 runs a service-manager command, returning its output in the error.
func run1(name string, args ...string) error {
	out, err := runCommand(name, args...)
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, out)
	}
	return nil
}

func installSystemd(s serviceSpec, stdout io.Writer) error {
	path, err := unitPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(systemdUnit(s)), 0o644); err != nil {
		return err
	}
	if err := run1("systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	if err := run1("systemctl", "--user", "enable", "--now", systemdUnitName); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "installed %s and started it; it starts when you log in\n", path)
	fmt.Fprintln(stdout, "to start it at boot without logging in, run once: loginctl enable-linger $USER")
	fmt.Fprintln(stdout, "status: syncd ctl status   logs: journalctl --user -u syncd.service")
	return nil
}

func installLaunchd(s serviceSpec, stdout io.Writer) error {
	path, err := plistPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	_, _ = runCommand("launchctl", "bootout", launchdDomain()+"/"+launchdLabel) // replace an older install; not loaded is fine
	if err := os.WriteFile(path, []byte(launchdPlist(s)), 0o644); err != nil {
		return err
	}
	if err := run1("launchctl", "bootstrap", launchdDomain(), path); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "installed %s and started it; it starts when you log in\n", path)
	fmt.Fprintln(stdout, "status: syncd ctl status")
	return nil
}

func installWindows(s serviceSpec, stdout io.Writer) error {
	if err := run1("reg", "add", windowsRunKey, "/v", windowsRunValue, "/t", "REG_SZ", "/d", windowsCommand(s), "/f"); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "installed %s\\%s; syncd starts the next time you log in (in a console window until there is a tray app)\n", windowsRunKey, windowsRunValue)
	fmt.Fprintln(stdout, "to start it now, run: syncd run")
	return nil
}

func serviceUninstall(stdout, stderr io.Writer) int {
	var err error
	switch goos {
	case "linux":
		var path string
		if path, err = unitPath(); err == nil {
			// Not enabled or not loaded is fine: the goal is that it is gone.
			_, _ = runCommand("systemctl", "--user", "disable", "--now", systemdUnitName)
			if err = os.Remove(path); errors.Is(err, os.ErrNotExist) {
				err = nil
			}
			if err == nil {
				_, _ = runCommand("systemctl", "--user", "daemon-reload")
			}
		}
	case "darwin":
		var path string
		if path, err = plistPath(); err == nil {
			_, _ = runCommand("launchctl", "bootout", launchdDomain()+"/"+launchdLabel)
			if err = os.Remove(path); errors.Is(err, os.ErrNotExist) {
				err = nil
			}
		}
	case "windows":
		// reg exits 1 when the value is already gone, which is the goal.
		_, _ = runCommand("reg", "delete", windowsRunKey, "/v", windowsRunValue, "/f")
	default:
		return unsupported(stderr)
	}
	if err == nil {
		err = uninstallTray()
	}
	if err != nil {
		fmt.Fprintf(stderr, "syncd: service uninstall failed: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "uninstalled; a syncd that is running keeps running until it is stopped")
	return 0
}

func serviceStatus(stdout, stderr io.Writer) int {
	var (
		out string
		err error
	)
	switch goos {
	case "linux":
		var path string
		if path, err = unitPath(); err == nil {
			_, err = os.Stat(path)
		}
		if err == nil {
			enabled, _ := runCommand("systemctl", "--user", "is-enabled", systemdUnitName)
			active, _ := runCommand("systemctl", "--user", "is-active", systemdUnitName) // exits non-zero when inactive
			out = "enabled: " + enabled + "\nactive: " + active
		}
	case "darwin":
		out, err = runCommand("launchctl", "print", launchdDomain()+"/"+launchdLabel)
		if err == nil {
			out = "loaded: " + launchdLabel
		}
	case "windows":
		out, err = runCommand("reg", "query", windowsRunKey, "/v", windowsRunValue)
	default:
		return unsupported(stderr)
	}
	if err != nil {
		fmt.Fprintln(stderr, "syncd: the service is not installed (run `syncd service install`)")
		return 1
	}
	fmt.Fprintln(stdout, out)
	return 0
}

// stopTray quits a running tray; none running is fine.
func stopTray() {
	if goos == "windows" {
		_, _ = runCommand("taskkill", "/IM", "syncui.exe", "/F")
		return
	}
	_, _ = runCommand("pkill", "-x", "syncui")
}

// uninstallCmd removes everything syncd put in your home: the login
// registrations, a running tray, and with --purge the saved sign-in and the
// app data dir (database, logs, state). The sync root and Drive are untouched.
// The package itself is removed with the package manager.
func uninstallCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	fs.SetOutput(stderr)
	purge := fs.Bool("purge", false, "also sign out (delete the saved token) and delete the app data dir")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: syncd uninstall [--purge]")
		return 2
	}
	if code := serviceUninstall(stdout, stderr); code != 0 {
		return code
	}
	stopTray()
	if *purge {
		if code := signOut(stdout, stderr); code != 0 {
			return code
		}
		dir := dataDir()
		if err := os.RemoveAll(dir); err != nil {
			fmt.Fprintf(stderr, "syncd: remove %s: %v\n", dir, err)
			return 1
		}
		fmt.Fprintf(stdout, "removed %s\n", dir)
	} else {
		fmt.Fprintln(stdout, "kept your sign-in and data; add --purge to remove them too")
	}
	return 0
}
