//go:build unix

package main

import (
	"os"
	"syscall"
)

func inodeForTest(st os.FileInfo) uint64 {
	if s, ok := st.Sys().(*syscall.Stat_t); ok {
		return uint64(s.Ino)
	}
	return 0
}
