// Package autostart registers the tray app to start when the user logs in: an
// XDG autostart entry on Linux, a launch agent on macOS, a Run-key value on
// Windows. Both binaries use it: `syncd service install` and `syncd login`
// register the tray that sits next to syncd, and the tray registers itself each
// time it starts, so a package or archive install needs no extra step.
package autostart

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Names of the registration on each OS.
const (
	DesktopFile = "burrow-syncui.desktop"
	LaunchdID   = "dev.burrow.syncui"
	WindowsKey  = `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`
	WindowsName = "syncui"

	// SystemDir is where the .deb puts its own entry for every user.
	SystemDir = "/etc/xdg/autostart"
)

// Env is everything the registration reads from the machine; tests replace it.
type Env struct {
	GOOS      string
	ConfigDir func() (string, error) // the user's config dir (XDG_CONFIG_HOME)
	HomeDir   func() (string, error)
	Run       func(name string, args ...string) (string, error) // runs a command, returns its trimmed output
	SystemDir string                                            // Linux: where the package's entry lives
	UID       int
}

// DefaultEnv is the real machine.
func DefaultEnv() Env {
	return Env{
		GOOS: runtime.GOOS, ConfigDir: os.UserConfigDir, HomeDir: os.UserHomeDir,
		Run: func(name string, args ...string) (string, error) {
			out, err := exec.Command(name, args...).CombinedOutput()
			return strings.TrimSpace(string(out)), err
		},
		SystemDir: SystemDir, UID: os.Getuid(),
	}
}

func (e Env) domain() string { return fmt.Sprintf("gui/%d", e.UID) }

// DesktopEntry is the XDG autostart entry for the tray at exe.
func DesktopEntry(exe string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", "$", `\$`)
	return fmt.Sprintf("[Desktop Entry]\nType=Application\nName=Burrow tray\nComment=Sync status for Burrow\nExec=\"%s\"\nTerminal=false\n", r.Replace(exe))
}

// Plist is the macOS launch agent for the tray at exe. It is not restarted:
// quitting the tray from its menu should stay quit.
func Plist(exe string) string {
	var esc bytes.Buffer
	_ = xml.EscapeText(&esc, []byte(exe))
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
`, LaunchdID, esc.String())
}

func (e Env) desktopPath() (string, error) {
	dir, err := e.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "autostart", DesktopFile), nil
}

func (e Env) plistPath() (string, error) {
	home, err := e.HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", LaunchdID+".plist"), nil
}

// Install registers the tray at exe to start at login, replacing an earlier
// registration, and returns a line telling the user what was done.
func (e Env) Install(exe string) (string, error) {
	switch e.GOOS {
	case "linux":
		path, err := e.desktopPath()
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(path, []byte(DesktopEntry(exe)), 0o644); err != nil {
			return "", err
		}
		return fmt.Sprintf("installed %s; the tray icon starts with your next login (start it now: %s &)", path, exe), nil
	case "darwin":
		path, err := e.plistPath()
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", err
		}
		_, _ = e.Run("launchctl", "bootout", e.domain()+"/"+LaunchdID)
		if err := os.WriteFile(path, []byte(Plist(exe)), 0o644); err != nil {
			return "", err
		}
		if out, err := e.Run("launchctl", "bootstrap", e.domain(), path); err != nil {
			return "", fmt.Errorf("launchctl bootstrap: %w: %s", err, out)
		}
		return fmt.Sprintf("installed %s and started the tray icon; it starts when you log in", path), nil
	case "windows":
		if out, err := e.Run("reg", "add", WindowsKey, "/v", WindowsName, "/t", "REG_SZ", "/d", `"`+exe+`"`, "/f"); err != nil {
			return "", fmt.Errorf("reg add: %w: %s", err, out)
		}
		return fmt.Sprintf("installed %s\\%s; the tray icon starts with your next login", WindowsKey, WindowsName), nil
	}
	return "", fmt.Errorf("tray autostart is not supported on %s", e.GOOS)
}

// Uninstall removes the registration; already gone is fine.
func (e Env) Uninstall() error {
	switch e.GOOS {
	case "linux":
		path, err := e.desktopPath()
		if err == nil {
			if err = os.Remove(path); errors.Is(err, os.ErrNotExist) {
				err = nil
			}
		}
		return err
	case "darwin":
		path, err := e.plistPath()
		if err == nil {
			_, _ = e.Run("launchctl", "bootout", e.domain()+"/"+LaunchdID)
			if err = os.Remove(path); errors.Is(err, os.ErrNotExist) {
				err = nil
			}
		}
		return err
	case "windows":
		_, _ = e.Run("reg", "delete", WindowsKey, "/v", WindowsName, "/f")
	}
	return nil
}

// Ensure is what the tray runs each time it starts: it registers the tray at
// exe unless that is already so, and reports whether it wrote anything. It
// leaves alone, on Linux, a user entry that says Hidden=true (the documented
// way to opt out) and a package entry for the same executable that no user
// entry overrides. It never starts the tray: it is already running.
func (e Env) Ensure(exe string) (changed bool, err error) {
	switch e.GOOS {
	case "linux":
		path, err := e.desktopPath()
		if err != nil {
			return false, err
		}
		want := DesktopEntry(exe)
		if have, err := os.ReadFile(path); err == nil {
			if string(have) == want || strings.Contains(string(have), "Hidden=true") {
				return false, nil
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		} else if sys, err := os.ReadFile(filepath.Join(e.SystemDir, DesktopFile)); err == nil && strings.Contains(string(sys), `Exec=`+exe+"\n") {
			return false, nil // the package's entry already starts this very binary
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return false, err
		}
		return true, os.WriteFile(path, []byte(want), 0o644)
	case "darwin":
		path, err := e.plistPath()
		if err != nil {
			return false, err
		}
		if have, err := os.ReadFile(path); err == nil && string(have) == Plist(exe) {
			return false, nil
		}
	case "windows":
		if out, err := e.Run("reg", "query", WindowsKey, "/v", WindowsName); err == nil && strings.Contains(out, exe) {
			return false, nil
		}
	default:
		return false, nil
	}
	// A launch agent or Run value that is missing or stale. The tray is already
	// running, so on macOS the bootstrap would start a second one: only write the file.
	if e.GOOS == "darwin" {
		path, _ := e.plistPath()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return false, err
		}
		return true, os.WriteFile(path, []byte(Plist(exe)), 0o644)
	}
	_, err = e.Install(exe)
	return err == nil, err
}
