package main

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/amitavroy/burrow/internal/autostart"
	"github.com/amitavroy/burrow/internal/store"
)

// lockPath is the single-instance lock file's base: the lock is lockPath + ".lock"
// in the data dir, never the sync root.
func lockPath() string { return filepath.Join(filepath.Dir(store.DefaultPath()), "syncui") }

// acquire takes the single-instance lock. ok is false when another tray holds
// it: the caller should exit quietly, since the launcher entry and autostart
// can both start one. Any other problem with the lock is not a reason to
// refuse to run, so it returns ok.
func acquire(base string) (lock *store.FileLock, ok bool) {
	lock, err := store.Lock(base)
	if errors.Is(err, store.ErrLocked) {
		return nil, false
	}
	if err != nil {
		slog.Warn("tray lock unavailable, starting anyway", "err", err)
	}
	return lock, true
}

// registerAutostart makes the tray start at the user's next login, unless it is
// running from a temp folder (a build or download that will not stay). A failure
// only costs the autostart, so it is logged and ignored.
func registerAutostart(env autostart.Env, exe, tmp string) {
	if exe == "" || strings.HasPrefix(exe, tmp+string(filepath.Separator)) {
		return
	}
	if changed, err := env.Ensure(exe); err != nil {
		slog.Warn("could not register the tray to start at login", "err", err)
	} else if changed {
		slog.Info("registered the tray to start at login")
	}
}

// selfPath is this executable with symlinks resolved, or "".
func selfPath() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}
