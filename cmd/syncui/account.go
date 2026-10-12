package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// accountLine is the tray's account label for a cached email.
func accountLine(email string) string {
	if email == "" {
		return "Not signed in"
	}
	return "Signed in as " + email
}

// syncdPath finds the syncd binary: the one next to this executable (how the
// packages install both), else whatever is on PATH.
func syncdPath(exe string) string {
	name := "syncd"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	p := filepath.Join(filepath.Dir(exe), name)
	if st, err := os.Stat(p); err == nil && !st.IsDir() {
		return p
	}
	return "syncd"
}

// runSyncd runs a syncd subcommand and returns "" on success, else a one-line
// reason for the tray to show (syncd's last line of output).
func runSyncd(args ...string) string {
	exe, _ := os.Executable()
	out, err := exec.Command(syncdPath(exe), args...).CombinedOutput()
	if err == nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
		return last
	}
	return err.Error()
}
