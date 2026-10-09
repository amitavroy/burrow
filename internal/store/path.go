package store

import (
	"path/filepath"

	"github.com/adrg/xdg"
)

// dbFile is the database location under the app data dir (Local, not Roaming,
// on Windows), next to state.json. It is never inside the sync root.
const dbFile = "burrow/burrow.db"

// DefaultPath returns where the state database lives. It only computes the
// path: unlike xdg.DataFile it creates no directories.
func DefaultPath() string {
	return filepath.Join(xdg.DataHome, filepath.FromSlash(dbFile))
}

// logFile is the rotating log under the app data dir, next to the database.
const logFile = "burrow/logs/syncd.log"

// LogPath returns where syncd writes its log file. Like DefaultPath it only
// computes the path.
func LogPath() string {
	return filepath.Join(xdg.DataHome, filepath.FromSlash(logFile))
}
