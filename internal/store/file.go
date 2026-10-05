package store

import "errors"

var (
	// ErrNotFound is returned when a row does not exist.
	ErrNotFound = errors.New("store: not found")
	// ErrClosed is returned by any Repo method called after Close.
	ErrClosed = errors.New("store: repo closed")
)

// File is one tracked file. Empty strings and a zero Inode mean "unknown" and
// are stored as NULL.
type File struct {
	// RelPath is the slash-form path relative to the sync root, with no
	// machine-specific parts.
	RelPath     string
	DriveFileID string
	Size        int64
	// MTime is Unix nanoseconds, so equality checks are exact.
	MTime int64
	// Inode is 0 when unknown.
	Inode          uint64
	LocalMD5       string
	SyncedMD5      string
	BaseMD5        string
	BaseRevisionID string
}
