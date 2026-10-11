//go:build !unix && !windows

package sync

import "io/fs"

// fileID is unknown on systems with neither inodes nor Windows file IDs.
func fileID(string, fs.FileInfo) uint64 { return 0 }
