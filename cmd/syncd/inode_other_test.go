//go:build !unix

package main

import "os"

func inodeForTest(os.FileInfo) uint64 { return 0 }
