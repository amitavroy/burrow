//go:build windows

package sync

import (
	"io/fs"
	"syscall"
)

// fileID is the stable identity of a file on its volume: the NTFS file index
// from GetFileInformationByHandle. It survives a rename within the volume and
// an edit, not a delete and recreate. 0 means unknown: a file that cannot be
// opened just has no ID. Opening costs a syscall pair per file, which Unix
// avoids because its Lstat already has the inode.
func fileID(path string, _ fs.FileInfo) uint64 {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0
	}
	// FILE_FLAG_BACKUP_SEMANTICS lets the same call open a directory.
	h, err := syscall.CreateFile(p, 0, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return 0
	}
	defer syscall.CloseHandle(h)
	var fi syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(h, &fi); err != nil {
		return 0
	}
	return uint64(fi.FileIndexHigh)<<32 | uint64(fi.FileIndexLow)
}
