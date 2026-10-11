//go:build unix

package sync

import (
	"io/fs"
	"syscall"
)

// fileID is the stable identity of a file on its filesystem: the inode number
// on Unix. It survives a rename and an edit, not a delete and recreate. 0
// means unknown. The info already comes from the scan's Lstat, so this costs
// nothing.
func fileID(_ string, info fs.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino)
	}
	return 0
}
